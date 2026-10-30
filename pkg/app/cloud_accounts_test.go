package app

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Bradthebrad/nullbot/pkg/infra"
)

// fakeCloudCLI is an aws or gcloud command that records its arguments and
// environment, then prints the body as its answer. The body may read $MODE
// from a file next to it, so one test can try several answers.
func fakeCloudCLI(t *testing.T, name, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		`echo "$*" >> "` + dir + `/calls"` + "\n" +
		`env | grep -E '^(AWS_|CLOUDSDK_|GOOGLE_)' | sort > "` + dir + `/env"` + "\n" +
		`MODE=$(cat "` + dir + `/mode" 2>/dev/null)` + "\n" +
		body
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(map[string]string{"aws": "NULLBOT_AWS_BIN", "gcloud": "NULLBOT_GCLOUD_BIN"}[name], path)
	return dir
}

func setMode(t *testing.T, dir, mode string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "mode"), []byte(mode), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCheckAWSAccount(t *testing.T) {
	dir := fakeCloudCLI(t, "aws", `
case "$MODE" in
  optin) case "$*" in *"--region us-east-1"*) ;; *) echo "An error occurred (InvalidClientTokenId) when calling the GetCallerIdentity operation: The security token included in the request is invalid." >&2; exit 254 ;; esac ;;
  skew) echo "An error occurred (SignatureDoesNotMatch) when calling the GetCallerIdentity operation: Signature expired: 20261029T120000Z is now earlier than 20261029T121000Z" >&2; exit 254 ;;
  offline) echo "Could not connect to the endpoint URL: \"https://sts.us-east-1.amazonaws.com/\"" >&2; exit 255 ;;
  badkey) echo "An error occurred (InvalidClientTokenId) when calling the GetCallerIdentity operation: The security token included in the request is invalid." >&2; exit 254 ;;
  badsecret) echo "An error occurred (SignatureDoesNotMatch) when calling the GetCallerIdentity operation: signature mismatch" >&2; exit 254 ;;
  leak) echo "could not sign in with $AWS_SECRET_ACCESS_KEY" >&2; exit 255 ;;
esac
echo '{"UserId":"AIDATEST","Account":"123456789012","Arn":"arn:aws:iam::123456789012:user/nullbot-test"}'
`)
	t.Setenv("AWS_PROFILE", "personal")
	t.Setenv("AWS_ENDPOINT_URL", "https://example.invalid")
	t.Setenv("AWS_CA_BUNDLE", "/etc/ssl/corp.pem")
	a := New(projectTestConfig(t, "read-only"))
	config := a.Config()
	if _, err := SaveSecret(config, SecretInput{Name: "aws-test", Kind: SecretAWS, Values: map[string]string{"access_key_id": "AKIACHECKTEST0000000", "secret_access_key": "check/secret/key/0000000000000000000000"}}); err != nil {
		t.Fatal(err)
	}

	// Listed before it is checked, without anything secret.
	st, err := a.InfraState()
	if err != nil || st.AccountsError != "" || len(st.Accounts) != 1 {
		t.Fatalf("%v %q %+v", err, st.AccountsError, st.Accounts)
	}
	if acct := st.Accounts[0]; acct.Name != "aws-test" || acct.Cloud != "aws" || acct.Checked != "" || acct.Account != "" {
		t.Fatalf("%+v", acct)
	}

	id, err := a.CheckCloudAccount(context.Background(), "aws-test")
	if err != nil {
		t.Fatal(err)
	}
	if id.Account != "123456789012" || id.Identity != "arn:aws:iam::123456789012:user/nullbot-test" || id.Region != "us-east-1" {
		t.Fatalf("%+v", id)
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
	if strings.TrimSpace(string(calls)) != "sts get-caller-identity --output json --region us-east-1" {
		t.Fatalf("calls: %s", calls)
	}
	// The same clean environment plans get: the key, and none of this
	// computer's profiles or endpoints.
	env, _ := os.ReadFile(filepath.Join(dir, "env"))
	// A certificate bundle stays, for networks that inspect TLS.
	for _, want := range []string{"AWS_ACCESS_KEY_ID=AKIACHECKTEST0000000", "AWS_CONFIG_FILE=", "AWS_REGION=us-east-1", "AWS_CA_BUNDLE=/etc/ssl/corp.pem"} {
		if !strings.Contains(string(env), want) {
			t.Fatalf("missing %s in:\n%s", want, env)
		}
	}
	for _, unwanted := range []string{"AWS_PROFILE", "AWS_ENDPOINT_URL"} {
		if strings.Contains(string(env), unwanted) {
			t.Fatalf("%s reached the check:\n%s", unwanted, env)
		}
	}

	// Recorded with the account, and shown by the Cloud view.
	st, _ = a.InfraState()
	if acct := st.Accounts[0]; acct.Account != "123456789012" || acct.Identity != id.Identity || acct.Checked == "" {
		t.Fatalf("%+v", acct)
	}
	// Changing the region keeps it; a new key has to be checked again.
	if _, err := SaveSecret(config, SecretInput{Name: "aws-test", Kind: SecretAWS, Public: map[string]string{"region": "eu-west-1"}}); err != nil {
		t.Fatal(err)
	}
	if st, _ = a.InfraState(); st.Accounts[0].Checked == "" || st.Accounts[0].Region != "eu-west-1" {
		t.Fatalf("a region change lost the check: %+v", st.Accounts[0])
	}
	if _, err := SaveSecret(config, SecretInput{Name: "aws-test", Kind: SecretAWS, Values: map[string]string{"secret_access_key": "check/secret/key/1111111111111111111111"}, Public: map[string]string{"region": "eu-west-1"}}); err != nil {
		t.Fatal(err)
	}
	if st, _ = a.InfraState(); st.Accounts[0].Checked != "" || st.Accounts[0].Account != "" || st.Accounts[0].Identity != "" {
		t.Fatalf("a new key kept the old check: %+v", st.Accounts[0])
	}

	// What went wrong, in words, and never the key.
	for mode, want := range map[string]string{
		"badkey":    "does not recognize this access key ID",
		"badsecret": "does not belong to this access key ID",
		"leak":      "could not sign in with",
	} {
		setMode(t, dir, mode)
		_, err := a.CheckCloudAccount(context.Background(), "aws-test")
		if err == nil || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "check/secret/key") {
			t.Fatalf("%s: %v", mode, err)
		}
	}
	if st, _ = a.InfraState(); st.Accounts[0].Checked != "" {
		t.Fatal("a failed check was recorded as signed in")
	}
	// A key AWS refuses no longer shows as checked; a check that could not
	// finish leaves the last answer alone.
	setMode(t, dir, "")
	if _, err := a.CheckCloudAccount(context.Background(), "aws-test"); err != nil {
		t.Fatal(err)
	}
	setMode(t, dir, "leak")
	_, _ = a.CheckCloudAccount(context.Background(), "aws-test")
	if st, _ = a.InfraState(); st.Accounts[0].Checked == "" {
		t.Fatal("a check that could not finish forgot the last answer")
	}
	setMode(t, dir, "badkey")
	_, _ = a.CheckCloudAccount(context.Background(), "aws-test")
	if st, _ = a.InfraState(); st.Accounts[0].Checked != "" || st.Accounts[0].Account != "" {
		t.Fatalf("a refused key still shows as signing in: %+v", st.Accounts[0])
	}

	// Neither a wrong clock nor a lost connection is the key's fault.
	setMode(t, dir, "")
	if _, err := a.CheckCloudAccount(context.Background(), "aws-test"); err != nil {
		t.Fatal(err)
	}
	for mode, want := range map[string]string{"skew": "clock is off", "offline": "could not reach AWS"} {
		setMode(t, dir, mode)
		if _, err := a.CheckCloudAccount(context.Background(), "aws-test"); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: %v", mode, err)
		}
		if st, _ = a.InfraState(); st.Accounts[0].Checked == "" {
			t.Fatalf("%s forgot the last check", mode)
		}
	}

	// A region the account has not turned on is told apart from a bad key,
	// and what it found is kept with the account until a clean check.
	setMode(t, dir, "optin")
	id, err = a.CheckCloudAccount(context.Background(), "aws-test")
	if err != nil || !strings.Contains(id.Note, "not in eu-west-1") || id.Account != "123456789012" {
		t.Fatalf("%v %+v", err, id)
	}
	if st, _ = a.InfraState(); !strings.Contains(st.Accounts[0].Note, "not in eu-west-1") || st.Accounts[0].Checked == "" {
		t.Fatalf("%+v", st.Accounts[0])
	}
	setMode(t, dir, "")
	if _, err := a.CheckCloudAccount(context.Background(), "aws-test"); err != nil {
		t.Fatal(err)
	}
	if st, _ = a.InfraState(); st.Accounts[0].Note != "" {
		t.Fatalf("a clean check kept the note: %+v", st.Accounts[0])
	}

	// A temporary key without its session token is told so.
	if _, err := SaveSecret(config, SecretInput{Name: "aws-temp", Kind: SecretAWS, Values: map[string]string{"access_key_id": "ASIATEMPCHECK0000000", "secret_access_key": "temp/secret/key/0000000000000000000000"}}); err != nil {
		t.Fatal(err)
	}
	setMode(t, dir, "badkey")
	if _, err := a.CheckCloudAccount(context.Background(), "aws-temp"); err == nil || !strings.Contains(err.Error(), "session token") {
		t.Fatalf("%v", err)
	}
	// Who an account signs in as stays out of secrets_list until agents
	// may use it.
	setMode(t, dir, "")
	if _, err := a.CheckCloudAccount(context.Background(), "aws-test"); err != nil {
		t.Fatal(err)
	}
	listed := ""
	for _, tool := range BuiltinTools(a.Config(), a) {
		if tool.Definition().Name == "secrets_list" {
			listed, _ = tool.Call(context.Background(), nil)
		}
	}
	if listed == "" || strings.Contains(listed, "123456789012") || !strings.Contains(listed, "aws-test") {
		t.Fatalf("secrets_list:\n%s", listed)
	}

	// Only cloud accounts can be checked.
	if _, err := SaveSecret(config, SecretInput{Name: "gh", Kind: SecretGitHubToken, Values: map[string]string{"token": "ghp_check000000000000"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CheckCloudAccount(context.Background(), "gh"); err == nil || !strings.Contains(err.Error(), "not an AWS or Google Cloud account") {
		t.Fatalf("%v", err)
	}
	if _, err := a.CheckCloudAccount(context.Background(), "nope"); err == nil {
		t.Fatal("checked a secret that does not exist")
	}
}

func TestCheckAWSAccountWithoutTheCLI(t *testing.T) {
	t.Setenv("NULLBOT_AWS_BIN", "")
	t.Setenv("PATH", t.TempDir())
	a := New(projectTestConfig(t, "read-only"))
	if _, err := SaveSecret(a.Config(), SecretInput{Name: "aws-test", Kind: SecretAWS, Values: map[string]string{"access_key_id": "AKIACHECKTEST0000000", "secret_access_key": "check/secret/key/0000000000000000000000"}}); err != nil {
		t.Fatal(err)
	}
	_, err := a.CheckCloudAccount(context.Background(), "aws-test")
	if err == nil || !strings.Contains(err.Error(), "install the AWS CLI") {
		t.Fatalf("%v", err)
	}
}

const testGCPKey = `{"type":"service_account","project_id":"key-project","client_email":"nullbot@key-project.iam.gserviceaccount.com","private_key":"-----BEGIN PRIVATE KEY-----\nMIItest\n-----END PRIVATE KEY-----\n"}`

func TestCheckGCPAccount(t *testing.T) {
	dir := fakeCloudCLI(t, "gcloud", `
case "$MODE" in
  denied) echo "ERROR: (gcloud.compute.project-info.describe) Could not fetch resource:" >&2; echo " - Required 'compute.projects.get' permission for 'projects/other-project'" >&2; exit 1 ;;
  disabled) echo "ERROR: (gcloud.compute.project-info.describe) Could not fetch resource:" >&2; echo " - Compute Engine API has not been used in project 123 before or it is disabled." >&2; exit 1 ;;
  revoked) echo "ERROR: (gcloud.compute.project-info.describe) There was a problem refreshing your current auth tokens: ('invalid_grant: Invalid JWT Signature.')" >&2; exit 1 ;;
  tls) echo "ERROR: (gcloud.compute.project-info.describe) There was a problem refreshing your current auth tokens: HTTPSConnectionPool(host='oauth2.googleapis.com', port=443): Max retries exceeded (Caused by SSLError(SSLCertVerificationError(1, '[SSL: CERTIFICATE_VERIFY_FAILED]')))" >&2; exit 1 ;;
  skew) echo "ERROR: (gcloud.compute.project-info.describe) There was a problem refreshing your current auth tokens: ('invalid_grant: Invalid JWT: Token must be a short-lived token (60 minutes) and in a reasonable timeframe. Check your iat and exp values in the JWT claim.')" >&2; exit 1 ;;
esac
echo '{"name":"other-project"}'
`)
	t.Setenv("CLOUDSDK_CORE_ACCOUNT", "me@example.com")
	a := New(projectTestConfig(t, "read-only"))
	config := a.Config()
	// The project set on the account wins over the key file's.
	if _, err := SaveSecret(config, SecretInput{Name: "gcp-lab", Kind: SecretGCP, Values: map[string]string{"credentials_json": testGCPKey}, Public: map[string]string{"project_id": "other-project"}}); err != nil {
		t.Fatal(err)
	}
	id, err := a.CheckCloudAccount(context.Background(), "gcp-lab")
	if err != nil || id.Note != "" {
		t.Fatalf("%v %+v", err, id)
	}
	if id.Project != "other-project" || id.Identity != "nullbot@key-project.iam.gserviceaccount.com" || id.Cloud != "gcp" {
		t.Fatalf("%+v", id)
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
	if !strings.Contains(string(calls), "compute project-info describe --project other-project") {
		t.Fatalf("calls: %s", calls)
	}
	env, _ := os.ReadFile(filepath.Join(dir, "env"))
	if !strings.Contains(string(env), "CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE=") || !strings.Contains(string(env), "CLOUDSDK_CORE_PROJECT=other-project") || strings.Contains(string(env), "CLOUDSDK_CORE_ACCOUNT") {
		t.Fatalf("environment:\n%s", env)
	}
	st, _ := a.InfraState()
	if acct := st.Accounts[0]; acct.Project != "other-project" || acct.Identity == "" || acct.Checked == "" {
		t.Fatalf("%+v", acct)
	}

	if !strings.Contains(string(calls), "--format json") {
		t.Fatalf("calls: %s", calls)
	}
	// Signed in but not yet able to make machines: a note, kept with the
	// account.
	setMode(t, dir, "denied")
	if id, err = a.CheckCloudAccount(context.Background(), "gcp-lab"); err != nil || !strings.Contains(id.Note, "Compute Admin") {
		t.Fatalf("%v %+v", err, id)
	}
	if st, _ = a.InfraState(); !strings.Contains(st.Accounts[0].Note, "Compute Admin") {
		t.Fatalf("%+v", st.Accounts[0])
	}
	setMode(t, dir, "disabled")
	if id, err = a.CheckCloudAccount(context.Background(), "gcp-lab"); err != nil || !strings.Contains(id.Note, "Compute Engine API is off") {
		t.Fatalf("%v %+v", err, id)
	}
	// A network that inspects TLS, or a wrong clock, is not a refused key.
	setMode(t, dir, "tls")
	if _, err = a.CheckCloudAccount(context.Background(), "gcp-lab"); err == nil || !strings.Contains(err.Error(), "could not reach Google Cloud") {
		t.Fatalf("%v", err)
	}
	setMode(t, dir, "skew")
	if _, err = a.CheckCloudAccount(context.Background(), "gcp-lab"); err == nil || !strings.Contains(err.Error(), "clock is off") {
		t.Fatalf("%v", err)
	}
	if st, _ = a.InfraState(); st.Accounts[0].Checked == "" {
		t.Fatal("a check that could not finish forgot the last answer")
	}
	// A key Google no longer accepts is an error, and no longer checked.
	setMode(t, dir, "revoked")
	if _, err = a.CheckCloudAccount(context.Background(), "gcp-lab"); err == nil || !strings.Contains(err.Error(), "did not accept this key") {
		t.Fatalf("%v", err)
	}
	if st, _ = a.InfraState(); st.Accounts[0].Checked != "" || st.Accounts[0].Identity == "" {
		t.Fatalf("%+v", st.Accounts[0])
	}

	// A new project means checking again.
	setMode(t, dir, "")
	if _, err := a.CheckCloudAccount(context.Background(), "gcp-lab"); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveSecret(config, SecretInput{Name: "gcp-lab", Kind: SecretGCP, Public: map[string]string{"project_id": "third-project"}}); err != nil {
		t.Fatal(err)
	}
	if st, _ = a.InfraState(); st.Accounts[0].Checked != "" || st.Accounts[0].Note != "" {
		t.Fatalf("a project change kept the check: %+v", st.Accounts[0])
	}
	// A project that is not a project id is refused before gcloud runs.
	if _, err := SaveSecret(config, SecretInput{Name: "gcp-lab", Kind: SecretGCP, Public: map[string]string{"project_id": "Bad Project&"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CheckCloudAccount(context.Background(), "gcp-lab"); err == nil || !strings.Contains(err.Error(), "not a Google Cloud project id") {
		t.Fatalf("%v", err)
	}
}

func TestReplacingAGCPKeyFollowsItsProject(t *testing.T) {
	config := projectTestConfig(t, "read-only")
	keyFor := func(project string) string { return strings.Replace(testGCPKey, `"key-project"`, `"`+project+`"`, 1) }
	if _, err := SaveSecret(config, SecretInput{Name: "gcp-lab", Kind: SecretGCP, Values: map[string]string{"credentials_json": testGCPKey}}); err != nil {
		t.Fatal(err)
	}
	// The editor sends back the project it showed, taken from the old file.
	info, err := SaveSecret(config, SecretInput{Name: "gcp-lab", Kind: SecretGCP, Values: map[string]string{"credentials_json": keyFor("next-project")}, Public: map[string]string{"project_id": "key-project"}})
	if err != nil || info.Public["project_id"] != "next-project" {
		t.Fatalf("%v %v", err, info.Public)
	}
	// A project set apart from the key file stays.
	if _, err := SaveSecret(config, SecretInput{Name: "gcp-lab", Kind: SecretGCP, Public: map[string]string{"project_id": "shared-project"}}); err != nil {
		t.Fatal(err)
	}
	info, err = SaveSecret(config, SecretInput{Name: "gcp-lab", Kind: SecretGCP, Values: map[string]string{"credentials_json": keyFor("third-project")}, Public: map[string]string{"project_id": "shared-project"}})
	if err != nil || info.Public["project_id"] != "shared-project" {
		t.Fatalf("%v %v", err, info.Public)
	}
}

func TestGCPPlansUseTheAccountsProject(t *testing.T) {
	fakeCloudCLI(t, "gcloud", "echo '{}'\n")
	a := New(projectTestConfig(t, "read-only"))
	if _, err := SaveSecret(a.Config(), SecretInput{Name: "gcp-lab", Kind: SecretGCP, Values: map[string]string{"credentials_json": testGCPKey}, Public: map[string]string{"project_id": "other-project"}}); err != nil {
		t.Fatal(err)
	}
	plan, err := a.PlanInfra(context.Background(), InfraRequest{Spec: infra.Spec{Cloud: infra.GCP, Credential: "gcp-lab", Buckets: []infra.BucketSpec{{Name: "acme-gcp-check"}}}}, "you")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Project != "other-project" {
		t.Fatalf("plan project %q", plan.Project)
	}
}
