package app

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	tcagent "github.com/Bradthebrad/tinychain/agent"
	"github.com/Bradthebrad/tinychain/openai"

	"github.com/Bradthebrad/nullbot/pkg/localmodels"
)

// Local models are model servers you host: Ollama, llama.cpp, LM Studio,
// vLLM or any OpenAI-compatible server, on this computer, another machine on
// your network or an internal domain. Each is a provider of its own,
// "local:<id>", so the manager and subagents can use different ones.

const LocalProviderPrefix = "local:"

// LocalEndpoint is a model server in the configuration.
type LocalEndpoint struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Kind    string `json:"kind"`
	BaseURL string `json:"base_url"`
	// KeySecret names the vault secret holding the server's API key, for
	// servers started with one. The key itself is never in the config.
	KeySecret string `json:"key_secret,omitempty"`
	// PlainHTTP accepts sending the key over plain http to another
	// computer. Without it NullBot refuses to.
	PlainHTTP bool `json:"plain_http,omitempty"`
	Disabled  bool `json:"disabled,omitempty"`
}

// LocalProvider is the provider name for an endpoint.
func LocalProvider(id string) string { return LocalProviderPrefix + id }

// IsLocalProvider reports a provider that is a local endpoint.
func IsLocalProvider(provider string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(provider)), LocalProviderPrefix)
}

var localIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// Title is how an endpoint is named in model lists.
func (e LocalEndpoint) Title() string {
	if strings.TrimSpace(e.Name) != "" {
		return e.Name
	}
	return localmodels.Kind(e.Kind).Label()
}

func localEndpointByID(config Config, id string) (LocalEndpoint, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, e := range config.LocalEndpoints {
		if e.ID == id {
			return e, true
		}
	}
	return LocalEndpoint{}, false
}

func localEndpointForProvider(config Config, provider string) (LocalEndpoint, error) {
	id := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(provider)), LocalProviderPrefix)
	e, ok := localEndpointByID(config, id)
	if !ok {
		return e, fmt.Errorf("there is no local model server %q; add it in Settings > Models or with nullbot models local add", id)
	}
	if e.Disabled {
		return e, fmt.Errorf("the local model server %s is turned off", e.Title())
	}
	return e, nil
}

// validateLocalEndpoint checks and tidies an endpoint before it is saved.
func validateLocalEndpoint(e LocalEndpoint) (LocalEndpoint, error) {
	e.ID = strings.ToLower(strings.TrimSpace(e.ID))
	e.Kind = strings.ToLower(strings.TrimSpace(e.Kind))
	e.Name = strings.TrimSpace(e.Name)
	e.KeySecret = strings.TrimSpace(e.KeySecret)
	kind := localmodels.Kind(e.Kind)
	if !kind.Valid() {
		return e, fmt.Errorf("choose a server kind: ollama, llamacpp, lmstudio, vllm or openai")
	}
	if e.ID == "" {
		e.ID = e.Kind
	}
	if !localIDPattern.MatchString(e.ID) {
		return e, fmt.Errorf("name the server with lowercase letters, digits, dashes or underscores (%q)", e.ID)
	}
	if strings.TrimSpace(e.BaseURL) == "" {
		e.BaseURL = localmodels.DefaultURL(kind)
	}
	base, err := localmodels.NormalizeBaseURL(e.BaseURL)
	if err != nil {
		return e, err
	}
	e.BaseURL = base
	return e, nil
}

// localClient is the endpoint with its key from the vault.
func localClient(config Config, e LocalEndpoint) (localmodels.Endpoint, error) {
	client := localmodels.Endpoint{Kind: localmodels.Kind(e.Kind), BaseURL: e.BaseURL}
	if e.KeySecret == "" {
		return client, nil
	}
	if warning := LocalEndpointWarning(e); warning != "" && !e.PlainHTTP {
		return client, errors.New(warning)
	}
	payload, err := loadSecretsPayload(config)
	if err != nil {
		return client, fmt.Errorf("the key for %s is in the vault, which could not be opened: %w", e.Title(), err)
	}
	for _, s := range payload.Secrets {
		if s.Name != e.KeySecret {
			continue
		}
		if err := localKeyAllowed(s, e); err != nil {
			return client, err
		}
		client.APIKey = s.Values["value"]
		return client, nil
	}
	return client, fmt.Errorf("the key secret %s for %s is missing; add it in Settings > Secrets", e.KeySecret, e.Title())
}

// cloudKeyProviders are providers whose keys NullBot uses itself. Their keys
// never go to a local server, whatever the configuration says.
var cloudKeyProviders = map[string]bool{
	"openai": true, "anthropic": true, "openrouter": true, "google": true, "gemini": true,
	"x-ai": true, "xai": true, "brave": true, "github": true, "gh": true, "huggingface": true, "hf": true,
	"claude": true, "codex": true, "aws": true, "gcp": true,
}

// localKeyAllowed checks that a secret is meant for this server: an API key
// whose provider is "local", the server's name, or "local-" and its name.
// A configuration file that names another key (the OpenAI key, say) cannot
// send it to a server.
func localKeyAllowed(s Secret, e LocalEndpoint) error {
	if s.Kind != SecretAPIKey {
		return fmt.Errorf("secret %s is a %s; a server key must be an API key secret with provider %q or %q", s.Name, s.Kind, "local", e.ID)
	}
	p := NormalizeProvider(s.Public["provider"])
	if cloudKeyProviders[p] {
		return fmt.Errorf("secret %s is a %s key, which NullBot does not send to a local server; add a key with provider %q or %q", s.Name, p, "local", e.ID)
	}
	if p != "local" && p != e.ID && p != "local-"+e.ID {
		return fmt.Errorf("secret %s is for %q; a key for the server %s needs provider %q or %q", s.Name, p, e.ID, "local", e.ID)
	}
	return nil
}

// localStatusCache keeps a recent successful check, so a run does not wait
// on the network every time it starts.
var localStatusCache sync.Map // base URL -> time.Time of the last success

func checkLocalReachable(ctx context.Context, e LocalEndpoint, client localmodels.Endpoint) error {
	if at, ok := localStatusCache.Load(e.BaseURL); ok && time.Since(at.(time.Time)) < 30*time.Second {
		return nil
	}
	status := client.Check(ctx)
	if !status.Reachable {
		localStatusCache.Delete(e.BaseURL)
		return fmt.Errorf("%s: %s", e.Title(), status.Error)
	}
	localStatusCache.Store(e.BaseURL, time.Now())
	return nil
}

// localModel is the chat model for a local provider, through the server's
// OpenAI-compatible API. The server must answer before a run starts, and
// NullBot learns the context the model runs with if it does not know it.
func localModel(config Config, modelConfig ModelConfig, temperature *float64, maxTokens *int) (tcagent.Model, error) {
	e, err := localEndpointForProvider(config, modelConfig.Provider)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(modelConfig.Model) == "" {
		return nil, fmt.Errorf("choose a model on %s", e.Title())
	}
	client, err := localClient(config, e)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := checkLocalReachable(ctx, e, client); err != nil {
		return nil, err
	}
	if _, known := savedContextWindows(config)[ModelKey(LocalProvider(e.ID), modelConfig.Model)]; !known {
		if models, err := client.Models(ctx); err == nil {
			recordLocalWindows(config, e, models)
		}
	}
	return tcagent.OpenAIModel{
		Client:      openai.Client{APIKey: client.APIKey, BaseURL: client.OpenAIBaseURL(), HTTPClient: localChatClient},
		Model:       modelConfig.Model,
		Temperature: temperature,
		MaxTokens:   maxTokens,
		Provider:    "local",
	}, nil
}

// localChatClient keeps a server's key from following a redirect elsewhere.
var localChatClient = localmodels.HTTPClient(0)

func recordLocalWindows(config Config, e LocalEndpoint, models []localmodels.Model) {
	sizes := map[string]int{}
	for _, m := range models {
		if m.ContextWindow > 0 {
			sizes[m.ID] = m.ContextWindow
			// Ollama lists "llama3.2:latest" for a model chosen as
			// "llama3.2"; both names find its size.
			if bare, ok := strings.CutSuffix(m.ID, ":latest"); ok && !hasModelTag(bare) {
				sizes[bare] = m.ContextWindow
			}
		}
	}
	recordContextWindows(config, LocalProvider(e.ID), sizes, "provider", "Reported by "+e.Title())
}

// localModelGroups lists each enabled endpoint's models for the model
// pickers. An endpoint that does not answer still shows, with its error.
func localModelGroups(ctx context.Context, config Config) []ModelGroup {
	var groups []ModelGroup
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, e := range config.LocalEndpoints {
		if e.Disabled {
			continue
		}
		wg.Add(1)
		go func(e LocalEndpoint) {
			defer wg.Done()
			group := ModelGroup{Provider: LocalProvider(e.ID), Title: e.Title()}
			client, err := localClient(config, e)
			if err == nil {
				ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
				var models []localmodels.Model
				models, err = client.Models(ctx)
				cancel()
				if err == nil {
					recordLocalWindows(config, e, models)
					for _, m := range models {
						if m.Embedding {
							continue
						}
						group.Models = append(group.Models, localModelOption(e, m))
					}
				}
			}
			if err != nil {
				group.Error = err.Error()
			}
			mu.Lock()
			groups = append(groups, group)
			mu.Unlock()
		}(e)
	}
	wg.Wait()
	sort.Slice(groups, func(i, j int) bool { return groups[i].Provider < groups[j].Provider })
	return groups
}

func localModelOption(e LocalEndpoint, m localmodels.Model) ModelOption {
	var details []string
	if m.Parameters != "" {
		details = append(details, m.Parameters)
	}
	if m.Quantization != "" {
		details = append(details, m.Quantization)
	}
	tools := false
	for _, c := range m.Capabilities {
		if c == "tools" {
			tools = true
		}
	}
	if tools {
		details = append(details, "tools")
	}
	desc := "On " + e.Title()
	if len(details) > 0 {
		desc += " (" + strings.Join(details, ", ") + ")"
	}
	if m.ContextNote != "" {
		desc += ". " + m.ContextNote
	}
	return ModelOption{Provider: LocalProvider(e.ID), ID: m.ID, Name: firstNonEmpty(m.Name, m.ID), Description: desc, OpenAICompatible: true, ContextWindow: m.ContextWindow, ContextSource: "provider"}
}

// --- App API ------------------------------------------------------------------

// LocalEndpointInfo is an endpoint with what NullBot knows about it now.
type LocalEndpointInfo struct {
	LocalEndpoint
	Provider string              `json:"provider"`
	Title    string              `json:"title"`
	Label    string              `json:"kind_label"`
	Status   localmodels.Status  `json:"status"`
	Models   []localmodels.Model `json:"models,omitempty"`
	// Insecure: a key goes to a server elsewhere over plain http.
	Insecure bool `json:"insecure,omitempty"`
}

// LocalEndpoints lists the configured servers, checking each when check is
// set.
func (a *App) LocalEndpoints(ctx context.Context, check bool) []LocalEndpointInfo {
	config := a.Config()
	out := make([]LocalEndpointInfo, len(config.LocalEndpoints))
	var wg sync.WaitGroup
	for i, e := range config.LocalEndpoints {
		out[i] = LocalEndpointInfo{LocalEndpoint: e, Provider: LocalProvider(e.ID), Title: e.Title(), Label: localmodels.Kind(e.Kind).Label(),
			Insecure: LocalEndpointWarning(e) != ""}
		if !check || e.Disabled {
			continue
		}
		wg.Add(1)
		go func(info *LocalEndpointInfo) {
			defer wg.Done()
			client, err := localClient(config, info.LocalEndpoint)
			if err != nil {
				info.Status.Error = err.Error()
				return
			}
			info.Status = client.Check(ctx)
			if info.Status.Reachable {
				localStatusCache.Store(info.BaseURL, time.Now())
				if models, err := client.Models(ctx); err == nil {
					recordLocalWindows(config, info.LocalEndpoint, models)
					info.Models = models
				} else {
					info.Status.Error = err.Error()
				}
			}
		}(&out[i])
	}
	wg.Wait()
	return out
}

// SaveLocalEndpoint adds or replaces a server. previousID, when set, is the
// endpoint being edited, so it can be renamed.
func (a *App) SaveLocalEndpoint(e LocalEndpoint, previousID string) (LocalEndpoint, error) {
	e, err := validateLocalEndpoint(e)
	if err != nil {
		return e, err
	}
	previousID = strings.ToLower(strings.TrimSpace(previousID))
	if e.KeySecret != "" {
		if err := a.checkLocalKeySecret(e); err != nil {
			return e, err
		}
	} else {
		e.PlainHTTP = false
	}
	if warning := LocalEndpointWarning(e); warning != "" && !e.PlainHTTP {
		return e, errors.New(warning + "; to send it anyway, allow plain http for this server")
	}
	if err := localSaveClash(a.Config(), e.ID, previousID); err != nil {
		return e, err
	}
	var clash error
	err = a.UpdateConfig(func(config *Config) {
		if clash = localSaveClash(*config, e.ID, previousID); clash != nil {
			return
		}
		var kept []LocalEndpoint
		for _, existing := range config.LocalEndpoints {
			if existing.ID == e.ID || (previousID != "" && existing.ID == previousID) {
				continue
			}
			kept = append(kept, existing)
		}
		config.LocalEndpoints = append(kept, e)
		// A renamed server keeps the models chosen on it, and the context
		// sizes set for them.
		if previousID != "" && previousID != e.ID {
			for _, m := range []*ModelConfig{&config.Model, &config.SubagentModel} {
				if strings.EqualFold(m.Provider, LocalProvider(previousID)) {
					m.Provider = LocalProvider(e.ID)
				}
			}
			oldPrefix, newPrefix := LocalProvider(previousID)+"/", LocalProvider(e.ID)+"/"
			if len(config.Compaction.ContextWindows) > 0 {
				windows := make(map[string]int, len(config.Compaction.ContextWindows))
				for key, tokens := range config.Compaction.ContextWindows {
					if strings.HasPrefix(key, oldPrefix) {
						key = newPrefix + strings.TrimPrefix(key, oldPrefix)
					}
					windows[key] = tokens
				}
				config.Compaction.ContextWindows = windows
			}
		}
	})
	if err == nil {
		err = clash
	}
	if err != nil {
		return e, err
	}
	localStatusCache.Delete(e.BaseURL)
	a.MarkRuntimeDirty("local model server " + e.ID + " saved")
	invalidateModelCatalog()
	return e, nil
}

// localSaveClash refuses to replace a server that is not the one being
// edited, so adding a second Ollama or renaming onto another name never
// drops a server silently.
func localSaveClash(config Config, id, previousID string) error {
	if previousID != "" {
		if _, ok := localEndpointByID(config, previousID); !ok {
			return fmt.Errorf("there is no local model server %q", previousID)
		}
	}
	if id != previousID {
		if _, ok := localEndpointByID(config, id); ok {
			return fmt.Errorf("there is already a local model server named %q; choose another name or edit that one", id)
		}
	}
	return nil
}

func (a *App) checkLocalKeySecret(e LocalEndpoint) error {
	payload, err := loadSecretsPayload(a.Config())
	if err != nil {
		return fmt.Errorf("the vault could not be opened: %w", err)
	}
	for _, s := range payload.Secrets {
		if s.Name == e.KeySecret {
			return localKeyAllowed(s, e)
		}
	}
	// It can be added later; the server is not used until it is.
	return nil
}

// LocalEndpointWarning is what to know before a key goes to a server: plain
// http to another computer sends it in the clear.
func LocalEndpointWarning(e LocalEndpoint) string {
	if e.KeySecret != "" && strings.HasPrefix(strings.ToLower(e.BaseURL), "http://") && !localmodels.IsLocalHost(e.BaseURL) {
		return fmt.Sprintf("%s uses plain http to another computer, so its key travels unencrypted; use https, or an SSH tunnel to the server", e.Title())
	}
	return ""
}

// RemoveLocalEndpoint removes a server. A model in use on it stops the
// removal, so the manager is never left without a model.
func (a *App) RemoveLocalEndpoint(id string) error {
	id = strings.ToLower(strings.TrimSpace(id))
	config := a.Config()
	if _, ok := localEndpointByID(config, id); !ok {
		return fmt.Errorf("there is no local model server %q", id)
	}
	if strings.EqualFold(config.Model.Provider, LocalProvider(id)) {
		return fmt.Errorf("the manager uses a model on %s; choose another model first", id)
	}
	if strings.EqualFold(config.SubagentModel.Provider, LocalProvider(id)) {
		return fmt.Errorf("subagents use a model on %s; choose another model first", id)
	}
	err := a.UpdateConfig(func(config *Config) {
		var kept []LocalEndpoint
		for _, e := range config.LocalEndpoints {
			if e.ID != id {
				kept = append(kept, e)
			}
		}
		config.LocalEndpoints = kept
	})
	if err == nil {
		invalidateModelCatalog()
	}
	return err
}

// FindLocalServers looks for model servers at their usual addresses on
// this computer, and says which are already configured.
func (a *App) FindLocalServers(ctx context.Context) []LocalServerFound {
	config := a.Config()
	var out []LocalServerFound
	for _, f := range localmodels.Probe(ctx, nil) {
		found := LocalServerFound{Found: f, Label: f.Kind.Label()}
		for _, e := range config.LocalEndpoints {
			if e.BaseURL == f.BaseURL {
				found.ConfiguredAs = e.ID
			}
		}
		out = append(out, found)
	}
	return out
}

// LocalServerFound is a server answering on this computer.
type LocalServerFound struct {
	localmodels.Found
	Label        string `json:"label"`
	ConfiguredAs string `json:"configured_as,omitempty"`
}

func (a *App) localOllama(id string) (LocalEndpoint, localmodels.Endpoint, error) {
	config := a.Config()
	e, ok := localEndpointByID(config, id)
	if !ok {
		return e, localmodels.Endpoint{}, fmt.Errorf("there is no local model server %q", id)
	}
	if localmodels.Kind(e.Kind) != localmodels.Ollama {
		return e, localmodels.Endpoint{}, fmt.Errorf("%s is a %s; NullBot installs models through Ollama", e.Title(), localmodels.Kind(e.Kind).Label())
	}
	client, err := localClient(config, e)
	return e, client, err
}

// PullLocalModel downloads a model into an Ollama server. The pull is
// recorded in the activity log.
func (a *App) PullLocalModel(ctx context.Context, id, name string, progress func(localmodels.Progress)) error {
	e, client, err := a.localOllama(id)
	if err != nil {
		return err
	}
	a.logInfo("local model pull started", "server", e.ID, "model", name)
	err = client.Pull(ctx, name, progress)
	if err != nil {
		a.logError("local model pull failed", "server", e.ID, "model", name, "error", err)
		return err
	}
	a.logInfo("local model pulled", "server", e.ID, "model", name)
	invalidateModelCatalog()
	a.notifyChange(ChangeConfig)
	return nil
}

// CopyLocalModelWithContext makes an Ollama copy of a model that runs with
// a larger context, and returns its name.
func (a *App) CopyLocalModelWithContext(ctx context.Context, id, from string, numCtx int) (string, error) {
	_, client, err := a.localOllama(id)
	if err != nil {
		return "", err
	}
	name := localCopyName(from, numCtx)
	if err := client.CreateWithContext(ctx, from, name, numCtx, nil); err != nil {
		return "", err
	}
	invalidateModelCatalog()
	return name + ":latest", nil
}

// hasModelTag reports an Ollama model name with a tag. A colon before the
// last slash is a registry's port, not a tag.
func hasModelTag(name string) bool {
	return strings.Contains(name[strings.LastIndex(name, "/")+1:], ":")
}

// ollamaFullName is a model name as Ollama lists it, with ":latest" when no
// tag was given.
func ollamaFullName(name string) string {
	name = strings.TrimSpace(name)
	if name != "" && !hasModelTag(name) {
		return name + ":latest"
	}
	return name
}

// localCopyName names a copy of a model with a larger context:
// qwen2.5:7b with 32768 tokens is qwen2.5-7b-32k. Sizes that are not a whole
// number of thousands keep their exact count, so two sizes never share a
// name.
func localCopyName(from string, numCtx int) string {
	from = ollamaFullName(from)
	slash := strings.LastIndex(from, "/") + 1
	base, tag := from, ""
	if i := strings.LastIndex(from[slash:], ":"); i >= 0 {
		base, tag = from[:slash+i], from[slash+i+1:]
	}
	size := strconv.Itoa(numCtx)
	if numCtx%1024 == 0 {
		size = strconv.Itoa(numCtx/1024) + "k"
	}
	if tag != "" && tag != "latest" {
		return base + "-" + tag + "-" + size
	}
	return base + "-" + size
}

// DeleteLocalModel removes a model from an Ollama server.
func (a *App) DeleteLocalModel(ctx context.Context, id, name string) error {
	config := a.Config()
	for _, m := range []ModelConfig{config.Model, config.SubagentModel} {
		if strings.EqualFold(m.Provider, LocalProvider(id)) && ollamaFullName(m.Model) == ollamaFullName(name) {
			return errors.New("that model is in use; choose another model first")
		}
	}
	_, client, err := a.localOllama(id)
	if err != nil {
		return err
	}
	if err := client.Delete(ctx, name); err != nil {
		return err
	}
	invalidateModelCatalog()
	return nil
}
