package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	tcagent "github.com/Bradthebrad/tinychain/agent"
	"github.com/Bradthebrad/tinychain/lc"
	"github.com/Bradthebrad/tinychain/streaming"
)

// The Claude CLI provider runs the claude command-line tool (Claude Code) as
// the model, so a Claude subscription can power NullBot. The CLI signs in
// and keeps its own credentials; NullBot never reads them. Claude works with
// its own tools, limited to what the project's permission allows, and NullBot
// shows its text, thinking and tool calls as they happen.

const ClaudeCLIProvider = "claude-cli"

// ClaudeCLIModel is a model backed by `claude -p`.
type ClaudeCLIModel struct {
	Config          Config
	Model           string
	ReasoningEffort string
}

// claudeCommandFunc finds the claude executable; tests replace it.
var claudeCommandFunc = func() (string, error) {
	for _, env := range []string{"NULLBOT_CLAUDE_BIN", "CLAUDE_BIN"} {
		if path := strings.TrimSpace(os.Getenv(env)); path != "" {
			return path, nil
		}
	}
	path, err := exec.LookPath("claude")
	if err != nil {
		return "", errClaudeNotFound
	}
	return path, nil
}

var errClaudeNotFound = errors.New("Claude CLI: the claude command was not found. Install Claude Code (https://claude.com/claude-code), sign in by running claude once, or set NULLBOT_CLAUDE_BIN to its path")

// claudeSessions maps a conversation, as NullBot last saw it, to the CLI
// session holding it, so the next request continues that session instead of
// sending the whole conversation again.
var claudeSessions = struct {
	sync.Mutex
	ids   map[string]string
	order []string
}{ids: map[string]string{}}

const maxClaudeSessions = 500

func rememberClaudeSession(key, id string) {
	if key == "" || id == "" {
		return
	}
	claudeSessions.Lock()
	defer claudeSessions.Unlock()
	if _, ok := claudeSessions.ids[key]; !ok {
		claudeSessions.order = append(claudeSessions.order, key)
	}
	claudeSessions.ids[key] = id
	for len(claudeSessions.order) > maxClaudeSessions {
		delete(claudeSessions.ids, claudeSessions.order[0])
		claudeSessions.order = claudeSessions.order[1:]
	}
}

func claudeSessionFor(key string) string {
	claudeSessions.Lock()
	defer claudeSessions.Unlock()
	return claudeSessions.ids[key]
}

// claudeConversationKey identifies the visible conversation: who said what,
// without the system prompt, which NullBot rebuilds on every request.
func claudeConversationKey(scope string, messages []lc.BaseMessage) string {
	h := sha256.New()
	h.Write([]byte(scope))
	for _, m := range messages {
		if m.Type == lc.RoleSystem {
			continue
		}
		fmt.Fprintf(h, "\x00%s\x00%s", m.Type, strings.TrimSpace(lcContentText(m.Content)))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// claudePlan is how one request runs: the folder, the tools and the
// permission mode, from the primary project's permission.
type claudePlan struct {
	Dir     string
	AddDirs []string
	Tools   []string
	Allowed []string
	Mode    string
}

func claudePlanFor(config Config) (claudePlan, error) {
	plan := claudePlan{}
	caps := []Capability{CapRead}
	primary, ok := PrimaryProject(config)
	if ok && primary.Path != "" {
		plan.Dir = primary.Path
		caps = ProjectCapabilities(primary)
	} else if root, err := workspaceRoot(config); err == nil {
		plan.Dir = root
	} else {
		return plan, fmt.Errorf("Claude CLI: no project folder to work in: %w", err)
	}
	if info, err := os.Stat(plan.Dir); err != nil || !info.IsDir() {
		return plan, fmt.Errorf("Claude CLI: the project folder %s is not available", plan.Dir)
	}
	has := map[Capability]bool{}
	for _, c := range caps {
		has[c] = true
	}
	// Reading inside the project folders needs no approval; reading
	// elsewhere would, and nobody is there to give it, so it is refused.
	plan.Tools = []string{"Read", "Grep", "Glob", "WebFetch", "WebSearch", "TodoWrite"}
	plan.Allowed = []string{"WebFetch", "WebSearch", "TodoWrite"}
	plan.Mode = "dontAsk"
	if has[CapWrite] {
		// Edits inside the project folders are approved; others are refused.
		plan.Tools = append(plan.Tools, "Edit", "Write", "NotebookEdit")
		plan.Mode = "acceptEdits"
	}
	if has[CapExecute] {
		plan.Tools = append(plan.Tools, "Bash")
		plan.Allowed = append(plan.Allowed, "Bash")
	}
	// Other projects join only when they allow everything the primary one
	// does, since the CLI applies one set of tools to every folder.
	for _, p := range effectiveProjects(config) {
		if p.ID == primary.ID || strings.TrimSpace(p.Path) == "" {
			continue
		}
		allows := true
		for c := range has {
			if !ProjectAllows(p, c) {
				allows = false
			}
		}
		if info, err := os.Stat(p.Path); allows && err == nil && info.IsDir() {
			plan.AddDirs = append(plan.AddDirs, p.Path)
		}
	}
	return plan, nil
}

// claudeEnv is NullBot's environment without credentials, like a secret_run
// command's, keeping the CLI's own sign-in token. ANTHROPIC_API_KEY is left
// out so the CLI uses the subscription, not API billing.
func claudeEnv() []string {
	var env []string
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		upper := strings.ToUpper(key)
		if upper == "CLAUDECODE" || upper == "CLAUDE_CODE_ENTRYPOINT" {
			continue // NullBot itself may run inside Claude Code
		}
		if commandCredentialEnv(key) && upper != "CLAUDE_CODE_OAUTH_TOKEN" {
			continue
		}
		env = append(env, item)
	}
	return env
}

func (m ClaudeCLIModel) Call(ctx context.Context, messages []lc.BaseMessage, _ []tcagent.Tool) (lc.BaseMessage, error) {
	command, err := claudeCommandFunc()
	if err != nil {
		return lc.BaseMessage{}, err
	}
	plan, err := claudePlanFor(m.Config)
	if err != nil {
		return lc.BaseMessage{}, err
	}
	model := strings.TrimSpace(m.Model)
	if model == "default" {
		model = ""
	}
	scope := strings.Join([]string{m.Config.AppDir, model, plan.Dir}, "\x00")

	// The trailing requests are what is new; the rest may already be in a
	// session from the last call.
	cut := len(messages)
	for cut > 0 && messages[cut-1].Type == lc.RoleHuman {
		cut--
	}
	if cut == len(messages) {
		return lc.BaseMessage{}, errors.New("Claude CLI: there is no request to send")
	}
	resume := ""
	if cut > 0 {
		resume = claudeSessionFor(claudeConversationKey(scope, messages[:cut]))
	}
	var prompt string
	if resume != "" {
		prompt = claudeRequests(messages[cut:])
	} else {
		prompt = claudeTranscript(messages, cut)
	}
	if strings.TrimSpace(prompt) == "" {
		return lc.BaseMessage{}, errors.New("Claude CLI: the request is empty")
	}

	args := []string{"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages",
		"--strict-mcp-config", "--setting-sources", "user",
		"--permission-mode", plan.Mode, "--tools", strings.Join(plan.Tools, ","),
	}
	if len(plan.Allowed) > 0 {
		args = append(args, "--allowedTools", strings.Join(plan.Allowed, ","))
	}
	for _, dir := range plan.AddDirs {
		args = append(args, "--add-dir", dir)
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	switch effort := ProviderEffort(ClaudeCLIProvider, m.ReasoningEffort); effort {
	case "low", "medium", "high", "xhigh", "max":
		args = append(args, "--effort", effort)
	}
	if resume != "" {
		args = append(args, "--resume", resume)
	} else if system := claudeSystemText(messages); system != "" {
		file, err := writeClaudePromptFile(system)
		if err != nil {
			return lc.BaseMessage{}, err
		}
		defer removeClaudePromptFile(file)
		args = append(args, "--append-system-prompt-file", file)
	}

	run, err := runClaude(ctx, m.Config, command, args, plan.Dir, prompt)
	if err != nil && resume != "" && run.sessionGone() {
		// The CLI no longer has that session: start over with everything.
		claudeSessions.Lock()
		delete(claudeSessions.ids, claudeConversationKey(scope, messages[:cut]))
		claudeSessions.Unlock()
		return m.Call(ctx, messages, nil)
	}
	if err != nil {
		return lc.BaseMessage{}, err
	}
	msg := lc.AI(run.answer)
	if run.usage.TotalTokens != 0 {
		usage := run.usage
		msg.UsageMetadata = &usage
	}
	msg.ResponseMetadata = map[string]any{
		"provider": ClaudeCLIProvider,
		"model":    firstNonEmpty(run.model, model, "default"),
		"auth":     "claude-cli",
	}
	if run.costUSD > 0 {
		msg.ResponseMetadata["cost_usd"] = run.costUSD
	}
	rememberClaudeSession(claudeConversationKey(scope, append(append([]lc.BaseMessage(nil), messages...), msg)), run.sessionID)
	return msg, nil
}

func claudeRequests(messages []lc.BaseMessage) string {
	var parts []string
	for _, m := range messages {
		if text := strings.TrimSpace(lcContentText(m.Content)); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// claudeTranscript is the request for a new session: the conversation so far
// for context, then what is being asked now.
func claudeTranscript(messages []lc.BaseMessage, cut int) string {
	var history strings.Builder
	for _, m := range messages[:cut] {
		if m.Type == lc.RoleSystem {
			continue
		}
		text := strings.TrimSpace(lcContentText(m.Content))
		if text == "" {
			continue
		}
		role := "User"
		switch m.Type {
		case lc.RoleAI:
			role = "Assistant"
		case lc.RoleTool:
			role = "Tool result"
		}
		fmt.Fprintf(&history, "[%s]\n%s\n\n", role, text)
	}
	request := claudeRequests(messages[cut:])
	if history.Len() == 0 {
		return request
	}
	return "The conversation so far, for context:\n\n" + history.String() + "Now answer this:\n\n" + request
}

func claudeSystemText(messages []lc.BaseMessage) string {
	var parts []string
	for _, m := range messages {
		if m.Type == lc.RoleSystem {
			if text := strings.TrimSpace(lcContentText(m.Content)); text != "" {
				parts = append(parts, text)
			}
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "You are the model behind NullBot, and the user reads your replies in NullBot's chat. NullBot's instructions follow. " +
		"The NullBot tools they name are not available in this run: use your own tools, within the permissions you were given.\n\n" +
		strings.Join(parts, "\n\n")
}

func writeClaudePromptFile(text string) (string, error) {
	f, err := os.CreateTemp("", "nullbot-claude-*.md")
	if err != nil {
		return "", err
	}
	_ = os.Chmod(f.Name(), 0o600)
	if _, err := f.WriteString(text); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

func removeClaudePromptFile(path string) { _ = os.Remove(path) }

// --- running the CLI ---------------------------------------------------------

type claudeRun struct {
	sessionID string
	model     string
	answer    string
	usage     lc.UsageMetadata
	costUSD   float64
	isError   bool
	errorText string
	stderr    string
}

// sessionGone reports a failed resume of a session the CLI does not have.
func (r claudeRun) sessionGone() bool {
	text := strings.ToLower(r.errorText + " " + r.stderr)
	return strings.Contains(text, "no conversation found") || strings.Contains(text, "session") && strings.Contains(text, "not found")
}

// claudeStream turns the CLI's stream-json output into NullBot's live
// stream: answer text, thinking, commentary and tool steps, every piece
// redacted before it leaves.
type claudeStream struct {
	ctx      context.Context
	redactor *secretRedactor
	text     *streamRedactor
	thinking *streamRedactor
	run      *claudeRun

	line     bytes.Buffer
	skipping bool
	partial  bool            // stream_event deltas arrive
	current  strings.Builder // text of the message being written
	lastText string          // the last message's text, if the result has none
	tools    map[string]string
	meta     map[string]map[string]any
	usage    claudeUsage
}

type claudeUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

type claudeEvent struct {
	Type            string          `json:"type"`
	Subtype         string          `json:"subtype"`
	SessionID       string          `json:"session_id"`
	Model           string          `json:"model"`
	ParentToolUseID *string         `json:"parent_tool_use_id"`
	Event           json.RawMessage `json:"event"`
	Message         struct {
		Model   string               `json:"model"`
		Content []claudeContentBlock `json:"content"`
	} `json:"message"`
	Result       string      `json:"result"`
	IsError      bool        `json:"is_error"`
	TotalCostUSD float64     `json:"total_cost_usd"`
	Usage        claudeUsage `json:"usage"`
	Errors       []string    `json:"errors"`
}

type claudeContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     map[string]any  `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

type claudeStreamEvent struct {
	Type         string `json:"type"`
	ContentBlock struct {
		Type string `json:"type"`
	} `json:"content_block"`
	Delta struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Thinking string `json:"thinking"`
	} `json:"delta"`
}

func runClaude(ctx context.Context, config Config, command string, args []string, dir, prompt string) (claudeRun, error) {
	run := claudeRun{}
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = dir
	cmd.Env = claudeEnv()
	cmd.Stdin = strings.NewReader(prompt)
	var stderr cappedBuffer
	stderr.limit = 64 * 1024
	cmd.Stderr = &stderr
	redactor := currentRedactor(config)
	stream := &claudeStream{ctx: ctx, redactor: redactor, text: newStreamRedactor(redactor), thinking: newStreamRedactor(redactor), run: &run, tools: map[string]string{}, meta: map[string]map[string]any{}}
	// The stream is a writer, so a program the CLI leaves holding its
	// output cannot keep the call waiting past WaitDelay.
	cmd.Stdout = stream
	cmd.WaitDelay = 5 * time.Second
	proc := newSecretProcess(cmd)
	if runtime.GOOS != "windows" {
		// An interrupt lets the CLI end its turn and stop its commands.
		cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	}
	if err := proc.start(); err != nil {
		proc.finish()
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return run, errClaudeNotFound
		}
		return run, fmt.Errorf("Claude CLI: could not start %s: %w", filepath.Base(command), err)
	}
	waitErr := cmd.Wait()
	proc.finish()
	stream.finish()
	run.stderr = RedactSecrets(config, strings.TrimSpace(stderr.buf.String()))
	if ctx.Err() != nil {
		return run, ctx.Err()
	}
	if run.isError {
		return run, claudeError(firstNonEmpty(run.errorText, run.stderr, "the run failed"))
	}
	if waitErr != nil && !errors.Is(waitErr, exec.ErrWaitDelay) && run.answer == "" {
		return run, claudeError(firstNonEmpty(run.stderr, waitErr.Error()))
	}
	if strings.TrimSpace(run.answer) == "" {
		return run, claudeError(firstNonEmpty(run.stderr, "no answer came back"))
	}
	return run, nil
}

func claudeError(text string) error {
	text = strings.TrimSpace(text)
	if len(text) > 2000 {
		text = text[:2000] + "…"
	}
	lower := strings.ToLower(text)
	if strings.Contains(lower, "/login") || strings.Contains(lower, "not logged in") || strings.Contains(lower, "invalid api key") || strings.Contains(lower, "authentication") || strings.Contains(lower, "oauth") {
		return fmt.Errorf("Claude CLI: %s. Sign in by running claude and then /login; on a server, run claude setup-token and give NullBot the token as CLAUDE_CODE_OAUTH_TOKEN", strings.TrimRight(text, ". "))
	}
	if strings.Contains(lower, "unknown option") || strings.Contains(lower, "unknown argument") {
		return fmt.Errorf("Claude CLI: %s. Update it with claude update", strings.TrimRight(text, ". "))
	}
	return fmt.Errorf("Claude CLI: %s", text)
}

// maxClaudeLine bounds one line of output; a longer one (a huge tool
// result) is skipped.
const maxClaudeLine = 32 << 20

// Write takes the CLI's output as it comes and handles each whole line.
func (s *claudeStream) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			if s.line.Len()+len(p) <= maxClaudeLine {
				s.line.Write(p)
			} else {
				s.skipping = true
			}
			break
		}
		if !s.skipping && s.line.Len()+i <= maxClaudeLine {
			s.line.Write(p[:i])
			if line := bytes.TrimSpace(s.line.Bytes()); len(line) > 0 {
				s.handle(line)
			}
		}
		s.line.Reset()
		s.skipping = false
		p = p[i+1:]
	}
	return n, nil
}

func (s *claudeStream) handle(line []byte) {
	if !bytes.HasPrefix(line, []byte("{")) {
		return
	}
	var event claudeEvent
	if json.Unmarshal(line, &event) != nil {
		return
	}
	if event.SessionID != "" {
		s.run.sessionID = event.SessionID
	}
	// Subagents' own steps stay inside their Agent call.
	if event.ParentToolUseID != nil && *event.ParentToolUseID != "" {
		return
	}
	switch event.Type {
	case "system":
		if event.Subtype == "init" && event.Model != "" {
			s.run.model = event.Model
		}
	case "stream_event":
		s.partial = true
		var se claudeStreamEvent
		if json.Unmarshal(event.Event, &se) != nil {
			return
		}
		s.streamEvent(se)
	case "assistant":
		if event.Message.Model != "" {
			s.run.model = event.Message.Model
		}
		s.assistant(event.Message.Content)
	case "user":
		s.toolResults(event.Message.Content)
	case "result":
		s.run.answer = event.Result
		s.run.isError = event.IsError || strings.HasPrefix(event.Subtype, "error")
		if s.run.isError {
			s.run.errorText = firstNonEmpty(event.Result, strings.Join(event.Errors, "; "), event.Subtype)
		}
		s.run.costUSD = event.TotalCostUSD
		s.usage = event.Usage
	}
}

func (s *claudeStream) streamEvent(se claudeStreamEvent) {
	switch se.Type {
	case "message_start":
		s.endMessage()
	case "content_block_start":
		if se.ContentBlock.Type == "tool_use" {
			s.commentary()
		}
	case "content_block_delta":
		switch se.Delta.Type {
		case "text_delta":
			s.current.WriteString(se.Delta.Text)
			streaming.Emit(s.ctx, s.text.write(se.Delta.Text), false)
		case "thinking_delta":
			streaming.Emit(s.ctx, s.thinking.write(se.Delta.Thinking), true)
		}
	}
}

// commentary turns the text written so far in this message into
// commentary: it led to a tool call, so it is not the answer.
func (s *claudeStream) commentary() {
	streaming.Emit(s.ctx, s.text.flush(), false)
	streaming.Emit(s.ctx, s.thinking.flush(), true)
	if text := strings.TrimSpace(s.current.String()); text != "" {
		streaming.EmitCommentary(s.ctx, s.redactor.trimPartialSecret(s.redactor.redact(text)))
	}
	s.current.Reset()
}

func (s *claudeStream) endMessage() {
	streaming.Emit(s.ctx, s.text.flush(), false)
	streaming.Emit(s.ctx, s.thinking.flush(), true)
	if text := strings.TrimSpace(s.current.String()); text != "" {
		s.lastText = text
	}
	s.current.Reset()
}

func (s *claudeStream) assistant(blocks []claudeContentBlock) {
	hasTool := false
	for _, b := range blocks {
		if b.Type == "tool_use" {
			hasTool = true
		}
	}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if s.partial || strings.TrimSpace(b.Text) == "" {
				continue // already streamed
			}
			text := s.redactor.redact(b.Text)
			if hasTool {
				streaming.EmitCommentary(s.ctx, strings.TrimSpace(text))
			} else {
				streaming.Emit(s.ctx, text, false)
				s.lastText = b.Text
			}
		case "tool_use":
			if b.ID == "" || b.Name == "" {
				continue
			}
			if _, seen := s.tools[b.ID]; seen {
				continue
			}
			s.tools[b.ID] = b.Name
			meta := claudeStepMetadata(b.Name, b.Input)
			s.meta[b.ID] = meta
			streaming.EmitStep(s.ctx, streaming.Step{ID: b.ID, Name: b.Name, Input: redactArgs(s.redactor, b.Input), Metadata: meta})
		}
	}
}

func (s *claudeStream) toolResults(blocks []claudeContentBlock) {
	for _, b := range blocks {
		if b.Type != "tool_result" || b.ToolUseID == "" {
			continue
		}
		name, ok := s.tools[b.ToolUseID]
		if !ok {
			continue
		}
		meta := s.meta[b.ToolUseID]
		delete(s.tools, b.ToolUseID)
		delete(s.meta, b.ToolUseID)
		output := s.redactor.trimPartialSecret(shortText(s.redactor.redact(claudeResultText(b.Content)), 6000))
		step := streaming.Step{ID: b.ToolUseID, Name: name, Done: true, Output: output, Metadata: meta}
		if b.IsError {
			step.Error, step.Output = firstNonEmpty(output, "failed"), ""
		}
		streaming.EmitStep(s.ctx, step)
	}
}

// finish closes what is still open: text not yet flushed, and tool calls
// that never got a result (the run was stopped).
func (s *claudeStream) finish() {
	if line := bytes.TrimSpace(s.line.Bytes()); len(line) > 0 && !s.skipping {
		s.handle(line)
	}
	s.line.Reset()
	s.endMessage()
	ids := make([]string, 0, len(s.tools))
	for id := range s.tools {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		streaming.EmitStep(s.ctx, streaming.Step{ID: id, Name: s.tools[id], Done: true, Error: "stopped before it finished", Metadata: s.meta[id]})
	}
	if strings.TrimSpace(s.run.answer) == "" && !s.run.isError {
		s.run.answer = s.lastText
	}
	s.run.answer = strings.TrimSpace(s.redactor.redact(s.run.answer))
	s.run.errorText = s.redactor.redact(s.run.errorText)
	u := s.usage
	input := u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
	s.run.usage = lc.UsageMetadata{InputTokens: input, OutputTokens: u.OutputTokens, TotalTokens: input + u.OutputTokens}
	if u.CacheReadInputTokens > 0 {
		s.run.usage.InputTokenDetails = map[string]int{"cached_tokens": u.CacheReadInputTokens}
	}
}

func claudeResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var out []string
		for _, p := range parts {
			switch p.Type {
			case "text":
				out = append(out, p.Text)
			case "image":
				out = append(out, "[image]")
			}
		}
		return strings.Join(out, "\n")
	}
	return string(raw)
}

// claudeStepMetadata labels a CLI step for the activity view.
func claudeStepMetadata(name string, input map[string]any) map[string]any {
	meta := map[string]any{"source": "claude-cli", "server": "claude-cli", "server_title": "Claude CLI"}
	str := func(key string) string { s, _ := input[key].(string); return s }
	switch name {
	case "Read", "Grep", "Glob", "WebFetch", "WebSearch", "TodoWrite":
		meta["read_only"] = true
	case "Bash":
		meta["read_only"] = false
		if d := str("description"); d != "" {
			meta["title"] = d
		}
	case "Edit", "Write", "NotebookEdit":
		meta["read_only"] = false
		if p := firstNonEmpty(str("file_path"), str("notebook_path")); p != "" {
			meta["title"] = name + " " + filepath.Base(p)
		}
	}
	return meta
}

// redactArgs removes secret values from a tool call's arguments.
func redactArgs(r *secretRedactor, args map[string]any) map[string]any {
	if len(args) == 0 {
		return args
	}
	out := make(map[string]any, len(args))
	for k, v := range args {
		out[k] = redactAnyValue(r, v)
	}
	return out
}

func redactAnyValue(r *secretRedactor, v any) any {
	switch t := v.(type) {
	case string:
		return r.redact(t)
	case map[string]any:
		return redactArgs(r, t)
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = redactAnyValue(r, item)
		}
		return out
	}
	return v
}

// --- redacting a stream ------------------------------------------------------

// streamRedactor redacts text that arrives in pieces. It holds back any end
// of the text that could be the start of a secret, so a value split across
// pieces is still caught.
type streamRedactor struct {
	r       *secretRedactor
	pending string
	byFirst map[byte][]string
}

func newStreamRedactor(r *secretRedactor) *streamRedactor {
	s := &streamRedactor{r: r, byFirst: map[byte][]string{}}
	for _, v := range r.values {
		if v.value != "" {
			s.byFirst[v.value[0]] = append(s.byFirst[v.value[0]], v.value)
		}
	}
	return s
}

func (s *streamRedactor) write(text string) string {
	if len(s.byFirst) == 0 {
		return text
	}
	s.pending += text
	cut := s.cut()
	out := s.pending[:cut]
	s.pending = s.pending[cut:]
	return s.r.replacer.Replace(out)
}

// cut is where the safe part of the pending text ends: before the earliest
// suffix that is the start of a secret, and never inside a whole one.
func (s *streamRedactor) cut() int {
	p := s.pending
	cut := len(p)
	for i := 0; i < len(p); i++ {
		for _, v := range s.byFirst[p[i]] {
			if len(v) > len(p)-i && strings.HasPrefix(v, p[i:]) {
				cut = i
				break
			}
		}
		if cut == i {
			break
		}
	}
	for moved := true; moved; {
		moved = false
		for _, values := range s.byFirst {
			for _, v := range values {
				from := max(0, cut-len(v)+1)
				for at := from; at < cut; {
					idx := strings.Index(p[at:], v)
					if idx < 0 {
						break
					}
					start := at + idx
					if start >= cut {
						break
					}
					if start+len(v) > cut {
						cut, moved = start, true
						break
					}
					at = start + 1
				}
			}
		}
	}
	return cut
}

func (s *streamRedactor) flush() string {
	out := s.pending
	s.pending = ""
	if out == "" {
		return ""
	}
	return s.r.trimPartialSecret(s.r.replacer.Replace(out))
}

// --- account status ----------------------------------------------------------

// ClaudeCLIInfo is what NullBot knows about the claude command: where it is
// and whether it is signed in. It never reads the CLI's credentials.
type ClaudeCLIInfo struct {
	Path       string `json:"path,omitempty"`
	Installed  bool   `json:"installed"`
	LoggedIn   bool   `json:"logged_in"`
	AuthMethod string `json:"auth_method,omitempty"`
	Error      string `json:"error,omitempty"`
}

var claudeStatusCache struct {
	sync.Mutex
	info ClaudeCLIInfo
	at   time.Time
}

// claudeAuthStatusFunc runs `claude auth status --json`; tests replace it.
var claudeAuthStatusFunc = func(ctx context.Context, command string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, command, "auth", "status", "--json")
	cmd.Env = claudeEnv()
	cmd.WaitDelay = time.Second
	return cmd.Output()
}

// claudeCLIFound reports whether the claude command is there, without
// running it.
func claudeCLIFound() bool {
	_, err := claudeCommandFunc()
	return err == nil
}

// ClaudeCLIStatus reports the claude command's sign-in, checked at most once
// a minute.
func ClaudeCLIStatus(ctx context.Context) ClaudeCLIInfo {
	claudeStatusCache.Lock()
	defer claudeStatusCache.Unlock()
	if !claudeStatusCache.at.IsZero() && time.Since(claudeStatusCache.at) < time.Minute {
		return claudeStatusCache.info
	}
	info := ClaudeCLIInfo{}
	command, err := claudeCommandFunc()
	if err != nil {
		info.Error = err.Error()
	} else {
		info.Path, info.Installed = command, true
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		out, err := claudeAuthStatusFunc(ctx, command)
		cancel()
		var status struct {
			LoggedIn   bool   `json:"loggedIn"`
			AuthMethod string `json:"authMethod"`
		}
		if jerr := json.Unmarshal(bytes.TrimSpace(out), &status); jerr == nil {
			info.LoggedIn, info.AuthMethod = status.LoggedIn, status.AuthMethod
		} else if err != nil {
			info.Error = "could not ask the claude command whether it is signed in: " + err.Error()
		} else {
			info.Error = "the claude command gave an answer NullBot does not understand; update it with claude update"
		}
	}
	claudeStatusCache.info, claudeStatusCache.at = info, time.Now()
	return info
}

func claudeAccountInfo(ctx context.Context) AccountInfo {
	info := ClaudeCLIStatus(ctx)
	account := AccountInfo{ID: ClaudeCLIProvider, Label: "Claude subscription (CLI)", Kind: "command-line sign-in"}
	switch {
	case !info.Installed:
		account.Status = "not installed"
		account.Detail = "Install Claude Code (https://claude.com/claude-code) and sign in with your Claude subscription to use Claude CLI models. NullBot runs the claude command and never reads its credentials."
	case info.LoggedIn:
		account.Status = "signed in"
		account.Detail = "Claude CLI models use your Claude subscription through " + info.Path + ". Claude works with its own tools, within the project's permission."
	case info.Error != "":
		account.Status = "unknown"
		account.Detail = info.Error
	default:
		account.Status = "not signed in"
		account.Detail = "Run claude in a terminal and sign in with /login. On a server, run claude setup-token and give NullBot the token as CLAUDE_CODE_OAUTH_TOKEN."
	}
	return account
}
