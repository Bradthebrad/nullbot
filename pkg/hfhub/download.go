package hfhub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	partSuffix              = ".part"
	defaultProgressInterval = 250 * time.Millisecond
	// spaceMargin is left free beyond the file itself, so a download does
	// not leave the disk with no room at all for everything else.
	spaceMargin = 64 << 20
)

// diskFree is a variable so tests can pretend the disk is full.
var diskFree = freeSpace

// DownloadRequest says which file to fetch and where to put it.
type DownloadRequest struct {
	Repo     string // owner/name
	Revision string // branch, tag or commit; empty means main
	Path     string // file path inside the repo, as Files lists it
	Dir      string // destination folder; created if missing

	// Name is where the file goes inside Dir, '/'-separated. Empty means
	// Path, so "Q4_K_M/model.gguf" lands in Dir/Q4_K_M/model.gguf.
	Name string

	// SHA256 and Size are what the file should be, normally copied from
	// Files. When empty, the values the Hub reports for the file are used.
	SHA256 string
	Size   int64

	// Overwrite allows replacing a different file already at the
	// destination. The old file stays until the new one is complete.
	Overwrite bool
	// KeepPartial keeps the .part file when the download is cancelled, so
	// a later Download resumes it. A cancelled download is otherwise
	// cleaned up; other interruptions (network, full disk) always keep it.
	KeepPartial bool

	// Progress, if set, is called from the downloading goroutine at most
	// once per ProgressInterval (default 250ms), plus once at the start and
	// once at the end.
	Progress         func(Progress)
	ProgressInterval time.Duration
}

// Progress reports how far a download has come.
type Progress struct {
	Name  string // the file's name inside the destination folder
	Done  int64  // bytes on disk so far, including any resumed part
	Total int64  // bytes in the whole file; -1 when not known
}

// Result describes a finished download.
type Result struct {
	Path           string // full local path of the file
	Size           int64
	SHA256         string // sha256 of the file on disk, lowercase hex
	Verified       bool   // the sha256 matched the one published for the file
	Resumed        bool   // part of the file came from an earlier, interrupted download
	AlreadyPresent bool   // an identical file was already there; nothing was downloaded
	Commit         string // the commit the file came from, when the Hub said
}

// Download fetches one file from GET /{repo}/resolve/{revision}/{path} into
// r.Dir. Large files are redirected by the Hub to a CDN; the redirect is
// followed without the token. The file is written to "<name>.part", resumed
// with a Range request if that exists, checked against its sha256 when the
// Hub publishes one (all LFS files), and only then renamed into place.
//
// Download refuses names that would land outside r.Dir, will not replace a
// different existing file unless r.Overwrite is set, and checks free disk
// space first where the system can tell. Two Downloads must not write the
// same destination at the same time.
func (c *Client) Download(ctx context.Context, r DownloadRequest) (Result, error) {
	s, err := c.session()
	if err != nil {
		return Result{}, err
	}
	rev, err := repoAndRevision(r.Repo, r.Revision)
	if err != nil {
		return Result{}, err
	}
	if err := checkRepoPath(r.Path); err != nil {
		return Result{}, err
	}
	name := r.Name
	if name == "" {
		name = r.Path
	}
	if err := checkLocalName(name); err != nil {
		return Result{}, err
	}
	want := strings.ToLower(strings.TrimSpace(r.SHA256))
	if want != "" && !isSHA256(want) {
		return Result{}, newError(ErrInvalidArgument, "the expected checksum %q is not a sha256 hex digest", r.SHA256)
	}
	if strings.TrimSpace(r.Dir) == "" {
		return Result{}, newError(ErrInvalidArgument, "choose a folder to download %s into", name)
	}
	dir, err := filepath.Abs(r.Dir)
	if err != nil {
		return Result{}, newError(ErrInvalidArgument, "the folder %q cannot be used: %v", r.Dir, err)
	}

	d := &download{
		s:     s,
		req:   r,
		t:     target{repo: r.Repo, revision: rev, path: r.Path},
		dir:   dir,
		name:  name,
		local: filepath.FromSlash(name),
		size:  -1,
	}
	d.part = d.local + partSuffix
	d.progress = progressReporter{fn: r.Progress, every: r.ProgressInterval, p: Progress{Name: name, Total: -1}}
	if d.progress.every <= 0 {
		d.progress.every = defaultProgressInterval
	}
	return d.run(ctx, want)
}

// download is the state of one Download call.
type download struct {
	s        *session
	req      DownloadRequest
	t        target
	dir      string   // absolute destination folder
	root     *os.Root // all file access goes through it, so nothing escapes dir
	name     string   // destination name inside dir, '/'-separated, for messages
	local    string   // the same with OS separators
	part     string   // local + ".part"
	size     int64    // expected size; -1 when unknown
	sha      string   // expected sha256; "" when unknown
	commit   string   // commit the Hub resolved the revision to
	progress progressReporter
}

func (d *download) what() string {
	return fmt.Sprintf("the download of %s from %s", d.name, d.t.repo)
}

func (d *download) run(ctx context.Context, want string) (Result, error) {
	// Ask the Hub about the file before touching the disk: a gated repo
	// fails here, and the size and checksum are known up front.
	meta, err := d.head(ctx)
	if err != nil {
		return Result{}, err
	}
	if err := d.settle(meta, want); err != nil {
		return Result{}, err
	}

	if err := os.MkdirAll(d.dir, 0o755); err != nil {
		return Result{}, d.fileError(err)
	}
	// Every file operation below goes through the root, so neither a name
	// nor a symlinked folder inside dir can lead outside it.
	root, err := os.OpenRoot(d.dir)
	if err != nil {
		return Result{}, d.fileError(err)
	}
	defer root.Close()
	d.root = root

	if res, done, err := d.checkExisting(ctx); done || err != nil {
		return res, err
	}
	return d.fetch(ctx)
}

func (d *download) resolveURL(rev string) *url.URL {
	return d.s.endpoint("/"+d.t.repo+"/resolve/"+url.PathEscape(rev)+"/"+escapeRepoPath(d.t.path), nil)
}

type fileMeta struct {
	size   int64 // -1 when unknown
	sha256 string
	commit string
}

// head reads what the Hub says about the file. For LFS files the Hub
// answers with a redirect to the CDN carrying X-Linked-Size and
// X-Linked-Etag (the sha256); the redirect is not followed, since the
// Hub's own headers are what we want. Same-host redirects, which the Hub
// uses for renamed repos, are followed.
func (d *download) head(ctx context.Context) (fileMeta, error) {
	meta := fileMeta{size: -1}
	req, err := d.s.newRequest(ctx, http.MethodHead, d.resolveURL(d.t.revision))
	if err != nil {
		return meta, err
	}
	resp, err := d.s.client(true).Do(req)
	if err != nil {
		return meta, networkError(ctx, d.what(), err)
	}
	switch {
	case resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusNotImplemented:
		drain(resp.Body)
		return meta, nil // a mirror without HEAD: the download itself tells the size
	case resp.StatusCode >= 400:
		return meta, d.s.responseError(resp, d.t)
	}
	drain(resp.Body)

	h := resp.Header
	if n, err := strconv.ParseInt(h.Get("X-Linked-Size"), 10, 64); err == nil && n >= 0 {
		meta.size = n
	} else if n, err := strconv.ParseInt(h.Get("Content-Length"), 10, 64); err == nil && n >= 0 && resp.StatusCode == http.StatusOK {
		meta.size = n
	}
	for _, v := range []string{h.Get("X-Linked-Etag"), h.Get("ETag")} {
		// A non-LFS file's ETag is its git blob id (40 hex), not a sha256,
		// so only a 64-digit value is taken as one.
		if v = cleanETag(v); isSHA256(v) {
			meta.sha256 = v
			break
		}
	}
	if c := strings.ToLower(h.Get("X-Repo-Commit")); len(c) == 40 && isHex(c) {
		meta.commit = c
	}
	return meta, nil
}

func cleanETag(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "W/")
	return strings.ToLower(strings.Trim(v, `"`))
}

// settle decides the size and checksum to expect. The caller's values
// (normally from Files) are kept, and if the Hub now reports different
// ones the file changed on the Hub since it was listed: better to say so
// now than after a long download that would fail its checksum.
func (d *download) settle(meta fileMeta, want string) error {
	d.sha, d.commit = want, meta.commit
	if d.req.Size > 0 {
		d.size = d.req.Size
	}
	changed := newError(ErrChecksumMismatch, "%s changed on Hugging Face since its file list was read; refresh the list and try again", d.name)
	if meta.size >= 0 {
		if d.size >= 0 && d.size != meta.size {
			return changed
		}
		d.size = meta.size
	}
	if meta.sha256 != "" {
		if d.sha != "" && d.sha != meta.sha256 {
			return changed
		}
		d.sha = meta.sha256
	}
	d.progress.p.Total = d.size
	return nil
}

// checkExisting looks at the destination. An identical file means there is
// nothing to do; a different one is only replaced when the caller allows.
func (d *download) checkExisting(ctx context.Context) (Result, bool, error) {
	fi, err := d.root.Lstat(d.local)
	if errors.Is(err, fs.ErrNotExist) {
		return Result{}, false, nil
	}
	if err != nil {
		return Result{}, true, d.fileError(err)
	}
	if !fi.Mode().IsRegular() {
		return Result{}, true, newError(ErrFileExists, "%s already exists in %s and is not a regular file; move it away and try again", d.name, d.dir)
	}
	if d.sha != "" && (d.size < 0 || fi.Size() == d.size) {
		sum, err := d.hashFile(ctx, d.local, fi.Size(), nil)
		if err != nil {
			return Result{}, true, err
		}
		if hex.EncodeToString(sum.Sum(nil)) == d.sha {
			return Result{
				Path: filepath.Join(d.dir, d.local), Size: fi.Size(), SHA256: d.sha,
				Verified: true, AlreadyPresent: true, Commit: d.commit,
			}, true, nil
		}
	}
	switch {
	case d.req.Overwrite:
		return Result{}, false, nil
	case d.sha == "":
		return Result{}, true, newError(ErrFileExists, "%s already exists in %s and cannot be checked against Hugging Face; remove it or allow overwriting", d.name, d.dir)
	}
	return Result{}, true, newError(ErrFileExists, "%s already exists in %s and differs from the file on Hugging Face; remove it or allow overwriting", d.name, d.dir)
}

// fetch downloads into the .part file, verifies it and renames it into
// place. The deferred cleanup removes the .part file when its content is
// known to be bad, or when the download was cancelled and the caller did
// not ask to keep it; other failures leave it for the next call to resume.
func (d *download) fetch(ctx context.Context) (res Result, err error) {
	removePart := false
	defer func() {
		if err != nil && ctx.Err() != nil && !d.req.KeepPartial {
			removePart = true
		}
		if removePart {
			_ = d.root.Remove(d.part)
		}
	}()

	offset, err := d.partSize()
	if err != nil {
		return Result{}, err
	}
	h := sha256.New()
	if offset > 0 {
		// The checksum covers the whole file, so the part already on disk
		// is hashed before new bytes are added.
		if _, err := d.hashFile(ctx, d.part, offset, h); err != nil {
			return Result{}, err
		}
	}
	resumedFrom := offset
	d.progress.report(offset, true)

	var body io.ReadCloser
	if d.size < 0 || offset < d.size {
		resp, start, err := d.get(ctx, offset)
		if err != nil {
			return Result{}, err
		}
		defer resp.Body.Close()
		if start != offset { // the server sent the whole file instead
			h.Reset()
			offset, resumedFrom = 0, 0
		}
		body = resp.Body
	}
	if err := d.checkSpace(offset); err != nil {
		return Result{}, err
	}

	if parent := filepath.Dir(d.local); parent != "." {
		if err := d.root.MkdirAll(parent, 0o755); err != nil {
			return Result{}, d.fileError(err)
		}
	}
	f, err := d.root.OpenFile(d.part, os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return Result{}, d.fileError(err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = f.Close() // before the cleanup above: Windows cannot remove an open file
		}
	}()
	if err := f.Truncate(offset); err != nil {
		return Result{}, d.fileError(err)
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return Result{}, d.fileError(err)
	}

	done := offset
	if body != nil {
		src := io.Reader(body)
		if d.size >= 0 {
			// One byte more than expected is enough to notice a server
			// sending too much, without writing an unbounded stream.
			src = io.LimitReader(body, d.size-offset+1)
		}
		n, rerr, werr := copyCtx(ctx, f, h, src, func(n int64) { d.progress.report(offset+n, false) })
		done = offset + n
		switch {
		case ctx.Err() != nil:
			return Result{}, stopped(d.what(), ctx.Err())
		case werr != nil:
			return Result{}, d.fileError(werr)
		case rerr != nil:
			return Result{}, &Error{Kind: ErrIncomplete, cause: rerr,
				msg: fmt.Sprintf("%s stopped before the end (%v); try again to resume it", d.what(), rerr)}
		}
	}

	if err := f.Sync(); err != nil { // so a crash after the rename cannot leave a short file
		return Result{}, d.fileError(err)
	}
	closed = true
	if err := f.Close(); err != nil {
		return Result{}, d.fileError(err)
	}

	if d.size >= 0 && done != d.size {
		if done > d.size {
			removePart = true
			return Result{}, newError(ErrBadResponse, "the server sent more data for %s than the file should hold, so the download was deleted; try again", d.name)
		}
		return Result{}, newError(ErrIncomplete, "%s stopped at %s of %s; try again to resume it", d.what(), formatBytes(done), formatBytes(d.size))
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if d.sha != "" && sum != d.sha {
		removePart = true
		return Result{}, newError(ErrChecksumMismatch, "%s did not match the checksum published on Hugging Face, so the download was deleted; try again", d.name)
	}
	if !d.req.Overwrite {
		if _, err := d.root.Lstat(d.local); err == nil {
			return Result{}, newError(ErrFileExists, "%s appeared in %s while downloading; the download was kept as %s", d.name, d.dir, d.name+partSuffix)
		}
	}
	if err := d.rename(); err != nil {
		return Result{}, err
	}
	d.progress.report(done, true)
	return Result{
		Path:     filepath.Join(d.dir, d.local),
		Size:     done,
		SHA256:   sum,
		Verified: d.sha != "",
		Resumed:  resumedFrom > 0,
		Commit:   d.commit,
	}, nil
}

// partSize returns how much of the file an earlier attempt left behind.
func (d *download) partSize() (int64, error) {
	fi, err := d.root.Lstat(d.part)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return 0, nil
	case err != nil:
		return 0, d.fileError(err)
	case !fi.Mode().IsRegular():
		return 0, newError(ErrFileExists, "%s exists in %s and is not a regular file; move it away and try again", d.name+partSuffix, d.dir)
	case d.size >= 0 && fi.Size() > d.size:
		return 0, nil // longer than the file: left from another version, start over
	}
	return fi.Size(), nil
}

// get starts the transfer at offset. It returns the response and the byte
// position its body starts at, which is 0 when the server sent the whole
// file instead of the range.
func (d *download) get(ctx context.Context, offset int64) (*http.Response, int64, error) {
	rev := d.t.revision
	if d.commit != "" {
		// Pin the commit the HEAD saw, so a push in between cannot mix
		// the size and checksum of one version with the bytes of another.
		rev = d.commit
	}
	for attempt := 0; ; attempt++ {
		req, err := d.s.newRequest(ctx, http.MethodGet, d.resolveURL(rev))
		if err != nil {
			return nil, 0, err
		}
		// The stored bytes, please: a transparently decompressed stream
		// would break both Range offsets and the checksum.
		req.Header.Set("Accept-Encoding", "identity")
		if offset > 0 {
			req.Header.Set("Range", "bytes="+strconv.FormatInt(offset, 10)+"-")
		}
		resp, err := d.s.client(false).Do(req)
		if err != nil {
			return nil, 0, networkError(ctx, d.what(), err)
		}
		switch resp.StatusCode {
		case http.StatusOK:
			d.learnSize(resp.ContentLength)
			return resp, 0, nil
		case http.StatusPartialContent:
			start, total, ok := parseContentRange(resp.Header.Get("Content-Range"))
			if ok && start == offset {
				d.learnSize(total)
				return resp, offset, nil
			}
			drain(resp.Body) // a range we did not ask for cannot be appended
		case http.StatusRequestedRangeNotSatisfiable:
			// The .part reaches past what the server has, so it belongs
			// to another version of the file.
			drain(resp.Body)
		default:
			return nil, 0, d.s.responseError(resp, d.t)
		}
		if attempt > 0 || offset == 0 {
			return nil, 0, newError(ErrBadResponse, "the server sent an unusable answer for %s; try again later", d.what())
		}
		offset = 0 // retry once from the start
	}
}

func (d *download) learnSize(n int64) {
	if d.size < 0 && n >= 0 {
		d.size = n
		d.progress.p.Total = n
	}
}

// parseContentRange reads "bytes start-end/total"; total is -1 for "*".
func parseContentRange(v string) (start, total int64, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(v), "bytes ")
	if !found {
		return 0, 0, false
	}
	rng, tot, found := strings.Cut(rest, "/")
	first, _, found2 := strings.Cut(rng, "-")
	if !found || !found2 {
		return 0, 0, false
	}
	start, err := strconv.ParseInt(first, 10, 64)
	if err != nil || start < 0 {
		return 0, 0, false
	}
	total = -1
	if tot != "*" {
		if total, err = strconv.ParseInt(tot, 10, 64); err != nil {
			return 0, 0, false
		}
	}
	return start, total, true
}

// checkSpace refuses to start when the folder cannot hold the rest of the
// file. Systems that cannot tell skip the check.
func (d *download) checkSpace(offset int64) error {
	if d.size < 0 {
		return nil
	}
	free, ok := diskFree(d.dir)
	need := uint64(d.size-offset) + spaceMargin
	if !ok || free >= need {
		return nil
	}
	return newError(ErrInsufficientSpace, "not enough free disk space in %s: %s needs about %s but only %s is free; free up space or choose another folder",
		d.dir, d.name, formatBytes(int64(need)), formatBytes(int64(free)))
}

func (d *download) rename() error {
	err := d.root.Rename(d.part, d.local)
	if err != nil && d.req.Overwrite {
		// Some systems will not rename over an existing file; the new file
		// is complete and verified, so the old one can go first.
		if rmErr := d.root.Remove(d.local); rmErr == nil || errors.Is(rmErr, fs.ErrNotExist) {
			err = d.root.Rename(d.part, d.local)
		}
	}
	if err != nil {
		return d.fileError(err)
	}
	return nil
}

// hashFile hashes the first n bytes of a file inside the root into h (a new
// sha256 when h is nil), stopping when ctx is cancelled.
func (d *download) hashFile(ctx context.Context, name string, n int64, h hash.Hash) (hash.Hash, error) {
	if h == nil {
		h = sha256.New()
	}
	f, err := d.root.Open(name)
	if err != nil {
		return nil, d.fileError(err)
	}
	defer f.Close()
	got, rerr, _ := copyCtx(ctx, h, nil, io.LimitReader(f, n), nil)
	switch {
	case ctx.Err() != nil:
		return nil, stopped(d.what(), ctx.Err())
	case rerr != nil:
		return nil, d.fileError(rerr)
	case got != n:
		return nil, d.fileError(fmt.Errorf("%s changed while it was being read", filepath.Base(name)))
	}
	return h, nil
}

func (d *download) fileError(err error) error {
	// os.Root refuses a path that leaves the folder (through a symlink, say)
	// with an unexported error, so its text is the only way to tell.
	if strings.Contains(err.Error(), "path escapes from parent") {
		return &Error{Kind: ErrUnsafePath, cause: err,
			msg: fmt.Sprintf("refusing to save %s: a link inside %s points outside it", d.name, d.dir)}
	}
	if isDiskFull(err) {
		return &Error{Kind: ErrInsufficientSpace, cause: err,
			msg: fmt.Sprintf("the disk holding %s is full; free up space and try again (what was downloaded so far is kept and will resume)", d.dir)}
	}
	return &Error{Kind: ErrFileSystem, cause: err,
		msg: fmt.Sprintf("could not save %s in %s: %v", d.name, d.dir, err)}
}

// copyCtx copies src to dst (and to the hash h, when set) until EOF,
// checking ctx between chunks so a cancel stops a long copy promptly. It
// reports read and write failures apart, since they mean different things
// to the user: a dropped connection versus a full disk.
func copyCtx(ctx context.Context, dst io.Writer, h hash.Hash, src io.Reader, onWrite func(int64)) (n int64, readErr, writeErr error) {
	buf := make([]byte, 256<<10)
	for {
		if err := ctx.Err(); err != nil {
			return n, err, nil
		}
		k, rerr := src.Read(buf)
		if k > 0 {
			w, werr := dst.Write(buf[:k])
			if h != nil && w > 0 {
				h.Write(buf[:w])
			}
			n += int64(w)
			if onWrite != nil {
				onWrite(n)
			}
			if werr == nil && w != k {
				werr = io.ErrShortWrite
			}
			if werr != nil {
				return n, nil, werr
			}
		}
		if rerr == io.EOF {
			return n, nil, nil
		}
		if rerr != nil {
			return n, rerr, nil
		}
	}
}

// progressReporter calls the caller's Progress function, at most once per
// interval unless forced, so a fast download does not flood a UI.
type progressReporter struct {
	fn    func(Progress)
	every time.Duration
	last  time.Time
	p     Progress
}

func (r *progressReporter) report(done int64, force bool) {
	if r.fn == nil {
		return
	}
	now := time.Now()
	if !force && now.Sub(r.last) < r.every {
		return
	}
	r.last = now
	r.p.Done = done
	r.fn(r.p)
}

func formatBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	v, i := float64(n), 0
	for v >= unit && i < 4 {
		v /= unit
		i++
	}
	return fmt.Sprintf("%.1f %s", v, []string{"B", "KB", "MB", "GB", "TB"}[i])
}
