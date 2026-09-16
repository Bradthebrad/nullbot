//go:build windows

package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func makeProjectTestJunction(t *testing.T, link, target string) {
	t.Helper()
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	powershell := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	out, err := exec.Command(powershell, "-NoProfile", "-NonInteractive", "-Command",
		"$ErrorActionPreference = 'Stop'; New-Item -ItemType Junction -Path "+quote(link)+" -Target "+quote(target)+" | Out-Null").CombinedOutput()
	if err != nil {
		t.Fatalf("create junction: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		if err := os.Remove(link); err != nil {
			t.Errorf("remove junction: %v", err)
		}
	})
}

func TestProjectJunctionEscape(t *testing.T) {
	c := projectTestConfig(t, "read-write")
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "existing.txt"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(outside, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(c.WorkspaceDir, "junction")
	makeProjectTestJunction(t, link, outside)
	for _, leaf := range []string{"", "existing.txt", "new.txt", filepath.Join("missing", "new.txt")} {
		t.Run("target/"+leaf, func(t *testing.T) {
			path := filepath.Join(link, leaf)
			if _, err := canonicalPath(path); err == nil || !strings.Contains(err.Error(), "reparse point") {
				t.Fatalf("canonicalPath(%q) should reject junction, got %v", path, err)
			}
			for _, write := range []bool{false, true} {
				if _, err := CheckProjectPath(c, path, write, false); err == nil {
					t.Fatalf("junction escape permitted (write=%v)", write)
				}
			}
		})
	}
	// A configured root must not silently grant access through a junction, even
	// when the target being checked is an ordinary path in another project.
	for _, leaf := range []string{"", "nested", filepath.Join("missing", "root")} {
		t.Run("configured-root/"+leaf, func(t *testing.T) {
			cfg := c
			cfg.Projects = append([]Project(nil), c.Projects...)
			cfg.Projects = append(cfg.Projects, Project{ID: "junction", Path: filepath.Join(link, leaf), Permission: "read-write"})
			if _, err := CheckProjectPath(cfg, filepath.Join(c.WorkspaceDir, "ordinary.txt"), true, false); err == nil || !strings.Contains(err.Error(), "reparse point") {
				t.Fatalf("junction configured root accepted: %v", err)
			}
		})
	}
	// The fail-closed policy also rejects reparse points whose target is inside.
	internal := filepath.Join(c.WorkspaceDir, "internal")
	if err := os.Mkdir(internal, 0700); err != nil {
		t.Fatal(err)
	}
	internalLink := filepath.Join(c.WorkspaceDir, "internal-junction")
	makeProjectTestJunction(t, internalLink, internal)
	if _, err := CheckProjectPath(c, filepath.Join(internalLink, "new.txt"), true, false); err == nil {
		t.Fatal("internal junction accepted")
	}
	if _, err := CheckProjectPath(c, filepath.Join("ordinary", "missing", "new.txt"), true, false); err != nil {
		t.Fatalf("ordinary nonexistent leaves rejected: %v", err)
	}
}
