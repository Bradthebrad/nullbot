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
	"sort"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/Bradthebrad/nullbot/pkg/app"
	"github.com/Bradthebrad/nullbot/pkg/vault"
)

const secretsUsage = `Secrets are kept in an encrypted vault in your NullBot folder. Agents never
see a value: they run commands with secrets by name (secret_run), and the
values are removed from everything those commands print.

Usage:
  nullbot vault                    show how the vault is protected
  nullbot vault init               create it, protected by this system's keychain
                                   (Windows DPAPI, macOS Keychain, Secret Service)
  nullbot vault init --passphrase  create it protected by a passphrase you type
  nullbot vault passphrase         protect it with a (new) passphrase instead
  nullbot vault keychain           protect it with this system's keychain instead

  nullbot secrets                  list secrets: names, kinds, what they provide
  nullbot secrets kinds            list the kinds of secret and their fields
  nullbot secrets set NAME --kind KIND [options]
                                   add or change a secret; values are asked for
                                   without echo, or read from standard input
      --set FIELD=VALUE            a public field: provider, host, region, env...
      --file FIELD=PATH            read a value from a file (an SSH key, a JSON key)
      --from-env FIELD=VAR         read a value from an environment variable
      --agent                      let agents use it with secret_run
      --server ID                  give it to this tool server when it starts
      --description TEXT
  nullbot secrets remove NAME
  nullbot secrets ssh-keygen NAME [--host github.com] [--agent]
                                   make an Ed25519 key and print its public key
  nullbot secrets github NAME      check a GitHub token and record its login
  nullbot secrets github-add-key SSH_NAME TOKEN_NAME
                                   add an SSH key's public key to that GitHub account

Options:
  --app-dir DIR    use another NullBot folder

On a server without a keychain, set NULLBOT_VAULT_PASSPHRASE, or
NULLBOT_VAULT_PASSPHRASE_FILE to a file holding it (a systemd credential, say).
NullBot removes the variable from its environment at start, so nothing it runs
inherits it.

Examples:
  nullbot secrets set github --kind github_token --agent
  nullbot secrets set deploy --kind ssh_key --file private_key=$HOME/.ssh/nullbot --agent
  nullbot secrets set aws-prod --kind aws --set region=us-east-1 --agent
  nullbot secrets set gcp --kind gcp --file credentials_json=key.json --agent
  nullbot secrets set openai --kind api_key --set provider=openai
`

// cliInput reads answers from a terminal without echo, or line by line from
// piped input.
type cliInput struct {
	in     io.Reader
	file   *os.File
	reader *bufio.Reader
	stderr io.Writer
}

func newCLIInput(stdin io.Reader, stderr io.Writer) *cliInput {
	c := &cliInput{in: stdin, stderr: stderr}
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		c.file = f
	} else {
		c.reader = bufio.NewReader(stdin)
	}
	return c
}

func (c *cliInput) terminal() bool { return c.file != nil }

func (c *cliInput) hidden(label string) (string, error) {
	if c.file != nil {
		fmt.Fprint(c.stderr, label)
		data, err := term.ReadPassword(int(c.file.Fd()))
		fmt.Fprintln(c.stderr)
		return strings.TrimSpace(string(data)), err
	}
	line, err := c.reader.ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && line != "") {
		return "", fmt.Errorf("no answer for %q on standard input", strings.TrimSpace(label))
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func (c *cliInput) all() (string, error) {
	if c.reader == nil {
		return "", errors.New("nothing to read")
	}
	data, err := io.ReadAll(io.LimitReader(c.reader, 1<<20))
	return string(data), err
}

type pairFlag []string

func (p *pairFlag) String() string     { return strings.Join(*p, ",") }
func (p *pairFlag) Set(v string) error { *p = append(*p, v); return nil }

func splitPairs(pairs []string, what string) (map[string]string, error) {
	out := map[string]string{}
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("--%s takes FIELD=VALUE, not %q", what, p)
		}
		out[strings.TrimSpace(k)] = v
	}
	return out, nil
}

// runVaultCLI handles "nullbot vault ..." and "nullbot secrets ...".
func runVaultCLI(command string, args []string, appDir string, stdin io.Reader, stdout, stderr io.Writer) int {
	// --app-dir may come anywhere, including before the subcommand.
	args, appDir = takeAppDir(args, appDir)
	sub := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	var positional []string
	for len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		positional, args = append(positional, args[0]), args[1:]
	}
	flags := flag.NewFlagSet("nullbot "+command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprint(stderr, secretsUsage) }
	kind := flags.String("kind", "", "")
	description := flags.String("description", "", "")
	host := flags.String("host", "", "")
	agentUse := flags.Bool("agent", false, "")
	usePassphrase := flags.Bool("passphrase", false, "")
	var sets, files, envs, servers pairFlag
	flags.Var(&sets, "set", "")
	flags.Var(&files, "file", "")
	flags.Var(&envs, "from-env", "")
	flags.Var(&servers, "server", "")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	positional = append(positional, flags.Args()...)
	config, err := loadConfig(options{appDir: appDir})
	if err != nil {
		fmt.Fprintln(stderr, "nullbot:", err)
		return 1
	}
	input := newCLIInput(stdin, stderr)
	fail := func(err error) int {
		fmt.Fprintln(stderr, "nullbot:", err)
		return 1
	}
	need := func(n int, what string) bool {
		if len(positional) < n {
			fmt.Fprintf(stderr, "nullbot: %s %s needs %s\n", command, sub, what)
			return false
		}
		return true
	}
	if err := unlockFromTerminal(config, input, stderr, false); err != nil {
		return fail(err)
	}

	if command == "vault" {
		switch sub {
		case "", "status":
			printVaultStatus(stdout, app.VaultStatus(config))
			return 0
		case "init":
			protection, pass := "", ""
			if *usePassphrase {
				protection = string(vault.Passphrase)
				if pass, err = newPassphrase(input); err != nil {
					return fail(err)
				}
			}
			if err := app.CreateVault(config, protection, pass); err != nil {
				return fail(err)
			}
			printVaultStatus(stdout, app.VaultStatus(config))
			return 0
		case "passphrase":
			pass, err := newPassphrase(input)
			if err != nil {
				return fail(err)
			}
			if err := app.ReprotectVault(config, string(vault.Passphrase), pass); err != nil {
				return fail(err)
			}
			fmt.Fprintln(stdout, "The vault is now protected by the new passphrase.")
			return 0
		case "keychain":
			if err := app.ReprotectVault(config, "", ""); err != nil {
				return fail(err)
			}
			printVaultStatus(stdout, app.VaultStatus(config))
			return 0
		case "help":
			fmt.Fprint(stdout, secretsUsage)
			return 0
		}
		fmt.Fprintf(stderr, "nullbot: unknown vault command %q\n\n%s", sub, secretsUsage)
		return 2
	}

	switch sub {
	case "", "list":
		secrets, err := app.ListSecrets(config)
		if err != nil {
			return fail(err)
		}
		if len(secrets) == 0 {
			fmt.Fprintln(stdout, "No secrets yet. Add one with: nullbot secrets set NAME --kind KIND")
			return 0
		}
		for _, s := range secrets {
			use := "NullBot only"
			if s.AgentUse {
				use = "agents may use"
			}
			line := fmt.Sprintf("%-20s %-14s %s; provides %s", s.Name, s.Kind, use, strings.Join(s.Provides, ", "))
			if s.Hint != "" {
				line += " (" + s.Hint + ")"
			}
			if len(s.Servers) > 0 {
				line += "; given to " + strings.Join(s.Servers, ", ")
			}
			fmt.Fprintln(stdout, line)
			if login := s.Public["login"]; login != "" {
				fmt.Fprintf(stdout, "%-20s signed in as %s\n", "", login)
			}
			if identity := s.Public["identity"]; identity != "" {
				fmt.Fprintf(stdout, "%-20s signs in as %s\n", "", identity)
			}
			if pub := s.Public["public_key"]; pub != "" {
				fmt.Fprintf(stdout, "%-20s %s\n", "", pub)
			}
		}
		return 0
	case "kinds":
		for _, k := range app.SecretKinds() {
			fmt.Fprintf(stdout, "%s: %s\n  %s\n", k.Kind, k.Label, k.Description)
			for _, f := range k.Fields {
				if f.Derived {
					continue
				}
				role := "--set"
				if f.Secret {
					role = "value"
				}
				req := ""
				if f.Required {
					req = ", required"
				}
				fmt.Fprintf(stdout, "    %-18s %s (%s%s)\n", f.Name, f.Label, role, req)
			}
		}
		return 0
	case "set", "add":
		if !need(1, "a name") {
			return 2
		}
		if *kind == "" {
			return fail(errors.New("say what kind of secret it is with --kind (nullbot secrets kinds lists them)"))
		}
		kindInfo, ok := findKind(*kind)
		if !ok {
			return fail(fmt.Errorf("unknown kind %q (nullbot secrets kinds lists them)", *kind))
		}
		public, err := splitPairs(sets, "set")
		if err != nil {
			return fail(err)
		}
		for field := range public {
			for _, f := range kindInfo.Fields {
				if f.Name == field && f.Secret {
					return fail(fmt.Errorf("%s is a secret value; it is asked for without echo, or give it with --file %s=PATH or --from-env %s=VAR", field, field, field))
				}
			}
		}
		if *host != "" {
			public["host"] = *host
		}
		values := map[string]string{}
		fromFiles, err := splitPairs(files, "file")
		if err != nil {
			return fail(err)
		}
		for field, path := range fromFiles {
			data, err := os.ReadFile(path)
			if err != nil {
				return fail(err)
			}
			values[field] = string(data)
		}
		fromEnv, err := splitPairs(envs, "from-env")
		if err != nil {
			return fail(err)
		}
		for field, name := range fromEnv {
			v := os.Getenv(name)
			if v == "" {
				return fail(fmt.Errorf("%s is not set", name))
			}
			values[field] = v
		}
		existing := map[string]bool{}
		var current *app.SecretInfo
		if list, err := app.ListSecrets(config); err == nil {
			for i, s := range list {
				if s.Name == positional[0] {
					current = &list[i]
					for _, f := range s.Set {
						existing[f] = true
					}
				}
			}
		}
		// Changing a secret keeps what the command line does not mention.
		given := map[string]bool{}
		flags.Visit(func(f *flag.Flag) { given[f.Name] = true })
		agent, desc, bound := *agentUse, *description, []string(servers)
		if current != nil {
			if !given["agent"] {
				agent = current.AgentUse
			}
			if !given["description"] {
				desc = current.Description
			}
			if !given["server"] {
				bound = current.Servers
			}
		}
		var missing []app.SecretField
		for _, f := range kindInfo.Fields {
			// At a terminal, optional values are offered too.
			if f.Secret && values[f.Name] == "" && !existing[f.Name] && (f.Required || input.terminal()) {
				missing = append(missing, f)
			}
		}
		if len(missing) > 0 {
			switch {
			case input.terminal():
				for _, f := range missing {
					label := f.Label + ": "
					if !f.Required {
						label = f.Label + " (Enter to skip): "
					}
					if f.Multiline {
						// A pasted multi-line value would leave its other
						// lines for the shell to run after this command.
						if !f.Required {
							continue
						}
						return fail(fmt.Errorf("give the %s as a file (--file %s=PATH), from a variable (--from-env %s=VAR) or on standard input", strings.ToLower(f.Label), f.Name, f.Name))
					}
					v, err := input.hidden(label)
					if err != nil {
						return fail(err)
					}
					values[f.Name] = v
				}
			case len(missing) == 1:
				v, err := input.all()
				if err != nil {
					return fail(err)
				}
				values[missing[0].Name] = v
			default:
				// Several values from a pipe: a JSON object of field: value.
				raw, err := input.all()
				if err != nil {
					return fail(err)
				}
				var obj map[string]string
				if err := json.Unmarshal([]byte(raw), &obj); err != nil {
					return fail(fmt.Errorf("pipe a JSON object with %s, or run this in a terminal to be asked", fieldNames(missing)))
				}
				for k, v := range obj {
					values[k] = v
				}
			}
		}
		info, err := app.SaveSecret(config, app.SecretInput{
			Name: positional[0], Kind: *kind, Description: desc,
			Values: values, Public: public, AgentUse: agent, Servers: bound,
		})
		for k := range values {
			values[k] = ""
		}
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "Saved %s (%s). Provides %s.\n", info.Name, info.Label, strings.Join(info.Provides, ", "))
		if pub := info.Public["public_key"]; pub != "" {
			fmt.Fprintln(stdout, pub)
		}
		return 0
	case "remove", "rm", "delete":
		if !need(1, "a name") {
			return 2
		}
		if err := app.RemoveSecret(config, positional[0]); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "Removed %s.\n", positional[0])
		return 0
	case "ssh-keygen":
		if !need(1, "a name") {
			return 2
		}
		info, err := app.GenerateSSHKey(config, positional[0], *host, *description, *agentUse)
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "Made %s (%s). Add this public key where it should sign in:\n%s\n", info.Name, info.Public["fingerprint"], info.Public["public_key"])
		return 0
	case "github":
		if !need(1, "the token's name") {
			return 2
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		id, err := app.VerifyGitHubToken(ctx, config, positional[0])
		if err != nil {
			return fail(err)
		}
		scopes := id.Scopes
		if scopes == "" {
			scopes = "fine-grained (permissions are set on GitHub)"
		}
		fmt.Fprintf(stdout, "%s signs in to %s as %s. Scopes: %s\n", positional[0], id.Host, id.Login, scopes)
		return 0
	case "github-add-key":
		if !need(2, "an SSH key name and a token name") {
			return 2
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := app.AddSSHKeyToGitHub(ctx, config, positional[0], positional[1], *description); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "Added %s's public key to the GitHub account of %s.\n", positional[0], positional[1])
		return 0
	case "help":
		fmt.Fprint(stdout, secretsUsage)
		return 0
	}
	fmt.Fprintf(stderr, "nullbot: unknown secrets command %q\n\n%s", sub, secretsUsage)
	return 2
}

func findKind(kind string) (app.SecretKindInfo, bool) {
	for _, k := range app.SecretKinds() {
		if k.Kind == kind {
			return k, true
		}
	}
	return app.SecretKindInfo{}, false
}

func fieldNames(fields []app.SecretField) string {
	names := make([]string, 0, len(fields))
	for _, f := range fields {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func newPassphrase(input *cliInput) (string, error) {
	first, err := input.hidden("New vault passphrase: ")
	if err != nil {
		return "", err
	}
	if len(first) < 8 {
		return "", errors.New("use a passphrase of at least 8 characters")
	}
	if input.terminal() {
		again, err := input.hidden("Type it again: ")
		if err != nil {
			return "", err
		}
		if again != first {
			return "", errors.New("the passphrases do not match")
		}
	}
	return first, nil
}

func printVaultStatus(w io.Writer, info app.VaultInfo) {
	switch {
	case !info.Exists:
		fmt.Fprintf(w, "No vault yet; it is made when the first secret is saved (%s).\n", info.Path)
		if info.Keychain != "" {
			fmt.Fprintf(w, "It will be protected by %s.\n", info.KeychainLabel)
		} else {
			fmt.Fprintln(w, "This system has no keychain NullBot can use: set NULLBOT_VAULT_PASSPHRASE, or run nullbot vault init --passphrase.")
		}
	case !info.Unlocked:
		fmt.Fprintf(w, "The vault at %s is protected by %s and is locked: %s\n", info.Path, info.ProtectionLabel, info.Error)
	default:
		fmt.Fprintf(w, "The vault at %s holds %d secret(s), protected by %s.\n", info.Path, info.Secrets, info.ProtectionLabel)
	}
	for _, p := range info.Plaintext {
		fmt.Fprintf(w, "Still in plain text: %s\n", p)
	}
}

// unlockFromTerminal asks for the passphrase of a locked vault when a person
// is at a terminal. Without one (a pipe, a service) it does nothing; the
// vault stays locked and NULLBOT_VAULT_PASSPHRASE is the way in.
func unlockFromTerminal(config app.Config, input *cliInput, stderr io.Writer, optional bool) error {
	info := app.VaultStatus(config)
	if !info.Exists || info.Unlocked || info.Protection != vault.Passphrase || !input.terminal() {
		return nil
	}
	for attempt := 0; attempt < 3; attempt++ {
		label := "Vault passphrase: "
		if optional {
			label = "Vault passphrase (Enter to skip): "
		}
		pass, err := input.hidden(label)
		if err != nil {
			return err
		}
		if pass == "" && optional {
			return nil
		}
		err = app.UnlockVault(config, pass)
		if err == nil {
			return nil
		}
		if !errors.Is(err, vault.ErrWrongPassphrase) {
			return err
		}
		fmt.Fprintln(stderr, "That passphrase does not open the vault.")
	}
	if optional {
		fmt.Fprintln(stderr, "Continuing with the vault locked: saved keys are unavailable.")
		return nil
	}
	return vault.ErrWrongPassphrase
}

// takeAppDir removes --app-dir DIR (or --app-dir=DIR) from args.
func takeAppDir(args []string, appDir string) ([]string, string) {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case (a == "--app-dir" || a == "-app-dir") && i+1 < len(args):
			appDir = args[i+1]
			i++
		case strings.HasPrefix(a, "--app-dir=") || strings.HasPrefix(a, "-app-dir="):
			appDir = a[strings.Index(a, "=")+1:]
		default:
			out = append(out, a)
		}
	}
	return out, appDir
}
