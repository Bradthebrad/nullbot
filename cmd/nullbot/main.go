// Command nullbot runs NullBot in the terminal UI, or headless: one request
// from the command line, or a long-running process that runs scheduled tasks
// and answers requests read from standard input.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Bradthebrad/nullbot/internal/tui"
	"github.com/Bradthebrad/nullbot/pkg/app"
	"github.com/Bradthebrad/nullbot/pkg/vault"
)

// version is set at build time: -ldflags "-X main.version=v0.5.0".
var version = "dev"

const usage = `NullBot: a small, auditable agent with controllable subagents.

Usage:
  nullbot                     open the terminal UI
  nullbot -p "request"        run one request headless and print the answer
  nullbot -p - < request.txt  read the request from standard input
  nullbot serve               run headless: scheduled tasks run, and each line
                              on standard input is a request
  nullbot secrets             list saved secrets; nullbot secrets help for more
  nullbot vault               show how the secrets vault is protected
  nullbot models              list models; nullbot models help for local
                              model servers and installing models
  nullbot infra               cloud machines and storage NullBot made, and
                              plans waiting for approval; nullbot infra help
  nullbot version             print the version

Options (headless):
  --json                 print each reply as JSON: the answer, its status, the
                         tool calls made and the session it was saved to
  --activity             stream progress (tool calls, subagents) to standard
                         error
  --output FILE          write the reply to FILE instead of standard output
  --project NAME         work in this project for the run (its id or name)
  --model P/MODEL        use this provider/model for the manager for the run
  --subagent-model P/MODEL
                         use this provider/model for subagents for the run
  --effort LEVEL         reasoning effort: minimal, low, medium, high or xhigh
  --max-iterations N     at most N model turns per agent
  --max-subagents N      at most N subagents at once
  --continue             carry on the most recent conversation
  --session ID           carry on this saved conversation (see History)
  --timeout DURATION     stop the request after this long, such as 10m
  --app-dir DIR          use another NullBot folder (default ~/.nullbot, or
                         NULLBOT_APP_DIR)

Headless runs use the same configuration, projects, permissions, skills,
MCP servers and history as the terminal and desktop apps; options change
this run only. Exit status is 0 when the request finished, 1 when it
failed, 2 for a usage error, 124 when --timeout stopped it, 130 when it
was interrupted.
`

func main() {
	// Keep the vault passphrase in memory only; nothing NullBot starts
	// inherits it.
	vault.TakeEnvPassphrase()
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

type options struct {
	prompt, project, model, appDir string
	json, activity                 bool

	subagentModel, effort, output, session string
	maxIterations, maxSubagents            int
	continueLast                           bool
	timeout                                time.Duration
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	// nullbot [--app-dir DIR] vault|secrets ... has its own options.
	if appDir, rest, ok := vaultCommand(args); ok {
		if rest[0] == "models" {
			return runModelsCLI(rest[1:], appDir, stdout, stderr)
		}
		if rest[0] == "infra" {
			return runInfraCLI(rest[1:], appDir, stdin, stdout, stderr)
		}
		return runVaultCLI(rest[0], rest[1:], appDir, stdin, stdout, stderr)
	}
	command := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command, args = args[0], args[1:]
	}
	var opts options
	flags := flag.NewFlagSet("nullbot", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprint(stderr, usage) }
	flags.StringVar(&opts.prompt, "p", "", "")
	flags.StringVar(&opts.prompt, "prompt", "", "")
	flags.StringVar(&opts.project, "project", "", "")
	flags.StringVar(&opts.model, "model", "", "")
	flags.StringVar(&opts.appDir, "app-dir", "", "")
	flags.BoolVar(&opts.json, "json", false, "")
	flags.BoolVar(&opts.activity, "activity", false, "")
	flags.StringVar(&opts.subagentModel, "subagent-model", "", "")
	flags.StringVar(&opts.effort, "effort", "", "")
	flags.StringVar(&opts.output, "output", "", "")
	flags.StringVar(&opts.output, "o", "", "")
	flags.StringVar(&opts.session, "session", "", "")
	flags.BoolVar(&opts.continueLast, "continue", false, "")
	flags.BoolVar(&opts.continueLast, "c", false, "")
	flags.IntVar(&opts.maxIterations, "max-iterations", 0, "")
	flags.IntVar(&opts.maxSubagents, "max-subagents", 0, "")
	flags.DurationVar(&opts.timeout, "timeout", 0, "")
	showVersion := flags.Bool("version", false, "")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	switch command {
	case "version":
		*showVersion = true
	case "help":
		fmt.Fprint(stdout, usage)
		return 0
	case "", "serve":
	default:
		fmt.Fprintf(stderr, "nullbot: unknown command %q\n\n%s", command, usage)
		return 2
	}
	if *showVersion {
		fmt.Fprintln(stdout, "nullbot", version)
		return 0
	}
	if rest := flags.Args(); command == "" && len(rest) > 0 && (rest[0] == "serve" || rest[0] == "version" || rest[0] == "help") {
		// Flags first: nullbot --app-dir DIR serve.
		command = rest[0]
		if err := flags.Parse(rest[1:]); err != nil {
			return 2
		}
		if command == "version" {
			fmt.Fprintln(stdout, "nullbot", version)
			return 0
		}
		if command == "help" {
			fmt.Fprint(stdout, usage)
			return 0
		}
	}
	if rest := flags.Args(); len(rest) > 0 && opts.prompt == "" && command == "" {
		// nullbot "request" works too.
		opts.prompt = strings.Join(rest, " ")
	}

	promptSet := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "p" || f.Name == "prompt" {
			promptSet = true
		}
	})

	if err := checkRunOptions(opts, command, command == "serve" || opts.prompt != "" || promptSet); err != nil {
		fmt.Fprintln(stderr, "nullbot:", err)
		return 2
	}
	config, err := loadConfig(opts)
	if err != nil {
		fmt.Fprintln(stderr, "nullbot:", err)
		return 1
	}
	bot := app.New(config)
	defer bot.Shutdown()
	if opts.continueLast || opts.session != "" {
		id := opts.session
		if id == "" {
			latest, ok := app.LatestHistorySession(config)
			if !ok {
				fmt.Fprintln(stderr, "nullbot: there is no saved conversation to continue")
				return 1
			}
			id = latest
		}
		if err := bot.ContinueSession(id); err != nil {
			fmt.Fprintln(stderr, "nullbot:", err)
			return 1
		}
	}
	if command == "serve" || opts.prompt != "" || promptSet {
		if info := app.VaultStatus(config); info.Exists && !info.Unlocked {
			fmt.Fprintf(stderr, "nullbot: the secrets vault is locked (%s); saved keys and secrets are unavailable. Set %s or %s.\n", info.Error, vault.PassphraseEnv, vault.PassphraseFileEnv)
		}
	}

	switch {
	case command == "serve":
		return serve(bot, opts, stdin, stdout, stderr)
	case opts.prompt != "" || promptSet:
		request := opts.prompt
		if request == "-" {
			data, err := io.ReadAll(io.LimitReader(stdin, 4<<20))
			if err != nil {
				fmt.Fprintln(stderr, "nullbot:", err)
				return 1
			}
			request = string(data)
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
		defer stop()
		return ask(ctx, bot, strings.TrimSpace(request), opts, stdout, stderr)
	}

	// A passphrase-protected vault is unlocked before the terminal UI starts.
	if err := unlockFromTerminal(config, newCLIInput(stdin, stderr), stderr, true); err != nil {
		fmt.Fprintln(stderr, "nullbot:", err)
		return 1
	}
	bot.StartScheduler()
	defer bot.StopScheduler()
	program := tea.NewProgram(tui.New(bot), tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := program.Run(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// loadConfig reads the configuration and applies this run's overrides,
// which are not saved.
func loadConfig(opts options) (app.Config, error) {
	var config app.Config
	var err error
	if opts.appDir != "" {
		config, err = app.LoadOrInitConfigAt(opts.appDir)
	} else {
		config, err = app.LoadOrInitConfig()
	}
	if err != nil {
		return config, err
	}
	if opts.project != "" {
		found := false
		for _, p := range config.Projects {
			if strings.EqualFold(p.ID, opts.project) || strings.EqualFold(p.Name, opts.project) {
				config.PrimaryProjectID, found = p.ID, true
				break
			}
		}
		if !found {
			var names []string
			for _, p := range config.Projects {
				names = append(names, p.Name)
			}
			return config, fmt.Errorf("no project %q; projects: %s", opts.project, strings.Join(names, ", "))
		}
	}
	if opts.model != "" {
		provider, model, ok := strings.Cut(opts.model, "/")
		if !ok || provider == "" || model == "" {
			return config, fmt.Errorf("--model takes provider/model, such as openai/gpt-5.1")
		}
		config.Model.Provider, config.Model.Model = provider, model
	}
	if opts.subagentModel != "" {
		provider, model, ok := strings.Cut(opts.subagentModel, "/")
		if !ok || provider == "" || model == "" {
			return config, fmt.Errorf("--subagent-model takes provider/model, such as anthropic/claude-haiku-5-5")
		}
		config.SubagentModel.Provider, config.SubagentModel.Model = provider, model
	}
	if opts.effort != "" {
		config.Model.ReasoningEffort = app.NormalizeEffort(opts.effort)
		config.SubagentModel.ReasoningEffort = config.Model.ReasoningEffort
	}
	if opts.maxIterations > 0 {
		config.Agent.MaxIterations = opts.maxIterations
	}
	if opts.maxSubagents > 0 {
		config.Agent.MaxSubagents = opts.maxSubagents
	}
	return config, nil
}

// checkRunOptions refuses options that make no sense, before anything
// starts.
func checkRunOptions(opts options, command string, headless bool) error {
	used := opts.subagentModel != "" || opts.effort != "" || opts.output != "" || opts.session != "" || opts.continueLast ||
		opts.maxIterations != 0 || opts.maxSubagents != 0 || opts.timeout != 0
	if used && !headless {
		return errors.New("run options go with a request (-p) or serve")
	}
	if opts.effort != "" {
		if _, ok := app.EffortByID(opts.effort); !ok {
			return fmt.Errorf("--effort is one of minimal, low, medium, high or xhigh, not %q", opts.effort)
		}
	}
	if opts.maxIterations < 0 || opts.maxIterations > 500 {
		return errors.New("--max-iterations is between 1 and 500")
	}
	if opts.maxSubagents < 0 || opts.maxSubagents > 32 {
		return errors.New("--max-subagents is between 1 and 32")
	}
	if opts.timeout < 0 {
		return errors.New("--timeout is a duration such as 90s or 10m")
	}
	if opts.continueLast && opts.session != "" {
		return errors.New("give --continue or --session, not both")
	}
	if opts.output != "" && command == "serve" {
		return errors.New("serve prints each reply as it is ready; --output goes with a single request (-p)")
	}
	return nil
}

// ask runs one request and prints its answer.
func ask(ctx context.Context, bot *app.App, request string, opts options, stdout, stderr io.Writer) int {
	if request == "" {
		fmt.Fprintln(stderr, "nullbot: the request is empty")
		return 2
	}
	if opts.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeoutCause(ctx, opts.timeout, errTimedOut)
		defer cancel()
	}
	// --output: the reply goes to a file, written whole when it is ready.
	var file *strings.Builder
	if opts.output != "" {
		file = &strings.Builder{}
		stdout = file
	}
	tools := []map[string]any{}
	sink := func(record app.ActivityRecord) {
		if opts.activity {
			if line := activityLine(record); line != "" {
				fmt.Fprintln(stderr, line)
			}
		}
		if record.Tool != "" && (record.Status == "tool complete" || record.Status == "tool error") {
			entry := map[string]any{"tool": record.Tool, "status": record.Status}
			if record.Error != "" {
				entry["error"] = record.Error
			}
			if record.DurationMs > 0 {
				entry["duration_ms"] = record.DurationMs
			}
			tools = append(tools, entry)
		}
	}
	reply, status, err := bot.Ask(ctx, request, sink)
	timedOut := errors.Is(context.Cause(ctx), errTimedOut)
	if timedOut {
		status, err = "timed out", fmt.Errorf("stopped after %s (--timeout)", opts.timeout)
	}
	if opts.json {
		out := map[string]any{"answer": reply.Message, "status": status, "tools": tools, "session": bot.SessionID()}
		if err != nil {
			out["error"] = err.Error()
		}
		data, _ := json.Marshal(out)
		fmt.Fprintln(stdout, string(data))
	} else {
		fmt.Fprintln(stdout, strings.TrimRight(reply.Message, "\n"))
		if err != nil && status != "canceled" && strings.TrimSpace(err.Error()) != strings.TrimSpace(reply.Message) {
			fmt.Fprintln(stderr, "nullbot:", err)
		}
	}
	if file != nil {
		if werr := writeOutput(opts.output, file.String()); werr != nil {
			fmt.Fprintln(stderr, "nullbot:", werr)
			if err == nil {
				return 1
			}
		}
	}
	switch {
	case timedOut:
		return 124
	case status == "canceled" || ctx.Err() != nil:
		return 130
	case err != nil:
		return 1
	}
	return 0
}

var errTimedOut = errors.New("timed out")

// writeOutput replaces path with content, readable by you only: a reply
// can hold what the agent read.
func writeOutput(path, content string) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".nullbot-reply-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := tmp.WriteString(content)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(name)
		return errors.Join(werr, cerr)
	}
	if err := os.Chmod(name, 0o600); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// serve runs scheduled tasks and answers requests from stdin until it is
// interrupted. An interrupt cancels the request under way, if any, and exits.
func serve(bot *app.App, opts options, stdin io.Reader, stdout, stderr io.Writer) int {
	bot.StartScheduler()
	defer bot.StopScheduler()
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	fmt.Fprintf(stderr, "nullbot %s: serving. Scheduled tasks run; each line on standard input is a request. Interrupt to stop.\n", version)

	lines := make(chan string)
	go func() {
		scanner := bufio.NewScanner(stdin)
		scanner.Buffer(make([]byte, 64<<10), 4<<20)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()
	exit := 0
	for {
		select {
		case <-signals:
			return exit
		case line, ok := <-lines:
			if !ok {
				// No more requests; keep running scheduled tasks.
				lines = nil
				continue
			}
			if strings.TrimSpace(line) == "" {
				continue
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan int, 1)
			go func() { done <- ask(ctx, bot, strings.TrimSpace(line), opts, stdout, stderr) }()
			select {
			case code := <-done:
				if code != 0 {
					exit = code
				}
			case <-signals:
				cancel()
				<-done
				cancel()
				return 130
			}
			cancel()
		}
	}
}

// activityLine is one line of progress for --activity.
func activityLine(r app.ActivityRecord) string {
	who := r.AgentID
	if who == "" {
		who = "manager"
	}
	switch {
	case r.Tool != "" && (r.Status == "tool start" || r.Status == "tool complete" || r.Status == "tool error"):
		line := fmt.Sprintf("[%s] %s %s", who, r.Status, r.Tool)
		if r.Error != "" {
			line += ": " + oneLine(r.Error, 160)
		}
		return line
	case r.Status == "context compact start", r.Kind == "runtime", r.Kind == "instruction":
		return fmt.Sprintf("[%s] %s %s", who, r.Status, oneLine(r.Detail, 160))
	}
	return ""
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}

// vaultCommand recognizes "vault ...", "secrets ..." and "models ...", with an optional
// --app-dir before them.
func vaultCommand(args []string) (string, []string, bool) {
	appDir := ""
	for len(args) > 0 {
		switch {
		case args[0] == "--app-dir" || args[0] == "-app-dir":
			if len(args) < 2 {
				return "", nil, false
			}
			appDir, args = args[1], args[2:]
			continue
		case strings.HasPrefix(args[0], "--app-dir=") || strings.HasPrefix(args[0], "-app-dir="):
			appDir, args = args[0][strings.Index(args[0], "=")+1:], args[1:]
			continue
		}
		break
	}
	if len(args) > 0 && (args[0] == "vault" || args[0] == "secrets" || args[0] == "models" || args[0] == "infra") {
		return appDir, args, true
	}
	return "", nil, false
}
