package app

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// Exercise the production history call site, not only the loader in isolation.
func TestHistoryLoadsAttachmentWithActiveProjectConfig(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "history.png")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(file, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	state := &App{config: Config{WorkspaceDir: root, Projects: []Project{{ID: "test", Path: root, Permission: "read-only"}}}}
	state.history = []Message{{Role: "user", Content: `inspect @file("` + path + `")`}}
	messages := state.langChainHistory()
	if len(messages) != 1 || len(messages[0].Content.Parts) != 2 || messages[0].Content.Parts[1].Type != "image" {
		t.Fatalf("history failed to load authorized image: %#v", messages)
	}
	state.config.Projects = []Project{{ID: "other", Path: t.TempDir(), Permission: "read-only"}}
	messages = state.langChainHistory()
	for _, part := range messages[0].Content.Parts {
		if part.Type == "image" {
			t.Fatal("history replay bypassed current project permission")
		}
	}
}
