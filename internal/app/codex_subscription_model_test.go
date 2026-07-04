package app

import (
	"strings"
	"testing"
)

func TestDecodeCodexResponseStreamTextDelta(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"hel"}`,
		"",
		`data: {"type":"response.output_text.delta","delta":"lo"}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")

	resp, err := decodeCodexResponseStream(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("decode stream: %v", err)
	}
	if resp.OutputText != "hello" {
		t.Fatalf("output text = %q", resp.OutputText)
	}
	if resp.Status != "completed" {
		t.Fatalf("status = %q", resp.Status)
	}
}

func TestIsCodexStreamPayloadWithEventPrefix(t *testing.T) {
	stream := strings.Join([]string{
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","delta":"hello"}`,
		"",
	}, "\n")

	if !isCodexStreamPayload([]byte(stream)) {
		t.Fatal("expected event-prefixed body to be detected as a stream")
	}
	resp, err := decodeCodexResponseStream(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("decode stream: %v", err)
	}
	if resp.OutputText != "hello" {
		t.Fatalf("output text = %q", resp.OutputText)
	}
}

func TestDecodeCodexResponseStreamToolCall(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":"list_dir","arguments":"{\"path\":\".\"}"}}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")

	resp, err := decodeCodexResponseStream(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("decode stream: %v", err)
	}
	msg := codexMessageFromResponse(resp)
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d", len(msg.ToolCalls))
	}
	if msg.ToolCalls[0].Name != "list_dir" || msg.ToolCalls[0].ID != "call_1" {
		t.Fatalf("tool call = %#v", msg.ToolCalls[0])
	}
	if msg.ToolCalls[0].Args["path"] != "." {
		t.Fatalf("tool args = %#v", msg.ToolCalls[0].Args)
	}
}
