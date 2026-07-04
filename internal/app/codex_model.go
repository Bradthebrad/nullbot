package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	tcagent "tinychain/agent"
	"tinychain/lc"
)

const defaultCodexSandbox = "workspace-write"

// CodexExecModel uses the local Codex CLI as a model backend. It intentionally
// does not read or reuse OpenAI API keys; Codex handles ChatGPT/API-key auth
// through its own local credential store.
type CodexExecModel struct {
	Config Config
	Model  string
}

func (m CodexExecModel) Call(ctx context.Context, messages []lc.BaseMessage, tools []tcagent.Tool) (lc.BaseMessage, error) {
	prompt := codexPromptFromMessages(messages, tools)
	if strings.TrimSpace(prompt) == "" {
		return lc.BaseMessage{}, errors.New("codex model: prompt is empty")
	}
	out, usage, err := runCodexExec(ctx, m.Config, m.Model, prompt)
	if err != nil {
		return lc.BaseMessage{}, err
	}
	msg := lc.AI(strings.TrimSpace(out))
	if usage.TotalTokens != 0 || usage.InputTokens != 0 || usage.OutputTokens != 0 {
		msg.UsageMetadata = &usage
	}
	msg.ResponseMetadata = map[string]any{
		"provider": "codex",
		"model":    firstNonEmpty(m.Model, "default"),
		"auth":     "codex",
	}
	return msg, nil
}

func runCodexExec(ctx context.Context, config Config, model, prompt string) (string, lc.UsageMetadata, error) {
	command, err := codexCommand()
	if err != nil {
		return "", lc.UsageMetadata{}, err
	}
	args := []string{"exec", "--json", "--ephemeral", "--sandbox", defaultCodexSandbox, "--ask-for-approval", "never"}
	if workdir, err := workspaceRoot(config); err == nil && workdir != "" {
		args = append(args, "--cd", workdir)
	}
	if strings.TrimSpace(model) != "" && model != "default" && model != "subscription-default" {
		args = append(args, "--model", strings.TrimSpace(model))
	}
	args = append(args, prompt)

	cmd := exec.CommandContext(ctx, command, args...)
	if workdir, err := workspaceRoot(config); err == nil && workdir != "" {
		cmd.Dir = workdir
	}
	cmd.Env = codexCommandEnv(config)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", lc.UsageMetadata{}, codexRunError(command, err, stderr.String())
	}
	text, usage := parseCodexJSONL(stdout.Bytes())
	if strings.TrimSpace(text) == "" {
		text = strings.TrimSpace(stdout.String())
	}
	if strings.TrimSpace(text) == "" {
		return "", usage, fmt.Errorf("codex model: no final response returned%s", stderrSuffix(stderr.String()))
	}
	return text, usage, nil
}

func codexPromptFromMessages(messages []lc.BaseMessage, tools []tcagent.Tool) string {
	var b strings.Builder
	b.WriteString("You are being used as the model backend for NullBot. Answer the latest user request directly and preserve the visible conversation context below.\n")
	if len(tools) > 0 {
		b.WriteString("NullBot has its own tool inventory, but this Codex-backed adapter returns a final answer rather than OpenAI-style tool calls. If workspace inspection or edits are needed, use the Codex CLI capabilities available in this run.\n")
	}
	b.WriteString("\nConversation:\n")
	for _, msg := range messages {
		text := strings.TrimSpace(lcContentText(msg.Content))
		if text == "" {
			continue
		}
		role := string(msg.Type)
		if role == "" {
			role = "message"
		}
		fmt.Fprintf(&b, "\n[%s]\n%s\n", strings.ToUpper(role), text)
	}
	return strings.TrimSpace(b.String())
}

func parseCodexJSONL(data []byte) (string, lc.UsageMetadata) {
	var final string
	var usage lc.UsageMetadata
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 || !bytes.HasPrefix(line, []byte("{")) {
			continue
		}
		var event struct {
			Type string `json:"type"`
			Item struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
			Usage struct {
				InputTokens           int `json:"input_tokens"`
				CachedInputTokens     int `json:"cached_input_tokens"`
				OutputTokens          int `json:"output_tokens"`
				ReasoningOutputTokens int `json:"reasoning_output_tokens"`
				TotalTokens           int `json:"total_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}
		if event.Type == "item.completed" && event.Item.Type == "agent_message" && strings.TrimSpace(event.Item.Text) != "" {
			final = event.Item.Text
		}
		if event.Type == "turn.completed" {
			usage.InputTokens = event.Usage.InputTokens
			usage.OutputTokens = event.Usage.OutputTokens
			usage.TotalTokens = event.Usage.TotalTokens
			details := map[string]int{}
			if event.Usage.CachedInputTokens != 0 {
				details["cached_tokens"] = event.Usage.CachedInputTokens
			}
			if len(details) > 0 {
				usage.InputTokenDetails = details
			}
			if event.Usage.ReasoningOutputTokens != 0 {
				usage.OutputTokenDetails = map[string]int{"reasoning_tokens": event.Usage.ReasoningOutputTokens}
			}
			if usage.TotalTokens == 0 {
				usage.TotalTokens = usage.InputTokens + usage.OutputTokens
			}
		}
	}
	return final, usage
}

func codexCommand() (string, error) {
	for _, env := range []string{"NULLBOT_CODEX_BIN", "CODEX_BIN"} {
		if path := strings.TrimSpace(os.Getenv(env)); path != "" {
			return path, nil
		}
	}
	path, err := exec.LookPath("codex")
	if err != nil {
		return "", fmt.Errorf("codex model: Codex CLI was not found. Install a runnable Codex CLI or set NULLBOT_CODEX_BIN")
	}
	return path, nil
}

func codexCommandEnv(config Config) []string {
	env := os.Environ()
	if strings.TrimSpace(config.AppDir) != "" {
		env = append(env, "NULLBOT_APP_DIR="+config.AppDir)
	}
	return env
}

func codexRunError(command string, err error, stderr string) error {
	stderr = strings.TrimSpace(stderr)
	if runtime.GOOS == "windows" && strings.Contains(strings.ToLower(command), `\windowsapps\`) {
		return fmt.Errorf("codex model: Windows found Codex at %s, but WindowsApps denied direct execution. Install the standalone Codex CLI or set NULLBOT_CODEX_BIN to a runnable codex executable%s", command, stderrSuffix(stderr))
	}
	if strings.Contains(strings.ToLower(err.Error()), "access is denied") {
		return fmt.Errorf("codex model: failed to run Codex CLI: access denied. Install a runnable Codex CLI or set NULLBOT_CODEX_BIN%s", stderrSuffix(stderr))
	}
	return fmt.Errorf("codex model: codex exec failed: %w%s", err, stderrSuffix(stderr))
}

func stderrSuffix(stderr string) string {
	stderr = strings.TrimSpace(stderr)
	if stderr == "" {
		return ""
	}
	return ": " + stderr
}
