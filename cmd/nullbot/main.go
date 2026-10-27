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
	"strings"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Bradthebrad/nullbot/internal/tui"
	"github.com/Bradthebrad/nullbot/pkg/app"
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
  nullbot version             print the version

Options (headless):
  --json             print each reply as JSON: the answer, its status and the
                     tool calls made
  --activity         stream progress (tool calls, subagents) to standard error
  --project NAME     work in this project for the run (its id or name)
  --model P/MODEL    use this provider/model for the manager for the run
  --app-dir DIR      use another NullBot folder (default ~/.nullbot, or
                     NULLBOT_APP_DIR)

Headless runs use the same configuration, projects, permissions, skills,
MCP servers and history as the terminal and desktop apps. Exit status is 0
when the request finished, 1 when it failed, 130 when it was interrupted.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

type options struct {
	prompt, project, model, appDir string
	json, activity                 bool
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
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

	config, err := loadConfig(opts)
	if err != nil {
		fmt.Fprintln(stderr, "nullbot:", err)
		return 1
	}
	bot := app.New(config)
	defer bot.Shutdown()

	switch {
	case command == "serve":
		return serve(bot, opts, stdin, stdout, stderr)
	case opts.prompt != "":
		request := opts.prompt
		if request == "-" {
			data, err := io.ReadAll(io.LimitReader(stdin, 4<<20))
			if err != nil {
				fmt.Fprintln(stderr, "nullbot:", err)
				return 1
			}
			request = string(data)
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return ask(ctx, bot, strings.TrimSpace(request), opts, stdout, stderr)
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
	return config, nil
}

// ask runs one request and prints its answer.
func ask(ctx context.Context, bot *app.App, request string, opts options, stdout, stderr io.Writer) int {
	if request == "" {
		fmt.Fprintln(stderr, "nullbot: the request is empty")
		return 2
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
	if opts.json {
		out := map[string]any{"answer": reply.Message, "status": status, "tools": tools}
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
	switch {
	case status == "canceled" || ctx.Err() != nil:
		return 130
	case err != nil:
		return 1
	}
	return 0
}

// serve runs scheduled tasks and answers requests from stdin until it is
// interrupted. A request already running is cancelled by the first
// interrupt; the second exits.
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
