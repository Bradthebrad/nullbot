package app

import (
	"context"
	"sync"
	"time"
)

type App struct {
	mu           sync.Mutex
	config       Config
	history      []Message
	logs         []string
	logger       *Logger
	plan         string
	paused       bool
	activeCancel context.CancelFunc
}

type Message struct {
	Role    string    `json:"role"`
	Content string    `json:"content"`
	Time    time.Time `json:"time"`
}

type Reply struct {
	Message     string         `json:"message"`
	Command     string         `json:"command,omitempty"`
	OpenPanel   string         `json:"open_panel,omitempty"`
	Config      Config         `json:"config"`
	History     []Message      `json:"history"`
	Suggestions []string       `json:"suggestions,omitempty"`
	Data        map[string]any `json:"data,omitempty"`
}

func New(config Config) *App {
	logger := NewLogger(config)
	logger.Info("app initialized", "app_dir", config.AppDir, "provider", config.Model.Provider, "model", config.Model.Model)
	return &App{config: config, logger: logger}
}

func (a *App) State() Reply {
	a.mu.Lock()
	defer a.mu.Unlock()
	return Reply{
		Message:     "ready",
		Config:      a.config,
		History:     append([]Message{}, a.history...),
		Suggestions: commandNames(),
		Data:        map[string]any{"runtime": RuntimeStatus(a.config)},
	}
}

func (a *App) Config() Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.config
}

func (a *App) UpdateConfig(update func(*Config)) error {
	a.mu.Lock()
	config := a.config
	update(&config)
	if config.AppDir == "" {
		config.AppDir = a.config.AppDir
	}
	a.mu.Unlock()

	if err := SaveConfig(config); err != nil {
		a.logError("config save failed", "error", err)
		return err
	}

	a.mu.Lock()
	a.config = config
	a.logger = NewLogger(config)
	a.mu.Unlock()
	a.logInfo("config saved", "app_dir", config.AppDir, "provider", config.Model.Provider, "model", config.Model.Model)
	return nil
}

func (a *App) APIKeys() APIKeys {
	a.mu.Lock()
	config := a.config
	a.mu.Unlock()
	keys, _ := LoadAPIKeys(config)
	return keys
}

func (a *App) SaveAPIKeys(keys APIKeys) error {
	a.mu.Lock()
	config := a.config
	a.mu.Unlock()
	if err := SaveAPIKeys(config, keys); err != nil {
		a.logError("api keys save failed", "error", err)
		return err
	}
	a.logInfo("api keys saved", "openai_set", keys.OpenAI != "", "anthropic_set", keys.Anthropic != "", "openrouter_set", keys.OpenRouter != "")
	return nil
}

func (a *App) Submit(ctx context.Context, input string) Reply {
	a.mu.Lock()
	a.history = append(a.history, Message{Role: "user", Content: input, Time: time.Now().UTC()})
	a.mu.Unlock()
	a.logInfo("submit", "input", input)

	reply := a.Execute(ctx, input)

	a.mu.Lock()
	a.history = append(a.history, Message{Role: "assistant", Content: reply.Message, Time: time.Now().UTC()})
	a.logs = append(a.logs, time.Now().UTC().Format(time.RFC3339)+" "+reply.Message)
	reply.Config = a.config
	reply.History = append([]Message{}, a.history...)
	a.mu.Unlock()
	a.logInfo("reply", "command", reply.Command, "panel", reply.OpenPanel, "message", reply.Message)
	return reply
}

func (a *App) Logs() []string {
	a.mu.Lock()
	logger := a.logger
	memory := append([]string{}, a.logs...)
	a.mu.Unlock()
	fileLines := logger.Recent(200)
	if len(fileLines) > 0 {
		return fileLines
	}
	return memory
}

func (a *App) LastOutput() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := len(a.history) - 1; i >= 0; i-- {
		if a.history[i].Role == "assistant" {
			return a.history[i].Content
		}
	}
	return ""
}

func (a *App) Plan() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.plan
}

func (a *App) SetPlan(plan string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.plan = plan
}

func (a *App) setPaused(paused bool) {
	a.mu.Lock()
	a.paused = paused
	if paused && a.activeCancel != nil {
		a.activeCancel()
	}
	a.mu.Unlock()
	if paused {
		a.logInfo("pause requested")
	}
}

func (a *App) clearHistory() {
	a.mu.Lock()
	a.history = nil
	a.mu.Unlock()
	a.logInfo("visible history cleared")
}

func (a *App) beginWork(parent context.Context) context.Context {
	ctx, cancel := context.WithCancel(parent)
	a.mu.Lock()
	a.paused = false
	a.activeCancel = cancel
	a.mu.Unlock()
	return ctx
}

func (a *App) endWork() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.activeCancel = nil
}

func (a *App) logInfo(message string, fields ...any) {
	a.mu.Lock()
	logger := a.logger
	a.mu.Unlock()
	logger.Info(message, fields...)
}

func (a *App) logError(message string, fields ...any) {
	a.mu.Lock()
	logger := a.logger
	a.mu.Unlock()
	logger.Error(message, fields...)
}
