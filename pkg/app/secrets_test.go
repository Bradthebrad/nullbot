package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	tcagent "github.com/Bradthebrad/tinychain/agent"

	"github.com/Bradthebrad/nullbot/pkg/vault"
)

// memoryKeyring stands in for the system keychain in tests.
type memoryKeyring struct {
	mu      sync.Mutex
	keys    map[string][]byte
	missing bool
}

var testKeyring = &memoryKeyring{}

func (k *memoryKeyring) Kind() vault.Protection { return vault.Keychain }
func (k *memoryKeyring) Available() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return !k.missing
}
func (k *memoryKeyring) Store(id string, key []byte) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.keys == nil {
		k.keys = map[string][]byte{}
	}
	k.keys[id] = append([]byte(nil), key...)
	return nil, nil
}
func (k *memoryKeyring) Load(id string, _ []byte) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	key, ok := k.keys[id]
	if !ok {
		return nil, errors.New("no item")
	}
	return append([]byte(nil), key...), nil
}
func (k *memoryKeyring) Delete(id string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.keys, id)
	return nil
}

// withoutKeychain simulates a system that cannot encrypt.
func withoutKeychain(t *testing.T) {
	t.Helper()
	testKeyring.mu.Lock()
	testKeyring.missing = true
	testKeyring.mu.Unlock()
	t.Cleanup(func() {
		testKeyring.mu.Lock()
		testKeyring.missing = false
		testKeyring.mu.Unlock()
	})
}

func writeLegacyKeys(t *testing.T, config Config, keys APIKeys) {
	t.Helper()
	data, _ := json.Marshal(keys)
	_ = os.MkdirAll(filepath.Dir(KeysPath(config)), 0o700)
	if err := os.WriteFile(KeysPath(config), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyKeysMoveIntoTheVault(t *testing.T) {
	config := projectTestConfig(t, "full")
	writeLegacyKeys(t, config, APIKeys{OpenAI: "sk-legacy-openai-0001", Other: map[string]string{"brave": "brave-legacy-key-0002"}})
	auth := codexAuthStore{AccessToken: "codex-access-token-0003", RefreshToken: "codex-refresh-token-0004"}
	data, _ := json.Marshal(auth)
	_ = os.WriteFile(CodexAuthPath(config), data, 0o600)

	keys, err := LoadAPIKeys(config)
	if err != nil || keys.OpenAI != "sk-legacy-openai-0001" || keys.ForProvider("brave") != "brave-legacy-key-0002" {
		t.Fatalf("keys %+v %v", keys, err)
	}
	for _, path := range []string{KeysPath(config), CodexAuthPath(config)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s was left in plain text", path)
		}
	}
	raw, _ := os.ReadFile(VaultPath(config))
	for _, value := range []string{"sk-legacy-openai-0001", "brave-legacy", "codex-access", "codex-refresh"} {
		if strings.Contains(string(raw), value) {
			t.Fatalf("vault file holds %s in plain text", value)
		}
	}
	got, err := loadCodexAuth(config)
	if err != nil || got.RefreshToken != "codex-refresh-token-0004" {
		t.Fatalf("codex auth %+v %v", got, err)
	}
	if !CodexAuthPresent(config) {
		t.Fatal("the ChatGPT sign-in was lost in the move")
	}
	secrets, _ := ListSecrets(config)
	if len(secrets) != 2 || secrets[0].Name != "brave" || secrets[0].Provides[0] != "BRAVE_API_KEY" || secrets[0].AgentUse {
		t.Fatalf("secrets %+v", secrets)
	}
	if info := VaultStatus(config); !info.Unlocked || info.Secrets != 2 || len(info.Plaintext) != 0 {
		t.Fatalf("status %+v", info)
	}
}

func TestWithoutAKeychainKeysStayWhereTheyWere(t *testing.T) {
	withoutKeychain(t)
	config := projectTestConfig(t, "full")
	writeLegacyKeys(t, config, APIKeys{Anthropic: "sk-ant-legacy-0005"})
	keys, _ := LoadAPIKeys(config)
	if keys.Anthropic != "sk-ant-legacy-0005" {
		t.Fatalf("keys %+v", keys)
	}
	if err := SaveAPIKeys(config, APIKeys{Anthropic: "sk-ant-new-key-0006"}); err != nil {
		t.Fatalf("saving without a keychain: %v", err)
	}
	if keys, _ := LoadAPIKeys(config); keys.Anthropic != "sk-ant-new-key-0006" {
		t.Fatalf("after save %+v", keys)
	}
	if info := VaultStatus(config); info.Exists || len(info.Plaintext) != 1 {
		t.Fatalf("status %+v", info)
	}
	// Other secrets are never written in plain text.
	if _, err := SaveSecret(config, SecretInput{Name: "github", Kind: SecretGitHubToken, Values: map[string]string{"token": "ghp_0123456789abcdef"}}); !errors.Is(err, vault.ErrNoKeychain) {
		t.Fatalf("a token saved without encryption: %v", err)
	}
}

func TestSaveAPIKeysManagesOnlyProviderNamedSecrets(t *testing.T) {
	config := projectTestConfig(t, "full")
	if _, err := SaveSecret(config, SecretInput{Name: "openai-work", Kind: SecretAPIKey, Public: map[string]string{"provider": "openai"}, Values: map[string]string{"value": "sk-work-key-000000007"}}); err != nil {
		t.Fatal(err)
	}
	keys, _ := LoadAPIKeys(config)
	if keys.OpenAI != "sk-work-key-000000007" {
		t.Fatalf("a named key does not stand in: %+v", keys)
	}
	keys.Anthropic = "sk-ant-000000000008"
	if err := SaveAPIKeys(config, keys); err != nil {
		t.Fatal(err)
	}
	secrets, _ := ListSecrets(config)
	names := []string{}
	for _, s := range secrets {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "anthropic,openai-work" {
		t.Fatalf("secrets after a round trip: %v", names)
	}
	keys.Anthropic = ""
	_ = SaveAPIKeys(config, keys)
	if keys, _ := LoadAPIKeys(config); keys.Anthropic != "" || keys.OpenAI == "" {
		t.Fatalf("clearing a key: %+v", keys)
	}
}

func TestSecretValidation(t *testing.T) {
	config := projectTestConfig(t, "full")
	cases := []SecretInput{
		{Name: "Bad Name", Kind: SecretGeneric, Values: map[string]string{"value": "x"}, Public: map[string]string{"env": "X"}},
		{Name: "path", Kind: SecretGeneric, Values: map[string]string{"value": "/evil"}, Public: map[string]string{"env": "PATH"}},
		{Name: "preload", Kind: SecretGeneric, Values: map[string]string{"value": "x.so"}, Public: map[string]string{"env": "LD_PRELOAD"}},
		{Name: "missing", Kind: SecretAWS, Values: map[string]string{"access_key_id": "AKIA0000000000000000"}},
		{Name: "key", Kind: SecretSSHKey, Values: map[string]string{"private_key": "not a key"}},
		{Name: "gcp", Kind: SecretGCP, Values: map[string]string{"credentials_json": "{"}},
		{Name: "file-to-server", Kind: SecretGeneric, Values: map[string]string{"value": "abc"}, Public: map[string]string{"env": "KUBECONFIG", "as_file": "yes"}, Servers: []string{"k8s"}},
		{Name: "kindless", Kind: "password"},
	}
	for _, input := range cases {
		if _, err := SaveSecret(config, input); err == nil {
			t.Errorf("%s was accepted", input.Name)
		}
	}
	// Changing a token forgets who it belonged to.
	_, _ = SaveSecret(config, SecretInput{Name: "github", Kind: SecretGitHubToken, Values: map[string]string{"token": "ghp_first0000000000"}})
	_ = updateSecrets(config, func(p *vaultPayload) error { p.Secrets[0].Public["login"] = "someone"; return nil })
	info, err := SaveSecret(config, SecretInput{Name: "github", Kind: SecretGitHubToken, Values: map[string]string{"token": "ghp_second000000000"}})
	if err != nil || info.Public["login"] != "" || info.Public["host"] != "github.com" || info.Hint != "ends in 0000" {
		t.Fatalf("after a new token: %+v %v", info, err)
	}
	if _, err := SaveSecret(config, SecretInput{Name: "github", Kind: SecretAWS}); err == nil {
		t.Fatal("a secret changed kind")
	}
}

func secretRunApp(t *testing.T, permission string) *App {
	t.Helper()
	cfg := projectTestConfig(t, permission)
	cfg.EnabledMCPServers = nil
	return New(cfg)
}

func runSecretTool(t *testing.T, a *App, args map[string]any) (SecretRunResult, error) {
	t.Helper()
	var tool tcagent.Tool
	for _, candidate := range BuiltinTools(a.Config(), a) {
		if candidate.Definition().Name == "secret_run" {
			tool = candidate
		}
	}
	out, err := tool.Call(context.Background(), args)
	var result SecretRunResult
	if err == nil {
		if jerr := json.Unmarshal([]byte(out), &result); jerr != nil {
			t.Fatalf("output %q", out)
		}
	}
	return result, err
}

func TestSecretRunGivesValuesToTheCommandButNotTheModel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	a := secretRunApp(t, "full")
	config := a.Config()
	token := "ghp_secretRunToken1234567890"
	if _, err := SaveSecret(config, SecretInput{Name: "github", Kind: SecretGitHubToken, Values: map[string]string{"token": token}, AgentUse: true}); err != nil {
		t.Fatal(err)
	}
	result, err := runSecretTool(t, a, map[string]any{"command": `echo "token=$GH_TOKEN"; echo "len=${#GITHUB_TOKEN}"; echo "$GH_TOKEN" | base64; echo "err=$GH_TOKEN" >&2`, "shell": true, "secrets": []any{"github"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Stdout+result.Stderr, token) || strings.Contains(result.Stdout, "Z2hwX3NlY3JldFJ1blRva2Vu") {
		t.Fatalf("the token reached the model: %+v", result)
	}
	if !strings.Contains(result.Stdout, "token=[secret:github]") || !strings.Contains(result.Stdout, "len=28") || !strings.Contains(result.Stderr, "err=[secret:github]") {
		t.Fatalf("output %+v", result)
	}
	// Arguments are passed as written: values never go on a command line.
	result, err = runSecretTool(t, a, map[string]any{"command": "printf", "args": []any{"%s|%s", "${GH_TOKEN}", "${HOME}"}, "secrets": []any{"github"}})
	if err != nil || result.Stdout != "${GH_TOKEN}|${HOME}" {
		t.Fatalf("arguments %+v %v", result, err)
	}
	// The inherited environment loses anything that looks like a credential.
	t.Setenv("SOME_SERVICE_TOKEN", "inherited-token-should-not-pass")
	result, _ = runSecretTool(t, a, map[string]any{"command": "env", "secrets": []any{"github"}})
	if strings.Contains(result.Stdout, "inherited-token") || strings.Contains(result.Stdout, "NULLBOT_VAULT") {
		t.Fatalf("inherited credentials passed through: %s", result.Stdout)
	}
	// Activity records the use without the value.
	found := false
	a.mu.Lock()
	records := append([]ActivityRecord(nil), a.activity...)
	a.mu.Unlock()
	for _, record := range records {
		if record.Kind == "secret" && strings.Contains(record.Detail, "github in") {
			found = true
		}
		if strings.Contains(record.Detail, token) {
			t.Fatal("the token is in the activity log")
		}
	}
	if !found {
		t.Fatal("secret use is not in the activity log")
	}
}

func TestSecretRunSignsGitInOverHTTPS(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil || runtime.GOOS == "windows" {
		t.Skip("needs git and sh")
	}
	a := secretRunApp(t, "full")
	_, _ = SaveSecret(a.Config(), SecretInput{Name: "github", Kind: SecretGitHubToken, Values: map[string]string{"token": "ghp_gitHelperToken000000"}, AgentUse: true})
	result, err := runSecretTool(t, a, map[string]any{"command": `printf 'protocol=https\nhost=github.com\n\n' | git credential fill`, "shell": true, "secrets": []any{"github"}})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("git credential fill: %+v %v", result, err)
	}
	if !strings.Contains(result.Stdout, "username=x-access-token") || !strings.Contains(result.Stdout, "password=[secret:github]") {
		t.Fatalf("git did not get the token: %+v", result)
	}
	// Other hosts are not given the token.
	result, _ = runSecretTool(t, a, map[string]any{"command": `printf 'protocol=https\nhost=example.com\n\n' | git credential fill`, "shell": true, "secrets": []any{"github"}})
	if strings.Contains(result.Stdout, "[secret:github]") {
		t.Fatalf("the token was offered to another host: %+v", result)
	}
}

func TestSecretRunRefusals(t *testing.T) {
	a := secretRunApp(t, "read-write")
	_, _ = SaveSecret(a.Config(), SecretInput{Name: "hidden", Kind: SecretGeneric, Values: map[string]string{"value": "hidden-value-000"}, Public: map[string]string{"env": "HIDDEN"}})
	_, _ = SaveSecret(a.Config(), SecretInput{Name: "open", Kind: SecretGeneric, Values: map[string]string{"value": "open-value-0000"}, Public: map[string]string{"env": "OPEN"}, AgentUse: true})
	if _, err := runSecretTool(t, a, map[string]any{"command": "true", "secrets": []any{"open"}}); err == nil || !strings.Contains(err.Error(), "run") {
		t.Fatalf("a read-write project ran a command: %v", err)
	}
	a = secretRunApp(t, "full")
	_, _ = SaveSecret(a.Config(), SecretInput{Name: "hidden", Kind: SecretGeneric, Values: map[string]string{"value": "hidden-value-000"}, Public: map[string]string{"env": "HIDDEN"}})
	if _, err := runSecretTool(t, a, map[string]any{"command": "true", "secrets": []any{"hidden"}}); err == nil || !strings.Contains(err.Error(), "not available to agents") {
		t.Fatalf("a secret not allowed for agents: %v", err)
	}
	if _, err := runSecretTool(t, a, map[string]any{"command": "true", "secrets": []any{"nope"}}); err == nil {
		t.Fatal("an unknown secret was accepted")
	}
	outside := t.TempDir()
	if _, err := runSecretTool(t, a, map[string]any{"command": "true", "secrets": []any{"hidden"}, "cwd": outside}); err == nil {
		t.Fatal("ran outside every project")
	}
}

func TestSecretRunCanSaveOutputAsASecret(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	a := secretRunApp(t, "full")
	_, _ = SaveSecret(a.Config(), SecretInput{Name: "seed", Kind: SecretGeneric, Values: map[string]string{"value": "seed-value-00000"}, Public: map[string]string{"env": "SEED"}, AgentUse: true})
	result, err := runSecretTool(t, a, map[string]any{"command": "echo new-credential-from-a-command", "shell": true, "secrets": []any{"seed"},
		"save_output_as": map[string]any{"name": "made", "env": "MADE_TOKEN"}})
	if err != nil || result.Saved != "made" || strings.Contains(result.Stdout, "new-credential") {
		t.Fatalf("save: %+v %v", result, err)
	}
	result, _ = runSecretTool(t, a, map[string]any{"command": `echo "$MADE_TOKEN"`, "shell": true, "secrets": []any{"made"}})
	if result.Stdout != "[secret:made]\n" {
		t.Fatalf("using the saved secret: %+v", result)
	}
	// An agent cannot overwrite a secret the user made.
	if _, err := runSecretTool(t, a, map[string]any{"command": "echo x", "shell": true, "secrets": []any{"seed"}, "save_output_as": map[string]any{"name": "seed", "env": "SEED"}}); err == nil {
		t.Fatal("an agent replaced a user's secret")
	}
}

func TestEveryToolResultIsRedacted(t *testing.T) {
	a := secretRunApp(t, "full")
	value := "sk-proj-redact-me-please-0000"
	_ = SaveAPIKeys(a.Config(), APIKeys{OpenAI: value})
	leaky := tcagent.ToolFunc{Name: "workspace_info", Func: func(ctx context.Context, args map[string]any) (string, error) {
		return "here it is: " + value, errors.New("failed with " + value)
	}}
	tool := guardTools(a, "", false, []tcagent.Tool{leaky})[0]
	out, err := tool.Call(context.Background(), map[string]any{})
	if strings.Contains(out, value) || err == nil || strings.Contains(err.Error(), value) || !strings.Contains(out, "[secret:openai]") {
		t.Fatalf("out %q err %v", out, err)
	}
}

func TestBoundSecretsReachOnlyTheirServer(t *testing.T) {
	config := projectTestConfig(t, "full")
	_, err := SaveSecret(config, SecretInput{Name: "gh", Kind: SecretGitHubToken, Values: map[string]string{"token": "ghp_bound00000000000"}, Servers: []string{"github-mcp"}})
	if err != nil {
		t.Fatal(err)
	}
	env := mcpRuntimeEnv(config, "github-mcp", MCPEntry{Command: "/bin/true", Enabled: true})
	if env[mcpEnvKey("GITHUB_TOKEN")] != "ghp_bound00000000000" {
		t.Fatalf("bound server env %v", env)
	}
	if other := mcpRuntimeEnv(config, "other-mcp", MCPEntry{Command: "/bin/true", Enabled: true}); other[mcpEnvKey("GITHUB_TOKEN")] != "" {
		t.Fatal("another server got the token")
	}
	if _, err := SaveSecret(config, SecretInput{Name: "deploy-key", Kind: SecretSSHKey, Values: map[string]string{"private_key": mustSSHKey(t)}, Servers: []string{"github-mcp"}}); err == nil {
		t.Fatal("an SSH key was bound to a server")
	}
}

func mustSSHKey(t *testing.T) string {
	t.Helper()
	key, _, err := generateSSHKey("test@nullbot")
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestSSHKeysAreGeneratedAndRead(t *testing.T) {
	config := projectTestConfig(t, "full")
	info, err := GenerateSSHKey(config, "deploy", "github.com", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(info.Public["public_key"], "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5") || !strings.HasPrefix(info.Public["fingerprint"], "SHA256:") || !strings.HasSuffix(info.Public["public_key"], " deploy@nullbot") {
		t.Fatalf("generated %+v", info)
	}
	payload, _ := loadSecretsPayload(config)
	private := payload.Secrets[0].Values["private_key"]
	pub, err := sshPublicKeyFromPrivate(private)
	if err != nil || pub.fingerprint() != info.Public["fingerprint"] || pub.comment != "deploy@nullbot" {
		t.Fatalf("reading it back: %+v %v", pub, err)
	}
	if strings.Contains(info.Public["public_key"], "PRIVATE") {
		t.Fatal("the private key leaked into public details")
	}
	if _, err := GenerateSSHKey(config, "deploy", "", "", false); err == nil {
		t.Fatal("a second key replaced the first")
	}
	encrypted := "-----BEGIN RSA PRIVATE KEY-----\nProc-Type: 4,ENCRYPTED\nDEK-Info: AES-128-CBC,00000000000000000000000000000000\n\nAAAA\n-----END RSA PRIVATE KEY-----\n"
	if _, err := sshPublicKeyFromPrivate(encrypted); !errors.Is(err, errSSHPassphrase) {
		t.Fatalf("a protected key: %v", err)
	}
	if runtime.GOOS != "windows" {
		a := New(config)
		result, err := a.RunWithSecrets(context.Background(), SecretRunRequest{Command: `test -f "$NULLBOT_SSH_KEY_FILE" && stat -c %a "$NULLBOT_SSH_KEY_FILE"; echo "$GIT_SSH_COMMAND"`, Shell: true, Secrets: []string{"deploy"}})
		if err != nil || !strings.HasPrefix(result.Stdout, "600\n") || !strings.Contains(result.Stdout, "IdentitiesOnly=yes") {
			t.Fatalf("key file: %+v %v", result, err)
		}
		path := strings.TrimSuffix(strings.SplitN(strings.SplitN(result.Stdout, "-i '", 2)[1], "'", 2)[0], "")
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("the key file %s was left behind", path)
		}
	}
}

func TestGitHubTokenVerification(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ghp_verify0000000000" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/user":
			w.Header().Set("X-OAuth-Scopes", "repo, admin:public_key")
			_, _ = w.Write([]byte(`{"login":"octocat","name":"Mona"}`))
		case "/user/keys":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if !strings.HasPrefix(body["key"], "ssh-ed25519 ") {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusCreated)
		}
	}))
	defer server.Close()
	old := githubAPIBase
	githubAPIBase = func(string) string { return server.URL }
	defer func() { githubAPIBase = old }()

	config := projectTestConfig(t, "full")
	_, _ = SaveSecret(config, SecretInput{Name: "github", Kind: SecretGitHubToken, Values: map[string]string{"token": "ghp_verify0000000000"}})
	id, err := VerifyGitHubToken(context.Background(), config, "github")
	if err != nil || id.Login != "octocat" || id.Scopes != "repo, admin:public_key" {
		t.Fatalf("verify %+v %v", id, err)
	}
	secrets, _ := ListSecrets(config)
	if secrets[0].Public["login"] != "octocat" {
		t.Fatalf("login not recorded: %+v", secrets[0])
	}
	if _, err := GenerateSSHKey(config, "laptop", "", "", false); err != nil {
		t.Fatal(err)
	}
	if err := AddSSHKeyToGitHub(context.Background(), config, "laptop", "github", ""); err != nil {
		t.Fatal(err)
	}
	_, _ = SaveSecret(config, SecretInput{Name: "bad", Kind: SecretGitHubToken, Values: map[string]string{"token": "ghp_wrong00000000000"}})
	if _, err := VerifyGitHubToken(context.Background(), config, "bad"); err == nil || !strings.Contains(err.Error(), "refused the token") {
		t.Fatalf("a wrong token: %v", err)
	}
}

func TestPartialSecretsAreTrimmedAtACut(t *testing.T) {
	r := &secretRedactor{values: []redactValue{{"abcdefghijklmnop", "[secret:x]"}}}
	if got := r.trimPartialSecret("output ends with abcdefg"); got != "output ends with [secret:x]" {
		t.Fatalf("got %q", got)
	}
	if got := r.trimPartialSecret("nothing here"); got != "nothing here" {
		t.Fatalf("got %q", got)
	}
}

func TestVaultPassphraseOnAServer(t *testing.T) {
	withoutKeychain(t)
	config := projectTestConfig(t, "full")
	if err := CreateVault(config, "passphrase", "server passphrase 1"); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveSecret(config, SecretInput{Name: "aws-prod", Kind: SecretAWS, Values: map[string]string{"access_key_id": "AKIAEXAMPLE000000000", "secret_access_key": "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}, Public: map[string]string{"region": "us-east-1"}}); err != nil {
		t.Fatal(err)
	}
	resetVaultCache() // a new process
	if _, err := ListSecrets(config); !errors.Is(err, vault.ErrLocked) {
		t.Fatalf("listing a locked vault: %v", err)
	}
	if err := UnlockVault(config, "server passphrase 1"); err != nil {
		t.Fatal(err)
	}
	secrets, err := ListSecrets(config)
	if err != nil || len(secrets) != 1 || strings.Join(secrets[0].Provides, ",") != "AWS_ACCESS_KEY_ID,AWS_SECRET_ACCESS_KEY,AWS_REGION,AWS_DEFAULT_REGION" {
		t.Fatalf("secrets %+v %v", secrets, err)
	}
}
