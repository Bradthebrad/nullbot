package app

import (
	"context"
	tcagent "github.com/Bradthebrad/tinychain/agent"
	"time"
)

// Broadcast is serialized with worker registration. Each invocation owns its own
// inbox; no worker can consume another recipient's instruction. The root inbox
// is the admission authority, so a completed manager cannot accept a late steer.
func (s *invocationScope) broadcastInstruction(in tcagent.Instruction) error {
	s.steerMu.Lock()
	defer s.steerMu.Unlock()
	if err := s.inbox.Submit(in); err != nil {
		return err
	}
	s.instructions = append(s.instructions, in)
	for _, inbox := range s.workerInboxes {
		_ = inbox.Submit(in)
	}
	return nil
}

func (a *App) workerInstructionInbox(ctx context.Context, taskID string) (*tcagent.InstructionInbox, func()) {
	scope := scopeFrom(ctx)
	if scope == nil {
		return nil, func() {}
	}
	inbox := &tcagent.InstructionInbox{OnDelivery: func(in tcagent.Instruction, applied bool) {
		status := "instruction applied"
		if !applied {
			status = "instruction rejected"
		}
		a.appendActivity(ActivityRecord{Time: time.Now().UTC(), Kind: "instruction", Status: status, Detail: in.Text, RunID: in.ID, TaskID: taskID, SubmissionID: scope.id, Lane: scope.lane})
	}}
	scope.steerMu.Lock()
	if scope.workerInboxes == nil {
		scope.workerInboxes = map[string]*tcagent.InstructionInbox{}
	}
	for _, in := range scope.instructions {
		_ = inbox.Submit(in)
	}
	scope.workerInboxes[taskID] = inbox
	scope.steerMu.Unlock()
	return inbox, func() {
		scope.steerMu.Lock()
		delete(scope.workerInboxes, taskID)
		scope.steerMu.Unlock()
		inbox.Close()
	}
}
