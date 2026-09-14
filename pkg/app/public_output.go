package app

import (
	"context"
	"strings"
	"time"

	"github.com/Bradthebrad/tinychain/callbacks"
)

type publicOutput struct{ summary strings.Builder }

// Only provider-designated visible summaries and assistant commentary are
// retained. Internal reasoning payloads are never decoded or reconstructed.
func (a *App) retainPublicCallback(ctx context.Context, taskID string, event callbacks.Event) {
	scope := scopeFrom(ctx)
	if scope == nil {
		return
	}
	key := taskID
	a.mu.Lock()
	if scope.public == nil {
		scope.public = map[string]*publicOutput{}
	}
	output := scope.public[key]
	if output == nil {
		output = &publicOutput{}
		scope.public[key] = output
	}
	var summary string
	switch event.Event {
	case callbacks.EventLLMReasoning:
		if output.summary.Len() < 128*1024 {
			text := event.Data.Token
			if remaining := 128*1024 - output.summary.Len(); len(text) > remaining {
				text = text[:remaining]
			}
			output.summary.WriteString(text)
		}
	case callbacks.EventChatModelStart, callbacks.EventToolStart, callbacks.EventLLMCommentary, callbacks.EventLLMEnd, callbacks.EventLLMError:
		summary = output.summary.String()
		output.summary.Reset()
	}
	a.mu.Unlock()
	if strings.TrimSpace(summary) != "" {
		a.retainPublicMessage(scope, taskID, event.AgentID, "reasoning", summary)
	}
	if event.Event == callbacks.EventLLMCommentary && strings.TrimSpace(event.Data.Token) != "" {
		a.retainPublicMessage(scope, taskID, event.AgentID, "commentary", event.Data.Token)
	}
}
func (a *App) retainPublicMessage(scope *invocationScope, taskID, agentID, role, text string) {
	message := Message{Role: role, Content: text, Time: time.Now().UTC(), VisibleOnly: true, SubmissionID: scope.id, Lane: scope.lane, AgentID: agentID, TaskID: taskID}
	if scope.lane != "also" {
		a.mu.Lock()
		a.history = append(a.history, message)
		a.mu.Unlock()
	}
	a.persistMessage(message)
}
