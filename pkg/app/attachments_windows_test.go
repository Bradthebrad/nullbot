//go:build windows

package app

import (
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestAttachmentUnreadableWindowsFile(t *testing.T) {
	c := projectTestConfig(t, "read-only")
	path := filepath.Join(c.WorkspaceDir, "locked.png")
	writeAttachmentTestPNG(t, path)
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows chmod cannot emulate denied reads. An exclusive file handle gives
	// a deterministic OS open failure without changing the user's ACLs.
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	part, err := attachmentContentPart(path, c)
	if err == nil || part.Source != nil {
		t.Fatalf("unreadable attachment: part=%#v err=%v", part, err)
	}
	msg := humanMessageWithAttachments(`@file("locked.png")`, c)
	if text := lcContentText(msg.Content); !strings.Contains(text, "could not be loaded") {
		t.Fatalf("unreadable file silently ignored: %q", text)
	}
}

func TestAttachmentJunctionEscape(t *testing.T) {
	c := projectTestConfig(t, "read-only")
	outside := t.TempDir()
	writeAttachmentTestPNG(t, filepath.Join(outside, "private.png"))
	link := filepath.Join(c.WorkspaceDir, "junction")
	makeProjectTestJunction(t, link, outside)
	if part, err := attachmentContentPart(filepath.Join(link, "private.png"), c); err == nil || !strings.Contains(err.Error(), "reparse point") || part.Source != nil {
		t.Fatalf("junction attachment: part=%#v err=%v", part, err)
	}
	// Explicit attachment staging is not an exemption for a redirected directory.
	stagedLink := filepath.Join(c.WorkspaceDir, ".nullbot")
	makeProjectTestJunction(t, stagedLink, outside)
	if _, err := attachmentContentPart(filepath.Join(stagedLink, "private.png"), c); err == nil {
		t.Fatal("staged junction bypassed project policy")
	}
}
