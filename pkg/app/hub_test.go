package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tcagent "github.com/Bradthebrad/tinychain/agent"
	"github.com/Bradthebrad/tinychain/lc"
)

// chatEchoModel answers each request with its last user message, after an
// optional wait, so concurrent chats can be told apart.
func chatEchoModel(wait <-chan struct{}) func(Config, bool) (tcagent.Model, error) {
	return func(Config, bool) (tcagent.Model, error) {
		return submissionModelFunc(func(ctx context.Context, m []lc.BaseMessage, tools []tcagent.Tool) (lc.BaseMessage, error) {
			last := lcContentText(m[len(m)-1].Content)
			if wait != nil && strings.Contains(last, "slow") {
				select {
				case <-wait:
				case <-ctx.Done():
					return lc.BaseMessage{}, ctx.Err()
				}
			}
			return lc.AI("echo: " + last), nil
		}), nil
	}
}

func hubTestHub(t *testing.T, wait <-chan struct{}) *Hub {
	t.Helper()
	main := submissionTestApp(t)
	main.modelFactory = chatEchoModel(wait)
	h := HubFor(main)
	t.Cleanup(h.Shutdown)
	return h
}

func TestChatsRunAtTheSameTimeWithTheirOwnHistory(t *testing.T) {
	release := make(chan struct{})
	h := hubTestHub(t, release)
	var mu sync.Mutex
	seen := map[string][]string{}
	stop := h.SubscribeActivity(func(chat string, r ActivityRecord) {
		mu.Lock()
		defer mu.Unlock()
		if r.Chat != chat {
			t.Errorf("record for %s tagged %q", chat, r.Chat)
		}
		seen[chat] = append(seen[chat], r.Status)
	})
	defer stop()

	info, err := h.Open("Research")
	if err != nil {
		t.Fatal(err)
	}
	chat, _ := h.Chat(info.ID)
	// The main chat is held on a slow request while the other answers.
	slow := h.Main().SubmitRouted(context.Background(), "slow question", "send")
	fast := chat.SubmitRouted(context.Background(), "quick question", "send")
	if slow.Status == "rejected" || fast.Status == "rejected" {
		t.Fatalf("rejected: %+v %+v", slow, fast)
	}
	waitSubmission(t, chat, fast.ID)
	if got := lastAssistant(chat); !strings.Contains(got, "quick question") {
		t.Fatalf("chat answered %q", got)
	}
	if list := h.Chats(); len(list) != 2 || list[0].Status != "running" || list[1].Status != "idle" || list[1].Title != "Research" {
		t.Fatalf("chats while main runs: %+v", list)
	}
	close(release)
	waitSubmission(t, h.Main(), slow.ID)
	if got := lastAssistant(h.Main()); !strings.Contains(got, "slow question") {
		t.Fatalf("main answered %q", got)
	}
	for _, m := range h.Main().State().History {
		if strings.Contains(m.Content, "quick question") {
			t.Fatal("one chat's message reached the other's history")
		}
	}
	mainInfo, _ := h.ChatInfo(MainChat)
	chatInfo, _ := h.ChatInfo(info.ID)
	if mainInfo.HistoryID == chatInfo.HistoryID {
		t.Fatalf("both chats write to history %s", mainInfo.HistoryID)
	}
	for _, info := range []ChatInfo{mainInfo, chatInfo} {
		if _, err := os.Stat(filepath.Join(h.Main().Config().AppDir, "history", info.HistoryID+".jsonl")); err != nil {
			t.Fatalf("history for %s: %v", info.ID, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen[MainChat]) == 0 || len(seen[info.ID]) == 0 {
		t.Fatalf("activity by chat: %v", seen)
	}
}

func TestChatsShareConfigurationAndSchedule(t *testing.T) {
	h := hubTestHub(t, nil)
	info, _ := h.Open("")
	chat, _ := h.Chat(info.ID)
	if info.Title != "Chat 1" {
		t.Fatalf("default title %q", info.Title)
	}
	if err := chat.UpdateConfig(func(c *Config) { c.BotName = "Atlas" }); err != nil {
		t.Fatal(err)
	}
	if got := h.Main().Config().BotName; got != "Atlas" {
		t.Fatalf("main chat's bot name %q", got)
	}
	task, err := chat.CreateScheduledTask(ScheduleRequest{Prompt: "check the backups", Every: "24h"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range h.Main().ScheduledTasks() {
		found = found || s.ID == task.ID
	}
	if !found || len(chat.ScheduledTasks()) != len(h.Main().ScheduledTasks()) {
		t.Fatal("a chat's schedule is not the main chat's")
	}
	// Closing a chat leaves the shared tool servers and the main chat working.
	if err := h.Close(MainChat); err == nil {
		t.Fatal("the main chat closed")
	}
	if err := h.Close(info.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.Chat(info.ID); ok || len(h.Chats()) != 1 {
		t.Fatal("the chat is still open")
	}
	r := h.Main().SubmitRouted(context.Background(), "still here?", "send")
	waitSubmission(t, h.Main(), r.ID)
	if h.Main().mcp().closed {
		t.Fatal("closing a chat closed the tool servers")
	}
}

func callChatTool(t *testing.T, a *App, name string, args map[string]any) (map[string]any, error) {
	t.Helper()
	for _, tool := range BuiltinTools(a.Config(), a) {
		if tool.Definition().Name == name {
			out, err := tool.Call(context.Background(), args)
			if err != nil {
				return nil, err
			}
			var result map[string]any
			if jerr := json.Unmarshal([]byte(out), &result); jerr != nil {
				return map[string]any{"text": out}, nil
			}
			return result, nil
		}
	}
	t.Fatalf("no %s tool", name)
	return nil, nil
}

func TestTheMainChatSeesAndSteersTheOthers(t *testing.T) {
	release := make(chan struct{})
	h := hubTestHub(t, release)
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	main := h.Main()
	opened, err := callChatTool(t, main, "chat_open", map[string]any{"title": "Backups", "message": "slow audit of the backups"})
	if err != nil {
		t.Fatal(err)
	}
	id := opened["chat"].(string)
	chat, _ := h.Chat(id)
	waitFor(t, "the opened chat to run", func() bool { return chat.SubmissionState().PrimaryID != "" })

	list, _ := callChatTool(t, main, "chats_list", nil)
	chats := list["chats"].([]any)
	if len(chats) != 2 || chats[1].(map[string]any)["status"] != "running" || !strings.Contains(chats[1].(map[string]any)["request"].(string), "from the main chat") {
		t.Fatalf("chats_list: %v", list)
	}
	// While it runs, a send is queued behind it.
	sent, err := callChatTool(t, main, "chat_send", map[string]any{"chat": id, "message": "then list the snapshots"})
	if err != nil || sent["mode"] != "queue" || sent["status"] != "queued" {
		t.Fatalf("send to a busy chat: %v %v", sent, err)
	}
	if _, err := callChatTool(t, main, "chat_control", map[string]any{"chat": id, "action": "pause"}); err != nil {
		t.Fatal(err)
	}
	if info, _ := h.ChatInfo(id); info.Status != "paused" && info.Status != "held" {
		t.Fatalf("after pause: %+v", info)
	}
	_, _ = callChatTool(t, main, "chat_control", map[string]any{"chat": id, "action": "resume"})
	close(release)
	waitFor(t, "both requests to finish", func() bool {
		s := chat.SubmissionState()
		return s.PrimaryID == "" && len(s.Queue) == 0 && len(chat.State().History) >= 4
	})
	read, err := callChatTool(t, main, "chat_read", map[string]any{"chat": id})
	if err != nil {
		t.Fatal(err)
	}
	messages := read["messages"].([]any)
	if len(messages) < 4 || !strings.Contains(messages[len(messages)-1].(map[string]any)["content"].(string), "snapshots") {
		t.Fatalf("chat_read: %v", read)
	}
	if _, err := callChatTool(t, main, "chat_send", map[string]any{"chat": MainChat, "message": "x"}); err == nil {
		t.Fatal("the main chat sent to itself")
	}
	// Other chats don't get these tools.
	for _, tool := range BuiltinTools(chat.Config(), chat) {
		if strings.HasPrefix(tool.Definition().Name, "chat") {
			t.Fatalf("a chat has %s", tool.Definition().Name)
		}
	}
}

func TestOpenChatsComeBackAfterARestart(t *testing.T) {
	h := hubTestHub(t, nil)
	info, _ := h.Open("Release notes")
	chat, _ := h.Chat(info.ID)
	r := chat.SubmitRouted(context.Background(), "draft the notes", "send")
	waitSubmission(t, chat, r.ID)
	empty, _ := h.Open("Empty")
	_ = empty
	config := h.Main().Config()
	h.Shutdown()

	next := HubFor(New(config))
	defer next.Shutdown()
	restored := next.Restore()
	if len(restored) != 2 || restored[0].Title != "Release notes" || restored[1].Title != "Empty" {
		t.Fatalf("restored %+v", restored)
	}
	again, _ := next.Chat(restored[0].ID)
	history := again.State().History
	if len(history) != 2 || !strings.Contains(history[1].Content, "draft the notes") {
		t.Fatalf("restored history %+v", history)
	}
	if restored[0].HistoryID != info.HistoryID {
		t.Fatalf("the restored chat writes to %s, not %s", restored[0].HistoryID, info.HistoryID)
	}
	time.Sleep(10 * time.Millisecond)
}
