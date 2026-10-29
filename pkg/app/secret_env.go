package app

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Bradthebrad/nullbot/pkg/vault"
)

// secretEnv is what one command gets from the secrets it asked for.
type secretEnv struct {
	// vars are the environment variables to set.
	vars map[string]string
	// dir holds temporary files (SSH keys, key files); removed by cleanup.
	dir string
}

func (e *secretEnv) cleanup() {
	if e.dir == "" {
		return
	}
	// Overwrite before removing; see removeLegacyFile for the limits.
	_ = filepath.WalkDir(e.dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			removeLegacyFile(path)
		}
		return nil
	})
	_ = os.RemoveAll(e.dir)
	e.dir = ""
}

func (e *secretEnv) set(name, value, from string, sources map[string]string) error {
	key := mcpEnvKey(name)
	if prev, ok := sources[key]; ok && prev != from {
		return fmt.Errorf("%s and %s both set %s; use one of them per command", prev, from, name)
	}
	sources[key] = from
	e.vars[key] = value
	return nil
}

func (e *secretEnv) file(name, content string) (string, error) {
	if e.dir == "" {
		dir, err := os.MkdirTemp("", "nullbot-secrets-")
		if err != nil {
			return "", err
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			_ = os.RemoveAll(dir)
			return "", err
		}
		e.dir = dir
	}
	path := filepath.Join(e.dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// buildSecretEnv turns secrets into environment variables. files allows
// secrets that need a temporary file (commands); tool servers pass false.
func buildSecretEnv(secrets []Secret, files bool) (*secretEnv, error) {
	env := &secretEnv{vars: map[string]string{}}
	sources := map[string]string{}
	gitConfig := [][2]string{}
	fail := func(err error) (*secretEnv, error) {
		env.cleanup()
		return nil, err
	}
	for i, s := range secrets {
		set := func(name, value string) error { return env.set(name, value, s.Name, sources) }
		switch s.Kind {
		case SecretAPIKey:
			for _, name := range providerEnvNames(s.Public["provider"]) {
				if err := set(name, s.Values["value"]); err != nil {
					return fail(err)
				}
			}
		case SecretGitHubToken:
			token := s.Values["token"]
			host := firstNonEmpty(s.Public["host"], "github.com")
			for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
				if err := set(name, token); err != nil {
					return fail(err)
				}
			}
			if host != "github.com" {
				if err := set("GH_ENTERPRISE_TOKEN", token); err != nil {
					return fail(err)
				}
				if err := set("GH_HOST", host); err != nil {
					return fail(err)
				}
			}
			// git over HTTPS: a credential helper for this host only, after
			// clearing the helpers configured elsewhere for it, reading the
			// token from the environment so it is never on a command line.
			tokenVar := "NULLBOT_GIT_TOKEN_" + strconv.Itoa(i)
			if err := set(tokenVar, token); err != nil {
				return fail(err)
			}
			key := "credential.https://" + host + ".helper"
			gitConfig = append(gitConfig, [2]string{key, ""}, [2]string{key,
				`!f() { test "$1" = get || exit 0; echo username=x-access-token; echo "password=$` + tokenVar + `"; }; f`})
		case SecretSSHKey:
			if !files {
				return fail(fmt.Errorf("%s is an SSH key, which is passed as a file; tool servers cannot get it", s.Name))
			}
			path, err := env.file(fmt.Sprintf("%d-%s.key", i, s.Name), s.Values["private_key"])
			if err != nil {
				return fail(err)
			}
			if err := set("NULLBOT_SSH_KEY_FILE", path); err != nil {
				return fail(err)
			}
			slash := filepath.ToSlash(path)
			// No shared master connection: one would outlive the command.
			command := "ssh -i '" + slash + "' -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o ControlMaster=no -o ControlPath=none"
			if err := set("GIT_SSH_COMMAND", command); err != nil {
				return fail(err)
			}
		case SecretAWS:
			pairs := [][2]string{{"AWS_ACCESS_KEY_ID", s.Values["access_key_id"]}, {"AWS_SECRET_ACCESS_KEY", s.Values["secret_access_key"]}}
			if v := s.Values["session_token"]; v != "" {
				pairs = append(pairs, [2]string{"AWS_SESSION_TOKEN", v})
			}
			if r := s.Public["region"]; r != "" {
				pairs = append(pairs, [2]string{"AWS_REGION", r}, [2]string{"AWS_DEFAULT_REGION", r})
			}
			for _, p := range pairs {
				if err := set(p[0], p[1]); err != nil {
					return fail(err)
				}
			}
		case SecretGCP:
			if !files {
				return fail(fmt.Errorf("%s is a Google Cloud key file; tool servers cannot get it", s.Name))
			}
			path, err := env.file(fmt.Sprintf("%d-%s.json", i, s.Name), s.Values["credentials_json"])
			if err != nil {
				return fail(err)
			}
			for _, name := range []string{"GOOGLE_APPLICATION_CREDENTIALS", "CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE"} {
				if err := set(name, path); err != nil {
					return fail(err)
				}
			}
			if p := s.Public["project_id"]; p != "" {
				for _, name := range []string{"GOOGLE_CLOUD_PROJECT", "CLOUDSDK_CORE_PROJECT"} {
					if err := set(name, p); err != nil {
						return fail(err)
					}
				}
			}
		case SecretGeneric:
			value := s.Values["value"]
			if s.Public["as_file"] == "yes" {
				if !files {
					return fail(fmt.Errorf("%s is passed as a file; tool servers cannot get it", s.Name))
				}
				path, err := env.file(fmt.Sprintf("%d-%s", i, s.Name), value)
				if err != nil {
					return fail(err)
				}
				value = path
			}
			if err := validateEnvName(s.Public["env"]); err != nil {
				return fail(err)
			}
			if err := set(s.Public["env"], value); err != nil {
				return fail(err)
			}
		default:
			return fail(fmt.Errorf("%s has an unknown kind %q", s.Name, s.Kind))
		}
	}
	if len(gitConfig) > 0 {
		// Append to any GIT_CONFIG_COUNT entries already in the environment.
		base := 0
		if n, err := strconv.Atoi(os.Getenv("GIT_CONFIG_COUNT")); err == nil && n > 0 && n < 1000 {
			base = n
		}
		for i, kv := range gitConfig {
			env.vars[mcpEnvKey("GIT_CONFIG_KEY_"+strconv.Itoa(base+i))] = kv[0]
			env.vars[mcpEnvKey("GIT_CONFIG_VALUE_"+strconv.Itoa(base+i))] = kv[1]
		}
		env.vars[mcpEnvKey("GIT_CONFIG_COUNT")] = strconv.Itoa(base + len(gitConfig))
		env.vars[mcpEnvKey("GIT_TERMINAL_PROMPT")] = "0"
	}
	return env, nil
}

// commandBaseEnv is NullBot's environment without anything that looks like
// a credential, so a command gets only the secrets it asked for.
func commandBaseEnv() map[string]string {
	env := map[string]string{}
	for _, item := range os.Environ() {
		key, value, ok := strings.Cut(item, "=")
		if !ok || key == "" {
			continue
		}
		if commandCredentialEnv(key) {
			continue
		}
		env[mcpEnvKey(key)] = value
	}
	return env
}

// commandCredentialEnv reports variables a command never inherits from
// NullBot: anything that looks like a credential, cloud configuration that
// would compete with a chosen secret, and the user's SSH agent.
func commandCredentialEnv(key string) bool {
	upper := strings.ToUpper(key)
	if inheritedCredentialEnv(key) || strings.HasPrefix(upper, "NULLBOT_") || strings.HasPrefix(upper, "AWS_") {
		return true
	}
	for _, suffix := range []string{"_KEY", "_PASS", "_PASSWD", "_APIKEY", "_CREDENTIAL", "_PAT", "_PRIVATE_KEY"} {
		if strings.HasSuffix(upper, suffix) {
			return true
		}
	}
	switch upper {
	case "GOOGLE_APPLICATION_CREDENTIALS", "CLOUDSDK_AUTH_ACCESS_TOKEN_FILE", "DATABASE_URL", "SSH_AUTH_SOCK", "GIT_ASKPASS", "SSH_ASKPASS":
		return true
	}
	return false
}

func envList(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}

// serverSecretEnv returns the variables for secrets bound to tool server id.
// File-based secrets cannot be bound, so nothing is written to disk.
func serverSecretEnv(config Config, id string) map[string]string {
	payload, err := loadSecretsPayload(config)
	if err != nil {
		return nil
	}
	var bound []Secret
	for _, s := range payload.Secrets {
		for _, server := range s.Servers {
			if server == id {
				bound = append(bound, s)
			}
		}
	}
	if len(bound) == 0 {
		return nil
	}
	env, err := buildSecretEnv(bound, false)
	if err != nil {
		return nil
	}
	launchedWithSecrets.Store(id, true)
	return env.vars
}

// launchedWithSecrets remembers servers started with secrets, so locking
// the vault can stop them.
var launchedWithSecrets sync.Map

func serversWithSecrets() []string {
	var out []string
	launchedWithSecrets.Range(func(k, _ any) bool {
		out = append(out, k.(string))
		return true
	})
	return out
}

// --- redaction --------------------------------------------------------------

// secretRedactor replaces every known secret value in text with
// [secret:name]. It is rebuilt when the vault changes. Values it has seen
// stay known for the life of the process, so a rotated, removed or
// locked-away value is still caught.
type secretRedactor struct {
	// stamp identifies the vault and legacy files the redactor was built
	// from; a change to any of them rebuilds it.
	stamp    string
	built    time.Time
	replacer *strings.Replacer
	values   []redactValue
}

type redactValue struct {
	value, label string
}

var (
	redactorsMu sync.Mutex
	redactors   = map[string]*secretRedactor{}
	// seenValues: per vault, every secret value redacted so far, by value.
	seenValues = map[string]map[string]string{}
)

const maxSeenValues = 5000

func invalidateRedactor(config Config) {
	redactorsMu.Lock()
	delete(redactors, VaultPath(config))
	redactorsMu.Unlock()
}

// providerEnvKeys are variables whose values NullBot uses as keys when the
// vault has none; they are redacted too.
var providerEnvKeys = []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "OPENROUTER_API_KEY", "GOOGLE_API_KEY", "GEMINI_API_KEY", "BRAVE_API_KEY", "GH_TOKEN", "GITHUB_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN"}

// secretValues lists the values a secret holds that must be redacted. A
// Google Cloud key file also contributes its private key and key id, which
// appear on their own when a command reads one field.
func secretValues(s Secret) []string {
	var out []string
	for _, v := range s.Values {
		out = append(out, v)
	}
	if raw := s.Values["credentials_json"]; raw != "" {
		var key map[string]any
		if json.Unmarshal([]byte(raw), &key) == nil {
			for _, field := range []string{"private_key", "private_key_id", "refresh_token", "client_secret"} {
				if v, ok := key[field].(string); ok {
					out = append(out, v)
				}
			}
		}
	}
	return out
}

// payloadValues are the values in a vault payload to redact, with labels.
func payloadValues(payload vaultPayload) map[string]string {
	out := map[string]string{}
	add := func(value, label string) {
		if value = strings.TrimSpace(value); len(value) >= 8 {
			out[value] = label
		}
	}
	for _, s := range payload.Secrets {
		for _, v := range secretValues(s) {
			add(v, "[secret:"+s.Name+"]")
		}
	}
	if c := payload.System.CodexAuth; c != nil {
		add(c.AccessToken, "[secret:chatgpt-sign-in]")
		add(c.RefreshToken, "[secret:chatgpt-sign-in]")
		add(c.IDToken, "[secret:chatgpt-sign-in]")
	}
	return out
}

// rememberSecrets records a payload's values as seen, so they stay redacted
// even if the vault is locked before the next tool result.
func rememberSecrets(config Config, payload vaultPayload) {
	rememberValues(config, payloadValues(payload))
}

// rememberVaultKey records the vault's own key material as seen. It takes
// the vault's lock, so it is called after a read or update, never inside.
func rememberVaultKey(config Config) {
	values := map[string]string{}
	for _, v := range openVault(config).RedactionValues() {
		if len(v) >= 8 {
			values[v] = "[secret:vault key]"
		}
	}
	rememberValues(config, values)
}

// rememberValues adds values to the vault's seen set. The set is capped;
// values past the cap are still redacted while they are current.
func rememberValues(config Config, values map[string]string) {
	if len(values) == 0 {
		return
	}
	path := VaultPath(config)
	redactorsMu.Lock()
	defer redactorsMu.Unlock()
	seen := seenValues[path]
	if seen == nil {
		seen = map[string]string{}
		seenValues[path] = seen
	}
	added := false
	for v, label := range values {
		if _, ok := seen[v]; !ok && len(seen) < maxSeenValues {
			seen[v] = label
			added = true
		}
	}
	if added {
		delete(redactors, path)
	}
}

// redactorStamp identifies the files the redactor reads: the vault and the
// legacy plain-text files.
func redactorStamp(config Config) string {
	var b strings.Builder
	for _, path := range []string{VaultPath(config), KeysPath(config), CodexAuthPath(config)} {
		if info, err := os.Stat(path); err == nil {
			fmt.Fprintf(&b, "%d/%d;", info.ModTime().UnixNano(), info.Size())
		} else {
			b.WriteString("-;")
		}
	}
	return b.String()
}

func currentRedactor(config Config) *secretRedactor {
	path := VaultPath(config)
	stamp := redactorStamp(config)
	redactorsMu.Lock()
	r := redactors[path]
	redactorsMu.Unlock()
	// Rebuilt when the vault or a legacy file changes, and every few minutes
	// for keys that come from the environment.
	if r != nil && time.Since(r.built) < 5*time.Minute && r.stamp == stamp {
		return r
	}
	current := map[string]string{}
	add := func(value, label string) {
		if value = strings.TrimSpace(value); len(value) >= 8 {
			current[value] = label
		}
	}
	if payload, err := loadSecretsPayload(config); err == nil {
		for v, label := range payloadValues(payload) {
			current[v] = label
		}
	}
	for _, v := range openVault(config).RedactionValues() {
		add(v, "[secret:vault key]")
	}
	if legacy, err := readLegacyAPIKeys(config); err == nil {
		for _, p := range namedProviders {
			add(legacy.ForProvider(p), "[secret:"+p+"]")
		}
		for p, v := range legacy.Other {
			add(v, "[secret:"+p+"]")
		}
	}
	if legacy, err := readLegacyCodexAuth(config); err == nil {
		add(legacy.AccessToken, "[secret:chatgpt-sign-in]")
		add(legacy.RefreshToken, "[secret:chatgpt-sign-in]")
		add(legacy.IDToken, "[secret:chatgpt-sign-in]")
	}
	for _, name := range providerEnvKeys {
		add(os.Getenv(name), "[secret:env "+name+"]")
	}
	if p, ok := vault.EnvPassphrase(); ok {
		add(p, "[secret:vault passphrase]")
	}

	redactorsMu.Lock()
	seen := seenValues[path]
	if seen == nil {
		seen = map[string]string{}
		seenValues[path] = seen
	}
	for v, label := range current {
		if _, ok := seen[v]; ok || len(seen) < maxSeenValues {
			seen[v] = label
		}
	}
	// Everything current, and everything seen before: the cap limits what
	// is remembered, never what is redacted now.
	all := make(map[string]string, len(seen)+len(current))
	for v, label := range seen {
		all[v] = label
	}
	for v, label := range current {
		all[v] = label
	}
	redactorsMu.Unlock()

	var values []redactValue
	for value, label := range all {
		values = append(values, redactForms(value, label)...)
	}
	// Longest first, so a whole key is replaced before any of its parts.
	sort.SliceStable(values, func(i, j int) bool {
		if len(values[i].value) != len(values[j].value) {
			return len(values[i].value) > len(values[j].value)
		}
		return values[i].value < values[j].value
	})
	pairs := make([]string, 0, len(values)*2)
	for _, v := range values {
		pairs = append(pairs, v.value, v.label)
	}
	r = &secretRedactor{built: time.Now(), stamp: stamp, replacer: strings.NewReplacer(pairs...), values: values}
	redactorsMu.Lock()
	redactors[path] = r
	redactorsMu.Unlock()
	return r
}

// redactForms are the shapes a value takes in output: as is, escaped inside
// JSON (as tools and tool servers return it), Base64 encoded, and for a
// private key, each line of its body.
func redactForms(value, label string) []redactValue {
	forms := map[string]bool{value: true}
	for _, html := range []bool{true, false} {
		var buf strings.Builder
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(html)
		if enc.Encode(value) == nil {
			escaped := strings.TrimSuffix(buf.String(), "\n")
			escaped = strings.TrimSuffix(strings.TrimPrefix(escaped, `"`), `"`)
			if len(escaped) >= 8 {
				forms[escaped] = true
			}
		}
	}
	if len(value) >= 12 {
		// Base64 of the value wherever it starts in an encoded stream: the
		// encoding of value[k:], cut to whole 3-byte groups, appears for one
		// of k = 0, 1, 2. At most two bytes at either edge stay visible.
		for k := 0; k < 3; k++ {
			chunk := value[k:]
			chunk = chunk[:len(chunk)/3*3]
			if len(chunk) >= 9 {
				forms[base64.StdEncoding.EncodeToString([]byte(chunk))] = true
			}
		}
	}
	if strings.Contains(value, "PRIVATE KEY-----") {
		for _, line := range strings.Split(strings.ReplaceAll(value, "\\n", "\n"), "\n") {
			line = strings.TrimSpace(line)
			if len(line) >= 24 && !strings.HasPrefix(line, "-----") {
				forms[line] = true
			}
		}
	}
	out := make([]redactValue, 0, len(forms))
	for f := range forms {
		out = append(out, redactValue{f, label})
	}
	return out
}

// base64Block matches Base64 wrapped over several lines, as the base64
// tool prints it.
var base64Block = regexp.MustCompile(`(?:[A-Za-z0-9+/]{16,}\r?\n)+[A-Za-z0-9+/]+={0,2}`)

// RedactSecrets removes every known secret value from text.
func RedactSecrets(config Config, text string) string {
	if text == "" {
		return text
	}
	r := currentRedactor(config)
	if len(r.values) == 0 {
		return text
	}
	return r.redact(text)
}

func (r *secretRedactor) redact(text string) string {
	if strings.Contains(text, "\n") {
		text = base64Block.ReplaceAllStringFunc(text, func(block string) string {
			joined := strings.NewReplacer("\r", "", "\n", "").Replace(block)
			if cleaned := r.replacer.Replace(joined); cleaned != joined {
				return cleaned
			}
			return block
		})
	}
	return r.replacer.Replace(text)
}

// trimPartialSecret removes a dangling start of a secret at the end of text
// (where output was cut off), so a cut cannot reveal part of a value.
func (r *secretRedactor) trimPartialSecret(text string) string {
	for _, v := range r.values {
		max := min(len(v.value)-1, len(text))
		for n := max; n >= 4; n-- {
			if strings.HasSuffix(text, v.value[:n]) {
				return text[:len(text)-n] + v.label
			}
		}
	}
	return text
}
