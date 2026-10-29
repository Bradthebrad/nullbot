package app

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
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
			path, err := env.file(s.Name+".key", s.Values["private_key"])
			if err != nil {
				return fail(err)
			}
			if err := set("NULLBOT_SSH_KEY_FILE", path); err != nil {
				return fail(err)
			}
			slash := filepath.ToSlash(path)
			command := "ssh -i '" + slash + "' -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=accept-new"
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
			path, err := env.file(s.Name+".json", s.Values["credentials_json"])
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
				path, err := env.file(s.Name, value)
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
		upper := strings.ToUpper(key)
		if inheritedCredentialEnv(key) || strings.HasPrefix(upper, "NULLBOT_VAULT") || strings.HasPrefix(upper, "AWS_") || upper == "GOOGLE_APPLICATION_CREDENTIALS" {
			continue
		}
		env[mcpEnvKey(key)] = value
	}
	return env
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
	return env.vars
}

// --- redaction --------------------------------------------------------------

// secretRedactor replaces every known secret value in text with
// [secret:name]. It is rebuilt when the vault file changes.
type secretRedactor struct {
	modTime  time.Time
	size     int64
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
)

func invalidateRedactor(config Config) {
	redactorsMu.Lock()
	delete(redactors, VaultPath(config))
	redactorsMu.Unlock()
}

// providerEnvKeys are variables whose values NullBot uses as keys when the
// vault has none; they are redacted too.
var providerEnvKeys = []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "OPENROUTER_API_KEY", "GOOGLE_API_KEY", "GEMINI_API_KEY", "BRAVE_API_KEY", "GH_TOKEN", "GITHUB_TOKEN"}

func currentRedactor(config Config) *secretRedactor {
	path := VaultPath(config)
	info, statErr := os.Stat(path)
	redactorsMu.Lock()
	r := redactors[path]
	redactorsMu.Unlock()
	var modTime time.Time
	var size int64
	if statErr == nil {
		modTime, size = info.ModTime(), info.Size()
	}
	// Rebuilt when the vault changes, and every few minutes for keys that
	// come from the environment or legacy files.
	if r != nil && time.Since(r.built) < 5*time.Minute && r.modTime.Equal(modTime) && r.size == size {
		return r
	}
	var values []redactValue
	add := func(value, label string) {
		value = strings.TrimSpace(value)
		if len(value) < 8 {
			return
		}
		values = append(values, redactValue{value, label})
		if len(value) >= 12 {
			// Base64 of the value wherever it starts in an encoded stream:
			// the encoding of value[k:], cut to whole 3-byte groups, appears
			// in the output for one of k = 0, 1, 2. At most two bytes at
			// either edge stay visible.
			for k := 0; k < 3; k++ {
				chunk := value[k:]
				chunk = chunk[:len(chunk)/3*3]
				values = append(values, redactValue{base64.StdEncoding.EncodeToString([]byte(chunk)), label})
			}
		}
		// Long multi-line values (private keys) are also caught line by line.
		if strings.Contains(value, "\n") {
			for _, line := range strings.Split(value, "\n") {
				line = strings.TrimSpace(line)
				if len(line) >= 24 && !strings.HasPrefix(line, "-----") {
					values = append(values, redactValue{line, label})
				}
			}
		}
	}
	payload, err := loadSecretsPayload(config)
	if err == nil {
		for _, s := range payload.Secrets {
			for _, v := range s.Values {
				add(v, "[secret:"+s.Name+"]")
			}
		}
		if c := payload.System.CodexAuth; c != nil {
			add(c.AccessToken, "[secret:chatgpt-sign-in]")
			add(c.RefreshToken, "[secret:chatgpt-sign-in]")
			add(c.IDToken, "[secret:chatgpt-sign-in]")
		}
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
	}
	for _, name := range providerEnvKeys {
		add(os.Getenv(name), "[secret:env "+name+"]")
	}
	if p, ok := vault.EnvPassphrase(); ok {
		add(p, "[secret:vault passphrase]")
	}
	// Longest first, so a whole key is replaced before any of its lines.
	sort.SliceStable(values, func(i, j int) bool { return len(values[i].value) > len(values[j].value) })
	pairs := make([]string, 0, len(values)*2)
	for _, v := range values {
		pairs = append(pairs, v.value, v.label)
	}
	r = &secretRedactor{built: time.Now(), modTime: modTime, size: size, replacer: strings.NewReplacer(pairs...), values: values}
	redactorsMu.Lock()
	redactors[path] = r
	redactorsMu.Unlock()
	return r
}

// RedactSecrets removes every known secret value from text.
func RedactSecrets(config Config, text string) string {
	if text == "" {
		return text
	}
	r := currentRedactor(config)
	if len(r.values) == 0 {
		return text
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
