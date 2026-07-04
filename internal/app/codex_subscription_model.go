package app

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	tcagent "tinychain/agent"
	"tinychain/lc"
	"tinychain/openai"
)

type CodexSubscriptionModel struct {
	Config          Config
	Model           string
	ReasoningEffort string
}

type codexResponsesRequest struct {
	Model             string                 `json:"model"`
	Instructions      string                 `json:"instructions,omitempty"`
	Input             openai.ResponsesInput  `json:"input"`
	Store             bool                   `json:"store"`
	Tools             []openai.ResponsesTool `json:"tools,omitempty"`
	ToolChoice        any                    `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool                  `json:"parallel_tool_calls,omitempty"`
	Reasoning         any                    `json:"reasoning,omitempty"`
	Include           []string               `json:"include,omitempty"`
	PromptCacheKey    string                 `json:"prompt_cache_key,omitempty"`
	Stream            bool                   `json:"stream"`
}

type codexResponsesResponse struct {
	openai.ResponsesResponse
	OutputText string `json:"output_text,omitempty"`
}

type codexStreamEvent struct {
	Type     string                       `json:"type"`
	Delta    string                       `json:"delta,omitempty"`
	Text     string                       `json:"text,omitempty"`
	Item     *openai.ResponsesOutputItem  `json:"item,omitempty"`
	Output   []openai.ResponsesOutputItem `json:"output,omitempty"`
	Response *codexResponsesResponse      `json:"response,omitempty"`
	Error    any                          `json:"error,omitempty"`
	Message  string                       `json:"message,omitempty"`
	Code     string                       `json:"code,omitempty"`
	Param    string                       `json:"param,omitempty"`
}

func (m CodexSubscriptionModel) Call(ctx context.Context, messages []lc.BaseMessage, tools []tcagent.Tool) (lc.BaseMessage, error) {
	model := strings.TrimSpace(m.Model)
	if model == "" || model == "default" || model == "subscription-default" {
		model = "gpt-5.5"
	}
	instructions, inputMessages := codexInstructionsAndMessages(messages)
	requestTools := codexResponseTools(tools)
	parallel := true
	req := codexResponsesRequest{
		Model:          model,
		Instructions:   instructions,
		Input:          openai.MessageInput(inputMessages),
		Store:          false,
		PromptCacheKey: codexPromptCacheKey(instructions, requestTools),
		Stream:         true,
	}
	if len(requestTools) > 0 {
		req.Tools = requestTools
		req.ToolChoice = "auto"
		req.ParallelToolCalls = &parallel
	}
	if effort := ProviderEffort("codex", m.ReasoningEffort); effort != "" {
		req.Reasoning = map[string]any{"effort": effort, "summary": "auto"}
		req.Include = []string{"reasoning.encrypted_content"}
	}

	creds, err := ResolveCodexRuntimeCredentials(ctx, m.Config, false)
	if err != nil {
		return lc.BaseMessage{}, err
	}
	resp, err := postCodexResponses(ctx, creds, req)
	if shouldRefreshCodexForError(err) {
		creds, refreshErr := ResolveCodexRuntimeCredentials(ctx, m.Config, true)
		if refreshErr != nil {
			return lc.BaseMessage{}, refreshErr
		}
		resp, err = postCodexResponses(ctx, creds, req)
	}
	if err != nil {
		return lc.BaseMessage{}, err
	}
	msg := codexMessageFromResponse(resp)
	if msg.ResponseMetadata == nil {
		msg.ResponseMetadata = map[string]any{}
	}
	msg.ResponseMetadata["provider"] = "codex"
	msg.ResponseMetadata["auth"] = creds.AuthMode
	if msg.ResponseMetadata["model"] == "" {
		msg.ResponseMetadata["model"] = model
	}
	return msg, nil
}

func codexInstructionsAndMessages(messages []lc.BaseMessage) (string, []lc.BaseMessage) {
	var instructions []string
	var input []lc.BaseMessage
	for _, msg := range messages {
		switch msg.Type {
		case lc.RoleSystem, lc.RoleDeveloper:
			if text := strings.TrimSpace(lcContentText(msg.Content)); text != "" {
				instructions = append(instructions, text)
			}
		default:
			input = append(input, msg)
		}
	}
	if len(input) == 0 && len(instructions) > 0 {
		input = append(input, lc.Human("Continue."))
	}
	return strings.Join(instructions, "\n\n"), input
}

func codexResponseTools(tools []tcagent.Tool) []openai.ResponsesTool {
	out := make([]openai.ResponsesTool, 0, len(tools))
	for _, tool := range tools {
		def := tool.Definition()
		schema := sanitizeCodexToolSchema(def.ArgsSchema)
		out = append(out, openai.ResponsesTool{
			Type:        "function",
			Name:        def.Name,
			Description: def.Description,
			Parameters:  schema,
		})
	}
	return out
}

func sanitizeCodexToolSchema(schema map[string]any) map[string]any {
	if schema == nil {
		return map[string]any{"type": "object", "properties": map[string]any{}, "required": []any{}}
	}
	out := sanitizeCodexSchemaValue(schema).(map[string]any)
	if strings.TrimSpace(fmt.Sprint(out["type"])) == "" {
		out["type"] = "object"
	}
	if out["properties"] == nil {
		out["properties"] = map[string]any{}
	}
	required, ok := out["required"]
	if !ok || required == nil {
		out["required"] = []any{}
	} else {
		switch required.(type) {
		case []any, []string:
		default:
			out["required"] = []any{}
		}
	}
	return out
}

func sanitizeCodexSchemaValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			out[key] = sanitizeCodexSchemaValue(child)
		}
		if _, ok := out["required"]; ok && out["required"] == nil {
			out["required"] = []any{}
		}
		if properties, ok := out["properties"]; ok && properties == nil {
			out["properties"] = map[string]any{}
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, child := range typed {
			out[i] = sanitizeCodexSchemaValue(child)
		}
		return out
	default:
		return value
	}
}

func codexPromptCacheKey(instructions string, tools []openai.ResponsesTool) string {
	if strings.TrimSpace(instructions) == "" && len(tools) == 0 {
		return ""
	}
	payload := strings.TrimSpace(instructions)
	if len(tools) > 0 {
		data, _ := json.Marshal(tools)
		payload += "\x00" + string(data)
	}
	hash := sha256.Sum256([]byte(payload))
	return "pck_" + hex.EncodeToString(hash[:])[:24]
}

func postCodexResponses(ctx context.Context, creds CodexRuntimeCredentials, payload codexResponsesRequest) (codexResponsesResponse, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return codexResponsesResponse{}, err
	}
	endpoint := strings.TrimRight(firstNonEmpty(creds.BaseURL, defaultCodexBackendURL), "/") + "/responses"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return codexResponsesResponse{}, err
	}
	for key, value := range codexBackendHeaders(creds.AccessToken) {
		req.Header.Set(key, value)
	}
	req.Header.Set("Content-Type", "application/json")
	if payload.PromptCacheKey != "" {
		req.Header.Set("session_id", payload.PromptCacheKey)
		req.Header.Set("x-client-request-id", payload.PromptCacheKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return codexResponsesResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return codexResponsesResponse{}, readErr
		}
		return codexResponsesResponse{}, codexHTTPError{Status: resp.StatusCode, Body: string(data)}
	}
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		return decodeCodexResponseStream(resp.Body)
	}
	data, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return codexResponsesResponse{}, readErr
	}
	if isCodexStreamPayload(data) {
		return decodeCodexResponseStream(bytes.NewReader(data))
	}
	var out codexResponsesResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return codexResponsesResponse{}, err
	}
	return out, nil
}

func isCodexStreamPayload(data []byte) bool {
	trimmed := strings.TrimSpace(string(data))
	return strings.HasPrefix(trimmed, "event:") || strings.HasPrefix(trimmed, "data:")
}

func decodeCodexResponseStream(reader io.Reader) (codexResponsesResponse, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	var out codexResponsesResponse
	var output []openai.ResponsesOutputItem
	var outputText strings.Builder
	var dataLines []string

	flush := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		raw := strings.TrimSpace(strings.Join(dataLines, "\n"))
		dataLines = nil
		if raw == "" || raw == "[DONE]" {
			return nil
		}
		var event codexStreamEvent
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			return nil
		}
		if event.Type == "" {
			var direct codexResponsesResponse
			if err := json.Unmarshal([]byte(raw), &direct); err == nil && (direct.ID != "" || direct.Status != "" || len(direct.Output) > 0 || strings.TrimSpace(direct.OutputText) != "") {
				out = direct
			}
			return nil
		}
		switch event.Type {
		case "error":
			if message := codexStreamErrorMessage(event.Error, event.Message); message != "" {
				return fmt.Errorf("codex stream error: %s", message)
			}
			return fmt.Errorf("codex stream error")
		case "response.output_text.delta":
			outputText.WriteString(event.Delta)
		case "response.output_text.done":
			if strings.TrimSpace(event.Text) != "" {
				outputText.Reset()
				outputText.WriteString(event.Text)
			}
		case "response.output_item.done":
			if event.Item != nil {
				output = append(output, *event.Item)
			}
		case "response.completed", "response.incomplete", "response.failed":
			if event.Response != nil {
				out = *event.Response
			}
			if len(event.Output) > 0 {
				output = append(output, event.Output...)
			}
			if event.Type == "response.failed" {
				message := codexResponseError(out.Error)
				if message == "" {
					message = "response failed"
				}
				return fmt.Errorf("codex stream failed: %s", message)
			}
		default:
			if event.Item != nil && strings.HasSuffix(event.Type, ".done") {
				output = append(output, *event.Item)
			}
		}
		return nil
	}

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if err := flush(); err != nil {
				return out, err
			}
			continue
		}
		if strings.HasPrefix(trimmed, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(trimmed, "data:")))
			continue
		}
		if strings.HasPrefix(trimmed, "{") {
			dataLines = append(dataLines, trimmed)
		}
	}
	if err := scanner.Err(); err != nil {
		return out, err
	}
	if err := flush(); err != nil {
		return out, err
	}
	if len(out.Output) == 0 && len(output) > 0 {
		out.Output = output
	}
	if strings.TrimSpace(out.OutputText) == "" && outputText.Len() > 0 {
		out.OutputText = outputText.String()
	}
	if out.Status == "" {
		out.Status = "completed"
	}
	if len(out.Output) == 0 && strings.TrimSpace(out.OutputText) == "" {
		return out, errors.New("codex stream returned no output")
	}
	return out, nil
}

func codexStreamErrorMessage(raw any, fallback string) string {
	if strings.TrimSpace(fallback) != "" {
		return fallback
	}
	return codexResponseError(raw)
}

func codexResponseError(raw any) string {
	switch typed := raw.(type) {
	case nil:
		return ""
	case string:
		return typed
	case map[string]any:
		if message := strings.TrimSpace(fmt.Sprint(typed["message"])); message != "" && message != "<nil>" {
			return message
		}
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return fmt.Sprint(raw)
	}
	return string(data)
}

func shouldRefreshCodexForError(err error) bool {
	var httpErr codexHTTPError
	return errors.As(err, &httpErr) && (httpErr.Status == http.StatusUnauthorized || httpErr.Status == http.StatusForbidden)
}

func codexMessageFromResponse(resp codexResponsesResponse) lc.BaseMessage {
	msg := lc.BaseMessage{
		Type:             lc.RoleAI,
		ID:               resp.ID,
		Content:          lc.PartsContent(),
		ResponseMetadata: map[string]any{"model": resp.Model, "status": resp.Status},
	}
	var parts []lc.ContentPart
	var reasoningSummaries []string
	var reasoningDetails []map[string]any
	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			for _, content := range item.Content {
				if content.Text != "" {
					parts = append(parts, lc.ContentPart{Type: firstNonEmpty(content.Type, "output_text"), Text: content.Text})
				}
			}
		case "reasoning":
			for _, summary := range item.Summary {
				if strings.TrimSpace(summary.Text) == "" {
					continue
				}
				reasoningSummaries = append(reasoningSummaries, summary.Text)
				reasoningDetails = append(reasoningDetails, map[string]any{
					"type": "reasoning.summary",
					"text": summary.Text,
					"id":   item.ID,
				})
			}
			if item.EncryptedContent != "" {
				reasoningDetails = append(reasoningDetails, map[string]any{
					"type": "reasoning.encrypted",
					"data": item.EncryptedContent,
					"id":   item.ID,
				})
			}
		case "function_call", "custom_tool_call":
			name := item.Name
			args := item.Arguments
			if item.Type == "custom_tool_call" && args == "" {
				args = item.Output
			}
			msg.ToolCalls = append(msg.ToolCalls, lc.ToolCall{
				Name: name,
				Args: parseCodexArgs(args),
				ID:   firstNonEmpty(item.CallID, item.ID, name),
				Type: "tool_call",
			})
		}
	}
	if len(parts) == 0 && strings.TrimSpace(resp.OutputText) != "" {
		parts = append(parts, lc.ContentPart{Type: "output_text", Text: strings.TrimSpace(resp.OutputText)})
	}
	if len(parts) == 1 && parts[0].Text != "" && len(msg.ToolCalls) == 0 {
		msg.Content = lc.TextContent(parts[0].Text)
	} else {
		msg.Content = lc.PartsContent(parts...)
	}
	if len(reasoningSummaries) > 0 || len(reasoningDetails) > 0 {
		msg.AdditionalKwargs = map[string]any{}
		if len(reasoningSummaries) > 0 {
			msg.AdditionalKwargs["reasoning_summaries"] = reasoningSummaries
		}
		if len(reasoningDetails) > 0 {
			msg.AdditionalKwargs["reasoning_details"] = reasoningDetails
		}
	}
	if resp.Usage != nil {
		msg.UsageMetadata = &lc.UsageMetadata{
			InputTokens:        resp.Usage.InputTokens,
			OutputTokens:       resp.Usage.OutputTokens,
			TotalTokens:        resp.Usage.TotalTokens,
			InputTokenDetails:  resp.Usage.InputTokensDetails,
			OutputTokenDetails: resp.Usage.OutputTokensDetails,
		}
		if msg.UsageMetadata.InputTokens == 0 {
			msg.UsageMetadata.InputTokens = resp.Usage.PromptTokens
		}
		if msg.UsageMetadata.OutputTokens == 0 {
			msg.UsageMetadata.OutputTokens = resp.Usage.CompletionTokens
		}
		if msg.UsageMetadata.TotalTokens == 0 {
			msg.UsageMetadata.TotalTokens = msg.UsageMetadata.InputTokens + msg.UsageMetadata.OutputTokens
		}
	}
	return msg
}

func parseCodexArgs(raw string) map[string]any {
	if strings.TrimSpace(raw) == "" {
		return map[string]any{}
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return map[string]any{}
	}
	return args
}
