// Package hfhub searches Hugging Face for local models and downloads model
// files safely.
//
// It is what NullBot uses to let the user find a GGUF build of a model and
// install it: Search finds repos, Files lists what a repo holds, GroupGGUF and
// Recommend turn that list into choices, and Download fetches a file.
//
// Downloads resume from a ".part" file, are checked against the sha256 the Hub
// publishes for LFS files, are renamed into place only once complete, and can
// never write outside the destination folder. The access token is sent only
// to the Hub's own host, never to the CDN a download is redirected to, and
// never appears in an error message.
//
// A typical install:
//
//	c := &hfhub.Client{Token: token} // token may be empty
//	files, err := c.Files(ctx, "Qwen/Qwen3-8B-GGUF", "")
//	model, ok := hfhub.Recommend(hfhub.GroupGGUF(files), hfhub.Preference{MaxSize: freeRAM})
//	for _, f := range model.Files { // one file, or every part of a split build
//		_, err = c.Download(ctx, hfhub.DownloadRequest{
//			Repo: "Qwen/Qwen3-8B-GGUF", Path: f.Path, Dir: modelsDir,
//			SHA256: f.SHA256, Size: f.Size, Progress: show,
//		})
//	}
package hfhub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const (
	// DefaultBaseURL is the public Hugging Face Hub.
	DefaultBaseURL = "https://huggingface.co"
	// DefaultRevision is the branch used when a call names none.
	DefaultRevision = "main"

	defaultUserAgent = "nullbot-hfhub/1.0"
	maxRedirects     = 10
)

// Client talks to the Hugging Face Hub. The zero value is ready to use against
// the public Hub without a token.
type Client struct {
	// BaseURL is the Hub address; empty means DefaultBaseURL. A path prefix
	// (for a mirror) is kept.
	BaseURL string
	// Token is a Hugging Face access token. It is only needed for gated and
	// private repos. It is sent only to BaseURL's host.
	Token string
	// HTTP makes the requests; nil means http.DefaultClient. It is copied per
	// call, never modified, and its CheckRedirect still runs after ours.
	// Leave its Timeout at zero if it is used for downloads: a model file
	// takes longer than any sensible timeout. Cancel through the context.
	HTTP *http.Client
	// UserAgent identifies the caller to the Hub; empty means a default.
	UserAgent string
}

// String hides the token so a Client printed with %v or %+v (for example in
// a log line or inside a config struct) cannot leak it. It has a value
// receiver on purpose: that way both Client and *Client values are covered.
func (c Client) String() string {
	token := "none"
	if c.Token != "" {
		token = "(hidden)"
	}
	return fmt.Sprintf("hfhub.Client{BaseURL: %q, Token: %s, UserAgent: %q}", c.BaseURL, token, c.UserAgent)
}

// GoString hides the token from %#v for the same reason.
func (c Client) GoString() string { return c.String() }

// session is the per-call view of a Client: the parsed base URL and the
// cleaned token, checked once.
type session struct {
	c     *Client
	base  *url.URL
	token string
}

func (c *Client) session() (*session, error) {
	raw := strings.TrimSpace(c.BaseURL)
	if raw == "" {
		raw = DefaultBaseURL
	}
	base, err := url.Parse(raw)
	if err != nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" || base.User != nil {
		return nil, errors.New("the Hugging Face address must be a plain http(s) URL such as https://huggingface.co")
	}
	base.RawQuery, base.Fragment = "", ""
	base.Path = strings.TrimRight(base.Path, "/")
	base.RawPath = strings.TrimRight(base.RawPath, "/")

	// Tokens are pasted by people; stray spaces are common, other control
	// or non-ASCII characters mean the paste went wrong.
	token := strings.TrimSpace(c.Token)
	for i := 0; i < len(token); i++ {
		if token[i] <= ' ' || token[i] >= 0x7f {
			return nil, newError(ErrTokenInvalid, "the Hugging Face access token contains spaces or characters that cannot be sent; copy it again from your Hugging Face settings")
		}
	}
	return &session{c: c, base: base, token: token}, nil
}

// endpoint builds a URL on the Hub from an already escaped path. Setting
// RawPath keeps escapes such as %2F in "refs%2Fpr%2F1" intact on the wire.
func (s *session) endpoint(escapedPath string, query url.Values) *url.URL {
	u := *s.base
	u.RawPath = s.base.EscapedPath() + escapedPath
	u.Path, _ = url.PathUnescape(u.RawPath) // built from escaped parts, so it always unescapes
	u.RawQuery = ""
	if query != nil {
		u.RawQuery = query.Encode()
	}
	return &u
}

func (s *session) newRequest(ctx context.Context, method string, u *url.URL) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
	if err != nil {
		return nil, err
	}
	ua := s.c.UserAgent
	if ua == "" {
		ua = defaultUserAgent
	}
	req.Header.Set("User-Agent", ua)
	if s.token != "" && sameOrigin(u, s.base) {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	return req, nil
}

// client returns a copy of the caller's http.Client whose redirects drop the
// Authorization header as soon as they leave the Hub's origin. Go's own rule
// keeps the header for subdomains, and the Hub's CDN can live on one (such as
// cdn-lfs.huggingface.co), so we use the stricter rule: same scheme and host
// only. With stayOnHub, a redirect off the Hub is not followed at all, which
// lets a HEAD read the Hub's own headers about the file.
func (s *session) client(stayOnHub bool) *http.Client {
	hc := s.c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	cp := *hc
	inner := hc.CheckRedirect
	cp.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return errors.New("too many redirects")
		}
		if !sameOrigin(req.URL, s.base) {
			if stayOnHub {
				return http.ErrUseLastResponse
			}
			req.Header.Del("Authorization")
		}
		if inner != nil {
			return inner(req, via)
		}
		return nil
	}
	return &cp
}

func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

// getJSON fetches u and decodes a JSON answer of at most limit bytes. The
// limit keeps a broken or hostile server from filling memory.
func (s *session) getJSON(ctx context.Context, u *url.URL, t target, what string, limit int64, v any) (*http.Response, error) {
	req, err := s.newRequest(ctx, http.MethodGet, u)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := s.client(false).Do(req)
	if err != nil {
		return nil, networkError(ctx, what, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, s.responseError(resp, t)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, networkError(ctx, what, err)
	}
	if int64(len(data)) > limit {
		return nil, newError(ErrBadResponse, "Hugging Face sent a larger answer than expected for %s; try a narrower request", what)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return nil, newError(ErrBadResponse, "Hugging Face sent an answer for %s that could not be read; try again later", what)
	}
	return resp, nil
}
