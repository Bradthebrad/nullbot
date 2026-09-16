package app

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Bradthebrad/tinychain/lc"
)

const maxAttachmentImageBytes int64 = 20 << 20

// Bound decoded allocations as well as compressed input. A tiny compressed file
// can otherwise cause image.Decode to allocate gigabytes.
const maxAttachmentImagePixels = 25_000_000

var attachmentTokenPattern = regexp.MustCompile(`@file\("([^"]+)"\)|@file\(([^)]+)\)`)

// Callers must supply the active config, including when replaying history. The
// optional argument preserves source compatibility but never grants access when
// omitted. Explicitly staged local files need no special bypass: staging under
// the workspace's .nullbot/attachments directory permits ordinary project reads.
func humanMessageWithAttachments(text string, configs ...Config) lc.BaseMessage {
	paths := attachmentPaths(text)
	if len(paths) == 0 {
		return lc.Human(text)
	}
	var loaded []string
	var images []lc.ContentPart
	var failures []string
	for _, path := range paths {
		part, err := attachmentContentPart(path, configs...)
		if err != nil {
			failures = append(failures, fmt.Sprintf("Attachment %q could not be loaded: %v", filepath.Base(path), err))
		} else if part.Type != "" {
			loaded = append(loaded, path)
			images = append(images, part)
		}
	}
	if len(images) == 0 && len(failures) == 0 {
		return lc.Human(text) // Non-image tokens retain their existing tool workflow.
	}
	// Only successfully loaded images are described as attached. Keep non-image
	// tokens for tools, but replace failed image tokens with explicit diagnostics.
	prompt := attachmentTokenPattern.ReplaceAllStringFunc(text, func(token string) string {
		tokenPaths := attachmentPaths(token)
		if len(tokenPaths) == 0 {
			return token
		}
		path := tokenPaths[0]
		if strings.HasPrefix(mediaTypeForPath(path), "image/") {
			return ""
		}
		return token
	})
	prompt = strings.TrimSpace(prompt)
	if len(loaded) > 0 {
		// Unlike attachmentPromptText, do not strip non-image tokens here.
		var names []string
		for _, path := range loaded {
			names = append(names, filepath.Base(path))
		}
		label := "Attached file: "
		if len(names) > 1 {
			label = "Attached files: "
		}
		if prompt != "" {
			prompt += "\n\n"
		}
		prompt += label + strings.Join(names, ", ") + "."
	}
	if len(failures) > 0 {
		if prompt != "" {
			prompt += "\n\n"
		}
		prompt += strings.Join(failures, "\n") + "\nDo not claim to have inspected images that could not be loaded."
	}
	if len(images) == 0 {
		return lc.Human(prompt)
	}
	parts := append([]lc.ContentPart{{Type: "text", Text: prompt}}, images...)
	return lc.BaseMessage{Type: lc.RoleHuman, Content: lc.PartsContent(parts...)}
}

func attachmentPromptText(text string, paths []string) string {
	clean := strings.TrimSpace(attachmentTokenPattern.ReplaceAllString(text, ""))
	var names []string
	for _, path := range paths {
		names = append(names, filepath.Base(path))
	}
	if len(names) == 0 {
		return text
	}
	attachmentLine := "Attached file"
	if len(names) > 1 {
		attachmentLine = "Attached files"
	}
	attachmentLine += ": " + strings.Join(names, ", ")
	if clean == "" {
		return attachmentLine + ". Please inspect the attachment."
	}
	return clean + "\n\n" + attachmentLine + "."
}

func attachmentPaths(text string) []string {
	matches := attachmentTokenPattern.FindAllStringSubmatch(text, -1)
	var paths []string
	for _, match := range matches {
		path := strings.TrimSpace(match[1])
		if path == "" && len(match) > 2 {
			path = strings.TrimSpace(match[2])
		}
		path = strings.Trim(path, "\"'")
		if path != "" {
			paths = append(paths, path)
		}
	}
	return paths
}

// An empty part with nil error means a non-image attachment, not a failed image.
func attachmentContentPart(path string, configs ...Config) (lc.ContentPart, error) {
	mediaType := mediaTypeForPath(path)
	if !strings.HasPrefix(mediaType, "image/") {
		return lc.ContentPart{}, nil
	}
	if len(configs) != 1 || (strings.TrimSpace(configs[0].WorkspaceDir) == "" && len(configs[0].Projects) == 0) {
		return lc.ContentPart{}, fmt.Errorf("project read access requires an active configuration")
	}
	resolved, err := CheckProjectPath(configs[0], path, false, false)
	if err != nil {
		return lc.ContentPart{}, fmt.Errorf("project read access denied: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return lc.ContentPart{}, fmt.Errorf("inspect image: %w", err)
	}
	if !info.Mode().IsRegular() {
		return lc.ContentPart{}, fmt.Errorf("image must be a regular file")
	}
	if info.Size() > maxAttachmentImageBytes {
		return lc.ContentPart{}, fmt.Errorf("image exceeds 20 MiB limit")
	}
	f, err := os.Open(resolved)
	if err != nil {
		return lc.ContentPart{}, fmt.Errorf("open image: %w", err)
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return lc.ContentPart{}, fmt.Errorf("inspect open image: %w", err)
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return lc.ContentPart{}, fmt.Errorf("image changed while opening")
	}
	data, err := readAttachmentImage(f)
	if err != nil {
		return lc.ContentPart{}, err
	}
	if err := validateAttachmentImage(data, mediaType); err != nil {
		return lc.ContentPart{}, fmt.Errorf("invalid or unreadable image: %w", err)
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	return lc.ContentPart{
		Type: "image",
		Source: &lc.ContentSource{
			Type:      "base64",
			MediaType: mediaType,
			Data:      encoded,
			URL:       "data:" + mediaType + ";base64," + encoded,
		},
	}, nil
}

func readAttachmentImage(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxAttachmentImageBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read image: %w", err)
	}
	if int64(len(data)) > maxAttachmentImageBytes {
		return nil, fmt.Errorf("image exceeds 20 MiB limit")
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("image is empty")
	}
	return data, nil
}

func validateAttachmentImage(data []byte, mediaType string) error {
	if mediaType == "image/webp" {
		return validateAttachmentWebP(data)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return err
	}
	if "image/"+format != mediaType {
		return fmt.Errorf("content does not match %s extension", mediaType)
	}
	if err := attachmentImageDimensions(cfg.Width, cfg.Height); err != nil {
		return err
	}
	// DecodeConfig alone accepts images with a valid header but truncated pixels.
	// GIF validation decodes the first frame, not every animation frame.
	_, _, err = image.Decode(bytes.NewReader(data))
	return err
}

func attachmentImageDimensions(width, height int) error {
	if width <= 0 || height <= 0 || int64(width)*int64(height) > maxAttachmentImagePixels {
		return fmt.Errorf("invalid dimensions or image exceeds %d pixel limit", maxAttachmentImagePixels)
	}
	return nil
}

// The existing module has no WebP decoder. Validate the RIFF container and
// image/frame headers without adding a dependency. This detects truncation and
// malformed containers, but does not validate compressed WebP pixel streams.
func validateAttachmentWebP(data []byte) error {
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" || uint64(binary.LittleEndian.Uint32(data[4:8]))+8 != uint64(len(data)) {
		return fmt.Errorf("invalid WebP RIFF container")
	}
	return validateAttachmentWebPChunks(data[12:], false)
}

func validateAttachmentWebPChunks(data []byte, frame bool) error {
	found := false
	for len(data) > 0 {
		if len(data) < 8 {
			return fmt.Errorf("truncated WebP chunk")
		}
		kind := string(data[:4])
		n := uint64(binary.LittleEndian.Uint32(data[4:8]))
		padded := n + n%2
		if padded > uint64(len(data)-8) {
			return fmt.Errorf("truncated WebP %s chunk", kind)
		}
		chunk := data[8 : 8+int(n)]
		data = data[8+int(padded):]
		var width, height int
		switch kind {
		case "VP8 ":
			if len(chunk) <= 10 || chunk[0]&1 != 0 || !bytes.Equal(chunk[3:6], []byte{0x9d, 0x01, 0x2a}) {
				return fmt.Errorf("invalid WebP VP8 header")
			}
			width = int(binary.LittleEndian.Uint16(chunk[6:8]) & 0x3fff)
			height = int(binary.LittleEndian.Uint16(chunk[8:10]) & 0x3fff)
			found = true
		case "VP8L":
			if len(chunk) <= 5 || chunk[0] != 0x2f || chunk[4]&0xe0 != 0 {
				return fmt.Errorf("invalid WebP VP8L header")
			}
			bits := binary.LittleEndian.Uint32(chunk[1:5])
			width, height = int(bits&0x3fff)+1, int((bits>>14)&0x3fff)+1
			found = true
		case "VP8X":
			if frame || len(chunk) != 10 {
				return fmt.Errorf("invalid WebP VP8X header")
			}
			width, height = webPUint24(chunk[4:7])+1, webPUint24(chunk[7:10])+1
		case "ANMF":
			if frame || len(chunk) < 16 {
				return fmt.Errorf("invalid WebP animation frame")
			}
			width, height = webPUint24(chunk[6:9])+1, webPUint24(chunk[9:12])+1
			if err := validateAttachmentWebPChunks(chunk[16:], true); err != nil {
				return err
			}
			found = true
		default:
			continue
		}
		if err := attachmentImageDimensions(width, height); err != nil {
			return err
		}
	}
	if !found {
		return fmt.Errorf("WebP contains no image frame")
	}
	return nil
}

func webPUint24(b []byte) int {
	return int(b[0]) | int(b[1])<<8 | int(b[2])<<16
}

func mediaTypeForPath(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		return "application/octet-stream"
	}
}
