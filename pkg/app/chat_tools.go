package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Bradthebrad/tinychain/agent"
)

// The main chat's tools for the other chats. Every request they start runs
// in that chat under the same project permissions as anything typed there.

const chatsGuidance = "Chats: the user can run several chats at once, and you are the main chat. chats_list shows each chat with its status, the request it is working on and its subagents; chat_read reads one; chat_send starts a request in a chat, queues one, or steers its running request or one of its subagents; chat_control pauses, resumes or stops a chat; chat_open starts a new chat, optionally with a first request, so work runs in parallel. Act on other chats when the user asks, or to carry out what they asked, and say what you did. A chat's results come back in that chat; read them with chat_read.\n"

func (a *App) isMainChat() bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.hub != nil && a.chatID == MainChat
}

func chatTools(main *App) []agent.Tool {
	return []agent.Tool{chatsListTool(main), chatReadTool(main), chatSendTool(main), chatControlTool(main), chatOpenTool(main)}
}

func chatSchema(props map[string]any, required ...string) map[string]any {
	return agent.ToolSchema(props, required...)
}

func (a *App) otherChat(id string) (*App, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("name the chat (its id from chats_list)")
	}
	if id == MainChat {
		return nil, errors.New("that is this chat; act here directly")
	}
	target, ok := a.hub.Chat(id)
	if !ok {
		return nil, fmt.Errorf("there is no chat %q; chats_list shows the open ones", id)
	}
	return target, nil
}

func chatsListTool(main *App) agent.Tool {
	return agent.ToolFunc{
		Name:        "chats_list",
		Description: "List the open chats: id, title, status (idle, running, paused, stopping, held), the running request, queued requests, subagents at work and the last answer.",
		Schema:      chatSchema(map[string]any{}),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			return prettyJSON(map[string]any{"chats": main.hub.Chats()}), nil
		},
	}
}

func chatReadTool(main *App) agent.Tool {
	return agent.ToolFunc{
		Name:        "chat_read",
		Description: "Read another chat: its recent messages, its status and the tool calls of its current or last request.",
		Schema: chatSchema(map[string]any{
			"chat":     agent.StringProperty("The chat's id from chats_list."),
			"messages": agent.NumberProperty("How many recent messages to return (default 10, at most 50)."),
		}, "chat"),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			target, err := main.otherChat(stringArg(args, "chat"))
			if err != nil {
				return "", err
			}
			n := min(max(intArg(args, "messages", 10), 1), 50)
			info, _ := main.hub.ChatInfo(stringArg(args, "chat"))
			target.mu.Lock()
			history := target.history
			if len(history) > n {
				history = history[len(history)-n:]
			}
			type msg struct {
				Role    string    `json:"role"`
				Content string    `json:"content"`
				Time    time.Time `json:"time"`
			}
			messages := make([]msg, 0, len(history))
			for _, m := range history {
				if m.VisibleOnly {
					continue
				}
				messages = append(messages, msg{m.Role, shortenKeepLines(m.Content, 4000), m.Time})
			}
			var calls []map[string]any
			for _, r := range target.activity {
				if r.Tool == "" || (r.Status != "tool complete" && r.Status != "tool error" && r.Status != "tool start") {
					continue
				}
				entry := map[string]any{"tool": r.Tool, "status": r.Status, "time": r.Time}
				if r.AgentID != "" {
					entry["agent"] = r.AgentID
				}
				if r.Error != "" {
					entry["error"] = shorten(r.Error, 300)
				}
				calls = append(calls, entry)
			}
			target.mu.Unlock()
			if len(calls) > 20 {
				calls = calls[len(calls)-20:]
			}
			return prettyJSON(map[string]any{"chat": info, "messages": messages, "recent_tool_calls": calls}), nil
		},
	}
}

func shortenKeepLines(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

func chatSendTool(main *App) agent.Tool {
	return agent.ToolFunc{
		Name: "chat_send",
		Description: "Send a message to another chat. mode send starts it as a request when the chat is idle (and queues it otherwise); queue adds it after the chat's current work; steer gives guidance to the request it is running. " +
			"With subagent, the guidance goes to that subagent instead. The message appears in that chat as coming from the main chat.",
		Schema: chatSchema(map[string]any{
			"chat":     agent.StringProperty("The chat's id from chats_list."),
			"message":  agent.StringProperty("The request or guidance."),
			"mode":     map[string]any{"type": "string", "enum": []string{"send", "queue", "steer"}, "description": "send (default), queue or steer."},
			"subagent": agent.StringProperty("A subagent's id from chats_list, to steer it."),
		}, "chat", "message"),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			target, err := main.otherChat(stringArg(args, "chat"))
			if err != nil {
				return "", err
			}
			text := strings.TrimSpace(stringArg(args, "message"))
			if text == "" {
				return "", errors.New("the message is empty")
			}
			if strings.HasPrefix(text, "/") {
				return "", errors.New("slash commands are the user's controls; send a request in words")
			}
			if sub := strings.TrimSpace(stringArg(args, "subagent")); sub != "" {
				if err := target.SteerTask(sub, "(from the main chat) "+text); err != nil {
					return "", err
				}
				main.logInfo("chat steered", "chat", stringArg(args, "chat"), "subagent", sub)
				return fmt.Sprintf("Guidance sent to subagent %s in %s.", sub, stringArg(args, "chat")), nil
			}
			mode := firstNonEmpty(stringArg(args, "mode"), "send")
			input := "(from the main chat) " + text
			var receipt Submission
			switch mode {
			case "send", "queue":
				receipt = target.SubmitRoutedRequest(context.Background(), input, mode, "", "")
				if mode == "send" && receipt.Status == "rejected" && strings.Contains(receipt.Error, "busy") {
					mode = "queue"
					receipt = target.SubmitRoutedRequest(context.Background(), input, "queue", "", "")
				}
			case "steer":
				primary := target.SubmissionState().PrimaryID
				if primary == "" {
					return "", errors.New("that chat is not running a request; use send instead")
				}
				receipt = target.SubmitRoutedRequest(context.Background(), input, "steer", "", primary)
			default:
				return "", fmt.Errorf("unknown mode %q; use send, queue or steer", mode)
			}
			if receipt.Status == "rejected" {
				return "", errors.New(firstNonEmpty(receipt.Error, "the chat did not accept the message"))
			}
			main.logInfo("chat message sent", "chat", stringArg(args, "chat"), "mode", mode, "status", receipt.Status)
			return prettyJSON(map[string]any{"chat": stringArg(args, "chat"), "mode": mode, "status": receipt.Status, "submission": receipt.ID}), nil
		},
	}
}

func chatControlTool(main *App) agent.Tool {
	return agent.ToolFunc{
		Name:        "chat_control",
		Description: "Pause, resume or stop another chat. stop lets its request wrap up after the current step; stop_everything cancels its work at once and holds its queue until resumed.",
		Schema: chatSchema(map[string]any{
			"chat":   agent.StringProperty("The chat's id from chats_list."),
			"action": map[string]any{"type": "string", "enum": []string{"pause", "resume", "stop", "stop_everything"}},
		}, "chat", "action"),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			target, err := main.otherChat(stringArg(args, "chat"))
			if err != nil {
				return "", err
			}
			switch action := stringArg(args, "action"); action {
			case "pause":
				target.PauseRuns()
			case "resume":
				target.ResumeRuns()
			case "stop":
				if err := target.StopRequest(""); err != nil {
					return "", err
				}
			case "stop_everything":
				target.StopEverything()
			default:
				return "", fmt.Errorf("unknown action %q", action)
			}
			main.logInfo("chat controlled", "chat", stringArg(args, "chat"), "action", stringArg(args, "action"))
			info, _ := main.hub.ChatInfo(stringArg(args, "chat"))
			return prettyJSON(info), nil
		},
	}
}

func chatOpenTool(main *App) agent.Tool {
	return agent.ToolFunc{
		Name:        "chat_open",
		Description: "Open a new chat, optionally starting it on a request, so a piece of work runs alongside this one. Returns the chat's id. The user sees it as a new tab.",
		Schema: chatSchema(map[string]any{
			"title":   agent.StringProperty("A short title for the tab."),
			"message": agent.StringProperty("The first request, if any."),
		}, "title"),
		Func: func(ctx context.Context, args map[string]any) (string, error) {
			info, err := main.hub.Open(stringArg(args, "title"))
			if err != nil {
				return "", err
			}
			out := map[string]any{"chat": info.ID, "title": info.Title}
			if text := strings.TrimSpace(stringArg(args, "message")); text != "" {
				target, _ := main.hub.Chat(info.ID)
				receipt := target.SubmitRoutedRequest(context.Background(), "(from the main chat) "+text, "send", "", "")
				out["status"] = receipt.Status
				if receipt.Error != "" {
					out["error"] = receipt.Error
				}
			}
			main.logInfo("chat opened", "chat", info.ID)
			return prettyJSON(out), nil
		},
	}
}
