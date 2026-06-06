package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Logger struct {
	mu   sync.Mutex
	path string
}

func NewLogger(config Config) *Logger {
	return &Logger{path: filepath.Join(config.AppDir, "logs", "nullbot.log")}
}

func (l *Logger) Info(message string, fields ...any) {
	l.write("INFO", message, fields...)
}

func (l *Logger) Error(message string, fields ...any) {
	l.write("ERROR", message, fields...)
}

func (l *Logger) write(level string, message string, fields ...any) {
	if l == nil || l.path == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(l.path), 0700); err != nil {
		return
	}
	line := fmt.Sprintf("%s %-5s %s", time.Now().UTC().Format(time.RFC3339), level, sanitizeLogText(message))
	if len(fields) > 0 {
		line += " " + formatFields(fields...)
	}
	line += "\n"
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line)
}

func (l *Logger) Recent(limit int) []string {
	if l == nil || l.path == "" {
		return nil
	}
	data, err := os.ReadFile(l.path)
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if limit <= 0 || limit > len(lines) {
		limit = len(lines)
	}
	return append([]string{}, lines[len(lines)-limit:]...)
}

func formatFields(fields ...any) string {
	var parts []string
	for i := 0; i < len(fields); i += 2 {
		key := fmt.Sprint(fields[i])
		value := ""
		if i+1 < len(fields) {
			value = fmt.Sprint(fields[i+1])
		}
		parts = append(parts, key+"="+sanitizeLogText(value))
	}
	return strings.Join(parts, " ")
}

func sanitizeLogText(text string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, text)
}
