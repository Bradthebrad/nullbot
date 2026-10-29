package hfhub

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Sort orders for SearchOptions.Sort. Results come in descending order:
// most downloaded, most liked, newest or most trending first.
const (
	SortDownloads    = "downloads"
	SortLikes        = "likes"
	SortLastModified = "lastModified"
	SortTrending     = "trending"
)

const (
	defaultSearchLimit = 20
	maxSearchLimit     = 100
	maxQueryLen        = 256
	maxSearchBody      = 16 << 20
)

// searchFields are asked for with expand= so every result carries the same
// fields; without it the Hub leaves out lastModified and gated.
var searchFields = []string{
	"author", "downloads", "likes", "lastModified", "createdAt", "tags",
	"pipeline_tag", "library_name", "gated", "private", "trendingScore",
}

// SearchOptions narrows a model search. All fields are optional.
type SearchOptions struct {
	Query  string   // free text matched against model ids
	Filter []string // tags every result must have, such as "gguf"
	Author string   // user or organization, such as "bartowski"
	Sort   string   // one of the Sort constants; empty means SortDownloads
	Limit  int      // results to return, 1 to 100; 0 means 20
}

// ModelInfo is one search result.
type ModelInfo struct {
	ID            string // owner/name
	Author        string
	PipelineTag   string // such as "text-generation"
	LibraryName   string // such as "gguf" or "transformers"
	Tags          []string
	Downloads     int64 // downloads over the last 30 days
	Likes         int64
	TrendingScore float64
	LastModified  time.Time
	CreatedAt     time.Time
	Private       bool
	// Gated is "" for open models, otherwise how the owner grants access:
	// "auto" or "manual" on huggingface.co today.
	Gated string
}

// NeedsToken reports whether downloading from this repo needs an access
// token, so a UI can say so before the user picks a file.
func (m ModelInfo) NeedsToken() bool { return m.Private || m.Gated != "" }

// UnmarshalJSON reads one entry of /api/models. It is lenient about the
// fields that vary: "gated" is false or a string, older answers use
// "modelId" instead of "id", and a malformed date leaves the time zero
// rather than failing the whole search.
func (m *ModelInfo) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID            string          `json:"id"`
		ModelID       string          `json:"modelId"`
		Author        string          `json:"author"`
		PipelineTag   string          `json:"pipeline_tag"`
		LibraryName   string          `json:"library_name"`
		Tags          []string        `json:"tags"`
		Downloads     int64           `json:"downloads"`
		Likes         int64           `json:"likes"`
		TrendingScore float64         `json:"trendingScore"`
		LastModified  string          `json:"lastModified"`
		CreatedAt     string          `json:"createdAt"`
		Private       bool            `json:"private"`
		Gated         json.RawMessage `json:"gated"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*m = ModelInfo{
		ID:            raw.ID,
		Author:        raw.Author,
		PipelineTag:   raw.PipelineTag,
		LibraryName:   raw.LibraryName,
		Tags:          raw.Tags,
		Downloads:     raw.Downloads,
		Likes:         raw.Likes,
		TrendingScore: raw.TrendingScore,
		LastModified:  parseTime(raw.LastModified),
		CreatedAt:     parseTime(raw.CreatedAt),
		Private:       raw.Private,
		Gated:         parseGated(raw.Gated),
	}
	if m.ID == "" {
		m.ID = raw.ModelID
	}
	if m.Author == "" {
		m.Author, _, _ = strings.Cut(m.ID, "/")
	}
	return nil
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func parseGated(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	var s string
	switch {
	case len(raw) == 0, bytes.Equal(raw, []byte("false")), bytes.Equal(raw, []byte("null")):
		return ""
	case bytes.Equal(raw, []byte("true")):
		return "true"
	case json.Unmarshal(raw, &s) == nil:
		return s
	}
	return "true" // an unknown shape: assume gated, so the user is told a token may be needed
}

// Search lists models on the Hub that match opts, using GET /api/models.
func (c *Client) Search(ctx context.Context, opts SearchOptions) ([]ModelInfo, error) {
	s, err := c.session()
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	if text := strings.TrimSpace(opts.Query); text != "" {
		if len(text) > maxQueryLen {
			return nil, newError(ErrInvalidArgument, "the search text is too long; use at most %d characters", maxQueryLen)
		}
		q.Set("search", text)
	}
	if author := strings.TrimSpace(opts.Author); author != "" {
		if !validRepoPart(author) {
			return nil, newError(ErrInvalidRepo, "%q is not a Hugging Face user or organization name", author)
		}
		q.Set("author", author)
	}
	for _, f := range opts.Filter {
		if f = strings.TrimSpace(f); f != "" {
			q.Add("filter", f)
		}
	}
	sort, err := sortParam(opts.Sort)
	if err != nil {
		return nil, err
	}
	q.Set("sort", sort)
	q.Set("direction", "-1")
	limit := opts.Limit
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	limit = min(limit, maxSearchLimit)
	q.Set("limit", strconv.Itoa(limit))
	for _, f := range searchFields {
		q.Add("expand", f)
	}

	var models []ModelInfo
	if _, err := s.getJSON(ctx, s.endpoint("/api/models", q), target{}, "the model search", maxSearchBody, &models); err != nil {
		return nil, err
	}
	if len(models) > limit {
		models = models[:limit]
	}
	return models, nil
}

// sortParam maps our sort names to the Hub's. "trending" is the name the
// website shows; the API field is trendingScore.
func sortParam(sort string) (string, error) {
	switch strings.TrimSpace(sort) {
	case "", SortDownloads:
		return "downloads", nil
	case SortLikes:
		return "likes", nil
	case SortLastModified, "last_modified", "lastmodified":
		return "lastModified", nil
	case SortTrending, "trendingScore", "trending_score":
		return "trendingScore", nil
	}
	return "", newError(ErrInvalidArgument, "%q is not a sort order; use downloads, likes, lastModified or trending", sort)
}
