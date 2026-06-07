package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	tcagent "tinychain/agent"
	"tinychain/anthropic"
	"tinychain/callbacks"
	"tinychain/lc"
	"tinychain/mcp"
	"tinychain/openai"
)

type runtimeBundle struct {
	agent   *tcagent.Agent
	closers []func() error
}

func (a *App) runAgent(ctx context.Context, skillHints []string) Reply {
	workCtx := a.beginWork(ctx)
	defer a.endWork()

	bundle, err := a.buildRuntime(workCtx, skillHints)
	if err != nil {
		a.logError("runtime build failed", "error", err)
		return a.reply(err.Error(), "", "config")
	}
	defer closeAll(bundle.closers)

	result, err := bundle.agent.InvokeMessages(workCtx, a.langChainHistory())
	if err != nil {
		if workCtx.Err() != nil {
			a.logInfo("agent paused")
			return a.reply("Paused.", "", "")
		}
		a.logError("agent execution failed", "error", err)
		return a.reply("Agent error: "+err.Error(), "", "")
	}
	a.logInfo("agent response", "chars", len(lcContentText(result.Output.Content)))
	return a.reply(lcContentText(result.Output.Content), "", "")
}

func (a *App) buildRuntime(ctx context.Context, skillHints []string) (*runtimeBundle, error) {
	a.mu.Lock()
	config := a.config
	dirty := a.runtimeDirty
	dirtyReason := a.runtimeDirtyReason
	if dirty {
		a.runtimeDirty = false
		a.runtimeDirtyReason = ""
	}
	a.mu.Unlock()
	if dirty {
		a.logInfo("runtime rebuild", "reason", dirtyReason)
	}

	model, err := modelFromConfig(config)
	if err != nil {
		return nil, err
	}

	skills, err := loadSkills(config)
	if err != nil {
		return nil, err
	}

	tools := BuiltinTools(config, a)
	mcpTools, closers := a.loadMCPTools(ctx, config)
	tools = append(tools, mcpTools...)
	systemPrompt := baseSystemPrompt(config, skillHints, tools, skills)

	return &runtimeBundle{
		agent: tcagent.New(tcagent.Config{
			Model:         model,
			SystemPrompt:  systemPrompt,
			Tools:         tools,
			Skills:        skills,
			MaxIterations: config.Agent.MaxIterations,
			Callbacks:     callbacks.SinkFunc(a.handleAgentCallback),
		}),
		closers: closers,
	}, nil
}

func (a *App) handleAgentCallback(event callbacks.Event) {
	record := ActivityRecord{
		Time: event.Time,
		Kind: string(event.Event),
		Name: event.Name,
	}
	if record.Time.IsZero() {
		record.Time = time.Now().UTC()
	}
	switch event.Event {
	case callbacks.EventChatModelStart:
		record.Status = "model start"
		record.Detail = "sending messages"
	case callbacks.EventLLMEnd:
		record.Status = "model done"
	case callbacks.EventLLMError:
		record.Status = "model error"
		record.Detail = event.Data.Error
	case callbacks.EventToolStart:
		record.Status = "tool start"
		record.Detail = compactAny(event.Data.Input, 120)
	case callbacks.EventToolEnd:
		record.Status = "tool done"
		record.Detail = compactAny(event.Data.Output, 160)
	case callbacks.EventToolError:
		record.Status = "tool error"
		record.Detail = event.Data.Error
	default:
		record.Status = string(event.Event)
	}
	a.appendActivity(record)
}

func modelFromConfig(config Config) (tcagent.Model, error) {
	maxTokens := config.Model.MaxTokens
	var maxTokensPtr *int
	if maxTokens > 0 {
		maxTokensPtr = &maxTokens
	}
	var tempPtr *float64
	if config.Model.Temperature != 0 {
		temp := config.Model.Temperature
		tempPtr = &temp
	}

	switch strings.ToLower(config.Model.Provider) {
	case "openai", "":
		apiKey := apiKeyForProvider(config, "openai", "OPENAI_API_KEY")
		if apiKey == "" {
			return nil, fmt.Errorf("OpenAI API key is not set. Use /config to save one locally.")
		}
		return tcagent.OpenAIModel{
			Client:       openai.Client{APIKey: apiKey},
			Model:        config.Model.Model,
			UseResponses: config.Agent.UseResponses,
			Temperature:  tempPtr,
			MaxTokens:    maxTokensPtr,
		}, nil
	case "anthropic":
		apiKey := apiKeyForProvider(config, "anthropic", "ANTHROPIC_API_KEY")
		if apiKey == "" {
			return nil, fmt.Errorf("Anthropic API key is not set. Use /config to save one locally.")
		}
		return tcagent.AnthropicModel{
			Client:      anthropic.Client{APIKey: apiKey},
			Model:       config.Model.Model,
			MaxTokens:   maxTokens,
			Temperature: tempPtr,
		}, nil
	case "openrouter":
		apiKey := apiKeyForProvider(config, "openrouter", "OPENROUTER_API_KEY")
		if apiKey == "" {
			return nil, fmt.Errorf("OpenRouter API key is not set. Use /config to save one locally.")
		}
		return tcagent.OpenAIModel{
			Client:       openai.Client{APIKey: apiKey, BaseURL: "https://openrouter.ai/api/v1"},
			Model:        config.Model.Model,
			UseResponses: false,
			Temperature:  tempPtr,
			MaxTokens:    maxTokensPtr,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported provider %q", config.Model.Provider)
	}
}

func RuntimeStatus(config Config) map[string]any {
	keys, _ := LoadAPIKeys(config)
	return map[string]any{
		"provider":               config.Model.Provider,
		"model":                  config.Model.Model,
		"openai_api_key_set":     keys.OpenAI != "" || os.Getenv("OPENAI_API_KEY") != "",
		"anthropic_api_key_set":  keys.Anthropic != "" || os.Getenv("ANTHROPIC_API_KEY") != "",
		"openrouter_api_key_set": keys.OpenRouter != "" || os.Getenv("OPENROUTER_API_KEY") != "",
		"mcp_servers_enabled":    len(config.EnabledMCPServers),
		"skill_dirs":             config.SkillDirs,
		"app_dir":                config.AppDir,
		"logs_path":              NewLogger(config).path,
	}
}

func apiKeyForProvider(config Config, provider string, env string) string {
	keys, err := LoadAPIKeys(config)
	if err == nil {
		if key := keys.ForProvider(provider); key != "" {
			return key
		}
	}
	return os.Getenv(env)
}

func loadSkills(config Config) ([]tcagent.Skill, error) {
	var skills []tcagent.Skill
	for _, dir := range config.SkillDirs {
		loaded, err := tcagent.LoadSkills(dir)
		if err != nil {
			continue
		}
		skills = append(skills, loaded...)
	}
	return skills, nil
}

func (a *App) loadMCPTools(ctx context.Context, config Config) ([]tcagent.Tool, []func() error) {
	var tools []tcagent.Tool
	var closers []func() error
	for id, entry := range config.EnabledMCPServers {
		if !entry.Enabled {
			continue
		}
		loadCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		a.logInfo("mcp load start", "id", id, "transport", entry.Transport, "command", entry.Command)
		client, err := mcpClientForEntry(loadCtx, entry)
		if err != nil {
			cancel()
			a.logError("mcp connect failed", "id", id, "error", err)
			continue
		}
		if _, err := client.Initialize(loadCtx); err != nil {
			cancel()
			_ = client.Close()
			a.logError("mcp initialize failed", "id", id, "error", err)
			continue
		}
		discovered, err := mcp.AgentTools(loadCtx, client)
		cancel()
		if err != nil {
			_ = client.Close()
			a.logError("mcp tools/list failed", "id", id, "error", err)
			continue
		}
		a.logInfo("mcp load done", "id", id, "tools", len(discovered))
		tools = append(tools, discovered...)
		closers = append(closers, client.Close)
	}
	return tools, closers
}

func mcpClientForEntry(ctx context.Context, entry MCPEntry) (*mcp.Client, error) {
	switch strings.ToLower(entry.Transport) {
	case "", "stdio":
		return mcp.NewStdioClient(ctx, entry.Command, entry.Args...)
	case "http", "streamable-http", "streamable_http":
		return mcp.NewHTTPClient(entry.Command, nil), nil
	case "sse":
		return mcp.NewSSEClient(entry.Command, nil), nil
	default:
		return nil, fmt.Errorf("unsupported MCP transport %q", entry.Transport)
	}
}

func (a *App) langChainHistory() []lc.BaseMessage {
	a.mu.Lock()
	defer a.mu.Unlock()
	limit := a.config.Compaction.KeepLastMessages
	if limit <= 0 || limit > len(a.history) {
		limit = len(a.history)
	}
	start := len(a.history) - limit
	messages := make([]lc.BaseMessage, 0, limit)
	for _, msg := range a.history[start:] {
		switch msg.Role {
		case "assistant":
			messages = append(messages, lc.AI(msg.Content))
		default:
			messages = append(messages, lc.Human(msg.Content))
		}
	}
	return messages
}

func baseSystemPrompt(config Config, skillHints []string, tools []tcagent.Tool, skills []tcagent.Skill) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are %s. %s\n", DisplayName(config), config.Tagline)
	b.WriteString("Be direct about what you can and cannot do. Do not claim coding, shell, web, email, filesystem, browser, or elevated local capabilities unless an enabled tool explicitly provides them.\n")
	b.WriteString("Slash commands are handled by the app UI. If the user references a skill token mid-sentence, treat it as a hint, not as a command execution request.\n")
	b.WriteString("Available built-in tools are constrained to NullBot app data: listing config-directory files, reading small config-directory text files, listing skills, creating SKILL.md files under the configured skills directory, refreshing/listing/installing market packages, enabling/disabling/removing installed MCP servers, listing configured MCP servers, summarizing recent visible chat history, reading compact persisted session history, and reading recent NullBot runtime log lines.\n")
	if len(tools) > 0 {
		b.WriteString("Current tool inventory:\n")
		for _, tool := range tools {
			def := tool.Definition()
			fmt.Fprintf(&b, "- %s: %s\n", def.Name, def.Description)
		}
	}
	if len(skills) > 0 {
		b.WriteString("Installed user skills:\n")
		for _, skill := range skills {
			fmt.Fprintf(&b, "- %s: %s\n", skill.Name, skill.Description)
		}
	} else {
		b.WriteString("No user skills are currently installed in the configured skills directories.\n")
	}
	if len(config.EnabledMCPServers) > 0 {
		b.WriteString("Configured MCP servers:\n")
		for name, server := range config.EnabledMCPServers {
			state := "disabled"
			if server.Enabled {
				state = "enabled"
			}
			fmt.Fprintf(&b, "- %s: %s (%s)\n", name, server.Transport, state)
		}
	} else {
		b.WriteString("No MCP servers are currently configured.\n")
	}
	if config.Model.ReasoningEffort != "" {
		fmt.Fprintf(&b, "Requested reasoning effort: %s.\n", config.Model.ReasoningEffort)
	}
	if len(skillHints) > 0 {
		fmt.Fprintf(&b, "Active skill hints from user text: %s.\n", strings.Join(skillHints, ", "))
	}
	return strings.TrimSpace(b.String())
}

func lcContentText(content lc.Content) string {
	if content.Text != nil {
		return *content.Text
	}
	var parts []string
	for _, part := range content.Parts {
		if part.Text != "" {
			parts = append(parts, part.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func compactAny(value any, limit int) string {
	if value == nil {
		return ""
	}
	data, err := json.Marshal(value)
	if err != nil {
		return truncate(fmt.Sprint(value), limit)
	}
	return truncate(string(data), limit)
}

func closeAll(closers []func() error) {
	for _, closer := range closers {
		_ = closer()
	}
}
