package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const DefaultPrefix = "Null"
const defaultMaxIterations = 20

type Config struct {
	BrandPrefix         string              `json:"brand_prefix"`
	Tagline             string              `json:"tagline"`
	AppDir              string              `json:"app_dir"`
	Model               ModelConfig         `json:"model"`
	SubagentModel       ModelConfig         `json:"subagent_model,omitempty"`
	Agent               AgentConfig         `json:"agent"`
	Compaction          CompactionConfig    `json:"compaction"`
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

type CompactionConfig struct {
	Enabled             bool   `json:"enabled"`
	ApproxTokenLimit    int    `json:"approx_token_limit"`
	MessageCountLimit   int    `json:"message_count_limit"`
	KeepLastMessages    int    `json:"keep_last_messages"`
	ToolResultCharLimit int    `json:"tool_result_char_limit"`
	DefaultFocus        string `json:"default_focus,omitempty"`
}

type EditorConfig struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}

type UIConfig struct {
	Theme string `json:"theme"`
}

type MCPEntry struct {
	Name      string            `json:"name"`
	Command   string            `json:"command"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Transport string            `json:"transport"`
	Enabled   bool              `json:"enabled"`
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
			MessageCountLimit:   80,
			KeepLastMessages:    12,
			ToolResultCharLimit: 12000,
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
	if err := json.Unmarshal(data, &config); err != nil {
		return Config{}, err
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

func normalizeConfig(config Config) Config {
	if config.Agent.MaxIterations <= 0 || config.Agent.MaxIterations == 8 {
		config.Agent.MaxIterations = defaultMaxIterations
	}
	if config.Agent.MaxSubagents <= 0 {
		config.Agent.MaxSubagents = 3
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
	config.EnabledMCPServers = syncMCPWorkspaceArgs(config.EnabledMCPServers, config.WorkspaceDir)
	if config.PermissionDefaults == nil {
		config.PermissionDefaults = map[string]string{"coding": "deny", "shell": "ask", "network": "ask"}
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
