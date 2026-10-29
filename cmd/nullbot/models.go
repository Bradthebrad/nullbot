package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Bradthebrad/nullbot/pkg/app"
	"github.com/Bradthebrad/nullbot/pkg/localmodels"
)

const modelsUsage = `Usage:
  nullbot models [--refresh]               every provider's models
  nullbot models local                     your model servers and whether they answer
  nullbot models local find                look for model servers on this computer
  nullbot models local add KIND [--id ID] [--url URL] [--name NAME] [--key-secret SECRET]
                                           add a server: ollama, llamacpp, lmstudio,
                                           vllm or openai (any OpenAI-compatible server)
  nullbot models local remove ID           remove a server
  nullbot models pull NAME [--server ID]   download a model into Ollama
  nullbot models context MODEL --size N [--server ID]
                                           make an Ollama copy of MODEL that runs
                                           with an N-token context
  nullbot models remove NAME [--server ID] delete a model from Ollama

Use a local model with --model local:ID/MODEL, for example
  nullbot -p "hello" --model local:ollama/qwen2.5:7b
`

// runModelsCLI handles "nullbot models ...".
func runModelsCLI(args []string, appDir string, stdout, stderr io.Writer) int {
	args, appDir = takeAppDir(args, appDir)
	// Flags may come before, between or after the words.
	var positional, rest []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positional = append(positional, arg)
			continue
		}
		rest = append(rest, arg)
		if !strings.Contains(arg, "=") && flagTakesValue(arg) && i+1 < len(args) {
			i++
			rest = append(rest, args[i])
		}
	}
	flags := flag.NewFlagSet("nullbot models", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprint(stderr, modelsUsage) }
	refresh := flags.Bool("refresh", false, "")
	asJSON := flags.Bool("json", false, "")
	id := flags.String("id", "", "")
	url := flags.String("url", "", "")
	name := flags.String("name", "", "")
	keySecret := flags.String("key-secret", "", "")
	server := flags.String("server", "", "")
	size := flags.Int("size", 0, "")
	if err := flags.Parse(rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	positional = append(positional, flags.Args()...)
	if len(positional) > 0 && (positional[0] == "help" || positional[0] == "-h") {
		fmt.Fprint(stdout, modelsUsage)
		return 0
	}
	config, err := loadConfig(options{appDir: appDir})
	if err != nil {
		fmt.Fprintln(stderr, "nullbot:", err)
		return 1
	}
	a := app.New(config)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	fail := func(err error) int {
		fmt.Fprintln(stderr, "nullbot:", err)
		if errors.Is(err, context.Canceled) {
			return 130
		}
		return 1
	}
	emit := func(v any) int {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(v)
		return 0
	}
	sub := ""
	if len(positional) > 0 {
		sub, positional = positional[0], positional[1:]
	}
	ollama := func() (string, error) {
		if *server != "" {
			return *server, nil
		}
		for _, e := range a.Config().LocalEndpoints {
			if e.Kind == string(localmodels.Ollama) && !e.Disabled {
				return e.ID, nil
			}
		}
		return "", errors.New("no Ollama server is set up; add one with nullbot models local add ollama")
	}

	switch sub {
	case "":
		groups := app.CachedModelGroups(ctx, a.Config(), *refresh)
		if *asJSON {
			return emit(groups)
		}
		w := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
		for _, g := range groups {
			title := g.Title
			if title == "" {
				title = g.Provider
			}
			fmt.Fprintf(w, "%s (%s)\n", title, g.Provider)
			if g.Error != "" {
				fmt.Fprintf(w, "  %s\n", g.Error)
			}
			for _, m := range g.Models {
				window := ""
				if m.ContextWindow > 0 {
					window = fmt.Sprintf("%dk context", m.ContextWindow/1000)
				}
				fmt.Fprintf(w, "  %s\t%s\t%s\n", m.ID, m.Name, window)
			}
		}
		_ = w.Flush()
		return 0

	case "local":
		action := ""
		if len(positional) > 0 {
			action, positional = positional[0], positional[1:]
		}
		switch action {
		case "", "list":
			infos := a.LocalEndpoints(ctx, true)
			if *asJSON {
				return emit(infos)
			}
			if len(infos) == 0 {
				fmt.Fprintln(stdout, "No model servers yet. Look for some with: nullbot models local find")
				return 0
			}
			for _, info := range infos {
				state := "answering"
				switch {
				case info.Disabled:
					state = "turned off"
				case !info.Status.Reachable:
					state = info.Status.Error
				}
				fmt.Fprintf(stdout, "%s  %s at %s: %s\n", info.ID, info.Label, info.BaseURL, state)
				if info.Insecure {
					fmt.Fprintln(stdout, "  warning: its key is sent over plain http to another machine")
				}
				for _, m := range info.Models {
					line := "  " + m.ID
					if m.ContextWindow > 0 {
						line += fmt.Sprintf("  %d-token context", m.ContextWindow)
					}
					if m.ContextNote != "" {
						line += "  (" + m.ContextNote + ")"
					}
					fmt.Fprintln(stdout, line)
				}
			}
			return 0
		case "find":
			found := a.FindLocalServers(ctx)
			if *asJSON {
				return emit(found)
			}
			if len(found) == 0 {
				fmt.Fprintln(stdout, "No model servers are answering at their usual addresses on this computer.")
				return 0
			}
			for _, f := range found {
				line := fmt.Sprintf("%s at %s, %d models", f.Label, f.BaseURL, f.Models)
				if f.ConfiguredAs != "" {
					line += " (set up as " + f.ConfiguredAs + ")"
				} else {
					line += fmt.Sprintf(" (add it with: nullbot models local add %s)", f.Kind)
				}
				fmt.Fprintln(stdout, line)
			}
			return 0
		case "add":
			if len(positional) != 1 {
				return fail(errors.New("say which kind of server: ollama, llamacpp, lmstudio, vllm or openai"))
			}
			saved, err := a.SaveLocalEndpoint(app.LocalEndpoint{ID: *id, Kind: positional[0], BaseURL: *url, Name: *name, KeySecret: *keySecret}, "")
			if err != nil {
				return fail(err)
			}
			for _, info := range a.LocalEndpoints(ctx, true) {
				if info.ID != saved.ID {
					continue
				}
				if info.Status.Reachable {
					fmt.Fprintf(stdout, "Added %s (%s at %s), %d models. Use one with --model local:%s/MODEL.\n", saved.ID, info.Label, saved.BaseURL, len(info.Models), saved.ID)
				} else {
					fmt.Fprintf(stdout, "Added %s (%s at %s), but it is not answering yet: %s\n", saved.ID, info.Label, saved.BaseURL, info.Status.Error)
				}
			}
			return 0
		case "remove":
			if len(positional) != 1 {
				return fail(errors.New("say which server to remove"))
			}
			if err := a.RemoveLocalEndpoint(positional[0]); err != nil {
				return fail(err)
			}
			fmt.Fprintf(stdout, "Removed %s.\n", positional[0])
			return 0
		}
		return fail(fmt.Errorf("unknown command: models local %s", action))

	case "pull":
		if len(positional) != 1 {
			return fail(errors.New("say which model to pull, such as qwen2.5:7b"))
		}
		target, err := ollama()
		if err != nil {
			return fail(err)
		}
		last := time.Time{}
		err = a.PullLocalModel(ctx, target, positional[0], func(p localmodels.Progress) {
			if time.Since(last) < 500*time.Millisecond && p.Status != "success" {
				return
			}
			last = time.Now()
			if p.Total > 0 {
				fmt.Fprintf(stderr, "\r%s: %d%% of %s   ", p.Status, p.Completed*100/p.Total, formatGB(p.Total))
			} else {
				fmt.Fprintf(stderr, "\r%s   ", p.Status)
			}
		})
		fmt.Fprintln(stderr)
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "Pulled %s into %s.\n", positional[0], target)
		return 0

	case "context":
		if len(positional) != 1 || *size == 0 {
			return fail(errors.New("say which model and the context size, such as: nullbot models context qwen2.5:7b --size 32768"))
		}
		target, err := ollama()
		if err != nil {
			return fail(err)
		}
		made, err := a.CopyLocalModelWithContext(ctx, target, positional[0], *size)
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "Made %s on %s, with a %d-token context. Use it with --model local:%s/%s.\n", made, target, *size, target, made)
		return 0

	case "remove":
		if len(positional) != 1 {
			return fail(errors.New("say which model to remove"))
		}
		target, err := ollama()
		if err != nil {
			return fail(err)
		}
		if err := a.DeleteLocalModel(ctx, target, positional[0]); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "Removed %s from %s.\n", positional[0], target)
		return 0
	}
	return fail(fmt.Errorf("unknown command: models %s (see nullbot models help)", sub))
}

// flagTakesValue reports a flag of runModelsCLI that is followed by a value,
// so "--server ollama pull" keeps "ollama" with its flag.
func flagTakesValue(arg string) bool {
	switch strings.TrimLeft(arg, "-") {
	case "id", "url", "name", "key-secret", "server", "size":
		return true
	}
	return false
}

func formatGB(n int64) string {
	return fmt.Sprintf("%.1f GB", float64(n)/1e9)
}
