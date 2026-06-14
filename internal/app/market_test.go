package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMarketManifestSeedsOfficialPackages(t *testing.T) {
	config := testMarketConfig(t)
	manifest, err := LoadMarketManifest(config)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Packages) < 5 {
		t.Fatalf("packages = %#v", manifest.Packages)
	}
	for _, id := range []string{"nullbot-code-mcp", "nullbot-parsers-mcp", "nullbot-imagetools-mcp", "api-probe", "mcp-skill"} {
		if _, _, err := findMarketPackage(manifest, id); err != nil {
			t.Fatalf("missing package %s: %v", id, err)
		}
	}
}

func TestInstallSkillPackageFromMarket(t *testing.T) {
	config := testMarketConfig(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("---\nname: test-skill\ndescription: Test.\n---\n\n# Test"))
	}))
	defer server.Close()

	manifest := defaultMarketManifest(config)
	manifest.Packages = []MarketPackage{{
		ID:          "test-skill",
		Kind:        "skill_pack",
		Name:        "Test Skill",
		Description: "Installable test skill.",
		Status:      "available",
		SkillFiles:  []MarketSkillFile{{Name: "test-skill", URL: server.URL + "/SKILL.md"}},
	}}
	if err := SaveMarketManifest(config, manifest); err != nil {
		t.Fatal(err)
	}

	pkg, err := InstallMarketPackage(context.Background(), config, "test-skill", false)
	if err != nil {
		t.Fatal(err)
	}
	if !pkg.Installed {
		t.Fatalf("pkg = %#v", pkg)
	}
	if _, err := os.Stat(filepath.Join(config.AppDir, "skills", "test-skill", "SKILL.md")); err != nil {
		t.Fatalf("skill not installed: %v", err)
	}
}

func TestEnableDisableRemoveMCPServer(t *testing.T) {
	config := testMarketConfig(t)
	dir := filepath.Join(config.AppDir, "mcp", "fake-mcp")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "fake-mcp.exe")
	if err := os.WriteFile(exe, []byte("fake"), 0600); err != nil {
		t.Fatal(err)
	}
	manifest := defaultMarketManifest(config)
	manifest.Packages = []MarketPackage{{
		ID:               "fake-mcp",
		Kind:             "mcp_server",
		Name:             "Fake MCP",
		Description:      "Fake.",
		DefaultTransport: "stdio",
		Installed:        true,
		InstallDir:       dir,
		InstalledAsset:   "fake-mcp.exe",
		Status:           "installed",
	}}
	if err := SaveMarketManifest(config, manifest); err != nil {
		t.Fatal(err)
	}
	config, err := EnableMCPServer(config, "fake-mcp")
	if err != nil {
		t.Fatal(err)
	}
	if !config.EnabledMCPServers["fake-mcp"].Enabled {
		t.Fatalf("enabled servers = %#v", config.EnabledMCPServers)
	}
	config, err = DisableMCPServer(config, "fake-mcp")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := config.EnabledMCPServers["fake-mcp"]; ok {
		t.Fatalf("server should be disabled: %#v", config.EnabledMCPServers)
	}
	config, err = RemoveMCPServer(config, "fake-mcp")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("mcp dir should be removed, err = %v", err)
	}
}

func testMarketConfig(t *testing.T) Config {
	t.Helper()
	config := DefaultConfig()
	config.AppDir = t.TempDir()
	config.SkillDirs = []string{filepath.Join(config.AppDir, "skills")}
	if err := EnsureAppDir(config); err != nil {
		t.Fatal(err)
	}
	return config
}
