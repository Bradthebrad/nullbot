package hfhub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const testCommit = "0123456789abcdef0123456789abcdef01234567"

// fakeHub is a Hub on http://hub.test and its CDN on http://cdn.hub.test.
// The CDN is a subdomain on purpose: Go's default redirect policy would
// forward the Authorization header there, ours must not.
type fakeHub struct {
	t   *testing.T
	hub *httptest.Server
	cdn *httptest.Server
	api *http.ServeMux // /api/... routes, added by each test
	mux *http.ServeMux // file routes

	mu          sync.Mutex
	lfs         map[string][]byte // served through a redirect to the CDN
	small       map[string][]byte // served by the Hub itself
	gated       bool              // 401 without a token, 403 with one
	corrupt     bool              // the CDN flips a byte
	ignoreRange bool              // the CDN always sends the whole file
	stall       bool              // the CDN sends 64 KiB then waits for the client to go away
	hubReqs     []seenRequest
	cdnReqs     []seenRequest
}

type seenRequest struct {
	method, path, auth, rng string
}

func newFakeHub(t *testing.T) *fakeHub {
	t.Helper()
	f := &fakeHub{t: t, lfs: map[string][]byte{}, small: map[string][]byte{}, api: http.NewServeMux(), mux: http.NewServeMux()}
	f.mux.HandleFunc("GET /{owner}/{name}/resolve/{rev}/{path...}", f.resolve)
	f.hub = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.record(&f.hubReqs, r)
		if strings.HasPrefix(r.URL.Path, "/api/") {
			f.api.ServeHTTP(w, r)
			return
		}
		f.mux.ServeHTTP(w, r)
	}))
	f.cdn = httptest.NewServer(http.HandlerFunc(f.serveCDN))
	t.Cleanup(func() {
		f.cdn.Close()
		f.hub.Close()
	})
	return f
}

func (f *fakeHub) record(list *[]seenRequest, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	*list = append(*list, seenRequest{r.Method, r.URL.EscapedPath(), r.Header.Get("Authorization"), r.Header.Get("Range")})
}

func (f *fakeHub) requests() (hub, cdn []seenRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]seenRequest(nil), f.hubReqs...), append([]seenRequest(nil), f.cdnReqs...)
}

// client returns a Client whose transport sends hub.test and cdn.hub.test
// to the two test servers.
func (f *fakeHub) client(token string) *Client {
	hosts := map[string]string{
		"hub.test":     f.hub.Listener.Addr().String(),
		"cdn.hub.test": f.cdn.Listener.Addr().String(),
	}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, _ := net.SplitHostPort(addr)
			to, ok := hosts[host]
			if !ok {
				return nil, fmt.Errorf("test transport: unexpected host %s", host)
			}
			var d net.Dialer
			return d.DialContext(ctx, network, to)
		},
	}
	f.t.Cleanup(tr.CloseIdleConnections)
	return &Client{BaseURL: "http://hub.test", Token: token, HTTP: &http.Client{Transport: tr}}
}

func sum256(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func (f *fakeHub) resolve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	gated, lfs, small := f.gated, f.lfs, f.small
	f.mu.Unlock()
	if gated {
		w.Header().Set("X-Error-Code", "GatedRepo")
		status := http.StatusUnauthorized
		if r.Header.Get("Authorization") != "" {
			status = http.StatusForbidden
		}
		http.Error(w, "Access to model o/n is restricted.", status)
		return
	}
	p := r.PathValue("path")
	w.Header().Set("X-Repo-Commit", testCommit)
	if content, ok := lfs[p]; ok {
		sum := sum256(content)
		w.Header().Set("X-Linked-Etag", `"`+sum+`"`)
		w.Header().Set("X-Linked-Size", strconv.Itoa(len(content)))
		w.Header().Set("ETag", `"`+sum+`"`)
		http.Redirect(w, r, "http://cdn.hub.test/blobs/"+sum+"?X-Amz-Signature=signed", http.StatusFound)
		return
	}
	if content, ok := small[p]; ok {
		w.Header().Set("ETag", `"`+sum256(content)[:40]+`"`) // a git blob id, not a sha256
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(content)
		}
		return
	}
	w.Header().Set("X-Error-Code", "EntryNotFound")
	http.Error(w, "Entry not found", http.StatusNotFound)
}

func (f *fakeHub) serveCDN(w http.ResponseWriter, r *http.Request) {
	f.record(&f.cdnReqs, r)
	f.mu.Lock()
	var content []byte
	for _, c := range f.lfs {
		if r.URL.Path == "/blobs/"+sum256(c) {
			content = bytes.Clone(c)
		}
	}
	corrupt, ignoreRange, stall := f.corrupt, f.ignoreRange, f.stall
	f.mu.Unlock()
	if content == nil {
		http.NotFound(w, r)
		return
	}
	if corrupt {
		content[len(content)/2] ^= 0xff
	}
	switch {
	case stall:
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		_, _ = w.Write(content[:64<<10])
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	case ignoreRange:
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		_, _ = w.Write(content)
	default:
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(content))
	}
}
