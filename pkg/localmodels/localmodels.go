// Package localmodels talks to model servers you host: Ollama, llama.cpp's
// server, LM Studio, vLLM and other OpenAI-compatible servers, on this
// computer, another machine on your network or an internal domain.
//
// Chat goes through each server's OpenAI-compatible API (OpenAIBaseURL).
// This package covers what that API does not: whether the server answers,
// which models it has and how large a context each is really run with, and
// for Ollama, pulling and removing models.
package localmodels

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Kind is the server software at an endpoint.
type Kind string

const (
	Ollama   Kind = "ollama"
	LlamaCpp Kind = "llamacpp"
	LMStudio Kind = "lmstudio"
	VLLM     Kind = "vllm"
	OpenAI   Kind = "openai" // any other OpenAI-compatible server
)

// Kinds lists the server kinds, in the order they are offered.
var Kinds = []Kind{Ollama, LlamaCpp, LMStudio, VLLM, OpenAI}

// Label names a kind for people.
func (k Kind) Label() string {
	switch k {
	case Ollama:
		return "Ollama"
	case LlamaCpp:
		return "llama.cpp server"
	case LMStudio:
		return "LM Studio"
	case VLLM:
		return "vLLM"
	case OpenAI:
		return "OpenAI-compatible server"
	}
	return string(k)
}

// Valid reports a known kind.
func (k Kind) Valid() bool {
	for _, known := range Kinds {
		if k == known {
			return true
		}
	}
	return false
}

// DefaultURL is where each kind listens when started with its defaults.
func DefaultURL(k Kind) string {
	switch k {
	case Ollama:
		return "http://localhost:11434"
	case LlamaCpp:
		return "http://localhost:8080"
	case LMStudio:
		return "http://localhost:1234"
	case VLLM:
		return "http://localhost:8000"
	}
	return ""
}

// NormalizeBaseURL checks an address and returns its root, without a
// trailing slash or /v1, which this package adds where it is needed.
// "localhost:11434" becomes "http://localhost:11434".
func NormalizeBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("give the server's address, such as http://localhost:11434")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("%q is not a server address; use one like http://192.168.1.20:11434", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("use an http or https address, not %s", u.Scheme)
	}
	if u.User != nil {
		return "", errors.New("put the key in a secret rather than in the address")
	}
	u.RawQuery, u.Fragment = "", ""
	path := strings.TrimRight(u.Path, "/")
	path = strings.TrimSuffix(path, "/v1")
	u.Path = strings.TrimRight(path, "/")
	return u.String(), nil
}

// IsLocalHost reports an address on this computer, where plain http is
// fine. Elsewhere a key sent over http can be read on the way.
func IsLocalHost(base string) bool {
	u, err := url.Parse(base)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Endpoint is one server.
type Endpoint struct {
	Kind    Kind
	BaseURL string // root, as NormalizeBaseURL returns it
	APIKey  string // optional; sent as a bearer token
	HTTP    *http.Client
}

// OpenAIBaseURL is the base of the server's OpenAI-compatible API.
func (e Endpoint) OpenAIBaseURL() string {
	return strings.TrimRight(e.BaseURL, "/") + "/v1"
}

func (e Endpoint) client() *http.Client {
	if e.HTTP != nil {
		return e.HTTP
	}
	return defaultClient
}

var defaultClient = &http.Client{Timeout: 30 * time.Second}

func (e Endpoint) request(ctx context.Context, method, path string, body any) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(e.BaseURL, "/")+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if e.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.APIKey)
	}
	return req, nil
}

// getJSON fetches path into out. A status other than 200 is an error that
// names it, with the start of what the server said.
func (e Endpoint) getJSON(ctx context.Context, method, path string, body, out any) error {
	req, err := e.request(ctx, method, path, body)
	if err != nil {
		return err
	}
	resp, err := e.client().Do(req)
	if err != nil {
		return e.reachError(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return statusError(resp.StatusCode, data)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%s at %s answered with something that is not JSON", e.Kind.Label(), e.BaseURL)
	}
	return nil
}

func statusError(status int, body []byte) error {
	var parsed struct {
		Error any `json:"error"`
	}
	text := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &parsed) == nil && parsed.Error != nil {
		switch v := parsed.Error.(type) {
		case string:
			text = v
		case map[string]any:
			if m, ok := v["message"].(string); ok {
				text = m
			}
		}
	}
	if len(text) > 300 {
		text = text[:300] + "…"
	}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("the server refused the request (%d); check its API key: %s", status, text)
	case http.StatusNotFound:
		return fmt.Errorf("not found (404): %s", text)
	}
	return fmt.Errorf("the server answered %d: %s", status, text)
}

func (e Endpoint) reachError(err error) error {
	if errors.Is(err, context.Canceled) {
		return err
	}
	return fmt.Errorf("%s at %s is not answering (%v). Start it, or check the address", e.Kind.Label(), e.BaseURL, unwrapURLError(err))
}

func unwrapURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// Status is the result of checking an endpoint.
type Status struct {
	Reachable bool          `json:"reachable"`
	Version   string        `json:"version,omitempty"`
	Error     string        `json:"error,omitempty"`
	Latency   time.Duration `json:"latency_ns,omitempty"`
}

// Check asks the server whether it is there, quickly.
func (e Endpoint) Check(ctx context.Context) Status {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	start := time.Now()
	var err error
	status := Status{}
	switch e.Kind {
	case Ollama:
		var v struct {
			Version string `json:"version"`
		}
		err = e.getJSON(ctx, http.MethodGet, "/api/version", nil, &v)
		status.Version = v.Version
	case LlamaCpp:
		err = e.getJSON(ctx, http.MethodGet, "/health", nil, nil)
	case VLLM:
		var v struct {
			Version string `json:"version"`
		}
		if err = e.getJSON(ctx, http.MethodGet, "/version", nil, &v); err != nil {
			err = e.getJSON(ctx, http.MethodGet, "/v1/models", nil, nil)
		}
		status.Version = v.Version
	default:
		err = e.getJSON(ctx, http.MethodGet, "/v1/models", nil, nil)
	}
	status.Latency = time.Since(start)
	if err != nil {
		status.Error = err.Error()
		return status
	}
	status.Reachable = true
	return status
}

// Model is a model a server offers.
type Model struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	// ContextWindow is the context the server runs the model with, as far
	// as it says; MaxContext is the most the model supports.
	ContextWindow int    `json:"context_window,omitempty"`
	MaxContext    int    `json:"max_context,omitempty"`
	ContextNote   string `json:"context_note,omitempty"`
	Size          int64  `json:"size,omitempty"`
	Parameters    string `json:"parameters,omitempty"`
	Quantization  string `json:"quantization,omitempty"`
	Family        string `json:"family,omitempty"`
	Loaded        bool   `json:"loaded,omitempty"`
	// Capabilities the server reports, such as "tools" and "vision".
	Capabilities []string `json:"capabilities,omitempty"`
	// Embedding marks a model for embeddings only, not chat.
	Embedding bool `json:"embedding,omitempty"`
}

// OllamaDefaultContext is the context Ollama gives a model with no num_ctx
// of its own, unless OLLAMA_CONTEXT_LENGTH says otherwise.
const OllamaDefaultContext = 4096

// Models lists the models the server offers, with the context each runs at.
func (e Endpoint) Models(ctx context.Context) ([]Model, error) {
	var models []Model
	var err error
	switch e.Kind {
	case Ollama:
		models, err = e.ollamaModels(ctx)
	case LlamaCpp:
		models, err = e.llamaCppModels(ctx)
	case LMStudio:
		models, err = e.lmStudioModels(ctx)
		if err != nil {
			models, err = e.openAIModels(ctx)
		}
	default:
		models, err = e.openAIModels(ctx)
	}
	if err != nil {
		return nil, err
	}
	sort.SliceStable(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}

func (e Endpoint) openAIModels(ctx context.Context) ([]Model, error) {
	var resp struct {
		Data []struct {
			ID          string `json:"id"`
			MaxModelLen int    `json:"max_model_len"` // vLLM
			Meta        struct {
				NCtxTrain int   `json:"n_ctx_train"`
				Size      int64 `json:"size"`
				NParams   int64 `json:"n_params"`
			} `json:"meta"` // llama.cpp
		} `json:"data"`
	}
	if err := e.getJSON(ctx, http.MethodGet, "/v1/models", nil, &resp); err != nil {
		return nil, err
	}
	out := make([]Model, 0, len(resp.Data))
	for _, d := range resp.Data {
		if strings.TrimSpace(d.ID) == "" {
			continue
		}
		m := Model{ID: d.ID, MaxContext: d.Meta.NCtxTrain, Size: d.Meta.Size}
		if d.Meta.NParams > 0 {
			m.Parameters = formatParams(d.Meta.NParams)
		}
		if d.MaxModelLen > 0 {
			m.ContextWindow, m.MaxContext = d.MaxModelLen, d.MaxModelLen
		}
		out = append(out, m)
	}
	return out, nil
}

func (e Endpoint) llamaCppModels(ctx context.Context) ([]Model, error) {
	models, err := e.openAIModels(ctx)
	if err != nil {
		return nil, err
	}
	// /props has the context the server was started with (-c).
	var props struct {
		NCtx     int `json:"n_ctx"`
		Settings struct {
			NCtx int `json:"n_ctx"`
		} `json:"default_generation_settings"`
		TotalSlots int `json:"total_slots"`
	}
	if e.getJSON(ctx, http.MethodGet, "/props", nil, &props) == nil {
		n := props.Settings.NCtx
		if n == 0 {
			n = props.NCtx
		}
		for i := range models {
			if n > 0 {
				models[i].ContextWindow = n
				if props.TotalSlots > 1 {
					models[i].ContextNote = fmt.Sprintf("Started with %d slots; each request gets its share of -c", props.TotalSlots)
				}
			}
			models[i].Loaded = true
		}
	}
	return models, nil
}

func (e Endpoint) lmStudioModels(ctx context.Context) ([]Model, error) {
	var resp struct {
		Data []struct {
			ID                  string `json:"id"`
			Type                string `json:"type"`
			Publisher           string `json:"publisher"`
			Arch                string `json:"arch"`
			Quantization        string `json:"quantization"`
			State               string `json:"state"`
			MaxContextLength    int    `json:"max_context_length"`
			LoadedContextLength int    `json:"loaded_context_length"`
		} `json:"data"`
	}
	if err := e.getJSON(ctx, http.MethodGet, "/api/v0/models", nil, &resp); err != nil {
		return nil, err
	}
	out := make([]Model, 0, len(resp.Data))
	for _, d := range resp.Data {
		if strings.TrimSpace(d.ID) == "" {
			continue
		}
		m := Model{ID: d.ID, Family: d.Arch, Quantization: d.Quantization, MaxContext: d.MaxContextLength, Loaded: d.State == "loaded", Embedding: d.Type == "embeddings"}
		if d.Type == "vlm" {
			m.Capabilities = []string{"vision"}
		}
		switch {
		case d.LoadedContextLength > 0:
			m.ContextWindow = d.LoadedContextLength
		case d.MaxContextLength > 0:
			// LM Studio loads a model on first use, with the context set in
			// the app; the most it can be is all NullBot knows.
			m.ContextWindow = d.MaxContextLength
			m.ContextNote = "Not loaded yet; LM Studio may load it with a smaller context"
		}
		out = append(out, m)
	}
	return out, nil
}

type ollamaTag struct {
	Name    string `json:"name"`
	Model   string `json:"model"`
	Size    int64  `json:"size"`
	Details struct {
		Family            string `json:"family"`
		ParameterSize     string `json:"parameter_size"`
		QuantizationLevel string `json:"quantization_level"`
	} `json:"details"`
}

type ollamaShow struct {
	Parameters   string         `json:"parameters"`
	ModelInfo    map[string]any `json:"model_info"`
	Capabilities []string       `json:"capabilities"`
}

var numCtxPattern = regexp.MustCompile(`(?m)^\s*num_ctx\s+(\d+)`)

func (e Endpoint) ollamaModels(ctx context.Context) ([]Model, error) {
	var tags struct {
		Models []ollamaTag `json:"models"`
	}
	if err := e.getJSON(ctx, http.MethodGet, "/api/tags", nil, &tags); err != nil {
		return nil, err
	}
	var running struct {
		Models []struct {
			Name          string `json:"name"`
			ContextLength int    `json:"context_length"`
		} `json:"models"`
	}
	_ = e.getJSON(ctx, http.MethodGet, "/api/ps", nil, &running)
	loaded := map[string]int{}
	for _, r := range running.Models {
		loaded[r.Name] = r.ContextLength
	}
	out := make([]Model, len(tags.Models))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, t := range tags.Models {
		name := firstNonEmpty(t.Name, t.Model)
		out[i] = Model{ID: name, Size: t.Size, Family: t.Details.Family, Parameters: t.Details.ParameterSize, Quantization: t.Details.QuantizationLevel}
		if n, ok := loaded[name]; ok {
			out[i].Loaded = true
			if n > 0 {
				out[i].ContextWindow = n
			}
		}
		wg.Add(1)
		go func(m *Model) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			var show ollamaShow
			if e.getJSON(ctx, http.MethodPost, "/api/show", map[string]any{"model": m.ID}, &show) != nil {
				return
			}
			applyOllamaShow(m, show)
		}(&out[i])
	}
	wg.Wait()
	kept := out[:0]
	for _, m := range out {
		if m.ID != "" {
			kept = append(kept, m)
		}
	}
	return kept, nil
}

func applyOllamaShow(m *Model, show ollamaShow) {
	m.Capabilities = show.Capabilities
	for _, c := range show.Capabilities {
		if c == "embedding" {
			m.Embedding = true
		}
	}
	for key, v := range show.ModelInfo {
		if strings.HasSuffix(key, ".context_length") {
			if f, ok := v.(float64); ok && f > 0 {
				m.MaxContext = int(f)
			}
		}
	}
	if m.ContextWindow > 0 {
		return // what the running model has
	}
	if match := numCtxPattern.FindStringSubmatch(show.Parameters); match != nil {
		if n, err := strconv.Atoi(match[1]); err == nil && n > 0 {
			m.ContextWindow = n
			return
		}
	}
	m.ContextWindow = OllamaDefaultContext
	if m.MaxContext > 0 && m.MaxContext < m.ContextWindow {
		m.ContextWindow = m.MaxContext
	}
	m.ContextNote = "Ollama's default unless OLLAMA_CONTEXT_LENGTH is set; make a copy with a larger context to use more"
}

func formatParams(n int64) string {
	switch {
	case n >= 1e9:
		return strconv.FormatFloat(float64(n)/1e9, 'f', 1, 64) + "B"
	case n >= 1e6:
		return strconv.FormatFloat(float64(n)/1e6, 'f', 0, 64) + "M"
	}
	return strconv.FormatInt(n, 10)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// --- Ollama model management --------------------------------------------------

// Progress is a step of a pull or create.
type Progress struct {
	Status    string `json:"status"`
	Digest    string `json:"digest,omitempty"`
	Total     int64  `json:"total,omitempty"`
	Completed int64  `json:"completed,omitempty"`
}

var ollamaNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:-]{0,199}$`)

// ValidOllamaName reports a model name Ollama could have, such as
// "llama3.1:8b" or "hf.co/bartowski/Qwen2.5-7B-Instruct-GGUF:Q4_K_M".
func ValidOllamaName(name string) bool {
	return ollamaNamePattern.MatchString(name) && !strings.Contains(name, "..") && !strings.Contains(name, "//")
}

// Pull downloads a model into Ollama, reporting progress as it goes.
func (e Endpoint) Pull(ctx context.Context, name string, progress func(Progress)) error {
	if e.Kind != Ollama {
		return fmt.Errorf("%s does not download models itself", e.Kind.Label())
	}
	if !ValidOllamaName(name) {
		return fmt.Errorf("%q is not a model name Ollama knows how to pull", name)
	}
	return e.stream(ctx, "/api/pull", map[string]any{"model": name, "stream": true}, progress)
}

// CreateWithContext makes a copy of a model that runs with a larger
// context: Ollama otherwise runs most models with a small default.
func (e Endpoint) CreateWithContext(ctx context.Context, from, name string, numCtx int, progress func(Progress)) error {
	if e.Kind != Ollama {
		return fmt.Errorf("only Ollama models can be copied with a new context size")
	}
	if !ValidOllamaName(from) || !ValidOllamaName(name) {
		return errors.New("use model names Ollama accepts")
	}
	if numCtx < 2048 || numCtx > 1<<21 {
		return errors.New("choose a context between 2,048 and 2,097,152 tokens")
	}
	return e.stream(ctx, "/api/create", map[string]any{"model": name, "from": from, "parameters": map[string]any{"num_ctx": numCtx}, "stream": true}, progress)
}

// Delete removes a model from Ollama.
func (e Endpoint) Delete(ctx context.Context, name string) error {
	if e.Kind != Ollama {
		return fmt.Errorf("remove models in %s itself", e.Kind.Label())
	}
	if !ValidOllamaName(name) {
		return fmt.Errorf("%q is not a model name", name)
	}
	return e.getJSON(ctx, http.MethodDelete, "/api/delete", map[string]any{"model": name}, nil)
}

// stream posts body and reads Ollama's newline-delimited progress until it
// reports success or an error.
func (e Endpoint) stream(ctx context.Context, path string, body any, progress func(Progress)) error {
	req, err := e.request(ctx, http.MethodPost, path, body)
	if err != nil {
		return err
	}
	client := *e.client()
	client.Timeout = 0 // a pull can take hours; ctx bounds it
	resp, err := client.Do(req)
	if err != nil {
		return e.reachError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return statusError(resp.StatusCode, data)
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	succeeded := false
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var p struct {
			Progress
			Error string `json:"error"`
		}
		if json.Unmarshal(line, &p) != nil {
			continue
		}
		if p.Error != "" {
			return errors.New(p.Error)
		}
		if progress != nil {
			progress(p.Progress)
		}
		if p.Status == "success" {
			succeeded = true
		}
	}
	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	if !succeeded {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("the server stopped before it finished")
	}
	return nil
}

// --- finding servers ----------------------------------------------------------

// Found is a server answering at a kind's default address.
type Found struct {
	Kind    Kind   `json:"kind"`
	BaseURL string `json:"base_url"`
	Version string `json:"version,omitempty"`
	Models  int    `json:"models"`
}

// Probe looks for servers at each kind's default address on this computer.
func Probe(ctx context.Context, client *http.Client) []Found {
	var mu sync.Mutex
	var found []Found
	var wg sync.WaitGroup
	for _, kind := range Kinds {
		base := DefaultURL(kind)
		if base == "" {
			continue
		}
		wg.Add(1)
		go func(kind Kind, base string) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			e := Endpoint{Kind: kind, BaseURL: base, HTTP: client}
			status := e.Check(ctx)
			if !status.Reachable {
				return
			}
			// Two kinds can share a port; only keep the one that answers in
			// its own way.
			if !e.looksLike(ctx) {
				return
			}
			models, _ := e.Models(ctx)
			mu.Lock()
			found = append(found, Found{Kind: kind, BaseURL: base, Version: status.Version, Models: len(models)})
			mu.Unlock()
		}(kind, base)
	}
	wg.Wait()
	sort.Slice(found, func(i, j int) bool { return found[i].Kind < found[j].Kind })
	return found
}

func (e Endpoint) looksLike(ctx context.Context) bool {
	switch e.Kind {
	case Ollama:
		return e.getJSON(ctx, http.MethodGet, "/api/tags", nil, nil) == nil
	case LlamaCpp:
		return e.getJSON(ctx, http.MethodGet, "/props", nil, nil) == nil
	case LMStudio:
		return e.getJSON(ctx, http.MethodGet, "/api/v0/models", nil, nil) == nil
	case VLLM:
		return e.getJSON(ctx, http.MethodGet, "/version", nil, nil) == nil
	}
	return true
}
