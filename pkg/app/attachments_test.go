package app

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeAttachmentTestPNG(t *testing.T, path string) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestAttachmentProjectReadAccess(t *testing.T) {
	for _, permission := range []string{"read-only", "read-write", "full"} {
		t.Run(permission, func(t *testing.T) {
			c := projectTestConfig(t, permission)
			staged := filepath.Join(c.WorkspaceDir, ".nullbot", "attachments", "paste")
			if err := os.MkdirAll(staged, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(staged, "clipboard.PNG")
			data := writeAttachmentTestPNG(t, path)
			rel, err := filepath.Rel(c.WorkspaceDir, path)
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{path, rel} {
				part, err := attachmentContentPart(path, c)
				if err != nil || part.Source == nil || part.Source.Data != base64.StdEncoding.EncodeToString(data) {
					t.Fatalf("staged image %q: part=%#v err=%v", path, part, err)
				}
			}
			outside := filepath.Join(t.TempDir(), "private.png")
			writeAttachmentTestPNG(t, outside)
			part, err := attachmentContentPart(outside, c)
			if err == nil || !strings.Contains(err.Error(), "outside configured projects") || part.Source != nil {
				t.Fatalf("outside image: part=%#v err=%v", part, err)
			}
			c.Projects = append(c.Projects, Project{ID: "second", Path: filepath.Dir(outside), Permission: "read-only"})
			if _, err := attachmentContentPart(outside, c); err != nil {
				t.Fatalf("secondary project read denied: %v", err)
			}
			c.Projects[1].Permission = "invalid"
			if _, err := attachmentContentPart(outside, c); err == nil {
				t.Fatal("invalid project permission allowed")
			}
		})
	}
}

func TestAttachmentMissingConfigurationFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "image.png")
	writeAttachmentTestPNG(t, path)
	for _, configs := range [][]Config{nil, {{}}} {
		part, err := attachmentContentPart(path, configs...)
		if err == nil || part.Source != nil {
			t.Fatalf("configuration-free read: part=%#v err=%v", part, err)
		}
	}
}

func TestAttachmentImageErrors(t *testing.T) {
	c := projectTestConfig(t, "read-only")
	valid := writeAttachmentTestPNG(t, filepath.Join(c.WorkspaceDir, "valid.png"))
	for _, tc := range []struct {
		name string
		data []byte
		want string
	}{
		{"empty.png", nil, "empty"},
		{"fake.png", []byte("fake-png-bytes"), "invalid or unreadable"},
		{"truncated.png", valid[:len(valid)-15], "invalid or unreadable"},
		{"wrong-extension.jpg", valid, "does not match"},
		{"fake.webp", []byte("RIFF\x00\x00\x00\x00WEBP"), "invalid WebP"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(c.WorkspaceDir, tc.name)
			if err := os.WriteFile(path, tc.data, 0600); err != nil {
				t.Fatal(err)
			}
			part, err := attachmentContentPart(path, c)
			if err == nil || !strings.Contains(err.Error(), tc.want) || part.Source != nil {
				t.Fatalf("part=%#v err=%v, want %q", part, err, tc.want)
			}
		})
	}
	if _, err := attachmentContentPart("missing.png", c); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing image error: %v", err)
	}
	if err := os.Mkdir(filepath.Join(c.WorkspaceDir, "directory.png"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := attachmentContentPart("directory.png", c); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("directory image error: %v", err)
	}
	// A sparse oversized file is rejected by stat without a full allocation/read.
	f, err := os.Create(filepath.Join(c.WorkspaceDir, "large.png"))
	if err != nil {
		t.Fatal(err)
	}
	err = f.Truncate(maxAttachmentImageBytes + 1)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("prepare oversized image: %v %v", err, closeErr)
	}
	if _, err := attachmentContentPart("large.png", c); err == nil || !strings.Contains(err.Error(), "20 MiB") {
		t.Fatalf("oversized image error: %v", err)
	}
}

type attachmentCountingReader struct{ remaining, read int64 }

func (r *attachmentCountingReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := int64(len(p))
	if n > r.remaining {
		n = r.remaining
	}
	clear(p[:int(n)])
	r.remaining -= n
	r.read += n
	return int(n), nil
}

type attachmentErrorReader struct{}

func (attachmentErrorReader) Read([]byte) (int, error) { return 0, os.ErrPermission }

func TestAttachmentBoundedRead(t *testing.T) {
	for _, size := range []int64{maxAttachmentImageBytes, maxAttachmentImageBytes + 1000} {
		r := &attachmentCountingReader{remaining: size}
		data, err := readAttachmentImage(r)
		if size == maxAttachmentImageBytes {
			if err != nil || int64(len(data)) != size {
				t.Fatalf("exact limit rejected: len=%d err=%v", len(data), err)
			}
		} else if err == nil || !strings.Contains(err.Error(), "20 MiB") || data != nil {
			t.Fatalf("overflow not rejected: err=%v", err)
		}
		if r.read > maxAttachmentImageBytes+1 {
			t.Fatalf("unbounded read: %d", r.read)
		}
	}
	if _, err := readAttachmentImage(attachmentErrorReader{}); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("read error swallowed: %v", err)
	}
}

func TestAttachmentSupportedImageValidation(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for _, format := range []string{"png", "jpeg", "gif"} {
		t.Run(format, func(t *testing.T) {
			var b bytes.Buffer
			var err error
			switch format {
			case "png":
				err = png.Encode(&b, img)
			case "jpeg":
				err = jpeg.Encode(&b, img, nil)
			case "gif":
				err = gif.Encode(&b, img, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := validateAttachmentImage(b.Bytes(), "image/"+format); err != nil {
				t.Fatal(err)
			}
		})
	}
	// A 1x1 lossless WebP fixture. Its RIFF framing and VP8L dimensions must
	// remain supported without a new image-decoder dependency.
	webp, err := base64.StdEncoding.DecodeString("UklGRhoAAABXRUJQVlA4TA0AAAAvAAAAEAcQERGIiP4HAA==")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateAttachmentImage(webp, "image/webp"); err != nil {
		t.Fatalf("WebP: %v", err)
	}
	if err := validateAttachmentImage(webp[:len(webp)-1], "image/webp"); err == nil {
		t.Fatal("truncated WebP accepted")
	}
	if err := attachmentImageDimensions(100000, 100000); err == nil {
		t.Fatal("excessive decoded dimensions accepted")
	}
}

func TestHumanMessageAttachmentFailuresAreExplicit(t *testing.T) {
	c := projectTestConfig(t, "read-only")
	writeAttachmentTestPNG(t, filepath.Join(c.WorkspaceDir, "good.png"))
	msg := humanMessageWithAttachments(`inspect @file("good.png") @file("missing.png") @file("notes.txt")`, c)
	if len(msg.Content.Parts) != 2 || msg.Content.Parts[1].Type != "image" {
		t.Fatalf("parts=%#v", msg.Content.Parts)
	}
	text := lcContentText(msg.Content)
	for _, want := range []string{"Attached file: good.png.", `Attachment "missing.png" could not be loaded`, `@file("notes.txt")`, "Do not claim"} {
		if !strings.Contains(text, want) {
			t.Fatalf("text %q missing %q", text, want)
		}
	}
	if strings.Contains(text, "Attached files:") || strings.Contains(text, `@file("missing.png")`) {
		t.Fatalf("failed image advertised: %q", text)
	}
	msg = humanMessageWithAttachments(`@file("missing.png")`, c)
	if text := lcContentText(msg.Content); !strings.Contains(text, "could not be loaded") || strings.Contains(text, "Attached file:") {
		t.Fatalf("failure-only message: %q", text)
	}
	for _, text := range []string{"hello", `@file("notes.txt")`, `@file(" ")`} {
		if got := lcContentText(humanMessageWithAttachments(text, c).Content); got != text {
			t.Fatalf("plain/non-image text changed: %q -> %q", text, got)
		}
	}
}

func TestAttachmentSymlinkEscape(t *testing.T) {
	c := projectTestConfig(t, "read-only")
	outside := filepath.Join(t.TempDir(), "private.png")
	writeAttachmentTestPNG(t, outside)
	link := filepath.Join(c.WorkspaceDir, "link.png")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if part, err := attachmentContentPart(link, c); err == nil || part.Source != nil {
		t.Fatalf("symlink escaped project: part=%#v err=%v", part, err)
	}
}
