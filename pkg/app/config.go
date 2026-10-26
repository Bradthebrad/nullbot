package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const DefaultPrefix = "Null"
const defaultMaxIterations = 20

type Config struct {
	Projects            []Project           `json:"projects"`
	PrimaryProjectID    string              `json:"primary_project_id"`
	BrandPrefix         string              `json:"brand_prefix"`
	BotName             string              `json:"bot_name,omitempty"`
	Tagline             string              `json:"tagline"`
	AppDir              string              `json:"app_dir"`
	Model               ModelConfig         `json:"model"`
	SubagentModel       ModelConfig         `json:"subagent_model,omitempty"`
	Agent               AgentConfig         `json:"agent"`
	Compaction          CompactionConfig    `json:"compaction"`
	Prompts             PromptConfig        `json:"prompts,omitempty"`
	Editor              EditorConfig        `json:"editor"`
	UI                  UIConfig            `json:"ui"`
	WorkspaceDir        string              `json:"workspace_dir"`
	SkillDirs           []string            `json:"skill_dirs"`
	EnabledMCPServers   map[string]MCPEntry `json:"enabled_mcp_servers"`
	PermissionDefaults  map[string]string   `json:"permission_defaults"`
	HistoryRecentLimit  int                 `json:"history_recent_limit"`
	ArtifactRecentLimit int                 `json:"artifact_recent_limit"`
}

type ModelConfig struct {
	Provider        string  `json:"provider"`
	Model           string  `json:"model"`
	ReasoningEffort string  `json:"reasoning_effort,omitempty"`
	Temperature     float64 `json:"temperature,omitempty"`
	MaxTokens       int     `json:"max_tokens,omitempty"`
}

type AgentConfig struct {
	MaxIterations int  `json:"max_iterations"`
	MaxSubagents  int  `json:"max_subagents"`
	UseResponses  bool `json:"use_responses"`
}

// CompactionConfig controls how NullBot keeps requests inside each model's
// context window. See docs/CONTEXT.md.
type CompactionConfig struct {
	Enabled bool `json:"enabled"`
	// ApproxTokenLimit is the context size used for a model whose size is
	// unknown: no override, nothing reported by its provider, and not a
	// family NullBot knows.
	ApproxTokenLimit int `json:"approx_token_limit"`
	// ContextWindows overrides a model's context size, keyed
	// "provider/model".
	ContextWindows map[string]int `json:"context_windows,omitempty"`
	// ShortenRatio is how full the context gets before earlier tool results
	// are shortened to the tool, its arguments and the start of its output.
	ShortenRatio float64 `json:"shorten_ratio,omitempty"`
	// SummarizeRatio is how full it gets before the model summarizes
	// older messages.
	SummarizeRatio float64 `json:"summarize_ratio,omitempty"`
	// ThresholdRatio is the former single threshold. A customized value
	// becomes SummarizeRatio.
	ThresholdRatio float64 `json:"threshold_ratio,omitempty"`
	// HistoryRatio is how much of the window earlier messages in the
	// conversation may take when a new request starts.
	HistoryRatio float64 `json:"history_ratio,omitempty"`
	// MessageCountLimit also starts shortening at this many messages in one
	// request.
	MessageCountLimit int `json:"message_count_limit"`
	// KeepLastMessages is used only when a model's size can't be measured.
	KeepLastMessages int `json:"keep_last_messages"`
	// ToolResultCharLimit is the most of an oversized tool result shown.
	ToolResultCharLimit int `json:"tool_result_char_limit"`
	// ToolDigestChars is how much of a tool's output a shortened result
	// keeps.
	ToolDigestChars int    `json:"tool_digest_chars,omitempty"`
	DefaultFocus    string `json:"default_focus,omitempty"`
}

type PromptConfig struct {
	ManagerPrompt  string `json:"manager_prompt,omitempty"`
	SubagentPrompt string `json:"subagent_prompt,omitempty"`
	ManagerAppend  string `json:"manager_append,omitempty"`
	SubagentAppend string `json:"subagent_append,omitempty"`
}

type EditorConfig struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}

type UIConfig struct {
	Theme                 string `json:"theme"`
	SuggestMatchingSkills bool   `json:"suggest_matching_skills"`
	// Appearance is system, light or dark (desktop UI). Accent names the
	// signal color used for live state and focus.
	Appearance string `json:"appearance,omitempty"`
	Accent     string `json:"accent,omitempty"`
}

type MCPEntry struct {
	Name      string            `json:"name"`
	Command   string            `json:"command"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Transport string            `json:"transport"`
	Enabled   bool              `json:"enabled"`
	// Headers are sent with every request to a remote (HTTP) server, for
	// example an Authorization header.
	Headers map[string]string `json:"headers,omitempty"`
	// ToolPolicies overrides the capability a tool is treated as needing:
	// auto, read, write, delete, execute or blocked.
	ToolPolicies map[string]string `json:"tool_policies,omitempty"`
}

func DefaultConfig() Config {
	appDir := DefaultAppDir(DefaultPrefix)
	return Config{
		BrandPrefix: DefaultPrefix,
		Tagline:     "It's just a client - no magic here.",
		AppDir:      appDir,
		Model: ModelConfig{
			Provider: "openai",
			Model:    "gpt-4.1-mini",
		},
		Agent: AgentConfig{
			MaxIterations: defaultMaxIterations,
			MaxSubagents:  3,
			UseResponses:  true,
		},
		Compaction: CompactionConfig{
			Enabled:             true,
			ApproxTokenLimit:    64000,
			ShortenRatio:        0.60,
			SummarizeRatio:      0.85,
			HistoryRatio:        0.30,
			MessageCountLimit:   80,
			KeepLastMessages:    12,
			ToolResultCharLimit: 12000,
			ToolDigestChars:     600,
		},
		Editor: EditorConfig{
			Command: defaultEditor(),
		},
		UI:           UIConfig{Theme: "steel"},
		WorkspaceDir: defaultWorkspaceDir(),
		SkillDirs: []string{
			filepath.Join(appDir, "skills"),
		},
		EnabledMCPServers:   map[string]MCPEntry{},
		PermissionDefaults:  map[string]string{"coding": "deny", "shell": "ask", "network": "ask"},
		HistoryRecentLimit:  20,
		ArtifactRecentLimit: 20,
	}
}

func LoadOrInitConfigAt(appDir string) (Config, error) {
	config := DefaultConfig()
	if appDir != "" {
		config.AppDir = appDir
		config.SkillDirs = []string{filepath.Join(appDir, "skills")}
	}
	return loadOrInit(config)
}

func DefaultAppDir(prefix string) string {
	if override := os.Getenv("NULLBOT_APP_DIR"); override != "" {
		return override
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "." + strings.ToLower(prefix) + "bot"
	}
	return filepath.Join(home, "."+strings.ToLower(prefix)+"bot")
}

func LoadOrInitConfig() (Config, error) {
	return loadOrInit(DefaultConfig())
}

func loadOrInit(config Config) (Config, error) {
	config = normalizeConfig(config)
	if err := EnsureAppDir(config); err != nil {
		return Config{}, err
	}
	path := filepath.Join(config.AppDir, "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			if err := SaveConfig(config); err != nil {
				return Config{}, err
			}
			return config, nil
		}
		return Config{}, err
	}
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	config.Projects = nil
	config.PrimaryProjectID = ""
	if err := json.Unmarshal(data, &config); err != nil {
		return Config{}, err
	}
	// The defaults filled in the new ratios before the file was read; a file
	// from before they existed must not keep them over its own threshold.
	var raw struct {
		Compaction map[string]json.RawMessage `json:"compaction"`
	}
	if json.Unmarshal(data, &raw) == nil && raw.Compaction != nil {
		if _, ok := raw.Compaction["summarize_ratio"]; !ok {
			config.Compaction.SummarizeRatio = 0
		}
		if _, ok := raw.Compaction["shorten_ratio"]; !ok {
			config.Compaction.ShortenRatio = 0
		}
	}
	config = normalizeConfig(config)
	if config.AppDir == "" {
		config.AppDir = DefaultAppDir(config.BrandPrefix)
	}
	_ = SaveConfig(config)
	return config, EnsureAppDir(config)
}

func SaveConfig(config Config) error {
	config = normalizeConfig(config)
	if err := EnsureAppDir(config); err != nil {
		return err
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(config.AppDir, "config.json"), append(data, '\n'), 0600)
}

func EnsureAppDir(config Config) error {
	config = normalizeConfig(config)
	dirs := []string{
		config.AppDir,
		filepath.Join(config.AppDir, "skills"),
		filepath.Join(config.AppDir, "mcp"),
		filepath.Join(config.AppDir, "api"),
		filepath.Join(config.AppDir, "history"),
		filepath.Join(config.AppDir, "logs"),
		filepath.Join(config.AppDir, "market"),
		filepath.Join(config.AppDir, "plans"),
		filepath.Join(config.AppDir, "usage"),
		filepath.Join(config.AppDir, "artifacts"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	return ensureDefaultSkill(defaultSkillPath(config))
}

func normalizeCompaction(c CompactionConfig) CompactionConfig {
	if c.SummarizeRatio <= 0 || c.SummarizeRatio >= 1 {
		c.SummarizeRatio = 0.85
		// 0.75 was the old default; anything else was chosen on purpose.
		if c.ThresholdRatio > 0 && c.ThresholdRatio < 1 && c.ThresholdRatio != 0.75 {
			c.SummarizeRatio = c.ThresholdRatio
		}
	}
	c.ThresholdRatio = 0
	if c.ShortenRatio <= 0 {
		c.ShortenRatio = 0.60
	}
	if c.ShortenRatio >= c.SummarizeRatio {
		c.ShortenRatio = c.SummarizeRatio * 0.7
	}
	if c.HistoryRatio <= 0 {
		c.HistoryRatio = 0.30
	}
	c.HistoryRatio = math.Min(math.Max(c.HistoryRatio, 0.05), c.ShortenRatio)
	if c.ToolDigestChars <= 0 {
		c.ToolDigestChars = 600
	}
	// Build a new map: configs are copied by value, so the map is shared
	// with running agents and must never be changed in place.
	if len(c.ContextWindows) > 0 {
		windows := make(map[string]int, len(c.ContextWindows))
		for key, tokens := range c.ContextWindows {
			if tokens >= 1024 && strings.TrimSpace(key) != "" {
				windows[key] = tokens
			}
		}
		c.ContextWindows = windows
	}
	return c
}

func normalizeConfig(config Config) Config {
	if config.Agent.MaxIterations <= 0 || config.Agent.MaxIterations == 8 {
		config.Agent.MaxIterations = defaultMaxIterations
	}
	if config.Agent.MaxSubagents <= 0 {
		config.Agent.MaxSubagents = 3
	}
	config.Compaction = normalizeCompaction(config.Compaction)
	if config.Compaction.ApproxTokenLimit <= 0 {
		config.Compaction.ApproxTokenLimit = 64000
	}
	if config.Compaction.KeepLastMessages <= 0 {
		config.Compaction.KeepLastMessages = 12
	}
	if config.Compaction.ToolResultCharLimit <= 0 {
		config.Compaction.ToolResultCharLimit = 12000
	}
	if strings.TrimSpace(config.SubagentModel.Provider) == "" {
		config.SubagentModel = config.Model
	}
	if strings.TrimSpace(config.SubagentModel.Model) == "" {
		config.SubagentModel.Model = config.Model.Model
	}
	if strings.TrimSpace(config.BrandPrefix) == "" {
		config.BrandPrefix = DefaultPrefix
	}
	if strings.TrimSpace(config.AppDir) == "" {
		config.AppDir = DefaultAppDir(config.BrandPrefix)
	}
	if len(config.SkillDirs) == 0 {
		config.SkillDirs = []string{filepath.Join(config.AppDir, "skills")}
	}
	if strings.TrimSpace(config.WorkspaceDir) == "" {
		config.WorkspaceDir = defaultWorkspaceDir()
	}
	if abs, err := filepath.Abs(config.WorkspaceDir); err == nil {
		config.WorkspaceDir = normalizeWorkspaceDir(abs)
	}
	if config.EnabledMCPServers == nil {
		config.EnabledMCPServers = map[string]MCPEntry{}
	}
	normalizeProjects(&config)
	config.EnabledMCPServers = syncMCPWorkspaceArgs(config.EnabledMCPServers, config.WorkspaceDir)
	if config.PermissionDefaults == nil {
		config.PermissionDefaults = map[string]string{"coding": "deny", "shell": "ask", "network": "ask"}
	}
	if strings.TrimSpace(config.UI.Theme) == "" {
		config.UI.Theme = "steel"
	}
	return config
}

func syncMCPWorkspaceArgs(servers map[string]MCPEntry, workspace string) map[string]MCPEntry {
	if len(servers) == 0 || strings.TrimSpace(workspace) == "" {
		return servers
	}
	out := make(map[string]MCPEntry, len(servers))
	for id, entry := range servers {
		entry.Args = append([]string{}, entry.Args...)
		if shouldPassWorkspaceToMCP(id, entry) && !hasWorkspaceArg(entry.Args) {
			entry.Args = append(entry.Args, "--workspace", workspace)
		}
		for i, arg := range entry.Args {
			if arg == "{{workspace}}" {
				entry.Args[i] = workspace
				continue
			}
			if arg == "--workspace" && i+1 < len(entry.Args) {
				entry.Args[i+1] = workspace
			}
		}
		out[id] = entry
	}
	return out
}

func shouldPassWorkspaceToMCP(id string, entry MCPEntry) bool {
	id = strings.ToLower(id)
	command := strings.ToLower(filepath.Base(entry.Command))
	return strings.Contains(id, "nullbot-code-mcp") ||
		strings.Contains(id, "nullbot-parsers-mcp") ||
		strings.Contains(command, "nullbot-code-mcp") ||
		strings.Contains(command, "nullbot-parsers-mcp")
}

func hasWorkspaceArg(args []string) bool {
	for _, arg := range args {
		if arg == "--workspace" {
			return true
		}
	}
	return false
}

func DisplayName(config Config) string {
	name := strings.TrimSpace(config.BotName)
	if name != "" {
		return name
	}
	prefix := strings.TrimSpace(config.BrandPrefix)
	if prefix == "" {
		prefix = DefaultPrefix
	}
	return prefix + "Bot"
}

func defaultEditor() string {
	if runtime.GOOS == "windows" {
		return "notepad"
	}
	return "vi"
}

func defaultWorkspaceDir() string {
	dir, err := os.Getwd()
	if err == nil {
		return normalizeWorkspaceDir(dir)
	}
	home, err := os.UserHomeDir()
	if err == nil {
		return home
	}
	return "."
}

func normalizeWorkspaceDir(dir string) string {
	dir = filepath.Clean(dir)
	if strings.EqualFold(filepath.Base(dir), "dist") {
		parent := filepath.Dir(dir)
		if looksLikeProjectRoot(parent) {
			return parent
		}
	}
	return dir
}

func looksLikeProjectRoot(dir string) bool {
	for _, marker := range []string{".git", "go.mod", "package.json", "pyproject.toml", "Cargo.toml"} {
		if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
			return true
		}
	}
	return false
}
