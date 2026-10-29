package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	tcagent "github.com/Bradthebrad/tinychain/agent"

	"github.com/Bradthebrad/nullbot/pkg/vault"
)

func TestAMaskSentBackNeverReplacesAKey(t *testing.T) {
	config := projectTestConfig(t, "full")
	_ = SaveAPIKeys(config, APIKeys{OpenAI: "sk-proj-0123456789abcdefghij", Anthropic: "sk-ant-x"})
	keys, _ := LoadAPIKeys(config)
	keys.OpenAI = MaskSecret(keys.OpenAI)
	keys.Anthropic = MaskSecret(keys.Anthropic)
	if err := SaveAPIKeys(config, keys); err != nil {
		t.Fatal(err)
	}
	got, _ := LoadAPIKeys(config)
	if got.OpenAI != "sk-proj-0123456789abcdefghij" || got.Anthropic != "sk-ant-x" {
		t.Fatalf("masks replaced keys: %+v", got)
	}
}

func TestLegacyProvidersWithOddNamesSurviveTheMove(t *testing.T) {
	config := projectTestConfig(t, "full")
	writeLegacyKeys(t, config, APIKeys{Other: map[string]string{"x.ai": "xai-key-000000001", "Perplexity AI": "pplx-key-00000002"}})
	keys, _ := LoadAPIKeys(config)
	if keys.ForProvider("x-ai") != "xai-key-000000001" || keys.ForProvider("perplexity-ai") != "pplx-key-00000002" {
		t.Fatalf("keys %+v", keys)
	}
	if _, err := os.Stat(KeysPath(config)); !os.IsNotExist(err) {
		t.Fatal("keys.json was kept after a complete move")
	}
	// A name that cannot be used keeps the file, so nothing is lost.
	other := projectTestConfig(t, "full")
	writeLegacyKeys(t, other, APIKeys{OpenAI: "sk-moves-0000000003", Other: map[string]string{"!!!": "kept-key-000000004"}})
	keys, _ = LoadAPIKeys(other)
	if keys.OpenAI != "sk-moves-0000000003" {
		t.Fatalf("keys %+v", keys)
	}
	if _, err := os.Stat(KeysPath(other)); err != nil {
		t.Fatal("keys.json was deleted with a key still only in it")
	}
	if err := SaveAPIKeys(other, APIKeys{Other: map[string]string{"???": "x-key-000000005"}}); err == nil {
		t.Fatal("an unusable provider name was silently dropped")
	}
}

func TestGeminiAndNameCollisions(t *testing.T) {
	config := projectTestConfig(t, "full")
	_, err := SaveSecret(config, SecretInput{Name: "gemini", Kind: SecretAPIKey, Public: map[string]string{"provider": "gemini"}, Values: map[string]string{"value": "AIza-gemini-0000006"}, AgentUse: true, Description: "Gemini", Servers: []string{"vision"}})
	if err != nil {
		t.Fatal(err)
	}
	keys, _ := LoadAPIKeys(config)
	keys.OpenAI = "sk-openai-00000007"
	if err := SaveAPIKeys(config, keys); err != nil {
		t.Fatal(err)
	}
	secrets, _ := ListSecrets(config)
	var gemini *SecretInfo
	for i := range secrets {
		if secrets[i].Name == "gemini" {
			gemini = &secrets[i]
		}
		if secrets[i].Name == "google" {
			t.Fatal("the gemini key was duplicated as google")
		}
	}
	if gemini == nil || !gemini.AgentUse || len(gemini.Servers) != 1 {
		t.Fatalf("gemini lost its settings: %+v", gemini)
	}
	if _, err := SaveSecret(config, SecretInput{Name: "brave", Kind: SecretGeneric, Values: map[string]string{"value": "not-an-api-key"}, Public: map[string]string{"env": "BRAVE_THING"}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveAPIKeys(config, APIKeys{OpenAI: "sk-openai-00000007", Other: map[string]string{"brave": "BSA-brave-0000008"}}); err == nil || !strings.Contains(err.Error(), "already named brave") {
		t.Fatalf("a name collision: %v", err)
	}
}

func TestRedactionKeepsWhatItHasSeen(t *testing.T) {
	withoutKeychain(t)
	config := projectTestConfig(t, "full")
	if err := CreateVault(config, "passphrase", "redaction passphrase"); err != nil {
		t.Fatal(err)
	}
	_, _ = SaveSecret(config, SecretInput{Name: "svc", Kind: SecretGeneric, Values: map[string]string{"value": "svc-token-123456789"}, Public: map[string]string{"env": "SVC_TOKEN"}})
	LockVault(config)
	if got := RedactSecrets(config, "token svc-token-123456789"); strings.Contains(got, "svc-token") {
		t.Fatalf("a locked vault unredacted a known value: %q", got)
	}
	// Unlocking makes new values known at once.
	resetVaultCache()
	if err := UnlockVault(config, "redaction passphrase"); err != nil {
		t.Fatal(err)
	}
	if got := RedactSecrets(config, "token svc-token-123456789"); strings.Contains(got, "svc-token") {
		t.Fatalf("after unlock: %q", got)
	}
	// A removed value is still redacted for the rest of the session.
	_ = RemoveSecret(config, "svc")
	if got := RedactSecrets(config, "svc-token-123456789"); strings.Contains(got, "svc-token") {
		t.Fatalf("after removal: %q", got)
	}
	// And the vault's own key never shows.
	for _, v := range openVault(config).RedactionValues() {
		if got := RedactSecrets(config, "key "+v); strings.Contains(got, v) {
			t.Fatalf("vault key shown: %q", got)
		}
	}
}

func TestRedactionCatchesEncodedForms(t *testing.T) {
	config := projectTestConfig(t, "full")
	pem := mustSSHKey(t)
	gcp := map[string]string{"type": "service_account", "project_id": "acme", "private_key_id": "0123456789abcdef0123456789abcdef01234567", "private_key": pem, "client_email": "bot@acme.iam.gserviceaccount.com"}
	raw, _ := json.Marshal(gcp)
	if _, err := SaveSecret(config, SecretInput{Name: "gcp", Kind: SecretGCP, Values: map[string]string{"credentials_json": string(raw)}}); err != nil {
		t.Fatal(err)
	}
	token := "ghp_" + strings.Repeat("Zq7", 30)
	_, _ = SaveSecret(config, SecretInput{Name: "gh", Kind: SecretGitHubToken, Values: map[string]string{"token": token}})
	body := strings.Split(strings.TrimSpace(pem), "\n")[1]
	wrapped := base64.StdEncoding.EncodeToString([]byte(token + "\n"))
	var lines []string
	for len(wrapped) > 76 {
		lines, wrapped = append(lines, wrapped[:76]), wrapped[76:]
	}
	lines = append(lines, wrapped)
	escaped, _ := json.Marshal(map[string]string{"t": `quote"` + token})
	cases := map[string]string{
		"the private key alone (jq -r .private_key)": pem,
		"one line of the key body":                   body,
		"the key id":                                 gcp["private_key_id"],
		"Base64 wrapped at 76 columns":               strings.Join(lines, "\n"),
		"inside JSON":                                string(escaped),
	}
	for what, text := range cases {
		got := RedactSecrets(config, "out: "+text)
		if strings.Contains(got, body) || strings.Contains(got, gcp["private_key_id"]) || strings.Contains(got, token) || strings.Contains(got, lines[0]) {
			t.Errorf("%s leaked: %q", what, got)
		}
	}
	if got := RedactSecrets(config, `"auth_uri": "https://accounts.google.com/o/oauth2/auth"`); strings.Contains(got, "[secret") {
		t.Fatalf("public JSON lines were redacted: %q", got)
	}
}

func TestLegacyIDTokenIsRedactedAndCredentialsAreOutOfReach(t *testing.T) {
	withoutKeychain(t)
	config := projectTestConfig(t, "full")
	auth := codexAuthStore{AccessToken: "access-0000000000001", RefreshToken: "refresh-000000000001", IDToken: "idtoken-000000000001"}
	data, _ := json.Marshal(auth)
	_ = os.MkdirAll(filepath.Dir(CodexAuthPath(config)), 0o700)
	_ = os.WriteFile(CodexAuthPath(config), data, 0o600)
	if got := RedactSecrets(config, string(data)); strings.Contains(got, "idtoken-") {
		t.Fatalf("id token shown: %q", got)
	}
	a := New(config)
	for _, path := range []string{"api/codex_auth.json", "vault/vault.json", "api"} {
		for _, tool := range []string{"config_dir_read", "config_dir_list"} {
			for _, candidate := range BuiltinTools(config, a) {
				if candidate.Definition().Name != tool {
					continue
				}
				if _, err := candidate.Call(context.Background(), map[string]any{"path": path}); err == nil || !strings.Contains(err.Error(), "credentials") {
					t.Errorf("%s %s: %v", tool, path, err)
				}
			}
		}
	}
}

func TestSecretRunStopsWhatItLeavesBehind(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	a := secretRunApp(t, "full")
	_, _ = SaveSecret(a.Config(), SecretInput{Name: "gh", Kind: SecretGitHubToken, Values: map[string]string{"token": "ghp_background00000000"}, AgentUse: true})
	dir := t.TempDir()
	quiet, held := filepath.Join(dir, "quiet"), filepath.Join(dir, "held")
	start := time.Now()
	// One background job lets go of the output, one holds it open.
	result, err := a.RunWithSecrets(context.Background(), SecretRunRequest{Command: `(sleep 2; touch "` + quiet + `") >/dev/null 2>&1 & echo started`, Shell: true, Secrets: []string{"gh"}})
	if err != nil || !strings.Contains(result.Stdout, "started") || time.Since(start) > time.Second {
		t.Fatalf("result %+v %v after %s", result, err, time.Since(start))
	}
	result, err = a.RunWithSecrets(context.Background(), SecretRunRequest{Command: `(sleep 4; touch "` + held + `") & echo kept`, Shell: true, Secrets: []string{"gh"}})
	if err != nil || !strings.Contains(result.Stdout, "kept") {
		t.Fatalf("output lost when a child held the pipe: %+v %v", result, err)
	}
	time.Sleep(time.Until(start.Add(5 * time.Second)))
	for _, marker := range []string{quiet, held} {
		if _, err := os.Stat(marker); err == nil {
			t.Fatalf("a background process outlived secret_run with the token in its environment (%s)", filepath.Base(marker))
		}
	}
}

func TestSecretRunEnvironmentAndSavedOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	a := secretRunApp(t, "full")
	_, _ = SaveSecret(a.Config(), SecretInput{Name: "k1", Kind: SecretGeneric, Values: map[string]string{"value": "file-one-0000000"}, Public: map[string]string{"env": "K1", "as_file": "yes"}, AgentUse: true})
	_, _ = SaveSecret(a.Config(), SecretInput{Name: "k1.key", Kind: SecretGeneric, Values: map[string]string{"value": "file-two-0000000"}, Public: map[string]string{"env": "K2", "as_file": "yes"}, AgentUse: true})
	t.Setenv("DEPLOY_KEY", "inherited-deploy-key")
	t.Setenv("SSH_AUTH_SOCK", "/tmp/agent.sock")
	result, err := a.RunWithSecrets(context.Background(), SecretRunRequest{Command: `cat "$K1"; echo; cat "$K2"; echo; env`, Shell: true, Secrets: []string{"k1", "k1.key"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(result.Stdout, "[secret:k1]") != 1 || strings.Count(result.Stdout, "[secret:k1.key]") != 1 {
		t.Fatalf("file secrets collided: %q", result.Stdout)
	}
	if strings.Contains(result.Stdout, "inherited-deploy-key") || strings.Contains(result.Stdout, "SSH_AUTH_SOCK") {
		t.Fatalf("inherited credentials reached the command: %q", result.Stdout)
	}
	// An agent's saved output can't replace a secret the user made meanwhile.
	err = saveAgentSecret(a.Config(), SecretOutputTarget{Name: "k1", Env: "K1"}, "agent value")
	if err == nil {
		t.Fatal("an agent replaced a user's secret")
	}
}

func TestCodexGetsNoVaultEnvironment(t *testing.T) {
	t.Setenv("NULLBOT_VAULT_PASSPHRASE_FILE", "/run/credentials/nullbot/pass")
	t.Setenv("NULLBOT_SERVICE_ACCOUNTS", "[]")
	for _, item := range codexCommandEnv(projectTestConfig(t, "full")) {
		if strings.HasPrefix(item, "NULLBOT_VAULT") || strings.HasPrefix(item, "NULLBOT_SERVICE_") {
			t.Fatalf("codex inherits %s", item)
		}
	}
}

var _ = errors.New
var _ = exec.Command
var _ = vault.ErrLocked
var _ tcagent.Tool

// --- second review ----------------------------------------------------------

func TestConfigPathsCannotReachCredentialsByWindowsSpellings(t *testing.T) {
	config := projectTestConfig(t, "full")
	for _, path := range []string{"api./keys.json", "api ./keys.json", "API. . /keys.json", "vault./vault.json", "api:stream", "notes/x:y"} {
		if _, err := safeConfigPath(config.AppDir, path); err == nil {
			t.Errorf("%q was allowed", path)
		}
	}
	if _, err := safeConfigPath(config.AppDir, "apiary/notes.md"); err != nil {
		t.Errorf("an ordinary folder was refused: %v", err)
	}
}

func TestGoogleWinsOverGeminiAndGeminiIsKept(t *testing.T) {
	config := projectTestConfig(t, "full")
	for name, value := range map[string]string{"gemini": "AIza-gemini-key-0001", "google": "AIza-google-key-0002"} {
		if _, err := SaveSecret(config, SecretInput{Name: name, Kind: SecretAPIKey, Public: map[string]string{"provider": name}, Values: map[string]string{"value": value}}); err != nil {
			t.Fatal(err)
		}
	}
	keys, _ := LoadAPIKeys(config)
	if keys.Google != "AIza-google-key-0002" {
		t.Fatalf("google key %q", keys.Google)
	}
	keys.Google = "AIza-google-key-0003"
	if err := SaveAPIKeys(config, keys); err != nil {
		t.Fatal(err)
	}
	payload, _ := loadSecretsPayload(config)
	got := map[string]string{}
	for _, s := range payload.Secrets {
		got[s.Name] = s.Values["value"]
	}
	if got["google"] != "AIza-google-key-0003" || got["gemini"] != "AIza-gemini-key-0001" {
		t.Fatalf("secrets after save: %v", got)
	}
	// Clearing the Google key clears "google" only.
	keys.Google = ""
	_ = SaveAPIKeys(config, keys)
	payload, _ = loadSecretsPayload(config)
	got = map[string]string{}
	for _, s := range payload.Secrets {
		got[s.Name] = s.Values["value"]
	}
	if _, ok := got["google"]; ok || got["gemini"] != "AIza-gemini-key-0001" {
		t.Fatalf("secrets after clearing: %v", got)
	}
}

func TestProviderNamedSecretsMatchNormalizedNames(t *testing.T) {
	config := projectTestConfig(t, "full")
	if _, err := SaveSecret(config, SecretInput{Name: "x.ai", Kind: SecretAPIKey, Public: map[string]string{"provider": "x-ai"}, Values: map[string]string{"value": "xai-original-00001"}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveAPIKeys(config, APIKeys{Other: map[string]string{"x-ai": "xai-replaced-00002"}}); err != nil {
		t.Fatal(err)
	}
	payload, _ := loadSecretsPayload(config)
	if len(payload.Secrets) != 1 || payload.Secrets[0].Name != "x.ai" || payload.Secrets[0].Values["value"] != "xai-replaced-00002" {
		t.Fatalf("secrets %+v", payload.Secrets)
	}
}

func TestMigrationStoresWhatItCanAndDoesNotRewriteOnEveryRead(t *testing.T) {
	config := projectTestConfig(t, "full")
	writeLegacyKeys(t, config, APIKeys{OpenAI: "sk-legacy-openai-001", Other: map[string]string{"!!!": "kept-key-000000004"}})
	if _, err := LoadAPIKeys(config); err != nil {
		t.Fatal(err)
	}
	payload, _ := loadSecretsPayload(config)
	if apiKeysFromPayload(payload).OpenAI != "sk-legacy-openai-001" {
		t.Fatal("a usable key was not moved because another was unusable")
	}
	before, err := os.ReadFile(VaultPath(config))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		_, _ = LoadAPIKeys(config)
		_ = VaultStatus(config)
	}
	migrated.Delete(config.AppDir) // even without the memo, nothing changes
	_, _ = LoadAPIKeys(config)
	after, _ := os.ReadFile(VaultPath(config))
	if string(before) != string(after) {
		t.Fatal("reading the vault rewrote it")
	}
	// Saving other keys keeps the file while it holds a key the vault can't.
	if err := SaveAPIKeys(config, APIKeys{OpenAI: "sk-legacy-openai-001", Anthropic: "sk-ant-new-000000005"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(KeysPath(config)); err != nil {
		t.Fatal("keys.json was deleted while it held a key only it has")
	}
}

func TestSaveKeepsLegacyKeysAMaskStandsFor(t *testing.T) {
	config := projectTestConfig(t, "full")
	// A vault that can't take a key from the file (its name is taken) keeps
	// the file; a form then sends that key back masked.
	if _, err := SaveSecret(config, SecretInput{Name: "anthropic", Kind: SecretGeneric, Values: map[string]string{"value": "generic-value-0001"}, Public: map[string]string{"env": "SOME_VALUE"}}); err != nil {
		t.Fatal(err)
	}
	writeLegacyKeys(t, config, APIKeys{OpenAI: "sk-legacy-openai-002", Anthropic: "sk-ant-legacy-00003"})
	keys, _ := LoadAPIKeys(config)
	if keys.Anthropic != "sk-ant-legacy-00003" {
		t.Fatalf("keys %+v", keys)
	}
	keys.OpenAI = MaskSecret(keys.OpenAI)
	keys.Anthropic = MaskSecret(keys.Anthropic)
	_ = SaveAPIKeys(config, keys)
	if _, err := os.Stat(KeysPath(config)); err != nil {
		t.Fatal("keys.json was deleted with the Anthropic key only in it")
	}
	got, _ := LoadAPIKeys(config)
	if got.OpenAI != "sk-legacy-openai-002" || got.Anthropic != "sk-ant-legacy-00003" {
		t.Fatalf("keys after save: %+v", got)
	}
}

func TestRedactionPastTheCapAndOfSignInTokens(t *testing.T) {
	withoutKeychain(t)
	config := projectTestConfig(t, "full")
	path := VaultPath(config)
	redactorsMu.Lock()
	full := map[string]string{}
	for i := 0; i < maxSeenValues; i++ {
		full[fmt.Sprintf("filler-value-%06d", i)] = "[secret:filler]"
	}
	seenValues[path] = full
	redactorsMu.Unlock()
	t.Cleanup(func() {
		redactorsMu.Lock()
		delete(seenValues, path)
		redactorsMu.Unlock()
	})
	if err := CreateVault(config, "passphrase", "cap passphrase"); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveSecret(config, SecretInput{Name: "late", Kind: SecretGeneric, Values: map[string]string{"value": "late-value-00000001"}, Public: map[string]string{"env": "LATE"}}); err != nil {
		t.Fatal(err)
	}
	if got := RedactSecrets(config, "late-value-00000001"); strings.Contains(got, "late-value") {
		t.Fatalf("a current value past the cap was shown: %q", got)
	}
	// Sign-in tokens saved while unlocked stay redacted once locked.
	redactorsMu.Lock()
	delete(seenValues, path)
	redactorsMu.Unlock()
	if err := saveCodexAuth(config, codexAuthStore{AccessToken: "access-token-0000000009", RefreshToken: "refresh-token-000000009"}); err != nil {
		t.Fatal(err)
	}
	keyForms := openVault(config).RedactionValues()
	LockVault(config)
	for _, v := range append([]string{"access-token-0000000009", "refresh-token-000000009"}, keyForms...) {
		if got := RedactSecrets(config, "x "+v); strings.Contains(got, v) {
			t.Fatalf("shown after lock: %q", got)
		}
	}
}

func TestRedactorNoticesALegacyFileAtOnce(t *testing.T) {
	withoutKeychain(t)
	t.Setenv(vault.PassphraseEnv, "")
	config := projectTestConfig(t, "full")
	_ = RedactSecrets(config, "warm the cache")
	writeLegacyKeys(t, config, APIKeys{OpenAI: "sk-dropped-in-000000010"})
	if got := RedactSecrets(config, "sk-dropped-in-000000010"); strings.Contains(got, "sk-dropped") {
		t.Fatalf("a new legacy key was shown: %q", got)
	}
}

func TestShortKeysAreFullyMaskedAndSSHKeepsNoMaster(t *testing.T) {
	if got := MaskSecret("abcdefghijkl"); got != "********" {
		t.Fatalf("12-character key masked as %q", got)
	}
	if !LooksMasked("****") || !LooksMasked("(not set)") || LooksMasked("sk-real") {
		t.Fatal("LooksMasked")
	}
	env, err := buildSecretEnv([]Secret{{Name: "deploy", Kind: SecretSSHKey, Values: map[string]string{"private_key": mustSSHKey(t)}}}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer env.cleanup()
	if cmd := env.vars["GIT_SSH_COMMAND"]; !strings.Contains(cmd, "ControlMaster=no") || !strings.Contains(cmd, "ControlPath=none") {
		t.Fatalf("GIT_SSH_COMMAND %q", cmd)
	}
}

func TestCodexGetsNoInheritedCredentials(t *testing.T) {
	t.Setenv("DEPLOY_KEY", "inherited-deploy-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "aws-secret")
	t.Setenv("CODEX_API_KEY", "codex-own-key")
	env := strings.Join(codexCommandEnv(projectTestConfig(t, "full")), "\n")
	if strings.Contains(env, "inherited-deploy-key") || strings.Contains(env, "aws-secret") {
		t.Fatalf("codex inherits credentials")
	}
	if !strings.Contains(env, "CODEX_API_KEY=codex-own-key") {
		t.Fatal("codex lost its own key")
	}
}

// --- third review -----------------------------------------------------------

func TestMasksNeverDeleteOrRestoreKeys(t *testing.T) {
	config := projectTestConfig(t, "full")
	writeLegacyKeys(t, config, APIKeys{OpenAI: "sk-legacy-openai-0011", Other: map[string]string{"!!!": "kept-key-000000012"}})
	if _, err := SaveSecret(config, SecretInput{Name: "anthropic", Kind: SecretAPIKey, Public: map[string]string{"provider": "anthropic"}, Values: map[string]string{"value": "sk-ant-vault-0000013"}}); err != nil {
		t.Fatal(err)
	}
	shown, _ := LoadAPIKeys(config)
	form := APIKeys{OpenAI: MaskSecret(shown.OpenAI), Anthropic: MaskSecret(shown.Anthropic), OpenRouter: MaskSecret(shown.OpenRouter), Google: MaskSecret(shown.Google)}
	if err := SaveAPIKeys(config, form); err != nil {
		t.Fatal(err)
	}
	got, _ := LoadAPIKeys(config)
	if got.Anthropic != "sk-ant-vault-0000013" || got.OpenAI != "sk-legacy-openai-0011" {
		t.Fatalf("a masked form changed keys: %+v", got)
	}
}

func TestAnOlderLegacyKeyIsKeptAndTheFileGoes(t *testing.T) {
	config := projectTestConfig(t, "full")
	if _, err := SaveSecret(config, SecretInput{Name: "openai", Kind: SecretAPIKey, Public: map[string]string{"provider": "openai"}, Values: map[string]string{"value": "sk-newer-openai-0014"}}); err != nil {
		t.Fatal(err)
	}
	writeLegacyKeys(t, config, APIKeys{OpenAI: "sk-older-openai-0015"})
	keys, _ := LoadAPIKeys(config)
	if keys.OpenAI != "sk-newer-openai-0014" {
		t.Fatalf("the older key won: %+v", keys)
	}
	if _, err := os.Stat(KeysPath(config)); !os.IsNotExist(err) {
		t.Fatal("keys.json stayed although everything in it is kept")
	}
	payload, _ := loadSecretsPayload(config)
	found := false
	for _, s := range payload.Secrets {
		if s.Name == "openai-legacy" && s.Values["value"] == "sk-older-openai-0015" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the older key was lost: %+v", payload.Secrets)
	}
	keys.OpenAI = MaskSecret(keys.OpenAI)
	_ = SaveAPIKeys(config, keys)
	if got, _ := LoadAPIKeys(config); got.OpenAI != "sk-newer-openai-0014" {
		t.Fatalf("a mask brought back the older key: %+v", got)
	}
}

func TestOneManagedSecretPerProvider(t *testing.T) {
	config := projectTestConfig(t, "full")
	if err := SaveAPIKeys(config, APIKeys{Other: map[string]string{"x-ai": "xai-settings-00016"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveSecret(config, SecretInput{Name: "x.ai", Kind: SecretAPIKey, Public: map[string]string{"provider": "x-ai"}, Values: map[string]string{"value": "xai-secrets-000017"}, AgentUse: true}); err != nil {
		t.Fatal(err)
	}
	if err := SaveAPIKeys(config, APIKeys{Other: map[string]string{"x-ai": "xai-settings-00018"}}); err != nil {
		t.Fatal(err)
	}
	payload, _ := loadSecretsPayload(config)
	got := map[string]Secret{}
	for _, s := range payload.Secrets {
		got[s.Name] = s
	}
	if got["x-ai"].Values["value"] != "xai-settings-00018" || got["x.ai"].Values["value"] != "xai-secrets-000017" || !got["x.ai"].AgentUse {
		t.Fatalf("secrets %+v", payload.Secrets)
	}
}

func TestNoKeychainNeverWritesMasksAsKeys(t *testing.T) {
	withoutKeychain(t)
	t.Setenv(vault.PassphraseEnv, "")
	config := projectTestConfig(t, "full")
	if err := SaveAPIKeys(config, APIKeys{OpenAI: "(not set)", Anthropic: "sk-a...wxyz", OpenRouter: "sk-or-real-000019"}); err != nil {
		t.Fatal(err)
	}
	legacy, err := readLegacyAPIKeys(config)
	if err != nil {
		t.Fatal(err)
	}
	if legacy.OpenAI != "" || legacy.Anthropic != "" || legacy.OpenRouter != "sk-or-real-000019" {
		t.Fatalf("written %+v", legacy)
	}
}
