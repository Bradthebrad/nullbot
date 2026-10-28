package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Bradthebrad/tinychain/agent"
)

func TestAssetsFromReleaseReadsPlatforms(t *testing.T) {
	release := githubRelease{TagName: "v0.3.0", Assets: []githubAsset{
		{Name: "nullbot-web-mcp.exe"}, {Name: "nullbot-web-mcp-small.exe"},
		{Name: "nullbot-web-mcp-linux-amd64"}, {Name: "nullbot-web-mcp-linux-arm64"},
		{Name: "nullbot-web-mcp-darwin-arm64"}, {Name: "nullbot-web-mcp-windows-arm64.exe"},
		{Name: "SHA256SUMS.txt"}, {Name: "README.md"},
	}}
	got := map[string]string{}
	for _, a := range assetsFromRelease(release, "nullbot-web-mcp") {
		got[a.Name] = a.Platform + "/" + a.Arch
	}
	want := map[string]string{
		"nullbot-web-mcp.exe": "windows/amd64", "nullbot-web-mcp-small.exe": "windows/amd64",
		"nullbot-web-mcp-linux-amd64": "linux/amd64", "nullbot-web-mcp-linux-arm64": "linux/arm64",
		"nullbot-web-mcp-darwin-arm64": "darwin/arm64", "nullbot-web-mcp-windows-arm64.exe": "windows/arm64",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("assets = %v, want %v", got, want)
	}
}

func TestInstallUsesTheLatestReleaseAndItsChecksum(t *testing.T) {
	config := testMarketConfig(t)
	binary := []byte("pretend server build")
	sum := sha256.Sum256(binary)
	name := "nullbot-emails-mcp.exe"
	if runtime.GOOS != "windows" {
		name = "nullbot-emails-mcp-" + runtime.GOOS + "-" + runtime.GOARCH
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(binary)
	}))
	defer server.Close()
	calls := 0
	old := latestRelease
	latestRelease = func(ctx context.Context, repo string) (githubRelease, error) {
		calls++
		if repo != "Bradthebrad/nullbot-emails-mcp" {
			return githubRelease{}, fmt.Errorf("unexpected repo %s", repo)
		}
		return githubRelease{TagName: "v0.1.0", Assets: []githubAsset{{Name: name, BrowserDownloadURL: server.URL + "/" + name}},
			Checksums: map[string]string{name: hex.EncodeToString(sum[:])}}, nil
	}
	defer func() { latestRelease = old }()

	// The catalog still lists the server as unreleased; Install looks it up anyway.
	pkg, err := InstallMarketPackage(context.Background(), config, "nullbot-emails-mcp", false)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || pkg.ReleaseTag != "v0.1.0" || pkg.InstalledAsset != name || pkg.InstalledSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("installed %#v after %d release lookups", pkg, calls)
	}
	data, err := os.ReadFile(filepath.Join(config.AppDir, "mcp", "nullbot-emails-mcp", name))
	if err != nil || string(data) != string(binary) {
		t.Fatalf("installed file: %q, %v", data, err)
	}

	// Installing again replaces the build in place.
	binary = []byte("a newer build")
	sum = sha256.Sum256(binary)
	if _, err := InstallMarketPackage(context.Background(), config, "nullbot-emails-mcp", false); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(config.AppDir, "mcp", "nullbot-emails-mcp", name)); string(data) != "a newer build" {
		t.Fatalf("reinstall left %q", data)
	}
}

func TestInstallWithoutAReleaseSaysSo(t *testing.T) {
	config := testMarketConfig(t)
	old := latestRelease
	latestRelease = func(ctx context.Context, repo string) (githubRelease, error) {
		return githubRelease{}, fmt.Errorf("github release %s: 404 Not Found", repo)
	}
	defer func() { latestRelease = old }()
	_, err := InstallMarketPackage(context.Background(), config, "nullbot-drives-mcp", false)
	if err == nil || !strings.Contains(err.Error(), "has no release on GitHub yet") {
		t.Fatalf("err = %v", err)
	}
}

func TestOfficialServerToolsAreClassifiedByWhatTheyDo(t *testing.T) {
	for _, tc := range []struct {
		server, tool string
		want         Capability
	}{
		{"nullbot-drives-mcp", "drives_download", CapRead},
		{"nullbot-parsers-mcp", "detect_file_type", CapRead},
		{"nullbot-parsers-mcp", "parse_archive", CapRead},
		{"nullbot-code-mcp", "command_status", CapRead},
		{"nullbot-code-mcp", "run_command", CapExecute},
		{"nullbot-web-mcp", "browser_screenshot", CapWrite},
		{"nullbot-emails-mcp", "email_send", CapWrite},
		{"someone-elses-server", "drives_download", CapWrite}, // unknown servers still go by name
	} {
		if got := ClassifyTool(tc.tool, tc.server, agent.ToolInfo{}, "").Capability; got != tc.want {
			t.Errorf("%s/%s = %s, want %s", tc.server, tc.tool, got, tc.want)
		}
	}
	if got := ClassifyTool("drives_download", "nullbot-drives-mcp", agent.ToolInfo{}, "write"); got.Capability != CapWrite || got.Source != "override" {
		t.Errorf("an override still wins: %#v", got)
	}
}
