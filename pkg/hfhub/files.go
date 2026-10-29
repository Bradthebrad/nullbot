package hfhub

import (
	"context"
	"net/url"
	"sort"
	"strings"
)

const (
	maxTreePages = 500
	maxTreeBody  = 32 << 20
)

// File is one file in a repo.
type File struct {
	Path   string // path inside the repo, '/'-separated
	Size   int64  // bytes
	SHA256 string // lowercase hex sha256 of the content, known for LFS files
	BlobID string // git blob id
	LFS    bool   // stored with Git LFS (all model weights are)
}

// treeEntry is one item of /api/models/{repo}/tree/{revision}.
type treeEntry struct {
	Type string `json:"type"` // "file" or "directory"
	Path string `json:"path"`
	Size int64  `json:"size"`
	OID  string `json:"oid"`
	LFS  *struct {
		OID    string `json:"oid"`
		SHA256 string `json:"sha256"` // older answers named the digest this way
		Size   int64  `json:"size"`
	} `json:"lfs"`
}

// Files lists every file in repo at revision ("" means main), with sizes and
// LFS checksums, using the recursive tree API. Large repos are paged by the
// Hub with a Link: <...>; rel="next" header, which Files follows.
func (c *Client) Files(ctx context.Context, repo, revision string) ([]File, error) {
	s, err := c.session()
	if err != nil {
		return nil, err
	}
	rev, err := repoAndRevision(repo, revision)
	if err != nil {
		return nil, err
	}
	t := target{repo: repo, revision: rev}
	what := "the file list of " + repo

	next := s.endpoint("/api/models/"+repo+"/tree/"+url.PathEscape(rev), url.Values{"recursive": {"true"}})
	seen := map[string]bool{}
	var files []File
	for page := 0; next != nil; page++ {
		if page >= maxTreePages || seen[next.String()] {
			return nil, newError(ErrBadResponse, "the file list of %s did not finish (too many pages); try again later", repo)
		}
		seen[next.String()] = true

		var entries []treeEntry
		resp, err := s.getJSON(ctx, next, t, what, maxTreeBody, &entries)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.Type == "file" && e.Path != "" {
				files = append(files, e.file())
			}
		}

		next = nil
		if link := nextLink(resp.Header.Values("Link")); link != "" {
			u, err := resp.Request.URL.Parse(link)
			// The next page gets the token too, so it has to stay on the Hub.
			if err != nil || !sameOrigin(u, s.base) {
				return nil, newError(ErrBadResponse, "Hugging Face pointed the file list of %s at another server; stopped so the access token is not sent there", repo)
			}
			next = u
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func (e treeEntry) file() File {
	f := File{Path: e.Path, Size: e.Size, BlobID: e.OID}
	if e.LFS != nil {
		f.LFS = true
		if e.LFS.Size > 0 {
			f.Size = e.LFS.Size
		}
		for _, d := range []string{e.LFS.OID, e.LFS.SHA256} {
			if d = strings.ToLower(d); isSHA256(d) {
				f.SHA256 = d
				break
			}
		}
	}
	return f
}

// nextLink returns the target of the rel="next" link in RFC 8288 Link
// header values, or "".
func nextLink(values []string) string {
	for _, v := range values {
		for {
			start := strings.IndexByte(v, '<')
			end := strings.IndexByte(v, '>')
			if start < 0 || end < start {
				break
			}
			ref := v[start+1 : end]
			v = v[end+1:]
			// The parameters of this link run up to the next '<'.
			params := v
			if i := strings.IndexByte(v, '<'); i >= 0 {
				params = v[:i]
			}
			for _, p := range strings.Split(params, ";") {
				key, val, ok := strings.Cut(strings.TrimSpace(p), "=")
				if !ok || !strings.EqualFold(strings.TrimSpace(key), "rel") {
					continue
				}
				val = strings.Trim(strings.TrimSpace(val), `",`)
				for _, rel := range strings.Fields(val) {
					if strings.EqualFold(rel, "next") {
						return ref
					}
				}
			}
		}
	}
	return ""
}

func isSHA256(s string) bool { return len(s) == 64 && isHex(s) }

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !('0' <= c && c <= '9') && !('a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}
