package hfhub

import (
	"bytes"
	"context"
	"errors"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const modelPath = "Q4_K_M/model-Q4_K_M.gguf"

func testContent(n int) []byte {
	r := rand.New(rand.NewPCG(1, 2))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	return b
}

func readFile(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func assertMissing(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s should not exist (err = %v)", p, err)
		}
	}
}

func TestDownloadFollowsRedirectWithoutLeakingToken(t *testing.T) {
	f := newFakeHub(t)
	content := testContent(700 << 10)
	f.lfs[modelPath] = content
	dir := t.TempDir()

	var progress []Progress
	res, err := f.client("hf_secret").Download(context.Background(), DownloadRequest{
		Repo: "o/n", Path: modelPath, Dir: dir,
		Progress:         func(p Progress) { progress = append(progress, p) },
		ProgressInterval: time.Nanosecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "Q4_K_M", "model-Q4_K_M.gguf")
	if res.Path != dest || !res.Verified || res.Resumed || res.AlreadyPresent || res.SHA256 != sum256(content) ||
		res.Size != int64(len(content)) || res.Commit != testCommit {
		t.Errorf("result = %+v", res)
	}
	if !bytes.Equal(readFile(t, dest), content) {
		t.Error("content differs")
	}
	assertMissing(t, dest+partSuffix)

	hub, cdn := f.requests()
	if len(hub) != 2 || hub[0].method != "HEAD" || hub[1].method != "GET" {
		t.Fatalf("hub requests: %+v", hub)
	}
	for _, r := range hub {
		if r.auth != "Bearer hf_secret" {
			t.Errorf("hub %s had Authorization %q", r.method, r.auth)
		}
	}
	if !strings.Contains(hub[1].path, "/resolve/"+testCommit+"/") {
		t.Errorf("GET was not pinned to the commit the HEAD saw: %s", hub[1].path)
	}
	if len(cdn) != 1 {
		t.Fatalf("cdn requests: %+v", cdn)
	}
	if cdn[0].auth != "" {
		t.Errorf("token leaked to the CDN: %q", cdn[0].auth)
	}

	if len(progress) < 3 {
		t.Fatalf("too few progress reports: %+v", progress)
	}
	first, last := progress[0], progress[len(progress)-1]
	if first.Done != 0 || last.Done != int64(len(content)) || last.Total != int64(len(content)) || last.Name != modelPath {
		t.Errorf("progress first %+v last %+v", first, last)
	}
	for i := 1; i < len(progress); i++ {
		if progress[i].Done < progress[i-1].Done {
			t.Errorf("progress went backwards: %+v", progress)
			break
		}
	}
}

func TestDownloadProgressIsRateLimited(t *testing.T) {
	f := newFakeHub(t)
	f.lfs[modelPath] = testContent(4 << 20)
	calls := 0
	_, err := f.client("").Download(context.Background(), DownloadRequest{
		Repo: "o/n", Path: modelPath, Dir: t.TempDir(),
		Progress:         func(Progress) { calls++ },
		ProgressInterval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 { // the start and the end
		t.Errorf("progress called %d times", calls)
	}
}

func TestDownloadResumesPartFile(t *testing.T) {
	f := newFakeHub(t)
	content := testContent(500 << 10)
	f.lfs[modelPath] = content
	dir := t.TempDir()
	dest := filepath.Join(dir, "model.gguf")
	const have = 123457
	if err := os.WriteFile(dest+partSuffix, content[:have], 0o644); err != nil {
		t.Fatal(err)
	}

	var first Progress
	res, err := f.client("").Download(context.Background(), DownloadRequest{
		Repo: "o/n", Path: modelPath, Dir: dir, Name: "model.gguf",
		Progress: func(p Progress) {
			if first.Name == "" {
				first = p
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Resumed || !res.Verified || res.Path != dest {
		t.Errorf("result = %+v", res)
	}
	if first.Done != have {
		t.Errorf("first progress = %+v, want Done %d", first, have)
	}
	if !bytes.Equal(readFile(t, dest), content) {
		t.Error("content differs")
	}
	assertMissing(t, dest+partSuffix)
	if _, cdn := f.requests(); len(cdn) != 1 || cdn[0].rng != "bytes=123457-" {
		t.Errorf("cdn requests: %+v", cdn)
	}
}

func TestDownloadRestartsWhenServerIgnoresRange(t *testing.T) {
	f := newFakeHub(t)
	content := testContent(300 << 10)
	f.lfs[modelPath] = content
	f.ignoreRange = true
	dir := t.TempDir()
	dest := filepath.Join(dir, "model.gguf")
	// A stale part that does not match: appending to it would be corrupt.
	if err := os.WriteFile(dest+partSuffix, bytes.Repeat([]byte{'x'}, 1000), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := f.client("").Download(context.Background(), DownloadRequest{Repo: "o/n", Path: modelPath, Dir: dir, Name: "model.gguf"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Resumed || !res.Verified {
		t.Errorf("result = %+v", res)
	}
	if !bytes.Equal(readFile(t, dest), content) {
		t.Error("content differs")
	}
}

func TestDownloadCompletePartIsVerifiedWithoutRefetching(t *testing.T) {
	f := newFakeHub(t)
	content := testContent(64 << 10)
	f.lfs[modelPath] = content
	dir := t.TempDir()
	dest := filepath.Join(dir, "m.gguf")
	if err := os.WriteFile(dest+partSuffix, content, 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := f.client("").Download(context.Background(), DownloadRequest{Repo: "o/n", Path: modelPath, Dir: dir, Name: "m.gguf"})
	if err != nil || !res.Verified || !res.Resumed {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	if _, cdn := f.requests(); len(cdn) != 0 {
		t.Errorf("the complete part was downloaded again: %+v", cdn)
	}
}

func TestDownloadChecksumMismatchRemovesFile(t *testing.T) {
	f := newFakeHub(t)
	f.lfs[modelPath] = testContent(200 << 10)
	f.corrupt = true
	dir := t.TempDir()
	_, err := f.client("").Download(context.Background(), DownloadRequest{Repo: "o/n", Path: modelPath, Dir: dir, Name: "m.gguf"})
	if !errors.Is(err, ErrChecksumMismatch) || !strings.Contains(err.Error(), "deleted") {
		t.Fatalf("err = %v", err)
	}
	assertMissing(t, filepath.Join(dir, "m.gguf"), filepath.Join(dir, "m.gguf"+partSuffix))
}

func TestDownloadRefusesChecksumThatChangedSinceListing(t *testing.T) {
	f := newFakeHub(t)
	f.lfs[modelPath] = testContent(1000)
	dir := t.TempDir()
	_, err := f.client("").Download(context.Background(), DownloadRequest{
		Repo: "o/n", Path: modelPath, Dir: dir, SHA256: strings.Repeat("c", 64),
	})
	if !errors.Is(err, ErrChecksumMismatch) || !strings.Contains(err.Error(), "changed on Hugging Face") {
		t.Fatalf("err = %v", err)
	}
	if _, cdn := f.requests(); len(cdn) != 0 {
		t.Errorf("downloaded anyway: %+v", cdn)
	}
}

func TestDownloadRefusesUnsafeNames(t *testing.T) {
	f := newFakeHub(t)
	f.lfs[modelPath] = testContent(1000)
	parent := t.TempDir()
	dir := filepath.Join(parent, "models")
	c := f.client("")

	names := []string{
		"../evil.gguf", "a/../../evil.gguf", "/etc/evil.gguf", `C:\evil.gguf`, `C:evil.gguf`,
		`sub\..\..\evil.gguf`, "a//evil.gguf", "./evil.gguf", "NUL.gguf", "com1", "evil.gguf.", "evil.gguf ",
		"evil.gguf:stream", "ev\x00il.gguf", "e<vil>.gguf",
	}
	for _, name := range names {
		_, err := c.Download(context.Background(), DownloadRequest{Repo: "o/n", Path: modelPath, Dir: dir, Name: name})
		if !errors.Is(err, ErrUnsafePath) {
			t.Errorf("name %q: err = %v", name, err)
		}
	}
	// Without Name the repo path is the local name, so a hostile listing
	// is refused the same way.
	for _, p := range []string{"../evil.gguf", "a/../../evil.gguf", "/abs.gguf", `..\evil.gguf`, "x/./y.gguf"} {
		_, err := c.Download(context.Background(), DownloadRequest{Repo: "o/n", Path: p, Dir: dir})
		if !errors.Is(err, ErrUnsafePath) {
			t.Errorf("path %q: err = %v", p, err)
		}
	}
	if hub, _ := f.requests(); len(hub) != 0 {
		t.Errorf("requests were made: %+v", hub)
	}
	entries, _ := os.ReadDir(parent)
	if len(entries) != 0 {
		t.Errorf("something was written: %v", entries)
	}
}

func TestDownloadCannotEscapeThroughSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need extra privileges on Windows")
	}
	f := newFakeHub(t)
	f.lfs[modelPath] = testContent(1000)
	outside := t.TempDir()
	dir := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	_, err := f.client("").Download(context.Background(), DownloadRequest{Repo: "o/n", Path: modelPath, Dir: dir, Name: "link/evil.gguf"})
	if !errors.Is(err, ErrUnsafePath) || !strings.Contains(err.Error(), "points outside") {
		t.Fatalf("err = %v", err)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("wrote outside the folder: %v", entries)
	}
}

func TestDownloadCancelCleansUp(t *testing.T) {
	for _, keep := range []bool{false, true} {
		f := newFakeHub(t)
		f.lfs[modelPath] = testContent(1 << 20)
		f.stall = true
		dir := t.TempDir()
		ctx, cancel := context.WithCancel(context.Background())
		_, err := f.client("").Download(ctx, DownloadRequest{
			Repo: "o/n", Path: modelPath, Dir: dir, Name: "m.gguf", KeepPartial: keep,
			ProgressInterval: time.Nanosecond,
			Progress: func(p Progress) {
				if p.Done > 0 {
					cancel()
				}
			},
		})
		cancel()
		if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "cancelled") {
			t.Fatalf("keep=%v: err = %v", keep, err)
		}
		part := filepath.Join(dir, "m.gguf"+partSuffix)
		assertMissing(t, filepath.Join(dir, "m.gguf"))
		if keep {
			if fi, err := os.Stat(part); err != nil || fi.Size() == 0 {
				t.Errorf("keep=%v: part file should be kept with data: %v %v", keep, fi, err)
			}
		} else {
			assertMissing(t, part)
		}
	}
}

func TestDownloadGatedNeedsToken(t *testing.T) {
	f := newFakeHub(t)
	f.lfs[modelPath] = testContent(1000)
	f.gated = true
	dir := t.TempDir()

	_, err := f.client("").Download(context.Background(), DownloadRequest{Repo: "o/n", Path: modelPath, Dir: dir})
	var e *Error
	if !errors.As(err, &e) || !errors.Is(err, ErrTokenRequired) || e.StatusCode != 401 || !strings.Contains(err.Error(), "access token") {
		t.Fatalf("no token: %v", err)
	}

	_, err = f.client("hf_secret").Download(context.Background(), DownloadRequest{Repo: "o/n", Path: modelPath, Dir: dir})
	if !errors.Is(err, ErrAccessDenied) || !strings.Contains(err.Error(), "http://hub.test/o/n") || strings.Contains(err.Error(), "hf_secret") {
		t.Fatalf("token without access: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("files or folders were created: %v", entries)
	}
	if _, cdn := f.requests(); len(cdn) != 0 {
		t.Errorf("cdn contacted: %+v", cdn)
	}
}

func TestDownloadMissingFile(t *testing.T) {
	f := newFakeHub(t)
	_, err := f.client("").Download(context.Background(), DownloadRequest{Repo: "o/n", Path: "nope.gguf", Dir: t.TempDir()})
	if !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), `"nope.gguf"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestDownloadExistingFile(t *testing.T) {
	f := newFakeHub(t)
	content := testContent(100 << 10)
	f.lfs[modelPath] = content
	dir := t.TempDir()
	dest := filepath.Join(dir, "m.gguf")
	c := f.client("")
	req := DownloadRequest{Repo: "o/n", Path: modelPath, Dir: dir, Name: "m.gguf"}

	// Identical: nothing to download.
	if err := os.WriteFile(dest, content, 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := c.Download(context.Background(), req)
	if err != nil || !res.AlreadyPresent || !res.Verified {
		t.Fatalf("identical: %+v %v", res, err)
	}

	// Different: refused, and left alone.
	if err := os.WriteFile(dest, []byte("my own notes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Download(context.Background(), req); !errors.Is(err, ErrFileExists) {
		t.Fatalf("different: %v", err)
	}
	if string(readFile(t, dest)) != "my own notes" {
		t.Error("the existing file was changed")
	}
	if _, cdn := f.requests(); len(cdn) != 0 {
		t.Errorf("downloaded although the answer was known: %+v", cdn)
	}

	// Different, overwrite allowed: replaced.
	req.Overwrite = true
	res, err = c.Download(context.Background(), req)
	if err != nil || res.AlreadyPresent || !res.Verified {
		t.Fatalf("overwrite: %+v %v", res, err)
	}
	if !bytes.Equal(readFile(t, dest), content) {
		t.Error("content differs after overwrite")
	}
}

func TestDownloadChecksDiskSpace(t *testing.T) {
	f := newFakeHub(t)
	f.lfs[modelPath] = testContent(1000)
	dir := t.TempDir()
	defer func(old func(string) (uint64, bool)) { diskFree = old }(diskFree)
	diskFree = func(string) (uint64, bool) { return 10 << 20, true }

	_, err := f.client("").Download(context.Background(), DownloadRequest{Repo: "o/n", Path: modelPath, Dir: dir, Name: "m.gguf"})
	if !errors.Is(err, ErrInsufficientSpace) || !strings.Contains(err.Error(), "free") {
		t.Fatalf("err = %v", err)
	}
	assertMissing(t, filepath.Join(dir, "m.gguf"), filepath.Join(dir, "m.gguf"+partSuffix))
}

func TestDownloadSmallFileServedByHub(t *testing.T) {
	f := newFakeHub(t)
	f.small["README.md"] = []byte("# hello\n")
	dir := t.TempDir()
	res, err := f.client("hf_secret").Download(context.Background(), DownloadRequest{Repo: "o/n", Path: "README.md", Dir: dir, Name: "docs/readme.md"})
	if err != nil {
		t.Fatal(err)
	}
	// The ETag of a non-LFS file is a git blob id, not a sha256, so there
	// is nothing to verify against.
	if res.Verified || res.Size != 8 || res.Path != filepath.Join(dir, "docs", "readme.md") {
		t.Errorf("result = %+v", res)
	}
	if string(readFile(t, res.Path)) != "# hello\n" {
		t.Error("content differs")
	}

	// Without a checksum an existing file cannot be proven identical.
	_, err = f.client("").Download(context.Background(), DownloadRequest{Repo: "o/n", Path: "README.md", Dir: dir, Name: "docs/readme.md"})
	if !errors.Is(err, ErrFileExists) {
		t.Errorf("err = %v", err)
	}
}

func TestDownloadValidatesInput(t *testing.T) {
	c := &Client{BaseURL: "http://127.0.0.1:1"}
	cases := []struct {
		req  DownloadRequest
		kind error
	}{
		{DownloadRequest{Repo: "bad", Path: "x.gguf", Dir: "d"}, ErrInvalidRepo},
		{DownloadRequest{Repo: "o/n", Revision: "../x", Path: "x.gguf", Dir: "d"}, ErrInvalidRevision},
		{DownloadRequest{Repo: "o/n", Path: "x.gguf"}, ErrInvalidArgument},
		{DownloadRequest{Repo: "o/n", Path: "x.gguf", Dir: "d", SHA256: "abc"}, ErrInvalidArgument},
	}
	for _, tc := range cases {
		if _, err := c.Download(context.Background(), tc.req); !errors.Is(err, tc.kind) {
			t.Errorf("%+v: err = %v", tc.req, err)
		}
	}
	if _, err := (&Client{BaseURL: "ftp://x"}).Download(context.Background(), DownloadRequest{}); err == nil {
		t.Error("ftp base URL accepted")
	}
}

func TestNetworkErrorHidesURL(t *testing.T) {
	c := &Client{BaseURL: "http://127.0.0.1:1", Token: "hf_secret"}
	_, err := c.Download(context.Background(), DownloadRequest{Repo: "o/n", Path: "x.gguf", Dir: t.TempDir()})
	if !errors.Is(err, ErrNetwork) || strings.Contains(err.Error(), "resolve") || strings.Contains(err.Error(), "hf_secret") {
		t.Errorf("err = %v", err)
	}
}

func TestParseContentRange(t *testing.T) {
	if s, total, ok := parseContentRange("bytes 100-199/200"); !ok || s != 100 || total != 200 {
		t.Errorf("got %d %d %v", s, total, ok)
	}
	if s, total, ok := parseContentRange("bytes 5-9/*"); !ok || s != 5 || total != -1 {
		t.Errorf("got %d %d %v", s, total, ok)
	}
	for _, bad := range []string{"", "bytes */200", "items 1-2/3", "bytes -1-2/3", "bytes 1-2"} {
		if _, _, ok := parseContentRange(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}
