package app

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"tinychain/callbacks"
	"tinychain/lc"
)

type UsageRecord struct {
	Time         time.Time `json:"time"`
	SessionID    string    `json:"session_id"`
	Provider     string    `json:"provider"`
	Model        string    `json:"model"`
	Agent        string    `json:"agent"`
	TaskID       string    `json:"task_id,omitempty"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
	TotalTokens  int       `json:"total_tokens"`
	CostUSD      float64   `json:"cost_usd"`
	Estimated    bool      `json:"estimated"`
}

type UsageSnapshot struct {
	Records       []UsageRecord       `json:"records"`
	Recent        []UsageRecord       `json:"recent"`
	Session       UsageTotals         `json:"session"`
	Total         UsageTotals         `json:"total"`
	ByModel       []UsageModelSummary `json:"by_model"`
	Daily         []UsageDailySummary `json:"daily"`
	Pricing       []UsagePriceSummary `json:"pricing"`
	LastUpdatedAt time.Time           `json:"last_updated_at"`
}

type UsageTotals struct {
	Requests     int     `json:"requests"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	TotalTokens  int     `json:"total_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	Estimated    int     `json:"estimated"`
}

type UsageModelSummary struct {
	Provider string      `json:"provider"`
	Model    string      `json:"model"`
	Totals   UsageTotals `json:"totals"`
}

type UsageDailySummary struct {
	Day      string      `json:"day"`
	Provider string      `json:"provider,omitempty"`
	Model    string      `json:"model,omitempty"`
	Totals   UsageTotals `json:"totals"`
}

type UsagePriceSummary struct {
	Provider       string  `json:"provider"`
	ModelPattern   string  `json:"model_pattern"`
	InputPerMTok   float64 `json:"input_per_mtok"`
	OutputPerMTok  float64 `json:"output_per_mtok"`
	EstimatedPrice bool    `json:"estimated_price"`
}

type usagePrice struct {
	InputPerMTok   float64
	OutputPerMTok  float64
	EstimatedPrice bool
}

var usagePrices = []struct {
	Provider string
	Pattern  string
	Price    usagePrice
}{
	{"openai", "gpt-5.5-pro", usagePrice{15, 90, false}},
	{"openai", "gpt-5.5", usagePrice{2.5, 15, false}},
	{"openai", "gpt-5.4-mini", usagePrice{0.375, 2.25, false}},
	{"openai", "gpt-5.4-nano", usagePrice{0.10, 0.625, false}},
	{"openai", "gpt-5.4", usagePrice{1.25, 7.50, false}},
	{"openai", "gpt-5-mini", usagePrice{0.25, 2.00, false}},
	{"openai", "gpt-5", usagePrice{1.25, 10.00, true}},
	{"openai", "gpt-4.1-mini", usagePrice{0.40, 1.60, true}},
	{"openai", "gpt-4.1", usagePrice{2.00, 8.00, true}},
	{"anthropic", "claude-3-5-haiku", usagePrice{0.80, 4.00, true}},
	{"anthropic", "claude-sonnet", usagePrice{3.00, 15.00, false}},
	{"anthropic", "claude-opus", usagePrice{15.00, 75.00, true}},
	{"openrouter", "openai/gpt-5-mini", usagePrice{0.25, 2.00, false}},
	{"openrouter", "anthropic/claude", usagePrice{3.00, 15.00, true}},
}

func usagePath(config Config) string {
	return filepath.Join(config.AppDir, "usage", "usage.jsonl")
}

func (a *App) recordUsageCallback(taskID, agentName string, model ModelConfig, event callbacks.Event) {
	switch event.Event {
	case callbacks.EventChatModelStart:
		a.setTaskPendingInput(taskID, estimateMessagesTokens(event))
	case callbacks.EventLLMEnd:
		usage := usageFromLLMEnd(event)
		estimated := false
		if usage.Input == 0 && usage.Output == 0 && usage.Total == 0 {
			estimated = true
			usage.Input = a.takeTaskPendingInput(taskID)
			usage.Output = estimateTextTokens(llmEndText(event))
			usage.Total = usage.Input + usage.Output
		}
		if usage.Total == 0 {
			usage.Total = usage.Input + usage.Output
		}
		if usage.Total == 0 {
			return
		}
		record := UsageRecord{
			Time:         time.Now().UTC(),
			SessionID:    a.sessionID,
			Provider:     firstNonEmptyPlanValue(model.Provider, "openai"),
			Model:        model.Model,
			Agent:        firstNonEmptyPlanValue(agentName, "agent"),
			TaskID:       taskID,
			InputTokens:  usage.Input,
			OutputTokens: usage.Output,
			TotalTokens:  usage.Total,
			Estimated:    estimated,
		}
		record.CostUSD = estimateUsageCost(record)
		_ = a.appendUsageRecord(record)
	}
}

func (a *App) recordUsageDirect(agentName string, model ModelConfig, input []lc.BaseMessage, output lc.BaseMessage) {
	usage := TaskTokens{}
	estimated := false
	if output.UsageMetadata != nil {
		usage.Input = output.UsageMetadata.InputTokens
		usage.Output = output.UsageMetadata.OutputTokens
		usage.Total = output.UsageMetadata.TotalTokens
	}
	if usage.Input == 0 && usage.Output == 0 && usage.Total == 0 {
		estimated = true
		for _, msg := range input {
			usage.Input += estimateTextTokens(lcContentText(msg.Content))
		}
		usage.Output = estimateTextTokens(lcContentText(output.Content))
		usage.Total = usage.Input + usage.Output
	}
	if usage.Total == 0 {
		usage.Total = usage.Input + usage.Output
	}
	if usage.Total == 0 {
		return
	}
	record := UsageRecord{
		Time:         time.Now().UTC(),
		SessionID:    a.sessionID,
		Provider:     firstNonEmptyPlanValue(model.Provider, "openai"),
		Model:        model.Model,
		Agent:        firstNonEmptyPlanValue(agentName, "agent"),
		InputTokens:  usage.Input,
		OutputTokens: usage.Output,
		TotalTokens:  usage.Total,
		Estimated:    estimated,
	}
	record.CostUSD = estimateUsageCost(record)
	_ = a.appendUsageRecord(record)
}

func (a *App) setTaskPendingInput(taskID string, tokens int) {
	if taskID == "" || tokens <= 0 {
		return
	}
	a.mu.Lock()
	if a.taskPendingInput == nil {
		a.taskPendingInput = map[string]int{}
	}
	a.taskPendingInput[taskID] = tokens
	a.mu.Unlock()
}

func (a *App) takeTaskPendingInput(taskID string) int {
	if taskID == "" {
		return 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	tokens := a.taskPendingInput[taskID]
	delete(a.taskPendingInput, taskID)
	return tokens
}

func (a *App) appendUsageRecord(record UsageRecord) error {
	a.mu.Lock()
	config := a.config
	a.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(usagePath(config)), 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(usagePath(config), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	_, err = file.Write(append(data, '\n'))
	return err
}

func (a *App) UsageSnapshot() UsageSnapshot {
	a.mu.Lock()
	config := a.config
	sessionID := a.sessionID
	a.mu.Unlock()
	records := loadUsageRecords(config)
	return buildUsageSnapshot(records, sessionID)
}

func (a *App) ClearUsage() error {
	a.mu.Lock()
	config := a.config
	a.mu.Unlock()
	path := usagePath(config)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return os.WriteFile(path, nil, 0600)
}

func loadUsageRecords(config Config) []UsageRecord {
	file, err := os.Open(usagePath(config))
	if err != nil {
		return nil
	}
	defer file.Close()
	var records []UsageRecord
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var record UsageRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err == nil {
			records = append(records, record)
		}
	}
	return records
}

func buildUsageSnapshot(records []UsageRecord, sessionID string) UsageSnapshot {
	sort.Slice(records, func(i, j int) bool { return records[i].Time.Before(records[j].Time) })
	var snap UsageSnapshot
	snap.Records = append([]UsageRecord{}, records...)
	for _, record := range records {
		addUsageTotals(&snap.Total, record)
		if record.SessionID == sessionID {
			addUsageTotals(&snap.Session, record)
		}
	}
	start := len(records) - 40
	if start < 0 {
		start = 0
	}
	snap.Recent = append([]UsageRecord{}, records[start:]...)
	snap.ByModel = usageByModel(records)
	snap.Daily = usageDaily(records)
	snap.Pricing = usagePriceSummaries()
	snap.LastUpdatedAt = time.Now().UTC()
	return snap
}

func usageByModel(records []UsageRecord) []UsageModelSummary {
	byKey := map[string]*UsageModelSummary{}
	for _, record := range records {
		key := record.Provider + "\x00" + record.Model
		summary := byKey[key]
		if summary == nil {
			summary = &UsageModelSummary{Provider: record.Provider, Model: record.Model}
			byKey[key] = summary
		}
		addUsageTotals(&summary.Totals, record)
	}
	out := make([]UsageModelSummary, 0, len(byKey))
	for _, summary := range byKey {
		out = append(out, *summary)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Totals.CostUSD > out[j].Totals.CostUSD })
	return out
}

func usageDaily(records []UsageRecord) []UsageDailySummary {
	byKey := map[string]*UsageDailySummary{}
	for _, record := range records {
		day := record.Time.Format("2006-01-02")
		key := day + "\x00" + record.Provider + "\x00" + record.Model
		summary := byKey[key]
		if summary == nil {
			summary = &UsageDailySummary{Day: day, Provider: record.Provider, Model: record.Model}
			byKey[key] = summary
		}
		addUsageTotals(&summary.Totals, record)
	}
	out := make([]UsageDailySummary, 0, len(byKey))
	for _, summary := range byKey {
		out = append(out, *summary)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Day == out[j].Day {
			return out[i].Model < out[j].Model
		}
		return out[i].Day < out[j].Day
	})
	return out
}

func addUsageTotals(total *UsageTotals, record UsageRecord) {
	total.Requests++
	total.InputTokens += record.InputTokens
	total.OutputTokens += record.OutputTokens
	total.TotalTokens += record.TotalTokens
	total.CostUSD += record.CostUSD
	if record.Estimated {
		total.Estimated++
	}
}

func usagePriceSummaries() []UsagePriceSummary {
	out := make([]UsagePriceSummary, 0, len(usagePrices))
	for _, entry := range usagePrices {
		out = append(out, UsagePriceSummary{
			Provider:       entry.Provider,
			ModelPattern:   entry.Pattern,
			InputPerMTok:   entry.Price.InputPerMTok,
			OutputPerMTok:  entry.Price.OutputPerMTok,
			EstimatedPrice: entry.Price.EstimatedPrice,
		})
	}
	return out
}

func estimateUsageCost(record UsageRecord) float64 {
	price := priceForModel(record.Provider, record.Model)
	return (float64(record.InputTokens)/1_000_000.0)*price.InputPerMTok +
		(float64(record.OutputTokens)/1_000_000.0)*price.OutputPerMTok
}

func priceForModel(provider, model string) usagePrice {
	provider = strings.ToLower(provider)
	model = strings.ToLower(model)
	for _, entry := range usagePrices {
		if entry.Provider == provider && strings.Contains(model, strings.ToLower(entry.Pattern)) {
			return entry.Price
		}
	}
	return usagePrice{EstimatedPrice: true}
}

func estimateMessagesTokens(event callbacks.Event) int {
	total := 0
	for _, batch := range event.Data.Messages {
		for _, msg := range batch {
			total += estimateTextTokens(lcContentText(msg.Content))
		}
	}
	return total
}

func llmEndText(event callbacks.Event) string {
	if event.Data.Response == nil {
		return ""
	}
	var parts []string
	for _, batch := range event.Data.Response.Generations {
		for _, generation := range batch {
			parts = append(parts, lcContentText(generation.Message.Content))
		}
	}
	return strings.Join(parts, "\n")
}

func estimateTextTokens(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	// Cheap local approximation: English-ish text averages around 4 chars/token.
	tokens := len([]rune(text)) / 4
	if tokens < 1 {
		tokens = 1
	}
	return tokens
}

func formatCost(cost float64) string {
	if cost < 0.0001 {
		return fmt.Sprintf("$%.6f", cost)
	}
	if cost < 0.01 {
		return fmt.Sprintf("$%.4f", cost)
	}
	return fmt.Sprintf("$%.2f", cost)
}
