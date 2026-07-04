package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type persistedMessage struct {
	SessionID string    `json:"session_id"`
	Message   Message   `json:"message"`
	Artifact  string    `json:"artifact,omitempty"`
	Time      time.Time `json:"time"`
}

type HistoryFile struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

type HistorySessionSummary struct {
	SessionID string    `json:"session_id"`
	Messages  int       `json:"messages"`
	Preview   string    `json:"preview"`
	ModTime   time.Time `json:"mod_time"`
}

func newSessionID() string {
	return time.Now().UTC().Format("20060102-150405")
}

func (a *App) persistMessage(message Message) {
	a.mu.Lock()
	config := a.config
	sessionID := a.sessionID
	a.mu.Unlock()
	if sessionID == "" {
		sessionID = newSessionID()
	}
	artifact := ""
	if message.Role == "assistant" && strings.TrimSpace(message.Content) != "" {
		path, err := writeArtifact(config, sessionID, message)
		if err != nil {
			a.logError("artifact write failed", "error", err)
		} else {
			artifact = path
		}
	}
	if err := appendHistoryMessage(config, sessionID, message, artifact); err != nil {
		a.logError("history write failed", "error", err)
	}
}

func appendHistoryMessage(config Config, sessionID string, message Message, artifact string) error {
	if err := os.MkdirAll(filepath.Join(config.AppDir, "history"), 0700); err != nil {
		return err
	}
	record := persistedMessage{
		SessionID: sessionID,
		Message:   message,
		Artifact:  artifact,
		Time:      time.Now().UTC(),
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	path := filepath.Join(config.AppDir, "history", sessionID+".jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	return nil
}

func writeArtifact(config Config, sessionID string, message Message) (string, error) {
	if err := os.MkdirAll(filepath.Join(config.AppDir, "artifacts"), 0700); err != nil {
		return "", err
	}
	stamp := message.Time.UTC().Format("150405.000000000")
	stamp = strings.ReplaceAll(stamp, ".", "-")
	path := filepath.Join(config.AppDir, "artifacts", fmt.Sprintf("%s-assistant-%s.md", sessionID, stamp))
	content := fmt.Sprintf("# Assistant Output\n\n%s\n", strings.TrimSpace(message.Content))
	return path, os.WriteFile(path, []byte(content), 0600)
}

func listHistoryFiles(config Config, limit int) []HistoryFile {
	files := listFilesByModTime(filepath.Join(config.AppDir, "history"), ".jsonl")
	if limit > 0 && len(files) > limit {
		return files[:limit]
	}
	return files
}

func listArtifactFiles(config Config, limit int) []HistoryFile {
	files := listFilesByModTime(filepath.Join(config.AppDir, "artifacts"), ".md")
	if limit > 0 && len(files) > limit {
		return files[:limit]
	}
	return files
}

func listHistorySessionSummaries(config Config, limit int) []HistorySessionSummary {
	files := listHistoryFiles(config, limit)
	summaries := make([]HistorySessionSummary, 0, len(files))
	for _, file := range files {
		messages, _ := readHistorySessionFile(file.Path, 4)
		preview := ""
		if len(messages) > 0 {
			last := messages[len(messages)-1]
			preview = fmt.Sprintf("%s: %s", last.Role, truncate(last.Content, 140))
		}
		count := countHistoryMessages(file.Path)
		summaries = append(summaries, HistorySessionSummary{
			SessionID: strings.TrimSuffix(file.Name, ".jsonl"),
			Messages:  count,
			Preview:   preview,
			ModTime:   file.ModTime,
		})
	}
	return summaries
}

func readHistorySession(config Config, sessionID string, limit int) ([]Message, error) {
	sessionID = strings.TrimSuffix(filepath.Base(strings.TrimSpace(sessionID)), ".jsonl")
	if sessionID == "" || strings.Contains(sessionID, string(os.PathSeparator)) {
		return nil, fmt.Errorf("invalid session_id")
	}
	path := filepath.Join(config.AppDir, "history", sessionID+".jsonl")
	cleanRoot := filepath.Clean(filepath.Join(config.AppDir, "history"))
	cleanPath := filepath.Clean(path)
	if cleanPath != cleanRoot && !strings.HasPrefix(cleanPath, cleanRoot+string(os.PathSeparator)) {
		return nil, fmt.Errorf("session path escapes history directory")
	}
	return readHistorySessionFile(path, limit)
}

func readHistorySessionFile(path string, limit int) ([]Message, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var messages []Message
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var record persistedMessage
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			continue
		}
		messages = append(messages, record.Message)
	}
	if limit > 0 && len(messages) > limit {
		messages = messages[len(messages)-limit:]
	}
	return messages, nil
}

func countHistoryMessages(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	count := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

func listFilesByModTime(dir string, suffix string) []HistoryFile {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	files := make([]HistoryFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), suffix) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, HistoryFile{
			Name:    entry.Name(),
			Path:    filepath.Join(dir, entry.Name()),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
	}
	for i := 0; i < len(files); i++ {
		for j := i + 1; j < len(files); j++ {
			if files[j].ModTime.After(files[i].ModTime) {
				files[i], files[j] = files[j], files[i]
			}
		}
	}
	return files
}
