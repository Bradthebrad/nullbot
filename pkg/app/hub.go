package app

// Chats: one NullBot runs several conversations at once. A Hub owns them.
// They share the configuration, the tool servers, the scheduler and the
// vault; each has its own history file, queue, run controls and subagents.
// The main chat is the one front ends start with, and the only one with
// tools to see and steer the others.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// MainChat is the main chat's ID.
const MainChat = "main"

// ChangeChats: a chat was opened, closed or renamed, or a chat's run state
// changed.
const ChangeChats = "chats"

// ChatInfo describes one live chat.
type ChatInfo struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Main  bool   `json:"main,omitempty"`
	// Status is idle, running, paused, stopping or held.
	Status string `json:"status"`
	Queue  int    `json:"queue"`
	// Request is the running request, shortened.
	Request   string         `json:"request,omitempty"`
	LastReply string         `json:"last_reply,omitempty"`
	Messages  int            `json:"messages"`
	Subagents []ChatSubagent `json:"subagents,omitempty"`
	// HistoryID names the chat's saved session in history/.
	HistoryID string    `json:"history_id"`
	Created   time.Time `json:"created"`
	Updated   time.Time `json:"updated"`
}

// ChatSubagent is a subagent working in a chat.
type ChatSubagent struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Role    string `json:"role,omitempty"`
	Status  string `json:"status"`
	Current string `json:"current,omitempty"`
}

// Hub runs the chats.
type Hub struct {
	mu      sync.Mutex
	main    *App
	chats   map[string]*hubChat
	order   []string
	seq     int
	closed  bool
	sinkSeq int
	acts    map[int]func(chat string, record ActivityRecord)
	changes map[int]func(chat, topic string)
}

type hubChat struct {
	id      string
	title   string
	app     *App
	created time.Time
	updated time.Time
	stop    []func()
}

// NewHub starts NullBot with its main chat.
func NewHub(config Config) *Hub {
	return HubFor(New(config))
}

// HubFor makes main the main chat of a new hub.
func HubFor(main *App) *Hub {
	h := &Hub{main: main, chats: map[string]*hubChat{}, acts: map[int]func(string, ActivityRecord){}, changes: map[int]func(string, string){}}
	main.mu.Lock()
	main.hub, main.chatID = h, MainChat
	main.mu.Unlock()
	now := time.Now().UTC()
	h.add(&hubChat{id: MainChat, title: "Main", app: main, created: now, updated: now})
	return h
}

// Main is the main chat.
func (h *Hub) Main() *App { return h.main }

func (h *Hub) add(c *hubChat) {
	app := c.app
	id := c.id
	c.stop = append(c.stop,
		app.SubscribeActivity(func(record ActivityRecord) { h.publishActivity(id, record) }),
		app.SubscribeChanges(func(topic string) {
			if topic == ChangeSubmissions || topic == ChangeHistory {
				h.touch(id)
				h.publishChange(id, ChangeChats)
			}
			h.publishChange(id, topic)
		}),
	)
	h.mu.Lock()
	h.chats[id] = c
	h.order = append(h.order, id)
	h.mu.Unlock()
}

func (h *Hub) touch(id string) {
	h.mu.Lock()
	if c := h.chats[id]; c != nil {
		c.updated = time.Now().UTC()
	}
	h.mu.Unlock()
}

// Chat returns a live chat's App.
func (h *Hub) Chat(id string) (*App, bool) {
	if id == "" {
		id = MainChat
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.chats[id]
	if c == nil {
		return nil, false
	}
	return c.app, true
}

// Open starts a new chat. Its conversation is saved in its own history file.
func (h *Hub) Open(title string) (ChatInfo, error) {
	return h.open(title, "")
}

// Continue opens a chat that carries on a saved conversation, appending to
// the same history file.
func (h *Hub) Continue(historyID, title string) (ChatInfo, error) {
	id, err := ValidHistorySessionID(historyID)
	if err != nil {
		return ChatInfo{}, err
	}
	h.mu.Lock()
	for _, c := range h.chats {
		c.app.mu.Lock()
		same := c.app.sessionID == id
		c.app.mu.Unlock()
		if same {
			h.mu.Unlock()
			return h.info(c), nil
		}
	}
	h.mu.Unlock()
	return h.open(title, id)
}

func (h *Hub) open(title, historyID string) (ChatInfo, error) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return ChatInfo{}, errors.New("NullBot is shutting down")
	}
	if len(h.chats) >= maxChats {
		h.mu.Unlock()
		return ChatInfo{}, fmt.Errorf("at most %d chats can be open; close one first", maxChats)
	}
	h.seq++
	id := fmt.Sprintf("chat-%d", h.seq)
	for h.chats[id] != nil {
		h.seq++
		id = fmt.Sprintf("chat-%d", h.seq)
	}
	n := h.seq
	h.mu.Unlock()

	app := h.newChatApp(id)
	title = strings.TrimSpace(title)
	if historyID != "" {
		if err := app.continueSession(historyID); err != nil {
			app.Shutdown()
			return ChatInfo{}, err
		}
		if title == "" {
			title = chatTitleFromHistory(app)
		}
	}
	if title == "" {
		title = fmt.Sprintf("Chat %d", n)
	}
	now := time.Now().UTC()
	c := &hubChat{id: id, title: oneLineTitle(title), app: app, created: now, updated: now}
	h.add(c)
	h.saveLayout()
	h.publishChange(id, ChangeChats)
	return h.info(c), nil
}

const maxChats = 12

// newChatApp makes an App that shares the main chat's configuration, tool
// servers and logger, and leaves schedules to the main chat.
func (h *Hub) newChatApp(id string) *App {
	main := h.main
	config := main.Config()
	pool := main.mcp()
	a := &App{
		config:      config,
		logger:      NewLogger(config),
		sessionID:   newSessionID(),
		tasks:       map[string]*AgentTask{},
		taskCancels: map[string]context.CancelFunc{},
		schedules:   map[string]*ScheduledTask{},
		mcpPool:     pool,
		hub:         h,
		chatID:      id,
		// Tests give the main chat a model; chats use the same one.
		modelFactory: main.modelFactory,
	}
	if main.VisualsEnabled() {
		a.EnableVisuals()
	}
	return a
}

// continueSession loads a saved conversation and keeps appending to it.
func (a *App) continueSession(historyID string) error {
	entries, err := HistorySessionEntries(a.Config(), historyID, false)
	if err != nil {
		return err
	}
	var messages []Message
	for _, e := range entries {
		if e.Message != nil && e.Message.Lane != "also" {
			messages = append(messages, *e.Message)
		}
	}
	a.mu.Lock()
	a.sessionID = historyID
	a.history = messages
	a.mu.Unlock()
	return nil
}

func chatTitleFromHistory(a *App) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, m := range a.history {
		if m.Role == "user" && strings.TrimSpace(m.Content) != "" {
			return m.Content
		}
	}
	return ""
}

func oneLineTitle(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) > 48 {
		s = string([]rune(s)[:47]) + "…"
	}
	return s
}

// Close ends a chat: its running work is stopped and it leaves the hub. Its
// history stays saved. The main chat cannot be closed.
func (h *Hub) Close(id string) error {
	if id == MainChat || id == "" {
		return errors.New("the main chat stays open")
	}
	h.mu.Lock()
	c := h.chats[id]
	if c == nil {
		h.mu.Unlock()
		return fmt.Errorf("there is no chat %s", id)
	}
	delete(h.chats, id)
	for i, existing := range h.order {
		if existing == id {
			h.order = append(h.order[:i], h.order[i+1:]...)
			break
		}
	}
	h.mu.Unlock()
	for _, stop := range c.stop {
		stop()
	}
	c.app.StopEverything()
	c.app.Shutdown()
	h.saveLayout()
	h.publishChange(id, ChangeChats)
	return nil
}

// Rename gives a chat a new title.
func (h *Hub) Rename(id, title string) error {
	title = oneLineTitle(title)
	if title == "" {
		return errors.New("a chat needs a title")
	}
	h.mu.Lock()
	c := h.chats[id]
	if c != nil {
		c.title = title
	}
	h.mu.Unlock()
	if c == nil {
		return fmt.Errorf("there is no chat %s", id)
	}
	h.saveLayout()
	h.publishChange(id, ChangeChats)
	return nil
}

// Chats lists the live chats in the order they were opened.
func (h *Hub) Chats() []ChatInfo {
	h.mu.Lock()
	list := make([]*hubChat, 0, len(h.order))
	for _, id := range h.order {
		list = append(list, h.chats[id])
	}
	h.mu.Unlock()
	out := make([]ChatInfo, 0, len(list))
	for _, c := range list {
		out = append(out, h.info(c))
	}
	return out
}

// ChatInfo describes one chat.
func (h *Hub) ChatInfo(id string) (ChatInfo, bool) {
	h.mu.Lock()
	c := h.chats[firstNonEmpty(id, MainChat)]
	h.mu.Unlock()
	if c == nil {
		return ChatInfo{}, false
	}
	return h.info(c), true
}

func (h *Hub) info(c *hubChat) ChatInfo {
	a := c.app
	snap := a.SubmissionState()
	h.mu.Lock()
	info := ChatInfo{ID: c.id, Title: c.title, Main: c.id == MainChat, Created: c.created, Updated: c.updated}
	h.mu.Unlock()
	info.Queue = len(snap.Queue)
	switch {
	case snap.PrimaryID != "" && snap.Stopping:
		info.Status = "stopping"
	case snap.PrimaryID != "" && snap.RunPaused:
		info.Status = "paused"
	case snap.PrimaryID != "":
		info.Status = "running"
	case snap.Paused || snap.RunPaused:
		info.Status = "held"
	default:
		info.Status = "idle"
	}
	for _, job := range snap.Jobs {
		if job.ID == snap.PrimaryID {
			info.Request = shorten(job.Input, 200)
		}
	}
	a.mu.Lock()
	info.HistoryID = a.sessionID
	info.Messages = len(a.history)
	for i := len(a.history) - 1; i >= 0; i-- {
		m := a.history[i]
		if m.Role == "assistant" && !m.VisibleOnly && strings.TrimSpace(m.Content) != "" {
			info.LastReply = shorten(m.Content, 300)
			break
		}
	}
	a.mu.Unlock()
	for _, t := range a.TaskSnapshots() {
		if t.Status == TaskRunning || t.Status == TaskCanceling {
			info.Subagents = append(info.Subagents, ChatSubagent{ID: t.ID, Name: t.Name, Role: t.Role, Status: string(t.Status), Current: shorten(t.Current, 160)})
		}
	}
	return info
}

func shorten(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) > max {
		return string([]rune(s)[:max-1]) + "…"
	}
	return s
}

// SubscribeActivity tells fn about every chat's activity, tagged with the
// chat's ID.
func (h *Hub) SubscribeActivity(fn func(chat string, record ActivityRecord)) func() {
	h.mu.Lock()
	h.sinkSeq++
	id := h.sinkSeq
	h.acts[id] = fn
	h.mu.Unlock()
	return func() {
		h.mu.Lock()
		delete(h.acts, id)
		h.mu.Unlock()
	}
}

// SubscribeChanges tells fn when a chat's snapshot changes. Topics are the
// App's change topics, plus ChangeChats.
func (h *Hub) SubscribeChanges(fn func(chat, topic string)) func() {
	h.mu.Lock()
	h.sinkSeq++
	id := h.sinkSeq
	h.changes[id] = fn
	h.mu.Unlock()
	return func() {
		h.mu.Lock()
		delete(h.changes, id)
		h.mu.Unlock()
	}
}

func (h *Hub) publishActivity(chat string, record ActivityRecord) {
	h.mu.Lock()
	sinks := make([]func(string, ActivityRecord), 0, len(h.acts))
	for _, fn := range h.acts {
		sinks = append(sinks, fn)
	}
	h.mu.Unlock()
	for _, fn := range sinks {
		fn(chat, record)
	}
}

func (h *Hub) publishChange(chat, topic string) {
	h.mu.Lock()
	sinks := make([]func(string, string), 0, len(h.changes))
	for _, fn := range h.changes {
		sinks = append(sinks, fn)
	}
	h.mu.Unlock()
	for _, fn := range sinks {
		fn(chat, topic)
	}
}

// apps returns every chat's App.
func (h *Hub) apps() []*App {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*App, 0, len(h.order))
	for _, id := range h.order {
		out = append(out, h.chats[id].app)
	}
	return out
}

// configSaved gives every other chat the configuration one chat saved.
func (h *Hub) configSaved(from *App, config Config) {
	for _, a := range h.apps() {
		if a == from {
			continue
		}
		a.mu.Lock()
		a.config = config
		a.logger = NewLogger(config)
		a.mu.Unlock()
		a.notifyChange(ChangeConfig)
	}
}

// runtimeDirty makes every other chat rebuild its model clients.
func (h *Hub) runtimeDirty(from *App, reason string) {
	for _, a := range h.apps() {
		if a != from {
			a.markRuntimeDirty(reason)
		}
	}
}

// Shutdown stops every chat and the tool servers.
func (h *Hub) Shutdown() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	var others []*hubChat
	for _, id := range h.order {
		if id != MainChat {
			others = append(others, h.chats[id])
		}
	}
	h.mu.Unlock()
	for _, c := range others {
		c.app.StopEverything()
		c.app.Shutdown()
	}
	h.main.Shutdown()
}

// Tasks lists every chat's subagents and background tasks, newest first,
// each with its chat.
func (h *Hub) Tasks() []AgentTask {
	var out []AgentTask
	for _, c := range h.chatList() {
		for _, t := range c.app.TaskSnapshots() {
			t.Chat = c.id
			out = append(out, t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out
}

// TaskOwner is the chat running task id, for cancelling or steering it.
func (h *Hub) TaskOwner(chat, id string) (*App, bool) {
	if a, ok := h.Chat(chat); ok {
		if _, found := a.TaskDetails(id); found {
			return a, true
		}
	}
	return nil, false
}

// LiveVisualizations lists every chat's open visuals, each with its chat.
func (h *Hub) LiveVisualizations() []Visualization {
	var out []Visualization
	for _, c := range h.chatList() {
		for _, v := range c.app.LiveVisualizations() {
			v.Chat = c.id
			out = append(out, v)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// VisualOwner is the chat that showed visual id, or the main chat.
func (h *Hub) VisualOwner(id string) *App {
	for _, c := range h.chatList() {
		c.app.visualMu.Lock()
		live := c.app.visuals[id]
		c.app.visualMu.Unlock()
		if live != nil && live.live {
			return c.app
		}
	}
	return h.main
}

func (h *Hub) chatList() []*hubChat {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*hubChat, 0, len(h.order))
	for _, id := range h.order {
		out = append(out, h.chats[id])
	}
	return out
}

// --- layout: which chats were open -------------------------------------------

type chatLayout struct {
	Chats []chatLayoutEntry `json:"chats"`
}

type chatLayoutEntry struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	HistoryID string `json:"history_id"`
}

func chatLayoutPath(config Config) string {
	return filepath.Join(config.AppDir, "chats.json")
}

// saveLayout records the open chats so the next start can reopen them.
func (h *Hub) saveLayout() {
	h.mu.Lock()
	closed := h.closed
	var layout chatLayout
	for _, id := range h.order {
		c := h.chats[id]
		if id == MainChat {
			continue
		}
		c.app.mu.Lock()
		layout.Chats = append(layout.Chats, chatLayoutEntry{ID: id, Title: c.title, HistoryID: c.app.sessionID})
		c.app.mu.Unlock()
	}
	h.mu.Unlock()
	if closed {
		return
	}
	config := h.main.Config()
	if config.AppDir == "" {
		return
	}
	data, err := json.MarshalIndent(layout, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(config.AppDir, 0o700)
	_ = os.WriteFile(chatLayoutPath(config), append(data, '\n'), 0o600)
}

// Restore reopens the chats that were open when NullBot last stopped, each
// continuing its saved conversation. Chats whose history is gone are
// skipped.
func (h *Hub) Restore() []ChatInfo {
	config := h.main.Config()
	data, err := os.ReadFile(chatLayoutPath(config))
	if err != nil {
		return nil
	}
	var layout chatLayout
	if json.Unmarshal(data, &layout) != nil {
		return nil
	}
	var out []ChatInfo
	for _, entry := range layout.Chats {
		if entry.HistoryID == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(config.AppDir, "history", entry.HistoryID+".jsonl")); err != nil {
			// Never written to: nothing to continue, open it fresh.
			info, err := h.Open(entry.Title)
			if err == nil {
				out = append(out, info)
			}
			continue
		}
		info, err := h.Continue(entry.HistoryID, entry.Title)
		if err == nil {
			out = append(out, info)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}
