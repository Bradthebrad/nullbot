package app

// Searchable history. Conversations are saved one message per line in
// history/<session>.jsonl; finished tool calls are saved alongside them in
// history/activity/<session>.jsonl. SearchHistory reads both, newest first,
// with text, role, tool, session and date filters, so agents (and the UI)
// can find what was said and what was done in any past session.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	activityHistoryDir  = "activity"
	persistedArgChars   = 4000
	maxHistoryLineBytes = 32 << 20
	defaultSearchLimit  = 20
	maxSearchLimit      = 200
	snippetRadius       = 140
)

// SavedToolCall is one finished tool call (or model error) as saved on
// disk. Results are the same previews the live activity carries (about 2,000
// characters), not whole outputs.
type SavedToolCall struct {
	SessionID    string         `json:"session_id"`
	Time         time.Time      `json:"time"`
	SubmissionID string         `json:"submission_id,omitempty"`
	Lane         string         `json:"lane,omitempty"`
	AgentID      string         `json:"agent_id,omitempty"`
	TaskID       string         `json:"task_id,omitempty"`
	Status       string         `json:"status"`
	Tool         string         `json:"tool,omitempty"`
	Server       string         `json:"server,omitempty"`
	Args         map[string]any `json:"args,omitempty"`
	Result       string         `json:"result,omitempty"`
	Error        string         `json:"error,omitempty"`
	Detail       string         `json:"detail,omitempty"`
	DurationMs   int64          `json:"duration_ms,omitempty"`
	Permission   map[string]any `json:"permission,omitempty"`
}

func activityHistoryPath(config Config, sessionID string) string {
	return filepath.Join(config.AppDir, "history", activityHistoryDir, sessionID+".jsonl")
}

// persistActivity saves finished tool calls and model errors with the
// current session. Everything else (starts, deltas, model rounds) is either
// repeated by the finished record or already in the conversation.
func (a *App) persistActivity(record ActivityRecord) {
	switch record.Status {
	case "tool complete", "tool error", "model error":
	default:
		return
	}
	a.mu.Lock()
	config := a.config
	sessionID := a.sessionID
	a.mu.Unlock()
	if strings.TrimSpace(config.AppDir) == "" || sessionID == "" {
		return
	}
	entry := SavedToolCall{
		SessionID: sessionID, Time: record.Time, SubmissionID: record.SubmissionID, Lane: record.Lane,
		AgentID: record.AgentID, TaskID: record.TaskID, Status: record.Status, Tool: record.Tool,
		Server: record.Server, Args: record.Args, Result: record.Result, Error: record.Error,
		DurationMs: record.DurationMs, Permission: record.Permission,
	}
	if record.Status == "model error" {
		entry.Detail = record.Detail
	}
	if entry.Time.IsZero() {
		entry.Time = time.Now().UTC()
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	a.persistMu.Lock()
	defer a.persistMu.Unlock()
	path := activityHistoryPath(config, sessionID)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		a.logError("activity history write failed", "error", err)
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		a.logError("activity history write failed", "error", err)
		return
	}
	defer file.Close()
	if _, err := file.Write(append(data, '\n')); err != nil {
		a.logError("activity history write failed", "error", err)
	}
}

// HistoryQuery filters SearchHistory. Every set field must match.
type HistoryQuery struct {
	// Text is a list of words (all must appear, in any order, ignoring case);
	// "quoted phrases" must appear as written.
	Text      string `json:"text,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	// Roles limits results to message roles (user, assistant, reasoning,
	// commentary) and/or "tool" for tool calls. Empty means everything.
	Roles []string `json:"roles,omitempty"`
	// Tool keeps only tool calls whose tool or server name contains it.
	Tool   string    `json:"tool,omitempty"`
	Since  time.Time `json:"since,omitempty"`
	Until  time.Time `json:"until,omitempty"`
	Limit  int       `json:"limit,omitempty"`
	Offset int       `json:"offset,omitempty"`
}

// HistoryHit is one matching message or tool call. Index is its position in
// the session's message list (messages) or tool-call log (tools), counting
// from 0, as HistorySessionEntries numbers them.
type HistoryHit struct {
	SessionID    string    `json:"session_id"`
	Kind         string    `json:"kind"`
	Index        int       `json:"index"`
	Role         string    `json:"role,omitempty"`
	Tool         string    `json:"tool,omitempty"`
	Server       string    `json:"server,omitempty"`
	Status       string    `json:"status,omitempty"`
	Time         time.Time `json:"time"`
	SubmissionID string    `json:"submission_id,omitempty"`
	Snippet      string    `json:"snippet"`
}

type HistorySearchResult struct {
	Hits     []HistoryHit `json:"hits"`
	More     bool         `json:"more"`
	Searched int          `json:"sessions_searched"`
}

// HistoryEntry is a message or a tool call in a session, in time order.
type HistoryEntry struct {
	Kind    string         `json:"kind"`
	Index   int            `json:"index"`
	Message *Message       `json:"message,omitempty"`
	Tool    *SavedToolCall `json:"tool,omitempty"`
}

func (e HistoryEntry) time() time.Time {
	if e.Message != nil {
		return e.Message.Time
	}
	return e.Tool.Time
}

func (e HistoryEntry) text() string {
	if e.Message != nil {
		return e.Message.Content
	}
	return e.Tool.readable()
}

// readable renders a tool call as text people (and search) can read:
// "search_issues (tracker) query: nightly export, limit: 25. OPS-2219 …".
func (t *SavedToolCall) readable() string {
	var b strings.Builder
	b.WriteString(t.Tool)
	if t.Server != "" {
		b.WriteString(" (" + t.Server + ")")
	}
	if len(t.Args) > 0 {
		keys := make([]string, 0, len(t.Args))
		for key := range t.Args {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		args := make([]string, 0, len(keys))
		for _, key := range keys {
			value, ok := t.Args[key].(string)
			if !ok {
				data, _ := json.Marshal(t.Args[key])
				value = string(data)
			}
			args = append(args, key+": "+clipRunes(value, persistedArgChars))
		}
		b.WriteString(" " + strings.Join(args, ", "))
	}
	for _, tail := range []string{t.Error, t.Detail, t.Result} {
		if tail = strings.TrimSpace(tail); tail != "" {
			b.WriteString(". " + tail)
			break
		}
	}
	return b.String()
}

func (e HistoryEntry) role() string {
	if e.Message != nil {
		return e.Message.Role
	}
	return "tool"
}

// ValidHistorySessionID rejects ids that could name a file outside history.
func ValidHistorySessionID(sessionID string) (string, error) {
	id := strings.TrimSuffix(strings.TrimSpace(sessionID), ".jsonl")
	if id == "" || id != filepath.Base(id) || strings.ContainsAny(id, `/\:`) || id == "." || id == ".." {
		return "", fmt.Errorf("invalid session_id %q", sessionID)
	}
	return id, nil
}

// HistorySessionEntries reads a session's messages and tool calls, oldest
// first, numbering each kind from 0 in file order.
func HistorySessionEntries(config Config, sessionID string, includeTools bool) ([]HistoryEntry, error) {
	id, err := ValidHistorySessionID(sessionID)
	if err != nil {
		return nil, err
	}
	var entries []HistoryEntry
	path := filepath.Join(config.AppDir, "history", id+".jsonl")
	err = scanJSONLines(path, func(line []byte) {
		var record persistedMessage
		if json.Unmarshal(line, &record) != nil {
			return
		}
		message := record.Message
		entries = append(entries, HistoryEntry{Kind: "message", Message: &message})
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	messagesMissing := os.IsNotExist(err)
	for i := range entries {
		entries[i].Index = i
	}
	if includeTools {
		count := 0
		toolErr := scanJSONLines(activityHistoryPath(config, id), func(line []byte) {
			var record SavedToolCall
			if json.Unmarshal(line, &record) != nil {
				return
			}
			entries = append(entries, HistoryEntry{Kind: "tool", Index: count, Tool: &record})
			count++
		})
		if toolErr != nil && !os.IsNotExist(toolErr) {
			return nil, toolErr
		}
		if messagesMissing && count == 0 {
			return nil, fmt.Errorf("session %s not found", id)
		}
	} else if messagesMissing {
		return nil, fmt.Errorf("session %s not found", id)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i].time(), entries[j].time()
		if a.IsZero() || b.IsZero() {
			return false
		}
		return a.Before(b)
	})
	return entries, nil
}

func scanJSONLines(path string, each func(line []byte)) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), maxHistoryLineBytes)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		each(line)
	}
	return scanner.Err()
}

type searchTerms []string

// parseSearchTerms splits text into lowercase words, keeping "quoted
// phrases" together.
func parseSearchTerms(text string) searchTerms {
	var terms searchTerms
	text = strings.TrimSpace(text)
	for text != "" {
		if text[0] == '"' {
			end := strings.IndexByte(text[1:], '"')
			if end < 0 {
				end = len(text) - 1
			}
			if phrase := strings.ToLower(strings.TrimSpace(text[1 : end+1])); phrase != "" {
				terms = append(terms, phrase)
			}
			text = strings.TrimSpace(text[min(end+2, len(text)):])
			continue
		}
		end := strings.IndexAny(text, " \t\n\"")
		if end < 0 {
			end = len(text)
		}
		if word := strings.ToLower(text[:end]); word != "" {
			terms = append(terms, word)
		}
		text = strings.TrimSpace(text[end:])
	}
	return terms
}

func (t searchTerms) match(text string) bool {
	if len(t) == 0 {
		return true
	}
	lower := strings.ToLower(text)
	for _, term := range t {
		if !strings.Contains(lower, term) {
			return false
		}
	}
	return true
}

// snippet returns text around the first term, whitespace collapsed.
func (t searchTerms) snippet(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	at := -1
	if len(t) > 0 {
		at = strings.Index(strings.ToLower(text), t[0])
	}
	if at < 0 {
		return clipRunes(text, 2*snippetRadius)
	}
	start := at
	for i := 0; i < snippetRadius && start > 0; i++ {
		_, size := utf8.DecodeLastRuneInString(text[:start])
		start -= size
	}
	end := at
	for i := 0; i < snippetRadius+len(t[0]) && end < len(text); i++ {
		_, size := utf8.DecodeRuneInString(text[end:])
		end += size
	}
	out := text[start:end]
	if start > 0 {
		out = "…" + out
	}
	if end < len(text) {
		out += "…"
	}
	return out
}

func clipRunes(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	return string(runes[:limit]) + "…"
}

func wantsRole(roles []string, role string) bool {
	if len(roles) == 0 {
		return true
	}
	for _, r := range roles {
		if strings.EqualFold(strings.TrimSpace(r), role) {
			return true
		}
	}
	return false
}

// SearchHistory finds messages and tool calls across saved sessions, newest
// first.
func SearchHistory(config Config, q HistoryQuery) (HistorySearchResult, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	if limit > maxSearchLimit {
		limit = maxSearchLimit
	}
	offset := q.Offset
	if offset < 0 {
		offset = 0
	}
	terms := parseSearchTerms(q.Text)
	toolFilter := strings.ToLower(strings.TrimSpace(q.Tool))
	includeTools := wantsRole(q.Roles, "tool")
	includeMessages := toolFilter == "" && (len(q.Roles) == 0 || len(q.Roles) > 1 || !wantsRole(q.Roles, "tool"))

	var sessions []string
	if q.SessionID != "" {
		id, err := ValidHistorySessionID(q.SessionID)
		if err != nil {
			return HistorySearchResult{}, err
		}
		sessions = []string{id}
	} else {
		for _, file := range listHistoryFiles(config, 0) {
			if !q.Since.IsZero() && file.ModTime.Before(q.Since) {
				continue // nothing in it is new enough
			}
			sessions = append(sessions, strings.TrimSuffix(file.Name, ".jsonl"))
		}
	}

	result := HistorySearchResult{Hits: []HistoryHit{}}
	skipped := 0
	for _, id := range sessions {
		entries, err := HistorySessionEntries(config, id, includeTools)
		if err != nil {
			if q.SessionID != "" {
				return result, err
			}
			continue
		}
		result.Searched++
		for i := len(entries) - 1; i >= 0; i-- {
			entry := entries[i]
			if entry.Kind == "message" && !includeMessages {
				continue
			}
			if entry.Kind == "message" && !wantsRole(q.Roles, entry.role()) {
				continue
			}
			if entry.Kind == "tool" && toolFilter != "" && !strings.Contains(strings.ToLower(entry.Tool.Tool+" "+entry.Tool.Server), toolFilter) {
				continue
			}
			when := entry.time()
			if !q.Since.IsZero() && when.Before(q.Since) {
				continue
			}
			if !q.Until.IsZero() && when.After(q.Until) {
				continue
			}
			text := entry.text()
			if !terms.match(text) {
				continue
			}
			if skipped < offset {
				skipped++
				continue
			}
			if len(result.Hits) == limit {
				result.More = true
				return result, nil
			}
			hit := HistoryHit{SessionID: id, Kind: entry.Kind, Index: entry.Index, Role: entry.role(), Time: when, Snippet: terms.snippet(text)}
			if entry.Message != nil {
				hit.SubmissionID = entry.Message.SubmissionID
			} else {
				hit.Tool, hit.Server, hit.Status, hit.SubmissionID = entry.Tool.Tool, entry.Tool.Server, entry.Tool.Status, entry.Tool.SubmissionID
			}
			result.Hits = append(result.Hits, hit)
		}
	}
	return result, nil
}

// ParseHistoryTime accepts RFC 3339, a date (local midnight), or a duration
// back from now such as 90m, 36h, 7d or 2w.
func ParseHistoryTime(value string, endOfDay bool, now time.Time) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation("2006-01-02", value, time.Local); err == nil {
		if endOfDay {
			return t.AddDate(0, 0, 1).Add(-time.Nanosecond), nil
		}
		return t, nil
	}
	if n := len(value); n > 1 {
		unit := value[n-1]
		var count int
		if _, err := fmt.Sscanf(value[:n-1], "%d", &count); err == nil && count >= 0 {
			switch unit {
			case 'm':
				return now.Add(-time.Duration(count) * time.Minute), nil
			case 'h':
				return now.Add(-time.Duration(count) * time.Hour), nil
			case 'd':
				return now.AddDate(0, 0, -count), nil
			case 'w':
				return now.AddDate(0, 0, -7*count), nil
			}
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized time %q (use 2026-10-26, an RFC 3339 time, or 90m, 36h, 7d, 2w)", value)
}
