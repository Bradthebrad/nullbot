package app

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"tinychain/lc"
)

var attachmentTokenPattern = regexp.MustCompile(`@file\("([^"]+)"\)|@file\(([^)]+)\)`)

func humanMessageWithAttachments(text string) lc.BaseMessage {
	parts := []lc.ContentPart{{Type: "text", Text: text}}
	for _, path := range attachmentPaths(text) {
		part, ok := attachmentContentPart(path)
		if ok {
			parts = append(parts, part)
		}
	}
	if len(parts) == 1 {
		return lc.Human(text)
	}
	return lc.BaseMessage{Type: lc.RoleHuman, Content: lc.PartsContent(parts...)}
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

func attachmentContentPart(path string) (lc.ContentPart, bool) {
	mediaType := mediaTypeForPath(path)
	if !strings.HasPrefix(mediaType, "image/") {
		return lc.ContentPart{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return lc.ContentPart{}, false
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	dataURL := "data:" + mediaType + ";base64," + encoded
	return lc.ContentPart{
		Type: "image",
		Source: &lc.ContentSource{
			Type:      "base64",
			MediaType: mediaType,
			Data:      encoded,
			URL:       dataURL,
		},
	}, true
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
