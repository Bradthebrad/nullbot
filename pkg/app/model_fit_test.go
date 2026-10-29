package app

import (
	"context"
	"strings"
	"testing"

	"github.com/Bradthebrad/nullbot/pkg/llmfit"
)

func TestSuggestModelsForThisMachine(t *testing.T) {
	old := detectHardware
	detectHardware = func(context.Context) (llmfit.Hardware, error) {
		return llmfit.Hardware{OS: "linux", Arch: "amd64", CPUName: "AMD Ryzen 9 7950X", CPUCores: 16, TotalRAMGB: 64, AvailableRAMGB: 48,
			GPUs: []llmfit.GPU{{Name: "NVIDIA GeForce RTX 4090", VRAMGB: 24, Count: 1, Backend: llmfit.BackendCUDA}}, Backend: llmfit.BackendCUDA}, nil
	}
	defer func() { detectHardware = old }()
	a := New(projectTestConfig(t, "read-only"))
	res, err := a.SuggestModels(context.Background(), ModelFitOptions{UseCase: "coding", Limit: 10, Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Fits) == 0 || len(res.Fits) > 10 || res.Options.ContextLength != NullBotContext {
		t.Fatalf("fits %d options %+v", len(res.Fits), res.Options)
	}
	for _, f := range res.Fits {
		if f.Level == llmfit.FitTooTight || f.Quant == "" {
			t.Fatalf("a model that doesn't fit was suggested: %+v", f)
		}
	}
	if s := MachineSummary(res.Hardware); !strings.Contains(s, "RTX 4090, 24 GB VRAM") || !strings.Contains(s, "64 GB RAM") {
		t.Fatal(s)
	}
	if _, err := a.SuggestModels(context.Background(), ModelFitOptions{UseCase: "cooking"}); err == nil {
		t.Fatal("an unknown use case was accepted")
	}
}
