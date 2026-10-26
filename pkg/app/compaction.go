package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tcagent "github.com/Bradthebrad/tinychain/agent"
	"github.com/Bradthebrad/tinychain/callbacks"
	"github.com/Bradthebrad/tinychain/lc"
)

// CompactionResult describes a compaction someone asked for.
type CompactionResult struct {
	Message Message                  `json:"message"`
	Report  tcagent.CompactionReport `json:"report"`
}

var errNothingToCompact = errors.New("there is nothing to compact yet")

// CompactConversation summarizes the conversation with the model, the same
// way a long request is compacted, and adds the summary to the history as a
// "compaction" message. Later requests start from the summary. The messages
// before it stay visible and saved, and the agent can still read them with
// history_session_read. focus says what the summary should keep in detail.
func (a *App) CompactConversation(ctx context.Context, focus string) (CompactionResult, error) {
	a.mu.Lock()
	if a.busyLocked() {
		a.mu.Unlock()
		return CompactionResult{}, errors.New("a request is running; compact when it finishes")
	}
	config := a.config
	history := append([]Message(nil), a.history...)
	a.mu.Unlock()

	var messages []lc.BaseMessage
	start := 0
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == compactionRole {
			messages = append(messages, lc.System(history[i].Content))
			start = i + 1
			break
		}
	}
	added := 0
	for _, msg := range history[start:] {
		if msg.Lane == "also" || msg.VisibleOnly || isCommandMessage(msg.Content) || (msg.Role != "user" && msg.Role != "assistant") {
			continue
		}
		// Text only: the summary doesn't need attached images again.
		if msg.Role == "assistant" {
			messages = append(messages, lc.AI(msg.Content))
		} else {
			messages = append(messages, lc.Human(msg.Content))
		}
		added++
	}
	if added == 0 {
		return CompactionResult{}, errNothingToCompact
	}

	model, err := a.invocationModel(config, false)
	if err != nil {
		return CompactionResult{}, err
	}
	modelConfig := effectiveModelConfig(config, config.Model)
	compactor := tcagent.New(tcagent.Config{
		Name:    "compaction",
		Model:   model,
		Context: agentContextPolicy(config, config.Model),
		Callbacks: callbacks.SinkFunc(func(event callbacks.Event) {
			a.recordUsageCallback("", "compaction", modelConfig, event)
		}),
	})
	next, report, err := compactor.Compact(ctx, messages, tcagent.CompactOptions{Focus: focus})
	if err != nil {
		return CompactionResult{}, err
	}
	if report.Summarized == 0 {
		return CompactionResult{}, errNothingToCompact
	}
	var parts []string
	for _, msg := range next {
		if msg.Type == lc.RoleSystem {
			parts = append(parts, strings.TrimSpace(lcContentText(msg.Content)))
		}
	}
	record := Message{Role: compactionRole, Content: strings.Join(parts, "\n\n"), Time: time.Now().UTC()}

	a.mu.Lock()
	if a.busyLocked() || len(a.history) != len(history) {
		a.mu.Unlock()
		return CompactionResult{}, errors.New("the conversation changed while it was being compacted; compact again")
	}
	a.history = append(a.history, record)
	a.mu.Unlock()
	a.persistMessage(record)
	go a.notifyChange(ChangeHistory)
	a.logInfo("conversation compacted", "messages", report.Summarized, "before_tokens", report.BeforeTokens, "after_tokens", report.AfterTokens)
	return CompactionResult{Message: record, Report: report}, nil
}

// busyLocked reports whether a request is running or waiting. a.mu is held.
func (a *App) busyLocked() bool {
	if a.primaryJob != nil || len(a.submissionQueue) > 0 {
		return true
	}
	for _, job := range a.submissions {
		if job.Status == "running" {
			return true
		}
	}
	return false
}

func (a *App) compactCommand(ctx context.Context, focus string) Reply {
	result, err := a.CompactConversation(ctx, focus)
	if errors.Is(err, errNothingToCompact) {
		return a.reply("There is nothing to compact yet.", "/compact", "")
	}
	if err != nil {
		return a.reply("Compaction failed: "+err.Error(), "/compact", "")
	}
	report := result.Report
	message := fmt.Sprintf("Compacted %d messages, from about %s to %s tokens. New requests start from the summary; earlier messages stay in this session's history.",
		report.Summarized, formatTokens(report.BeforeTokens), formatTokens(report.AfterTokens))
	reply := a.reply(message, "/compact", "")
	reply.Data = map[string]any{"summary": result.Message.Content, "focus": focus, "report": report}
	return reply
}

func formatTokens(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
