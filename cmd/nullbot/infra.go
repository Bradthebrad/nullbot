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
	"regexp"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/Bradthebrad/nullbot/pkg/app"
	"github.com/Bradthebrad/nullbot/pkg/infra"
)

const infraUsage = `Usage:
  nullbot infra                      what NullBot made in the cloud, and plans waiting
  nullbot infra plans                recent plans
  nullbot infra show PLAN            a plan's steps: the exact commands, and how they went
  nullbot infra approve PLAN [--apply]
                                     approve a plan (and run it now)
  nullbot infra reject PLAN          reject a plan; it never runs
  nullbot infra apply PLAN           run an approved plan
  nullbot infra plan --file SPEC.json
                                     propose a plan from a spec (see docs/INFRA.md)
  nullbot infra destroy ID... [--contents] [--reason TEXT]
                                     propose removing resources (ids, names or a plan id)
  nullbot infra ledger [PLAN] [--limit N]
                                     every proposal, decision and command run
  nullbot infra agents on|off        let agents propose and run approved plans
  nullbot infra inspect MACHINE      what a machine runs: ports, services, nginx sites,
                                     containers, its first-boot setup (over SSH)
  nullbot infra here                 what listens on this computer
  nullbot infra tunnel MACHINE [--port 11434] [--local PORT]
                                     forward a machine's private port here, until Ctrl+C
  nullbot infra connect MACHINE      tunnel to the machine's Ollama and use it as a
                                     local model server, until Ctrl+C

Nothing runs until you approve it. Credentials are aws or gcp secrets in the
vault; the commands run through the aws and gcloud tools.
`

// runInfraCLI handles "nullbot infra ...".
func runInfraCLI(args []string, appDir string, stdin io.Reader, stdout, stderr io.Writer) int {
	args, appDir = takeAppDir(args, appDir)
	var positional, rest []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") {
			positional = append(positional, arg)
			continue
		}
		rest = append(rest, arg)
		switch strings.TrimLeft(arg, "-") {
		case "file", "reason", "limit", "port", "local":
			if i+1 < len(args) {
				i++
				rest = append(rest, args[i])
			}
		}
	}
	flags := flag.NewFlagSet("nullbot infra", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprint(stderr, infraUsage) }
	asJSON := flags.Bool("json", false, "")
	file := flags.String("file", "", "")
	reason := flags.String("reason", "", "")
	limit := flags.Int("limit", 50, "")
	contents := flags.Bool("contents", false, "")
	applyNow := flags.Bool("apply", false, "")
	remotePort := flags.Int("port", 11434, "")
	localPort := flags.Int("local", 0, "")
	if err := flags.Parse(rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	positional = append(positional, flags.Args()...)
	sub := ""
	if len(positional) > 0 {
		sub, positional = positional[0], positional[1:]
	}
	if sub == "help" {
		fmt.Fprint(stdout, infraUsage)
		return 0
	}
	config, err := loadConfig(options{appDir: appDir})
	if err != nil {
		fmt.Fprintln(stderr, "nullbot:", err)
		return 1
	}
	a := app.New(config)
	// Stopped by Ctrl+C, a terminal closing or a kill: tunnels close, key
	// files go, and a plan records where it stopped.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
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
	one := func(what string) (string, error) {
		if len(positional) != 1 {
			return "", fmt.Errorf("say which %s", what)
		}
		return positional[0], nil
	}
	apply := func(id string) int {
		plan, err := a.ApplyInfraPlan(ctx, id, "you (command line)")
		printPlan(stdout, plan)
		if err != nil {
			return fail(err)
		}
		return 0
	}

	switch sub {
	case "":
		st, err := a.InfraState()
		if err != nil {
			return fail(err)
		}
		if *asJSON {
			return emit(st)
		}
		w := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
		live := 0
		for _, r := range st.Resources {
			if !r.Live() {
				continue
			}
			live++
			where := r.Region
			if r.Zone != "" {
				where = r.Zone
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s %s\t%s\t%s\n", r.ID, r.Name, r.Kind, r.Cloud, where, r.State, r.Details["public_ip"])
		}
		_ = w.Flush()
		if live == 0 {
			fmt.Fprintln(stdout, "NullBot has nothing running in the cloud.")
		} else {
			fmt.Fprintf(stdout, "\nAbout $%.2f an hour ($%.0f a month).\n", st.CostHourly, st.CostHourly*730)
		}
		for _, p := range st.Plans {
			if p.Pending() {
				fmt.Fprintf(stdout, "Waiting for you: %s  %s (nullbot infra show %s)\n", p.ID, p.Title, p.ID)
			}
		}
		for name, path := range st.Tools {
			if strings.HasPrefix(path, "missing") {
				fmt.Fprintf(stdout, "%s: %s\n", name, strings.TrimPrefix(path, "missing: "))
			}
		}
		if !st.AgentTools {
			fmt.Fprintln(stdout, "Agents cannot propose plans; turn that on with: nullbot infra agents on")
		}
		return 0

	case "plans":
		st, err := a.InfraState()
		if err != nil {
			return fail(err)
		}
		if *asJSON {
			return emit(st.Plans)
		}
		w := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
		for _, p := range st.Plans {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", p.ID, p.Created.Local().Format("2006-01-02 15:04"), p.State, p.Title, p.Requester)
		}
		_ = w.Flush()
		return 0

	case "show":
		id, err := one("plan")
		if err != nil {
			return fail(err)
		}
		plan, err := infraPlan(config, id)
		if err != nil {
			return fail(err)
		}
		if *asJSON {
			return emit(plan)
		}
		printPlan(stdout, plan)
		return 0

	case "approve", "reject":
		id, err := one("plan")
		if err != nil {
			return fail(err)
		}
		seen := ""
		if sub == "approve" {
			if seen, err = confirmApproval(config, id, stdin, stdout); err != nil {
				return fail(err)
			}
		}
		if _, err := a.DecideInfraPlan(id, sub == "approve", "you (command line)", seen); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "Plan %s %sd.\n", id, sub)
		if sub == "approve" && *applyNow {
			return apply(id)
		}
		return 0

	case "apply":
		id, err := one("plan")
		if err != nil {
			return fail(err)
		}
		return apply(id)

	case "plan":
		if *file == "" {
			return fail(errors.New("give the spec with --file SPEC.json"))
		}
		data, err := os.ReadFile(*file)
		if err != nil {
			return fail(err)
		}
		var req app.InfraRequest
		if err := json.Unmarshal(data, &req); err != nil {
			return fail(fmt.Errorf("%s is not a valid spec: %w", *file, err))
		}
		if err := json.Unmarshal(data, &req.Spec); err != nil {
			return fail(fmt.Errorf("%s is not a valid spec: %w", *file, err))
		}
		plan, err := a.PlanInfra(ctx, req, "you (command line)")
		if err != nil {
			return fail(err)
		}
		printPlan(stdout, plan)
		fmt.Fprintf(stdout, "\nApprove and run it with: nullbot infra approve %s --apply\n", plan.ID)
		return 0

	case "destroy":
		if len(positional) == 0 {
			return fail(errors.New("say what to remove: resource ids, names or a plan id"))
		}
		plan, err := a.PlanInfraDestroy(positional, *contents, "you (command line)", *reason)
		if err != nil {
			return fail(err)
		}
		printPlan(stdout, plan)
		fmt.Fprintf(stdout, "\nApprove and run it with: nullbot infra approve %s --apply\n", plan.ID)
		return 0

	case "ledger":
		plan := ""
		if len(positional) > 0 {
			plan = positional[0]
		}
		entries, err := a.InfraLedger(plan, *limit)
		if err != nil {
			return fail(err)
		}
		if *asJSON {
			return emit(entries)
		}
		for _, e := range entries {
			line := fmt.Sprintf("%s  %s  %-8s", e.Time.Local().Format("2006-01-02 15:04:05"), e.Plan, e.Event)
			if e.Summary != "" {
				line += "  " + e.Summary
			}
			if len(e.Argv) > 0 {
				line += "\n    $ " + strings.Join(e.Argv, " ")
			}
			if e.Error != "" {
				line += "\n    error: " + e.Error
			}
			if e.By != "" {
				line += "  (" + e.By + ")"
			}
			fmt.Fprintln(stdout, line)
		}
		return 0

	case "inspect", "here":
		var report app.NodeReport
		if sub == "here" {
			report = app.ThisComputer(ctx)
		} else {
			id, err := one("machine")
			if err != nil {
				return fail(err)
			}
			if report, err = a.InspectNode(ctx, id); err != nil {
				return fail(err)
			}
		}
		if *asJSON {
			return emit(report)
		}
		printNode(stdout, report)
		return 0

	case "tunnel", "connect":
		id, err := one("machine")
		if err != nil {
			return fail(err)
		}
		var t app.Tunnel
		if sub == "connect" {
			e, err := a.ConnectNodeOllama(id)
			if err != nil {
				return fail(err)
			}
			fmt.Fprintf(stdout, "Connected: %s's Ollama is local model server %s at %s. Use it with --model local:%s/MODEL while this runs.\n", id, e.ID, e.BaseURL, e.ID)
		} else {
			if t, err = a.OpenTunnel(id, *remotePort, *localPort); err != nil {
				return fail(err)
			}
			fmt.Fprintf(stdout, "Forwarding 127.0.0.1:%d here to port %d on %s. Ctrl+C closes it.\n", t.LocalPort, t.RemotePort, t.Name)
		}
		<-ctx.Done()
		app.CloseTunnels()
		return 0

	case "agents":
		on, err := one("setting (on or off)")
		if err != nil || (on != "on" && on != "off") {
			return fail(errors.New("say on or off"))
		}
		if err := a.UpdateConfig(func(c *app.Config) { c.Infra.AgentTools = on == "on" }); err != nil {
			return fail(err)
		}
		if on == "on" {
			fmt.Fprintln(stdout, "Agents can now propose cloud plans and run the ones you approve.")
		} else {
			fmt.Fprintln(stdout, "Agents can no longer propose cloud plans.")
		}
		return 0
	}
	return fail(fmt.Errorf("unknown command: infra %s (see nullbot infra help)", sub))
}

func infraPlan(config app.Config, id string) (infra.Plan, error) {
	return infra.Open(config.AppDir + string(os.PathSeparator) + "infra").Plan(id)
}

// confirmApproval shows the plan in full and asks the person at the
// terminal to type its code. Approving is a person's decision: a command an
// agent runs (no terminal, or marked as an agent's) is refused.
func confirmApproval(config app.Config, id string, stdin io.Reader, w io.Writer) (string, error) {
	if os.Getenv(app.AgentCommandEnv) != "" {
		return "", errors.New("agents cannot approve plans; approve it in the app or in your own terminal")
	}
	if !isTerminal(stdin) {
		return "", errors.New("approve from a terminal (or in the app): approving asks you to type the plan's code")
	}
	plan, err := infraPlan(config, id)
	if err != nil {
		return "", err
	}
	if plan.State != infra.PlanProposed {
		return "", fmt.Errorf("plan %s is %s, not waiting for a decision", id, plan.State)
	}
	printPlan(w, plan)
	code := plan.ID[len(plan.ID)-4:]
	fmt.Fprintf(w, "\nTo run these %d commands, type %s and press Enter: ", len(plan.Steps), code)
	line, _ := bufio.NewReader(stdin).ReadString('\n')
	if strings.TrimSpace(line) != code {
		return "", errors.New("not approved")
	}
	return plan.Hash, nil
}

// isTerminal reports input from a person at a terminal, not a pipe or a
// file; tests replace it.
var isTerminal = func(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func printPlan(w io.Writer, p infra.Plan) {
	if p.ID == "" {
		return
	}
	clean := infra.CleanText
	// One line, for text that must not pass for a step of its own.
	flat := func(s string) string { return strings.Join(strings.Fields(clean(s)), " ") }
	fmt.Fprintf(w, "%s  %s  [%s]\n", p.ID, flat(p.Title), p.State)
	if p.Requester != "" {
		fmt.Fprintf(w, "Asked for by %s\n", flat(p.Requester))
	}
	if p.Reason != "" {
		// The reason is the requester's words: each line is marked as such.
		for i, line := range strings.Split(strings.TrimSpace(clean(p.Reason)), "\n") {
			label := "Why: "
			if i > 0 {
				label = "     "
			}
			fmt.Fprintf(w, "%s> %s\n", label, line)
		}
	}
	fmt.Fprintf(w, "Uses the %s credentials %q", p.Cloud, p.Credential)
	if p.CostHourly != 0 {
		label := "costs about"
		if p.CostHourly < 0 {
			label = "saves about"
		}
		fmt.Fprintf(w, "; %s $%.3f an hour", label, abs(p.CostHourly))
	}
	fmt.Fprintln(w)
	for _, s := range p.Steps {
		mark := map[string]string{"done": "✓", "failed": "✗", "running": "…", "skipped": "-"}[s.State]
		if mark == "" {
			mark = " "
		}
		fmt.Fprintf(w, " %s %s. %s\n", mark, s.ID, flat(s.Summary))
		if len(s.Argv) > 0 {
			quoted := make([]string, len(s.Argv))
			for i, a := range s.Argv {
				quoted[i] = shellQuote(clean(a))
			}
			fmt.Fprintf(w, "     $ %s\n", strings.Join(quoted, " "))
		}
		if s.Input != "" {
			// The script runs as root on the machine: show all of it.
			fmt.Fprintln(w, "     with this first-boot script:")
			for _, line := range strings.Split(strings.TrimRight(clean(s.Input), "\n"), "\n") {
				fmt.Fprintf(w, "     | %s\n", line)
			}
		}
		if s.Error != "" {
			fmt.Fprintf(w, "     error: %s\n", flat(s.Error))
		}
	}
	for _, n := range p.Notes {
		fmt.Fprintf(w, "Note: %s\n", flat(n))
	}
	if p.Error != "" {
		fmt.Fprintf(w, "Stopped: %s\n", flat(p.Error))
	}
}

// shellQuote quotes an argument the way a POSIX shell needs it, so a
// command reads as it runs and can be copied. A placeholder on its own
// stays bare: NullBot fills it in when the step runs.
func shellQuote(arg string) string {
	if shellSafe.MatchString(arg) || placeholderArg.MatchString(arg) {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

var (
	shellSafe      = regexp.MustCompile(`^[A-Za-z0-9_./:=,@%+-]+$`)
	placeholderArg = regexp.MustCompile(`^\{\{(cloud_id:nb-[0-9a-f]+|input_file)\}\}$`)
)

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func printNode(w io.Writer, r app.NodeReport) {
	// Everything here came from the machine: no control characters reach
	// the terminal.
	w = cleanWriter{w}
	fmt.Fprintf(w, "%s", r.Name)
	if r.Address != "" {
		fmt.Fprintf(w, " (%s)", r.Address)
	}
	fmt.Fprintln(w)
	if r.Error != "" {
		fmt.Fprintln(w, "error:", r.Error)
	}
	if r.Setup != "" && r.Setup != "none" {
		fmt.Fprintf(w, "First-boot setup: %s\n", r.Setup)
		for _, l := range r.SetupLines {
			fmt.Fprintln(w, "  "+l)
		}
	}
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "PORT\tADDRESS\tWHAT\tPROCESS\tREACHABLE FROM")
	for _, l := range r.Listeners {
		from := "this machine only"
		if l.Public {
			from = "other machines"
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n", l.Port, l.Address, l.Known, l.Process, from)
	}
	_ = tw.Flush()
	for _, site := range r.Nginx {
		fmt.Fprintf(w, "nginx: %s listening on %s", strings.Join(site.ServerNames, " "), strings.Join(site.Listen, ", "))
		if len(site.ProxyPass) > 0 {
			fmt.Fprintf(w, " → %s", strings.Join(site.ProxyPass, ", "))
		} else if site.Root != "" {
			fmt.Fprintf(w, " → %s", site.Root)
		}
		fmt.Fprintln(w)
	}
	for _, c := range r.Containers {
		fmt.Fprintln(w, "container:", c)
	}
	if len(r.Services) > 0 {
		fmt.Fprintf(w, "services: %s\n", strings.Join(r.Services, ", "))
	}
	for _, l := range r.System {
		fmt.Fprintln(w, l)
	}
}

// cleanWriter drops control characters (other than new lines and tabs).
type cleanWriter struct{ w io.Writer }

func (c cleanWriter) Write(p []byte) (int, error) {
	if _, err := io.WriteString(c.w, infra.CleanText(string(p))); err != nil {
		return 0, err
	}
	return len(p), nil
}
