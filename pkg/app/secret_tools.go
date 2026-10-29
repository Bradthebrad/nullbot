package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Bradthebrad/tinychain/agent"
)

// secretsListTool shows agents which secrets exist and what they provide,
// never their values.
func secretsListTool(config Config, state *App) agent.Tool {
	return agent.ToolFunc{
		Name:        "secrets_list",
		Description: "List the secrets saved in NullBot's vault: names, kinds, what environment variables they provide and public details (a GitHub login, an SSH public key, a region). Values are never shown. Use a secret with secret_run.",
		Schema:      agent.ToolSchema(map[string]any{}),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			cfg := config
			if state != nil {
				cfg = state.Config()
			}
			secrets, err := ListSecrets(cfg)
			if err != nil {
				return "", fmt.Errorf("the vault could not be opened: %w", err)
			}
			type item struct {
				Name        string            `json:"name"`
				Kind        string            `json:"kind"`
				Description string            `json:"description,omitempty"`
				Provides    []string          `json:"provides"`
				Public      map[string]string `json:"public,omitempty"`
				Usable      bool              `json:"usable_by_agents"`
				Servers     []string          `json:"given_to_tool_servers,omitempty"`
			}
			out := []item{}
			for _, s := range secrets {
				public := s.Public
				if !s.AgentUse {
					// Who a cloud key signs in as is the account's business
					// only once the user lets agents use it.
					public = map[string]string{}
					for k, v := range s.Public {
						if k != "account" && k != "identity" && k != "check_note" {
							public[k] = v
						}
					}
				}
				out = append(out, item{s.Name, s.Kind, s.Description, s.Provides, public, s.AgentUse, s.Servers})
			}
			note := "Run commands with these secrets through secret_run (secrets: [names]). A secret with usable_by_agents=false must be allowed by the user in Settings > Secrets first. If what you need is missing, ask the user to add it there; never ask them to paste a secret into the chat."
			if len(out) == 0 {
				note = "No secrets are saved. Ask the user to add what you need in Settings > Secrets (or `nullbot secrets set`); never ask them to paste a secret into the chat."
			}
			return prettyJSON(map[string]any{"secrets": out, "note": note}), nil
		},
	}
}

func secretRunTool(state *App) agent.Tool {
	return agent.ToolFunc{
		Name: "secret_run",
		Description: "Run a command with saved secrets in its environment, without seeing them. Each secret provides variables (see secrets_list): a GitHub token gives GH_TOKEN/GITHUB_TOKEN and signs git in over HTTPS; an SSH key gives GIT_SSH_COMMAND and NULLBOT_SSH_KEY_FILE; AWS keys give AWS_ACCESS_KEY_ID and friends; a Google Cloud key gives GOOGLE_APPLICATION_CREDENTIALS. " +
			"Programs read the secrets from their environment; values are never put into arguments, where other programs could see them (prefer tools that read the environment: gh, git, aws, gcloud; with shell, \"$VAR\" works but puts the value in that program's arguments). Secret values are removed from the output. Waits for the command to finish (default 300 s); anything it leaves running is stopped. " +
			"save_output_as stores the command's standard output as a new secret instead of returning it, for credentials a command creates.",
		Schema: agent.ToolSchema(map[string]any{
			"command":         agent.StringProperty("Program to run, or a shell command when shell is true."),
			"args":            map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Arguments, when shell is false."},
			"secrets":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Names of the secrets to provide."},
			"cwd":             agent.StringProperty("Working folder inside a project with run permission. Defaults to the primary project."),
			"shell":           map[string]any{"type": "boolean", "description": "Run through sh -c (cmd.exe /C on Windows). Defaults to false."},
			"timeout_seconds": agent.NumberProperty("Stop the command after this many seconds (default 300, at most 3600)."),
			"reason":          agent.StringProperty("Why this command needs these secrets; shown to the user in the activity log."),
			"save_output_as": map[string]any{"type": "object", "description": "Store standard output as a new secret named name, provided to later commands as env.", "properties": map[string]any{
				"name":        map[string]any{"type": "string"},
				"env":         map[string]any{"type": "string"},
				"description": map[string]any{"type": "string"},
			}},
		}, "command", "secrets"),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			if state == nil {
				return "", errors.New("secret_run needs a running NullBot")
			}
			req := SecretRunRequest{
				Command: stringArg(args, "command"),
				Args:    stringsFromAny(args["args"]),
				Secrets: stringsFromAny(args["secrets"]),
				Cwd:     stringArg(args, "cwd"),
				Shell:   boolArg(args, "shell"),
				Timeout: time.Duration(intArg(args, "timeout_seconds", 300)) * time.Second,
				Reason:  stringArg(args, "reason"),
			}
			if save, ok := args["save_output_as"].(map[string]any); ok {
				req.SaveAs = &SecretOutputTarget{Name: stringArg(save, "name"), Env: stringArg(save, "env"), Description: stringArg(save, "description")}
			}
			result, err := state.RunWithSecrets(ctx, req)
			if err != nil {
				return "", err
			}
			return prettyJSON(result), nil
		},
	}
}

func stringsFromAny(value any) []string {
	switch v := value.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		if v == "" {
			return nil
		}
		return []string{v}
	}
	return nil
}

// SecretRunRequest is one command run with secrets.
type SecretRunRequest struct {
	Command string
	Args    []string
	Secrets []string
	Cwd     string
	Shell   bool
	Timeout time.Duration
	Reason  string
	SaveAs  *SecretOutputTarget
}

// SecretOutputTarget names the secret a command's output is saved as.
type SecretOutputTarget struct {
	Name        string `json:"name"`
	Env         string `json:"env"`
	Description string `json:"description,omitempty"`
}

// SecretRunResult is what the agent sees. Output has secrets removed.
type SecretRunResult struct {
	ExitCode   int      `json:"exit_code"`
	Stdout     string   `json:"stdout"`
	Stderr     string   `json:"stderr,omitempty"`
	TimedOut   bool     `json:"timed_out,omitempty"`
	DurationMs int64    `json:"duration_ms"`
	Secrets    []string `json:"secrets"`
	Cwd        string   `json:"cwd"`
	Saved      string   `json:"saved_as,omitempty"`
	Note       string   `json:"note,omitempty"`
}

const (
	maxSecretRunCapture = 8 << 20
	maxSecretRunShown   = 48 << 10
)

// RunWithSecrets runs a command with the named secrets in its environment.
// The caller (the tool wrapper) has already checked the project permission;
// it is checked again here against the resolved folder.
func (a *App) RunWithSecrets(ctx context.Context, req SecretRunRequest) (SecretRunResult, error) {
	config := a.Config()
	var result SecretRunResult
	req.Command = strings.TrimSpace(req.Command)
	if req.Command == "" {
		return result, errors.New("command is required")
	}
	if len(req.Secrets) == 0 {
		return result, errors.New("name at least one secret; use the code tools to run commands that need none")
	}
	if req.Timeout <= 0 {
		req.Timeout = 300 * time.Second
	}
	req.Timeout = min(req.Timeout, time.Hour)

	cwd := strings.TrimSpace(req.Cwd)
	if cwd == "" {
		primary, ok := PrimaryProject(config)
		if !ok {
			return result, errors.New("no primary project is configured")
		}
		cwd = primary.Path
	} else if normalized, ok := normalizePathValue(cwd, true); ok {
		// The same reading of ~ and file:// the permission check used.
		cwd = normalized
	}
	resolved, project, err := checkProjectCapability(config, cwd, CapExecute)
	if err != nil {
		return result, err
	}
	if info, err := os.Stat(resolved); err != nil || !info.IsDir() {
		return result, fmt.Errorf("%s is not a folder", resolved)
	}

	payload, err := loadSecretsPayload(config)
	if err != nil {
		return result, fmt.Errorf("the vault could not be opened: %w", err)
	}
	var chosen []Secret
	for _, name := range req.Secrets {
		i := secretIndex(payload, strings.TrimSpace(name))
		if i < 0 {
			return result, fmt.Errorf("there is no secret named %q; secrets_list shows what is saved", name)
		}
		s := payload.Secrets[i]
		if !s.AgentUse {
			return result, fmt.Errorf("%s is not available to agents; the user can allow it in Settings > Secrets", s.Name)
		}
		if !slices.ContainsFunc(chosen, func(c Secret) bool { return c.Name == s.Name }) {
			chosen = append(chosen, s)
		}
	}
	if req.SaveAs != nil {
		if err := checkSaveTarget(payload, *req.SaveAs); err != nil {
			return result, err
		}
	}
	sweepSecretTemp()
	injected, err := buildSecretEnv(chosen, true)
	if err != nil {
		return result, err
	}
	defer injected.cleanup()

	env := commandBaseEnv()
	env[AgentCommandEnv] = "1"
	for k, v := range injected.vars {
		env[k] = v
	}
	names := make([]string, 0, len(chosen))
	for _, s := range chosen {
		names = append(names, s.Name)
	}
	result.Secrets, result.Cwd = names, resolved

	runCtx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()
	// Values are never expanded into arguments: other programs on the
	// computer can read a process's arguments. Programs read the secrets
	// from their environment.
	var cmd *exec.Cmd
	if req.Shell {
		cmd = shellCommand(runCtx, req.Command)
	} else {
		cmd = exec.CommandContext(runCtx, req.Command, req.Args...)
	}
	cmd.Dir = resolved
	cmd.Env = envList(env)
	stdout := &cappedBuffer{limit: maxSecretRunCapture}
	stderr := &cappedBuffer{limit: maxSecretRunCapture}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	// Output still held open by something the command left behind is given
	// up after a moment; whatever that was is stopped below.
	cmd.WaitDelay = 3 * time.Second
	proc := newSecretProcess(cmd)

	summary := req.Command
	if !req.Shell {
		summary = strings.TrimSpace(req.Command + " " + strings.Join(req.Args, " "))
	}
	summary = shortText(redactInjected(RedactSecrets(config, summary), injected.vars), 160)
	detail := fmt.Sprintf("%s in %s: %s", strings.Join(names, ", "), projectDisplayName(project), summary)
	if why := strings.TrimSpace(req.Reason); why != "" {
		detail += " (" + shortText(redactInjected(RedactSecrets(config, why), injected.vars), 160) + ")"
	}
	a.appendActivity(ActivityRecord{Time: time.Now().UTC(), Kind: "secret", Name: "secret_run", Status: "started", Detail: detail})
	a.logInfo("secret used", "secrets", strings.Join(names, ","), "project", project.ID, "program", firstWord(req.Command), "reason", shortText(req.Reason, 200))

	started := time.Now()
	runErr := proc.start()
	if runErr == nil {
		runErr = cmd.Wait()
	}
	proc.finish()
	result.DurationMs = time.Since(started).Milliseconds()
	if runCtx.Err() == context.DeadlineExceeded {
		result.TimedOut = true
	}
	var exit *exec.ExitError
	switch {
	case runErr == nil, errors.Is(runErr, exec.ErrWaitDelay):
		if cmd.ProcessState != nil {
			result.ExitCode = cmd.ProcessState.ExitCode()
		}
	case errors.As(runErr, &exit):
		result.ExitCode = exit.ExitCode()
	case result.TimedOut:
		result.ExitCode = -1
	default:
		msg := redactInjected(RedactSecrets(config, runErr.Error()), injected.vars)
		a.appendActivity(ActivityRecord{Time: time.Now().UTC(), Kind: "secret", Name: "secret_run", Status: "error", Detail: detail, Error: msg})
		return result, fmt.Errorf("could not run %s: %s", firstWord(req.Command), msg)
	}

	redactor := currentRedactor(config)
	clean := func(b *cappedBuffer) string {
		text := b.String()
		if b.dropped > 0 {
			text = redactor.trimPartialSecret(text)
		}
		text = redactor.redact(text)
		text = redactInjected(text, injected.vars)
		if b.dropped > 0 {
			text += fmt.Sprintf("\n[%d more bytes not kept]", b.dropped)
		}
		return headTail(text, maxSecretRunShown)
	}
	if req.SaveAs != nil && result.ExitCode == 0 && !result.TimedOut {
		value := strings.TrimSpace(stdout.String())
		if value == "" || stdout.dropped > 0 || len(value) > 64*1024 {
			result.Note = "standard output was empty or too large to save as a secret"
		} else if err := saveAgentSecret(config, *req.SaveAs, value); err != nil {
			result.Note = "could not save the output as a secret: " + err.Error()
		} else {
			a.secretsChanged(false, nil)
			result.Saved = req.SaveAs.Name
			result.Stdout = fmt.Sprintf("[saved as secret %s: %d bytes, provided as %s]", req.SaveAs.Name, len(value), req.SaveAs.Env)
		}
	}
	if result.Saved == "" {
		result.Stdout = clean(stdout)
	}
	result.Stderr = clean(stderr)
	status := "done"
	if result.ExitCode != 0 || result.TimedOut {
		status = "error"
	}
	a.appendActivity(ActivityRecord{Time: time.Now().UTC(), Kind: "secret", Name: "secret_run", Status: status, Detail: fmt.Sprintf("%s (exit %d)", detail, result.ExitCode), DurationMs: result.DurationMs})
	return result, nil
}

// saveAgentSecret stores what a command printed as a secret, in one step
// with the check that it replaces nothing the user made.
func saveAgentSecret(config Config, target SecretOutputTarget, value string) error {
	if err := validateEnvName(target.Env); err != nil {
		return err
	}
	return updateSecrets(config, func(payload *vaultPayload) error {
		now := time.Now().UTC().Truncate(time.Second)
		i := secretIndex(*payload, target.Name)
		s := Secret{Name: target.Name, Kind: SecretGeneric, CreatedBy: "agent", Created: now}
		if i >= 0 {
			s = payload.Secrets[i]
			if s.CreatedBy != "agent" || s.Kind != SecretGeneric {
				return fmt.Errorf("a secret named %s already exists and was not made by an agent", target.Name)
			}
		}
		s.Values = map[string]string{"value": value}
		if s.Public == nil {
			s.Public = map[string]string{}
		}
		s.Public["env"] = target.Env
		delete(s.Public, "as_file")
		if d := strings.TrimSpace(target.Description); d != "" {
			s.Description = d
		}
		s.AgentUse = true
		s.Updated = now
		if i >= 0 {
			payload.Secrets[i] = s
		} else {
			payload.Secrets = append(payload.Secrets, s)
		}
		return nil
	})
}

var sweepOnce sync.Once

// sweepSecretTemp removes temporary key folders a crashed NullBot left
// behind (older than an hour).
func sweepSecretTemp() {
	sweepOnce.Do(func() {
		// Key files and gcloud's token cache left by a NullBot that was
		// killed.
		// A plan's run can take hours (machines with GPUs start slowly), so
		// its folders are left longer.
		for pattern, age := range map[string]time.Duration{"nullbot-secrets-*": time.Hour, "nullbot-infra-*": 12 * time.Hour} {
			dirs, _ := filepath.Glob(filepath.Join(os.TempDir(), pattern))
			for _, dir := range dirs {
				if info, err := os.Lstat(dir); err == nil && info.IsDir() && time.Since(info.ModTime()) > age {
					(&secretEnv{dir: dir}).cleanup()
				}
			}
		}
	})
}

// redactInjected removes the exact values given to this command, including
// short ones the vault-wide redactor skips (an AWS region is public, but
// short tokens are not).
func redactInjected(text string, vars map[string]string) string {
	for key, value := range vars {
		if len(value) >= 6 && !strings.HasPrefix(key, "GIT_CONFIG") && key != "GIT_TERMINAL_PROMPT" && !publicEnvValue(key) {
			text = strings.ReplaceAll(text, value, "[secret:"+key+"]")
		}
	}
	return text
}

// publicEnvValue: variables whose values are paths or public settings.
func publicEnvValue(key string) bool {
	switch strings.ToUpper(key) {
	case "AWS_REGION", "AWS_DEFAULT_REGION", "GH_HOST", "GOOGLE_CLOUD_PROJECT", "CLOUDSDK_CORE_PROJECT",
		"NULLBOT_SSH_KEY_FILE", "GIT_SSH_COMMAND", "GOOGLE_APPLICATION_CREDENTIALS", "CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE":
		return true
	}
	return false
}

func checkSaveTarget(payload vaultPayload, target SecretOutputTarget) error {
	if !secretNamePattern.MatchString(target.Name) {
		return errors.New("save_output_as.name: use lowercase letters, digits, dots, dashes or underscores")
	}
	if err := validateEnvName(target.Env); err != nil {
		return fmt.Errorf("save_output_as.env: %w", err)
	}
	if i := secretIndex(payload, target.Name); i >= 0 && payload.Secrets[i].CreatedBy != "agent" {
		return fmt.Errorf("a secret named %s already exists and was not made by an agent; choose another name", target.Name)
	}
	return nil
}

func firstWord(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// headTail keeps the start and end of long text.
func headTail(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	half := limit / 2
	return text[:half] + fmt.Sprintf("\n[… %d bytes …]\n", len(text)-2*half) + text[len(text)-half:]
}

// cappedBuffer keeps the first limit bytes written to it.
type cappedBuffer struct {
	buf     bytes.Buffer
	limit   int
	dropped int64
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	room := b.limit - b.buf.Len()
	if room <= 0 {
		b.dropped += int64(len(p))
		return len(p), nil
	}
	if len(p) > room {
		b.buf.Write(p[:room])
		b.dropped += int64(len(p) - room)
		return len(p), nil
	}
	b.buf.Write(p)
	return len(p), nil
}

func (b *cappedBuffer) String() string { return b.buf.String() }

// shortText is s on one line, cut to max runes.
func shortText(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		return string(r[:max-1]) + "…"
	}
	return s
}
