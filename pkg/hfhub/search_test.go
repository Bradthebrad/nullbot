package hfhub

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSearchSendsOptionsAndParsesResults(t *testing.T) {
	f := newFakeHub(t)
	f.api.HandleFunc("GET /api/models", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		checks := map[string]string{
			"search": "qwen", "author": "bartowski", "sort": "trendingScore", "direction": "-1", "limit": "5",
		}
		for k, want := range checks {
			if got := q.Get(k); got != want {
				t.Errorf("query %s = %q, want %q", k, got, want)
			}
		}
		if got := q["filter"]; !slices.Equal(got, []string{"gguf", "text-generation"}) {
			t.Errorf("filter = %v", got)
		}
		for _, field := range []string{"downloads", "likes", "lastModified", "tags", "pipeline_tag", "gated", "private"} {
			if !slices.Contains(q["expand"], field) {
				t.Errorf("expand is missing %s: %v", field, q["expand"])
			}
		}
		if got := r.Header.Get("Authorization"); got != "Bearer hf_secret" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != "nullbot-test" {
			t.Errorf("User-Agent = %q", got)
		}
		fmt.Fprint(w, `[
		  {"_id":"1","id":"bartowski/Qwen3-8B-GGUF","author":"bartowski","downloads":1234,"likes":56,
		   "trendingScore":7,"lastModified":"2025-01-02T03:04:05.000Z","createdAt":"2024-12-01T00:00:00.000Z",
		   "tags":["gguf","text-generation"],"pipeline_tag":"text-generation","library_name":"gguf",
		   "gated":false,"private":false},
		  {"modelId":"meta-llama/Llama-3.1-8B","gated":"manual","downloads":10,"lastModified":"not a date"},
		  {"id":"org/secret","private":true,"gated":true}
		]`)
	})
	c := f.client("  hf_secret\n") // pasted tokens often carry whitespace
	c.UserAgent = "nullbot-test"

	got, err := c.Search(context.Background(), SearchOptions{
		Query: "qwen", Author: "bartowski", Filter: []string{"gguf", " ", "text-generation"},
		Sort: SortTrending, Limit: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d results", len(got))
	}
	m := got[0]
	if m.ID != "bartowski/Qwen3-8B-GGUF" || m.Downloads != 1234 || m.Likes != 56 || m.PipelineTag != "text-generation" ||
		m.LibraryName != "gguf" || m.Gated != "" || m.NeedsToken() || !slices.Equal(m.Tags, []string{"gguf", "text-generation"}) {
		t.Errorf("first result = %+v", m)
	}
	if want := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC); !m.LastModified.Equal(want) {
		t.Errorf("LastModified = %v", m.LastModified)
	}
	if m := got[1]; m.ID != "meta-llama/Llama-3.1-8B" || m.Author != "meta-llama" || m.Gated != "manual" || !m.NeedsToken() || !m.LastModified.IsZero() {
		t.Errorf("second result = %+v", m)
	}
	if m := got[2]; !m.Private || m.Gated == "" || !m.NeedsToken() {
		t.Errorf("third result = %+v", m)
	}
}

func TestSearchDefaultsAndBadOptions(t *testing.T) {
	f := newFakeHub(t)
	f.api.HandleFunc("GET /api/models", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("sort") != "downloads" || q.Get("limit") != "100" || r.Header.Get("Authorization") != "" {
			t.Errorf("query = %v, auth = %q", q, r.Header.Get("Authorization"))
		}
		fmt.Fprint(w, `[]`)
	})
	c := f.client("")
	if _, err := c.Search(context.Background(), SearchOptions{Limit: 5000}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Search(context.Background(), SearchOptions{Sort: "random"}); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("bad sort: %v", err)
	}
	if _, err := c.Search(context.Background(), SearchOptions{Author: "../x"}); !errors.Is(err, ErrInvalidRepo) {
		t.Errorf("bad author: %v", err)
	}
}

func TestSearchErrors(t *testing.T) {
	f := newFakeHub(t)
	status := http.StatusTooManyRequests
	f.api.HandleFunc("GET /api/models", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		http.Error(w, `{"error":"slow down"}`, status)
	})
	c := f.client("hf_secret")
	_, err := c.Search(context.Background(), SearchOptions{})
	var e *Error
	if !errors.As(err, &e) || !errors.Is(err, ErrRateLimited) || e.RetryAfter != 30*time.Second || !strings.Contains(err.Error(), "30 seconds") {
		t.Fatalf("rate limit: %v (%+v)", err, e)
	}

	status = http.StatusUnauthorized
	_, err = c.Search(context.Background(), SearchOptions{})
	if !errors.Is(err, ErrTokenInvalid) || strings.Contains(err.Error(), "hf_secret") {
		t.Errorf("bad token: %v", err)
	}
	status = http.StatusBadGateway
	if _, err = c.Search(context.Background(), SearchOptions{}); !errors.Is(err, ErrServer) {
		t.Errorf("server error: %v", err)
	}
}

func TestFilesFollowsPaginationAndKeepsRevisionEscaped(t *testing.T) {
	f := newFakeHub(t)
	var pages []string
	f.api.HandleFunc("GET /api/models/{owner}/{name}/tree/{rev}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("owner") != "o" || r.PathValue("name") != "n" || r.PathValue("rev") != "refs/pr/1" {
			t.Errorf("path values: %s", r.URL.EscapedPath())
		}
		if !strings.Contains(r.URL.EscapedPath(), "/tree/refs%2Fpr%2F1") {
			t.Errorf("revision not escaped as one segment: %s", r.URL.EscapedPath())
		}
		if r.URL.Query().Get("recursive") != "true" || r.Header.Get("Authorization") != "Bearer hf_secret" {
			t.Errorf("query %v, auth %q", r.URL.Query(), r.Header.Get("Authorization"))
		}
		cursor := r.URL.Query().Get("cursor")
		pages = append(pages, cursor)
		switch cursor {
		case "":
			w.Header().Set("Link", `<http://hub.test/api/models/o/n/tree/refs%2Fpr%2F1?recursive=true&cursor=page2>; rel="next"`)
			fmt.Fprint(w, `[
			  {"type":"directory","oid":"d1","size":0,"path":"Q8_0"},
			  {"type":"file","oid":"b1","size":135,"path":"Q8_0/m-Q8_0-00001-of-00002.gguf",
			   "lfs":{"oid":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","size":5000,"pointerSize":135}},
			  {"type":"file","oid":"b2","size":42,"path":"README.md"}
			]`)
		case "page2":
			fmt.Fprint(w, `[
			  {"type":"file","oid":"b3","size":135,"path":"Q8_0/m-Q8_0-00002-of-00002.gguf",
			   "lfs":{"oid":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","size":3000,"pointerSize":135}}
			]`)
		default:
			t.Errorf("unexpected cursor %q", cursor)
		}
	})

	files, err := f.client("hf_secret").Files(context.Background(), "o/n", "refs/pr/1")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(pages, []string{"", "page2"}) {
		t.Errorf("pages fetched: %q", pages)
	}
	want := []File{
		{Path: "Q8_0/m-Q8_0-00001-of-00002.gguf", Size: 5000, SHA256: strings.Repeat("a", 64), BlobID: "b1", LFS: true},
		{Path: "Q8_0/m-Q8_0-00002-of-00002.gguf", Size: 3000, SHA256: strings.Repeat("b", 64), BlobID: "b3", LFS: true},
		{Path: "README.md", Size: 42, BlobID: "b2"},
	}
	if !slices.Equal(files, want) {
		t.Errorf("files =\n%+v\nwant\n%+v", files, want)
	}
	models := GroupGGUF(files)
	if len(models) != 1 || !models[0].Complete || models[0].Size != 8000 || models[0].Quant != "Q8_0" {
		t.Errorf("grouped: %+v", models)
	}
}

func TestFilesRefusesNextLinkToAnotherHost(t *testing.T) {
	f := newFakeHub(t)
	f.api.HandleFunc("GET /api/models/{owner}/{name}/tree/{rev}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", `<http://cdn.hub.test/steal?cursor=x>; rel="next"`)
		fmt.Fprint(w, `[]`)
	})
	_, err := f.client("hf_secret").Files(context.Background(), "o/n", "")
	if !errors.Is(err, ErrBadResponse) {
		t.Fatalf("err = %v", err)
	}
	if _, cdn := f.requests(); len(cdn) != 0 {
		t.Errorf("the other host was contacted: %+v", cdn)
	}
}

func TestFilesValidatesBeforeAnyRequest(t *testing.T) {
	f := newFakeHub(t)
	c := f.client("")
	for _, repo := range []string{"", "noslash", "o/n/extra", "../n", "o/..", "o/n?x=1", "o/-n", "o/n.", "o/a--b", "o/" + strings.Repeat("x", 97)} {
		if _, err := c.Files(context.Background(), repo, ""); !errors.Is(err, ErrInvalidRepo) {
			t.Errorf("repo %q: %v", repo, err)
		}
	}
	for _, rev := range []string{"..", "a/../b", "/main", "main/", "a//b", "-x", "a b", "a?b", "a%2fb", ".hidden"} {
		if _, err := c.Files(context.Background(), "o/n", rev); !errors.Is(err, ErrInvalidRevision) {
			t.Errorf("revision %q: %v", rev, err)
		}
	}
	if hub, _ := f.requests(); len(hub) != 0 {
		t.Errorf("requests were made: %+v", hub)
	}
}

func TestValidRepoIDsAndRevisions(t *testing.T) {
	for _, id := range []string{"Qwen/Qwen3-8B-GGUF", "bartowski/Meta-Llama-3.1-8B-Instruct-GGUF", "a/b", "ggml-org/gemma_3.gguf", "_x/y_"} {
		if err := ValidateRepoID(id); err != nil {
			t.Errorf("%q: %v", id, err)
		}
	}
	for _, rev := range []string{"main", "v1.0", "refs/pr/12", "refs/convert/parquet", testCommit} {
		if err := ValidateRevision(rev); err != nil {
			t.Errorf("%q: %v", rev, err)
		}
	}
}

func TestNextLink(t *testing.T) {
	cases := map[string]string{
		`<https://huggingface.co/api/x?cursor=a>; rel="next"`:              "https://huggingface.co/api/x?cursor=a",
		`<https://h/prev>; rel="prev", <https://h/next?a=1,2>; rel="next"`: "https://h/next?a=1,2",
		`<https://h/a>; rel="first last"`:                                  "",
		`<https://h/b>; title="x"; REL=next`:                               "https://h/b",
		`garbage`:                                                          "",
		`<https://h/c>; rel="prev next"`:                                   "https://h/c",
		`<https://h/d>;rel=next`:                                           "https://h/d",
		`<https://h/e>; rel="nextpage"`:                                    "",
		`<https://h/f>; rel="prev", <https://h/g>; anchor="#x"; rel="next"; foo="bar"`: "https://h/g",
	}
	for header, want := range cases {
		if got := nextLink([]string{header}); got != want {
			t.Errorf("nextLink(%q) = %q, want %q", header, got, want)
		}
	}
}

func TestParseRetryAfterDate(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if got := parseRetryAfter(now.Add(90*time.Second).Format(http.TimeFormat), now); got != 90*time.Second {
		t.Errorf("date form = %v", got)
	}
	if got := parseRetryAfter("999999999", now); got != 24*time.Hour {
		t.Errorf("cap = %v", got)
	}
	if got := parseRetryAfter("soon", now); got != 0 {
		t.Errorf("garbage = %v", got)
	}
}

func TestClientStringHidesToken(t *testing.T) {
	c := Client{Token: "hf_secret"}
	type config struct{ Hub Client }
	for _, s := range []string{fmt.Sprint(c), fmt.Sprintf("%+v", &c), fmt.Sprintf("%#v", c), fmt.Sprintf("%+v", config{c})} {
		if strings.Contains(s, "hf_secret") {
			t.Errorf("token leaked: %s", s)
		}
	}
}

func TestBadTokenCharactersRejectedWithoutEcho(t *testing.T) {
	_, err := (&Client{Token: "hf_se cret"}).Search(context.Background(), SearchOptions{})
	if !errors.Is(err, ErrTokenInvalid) || strings.Contains(err.Error(), "cret") {
		t.Errorf("err = %v", err)
	}
}
