package app

import (
	"context"
	"strings"
	"testing"
	"time"

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

func TestHardwareDetectionCachesOnlySuccess(t *testing.T) {
	old := detectHardware
	defer func() { detectHardware = old }()
	hardwareCache.Lock()
	hardwareCache.at = time.Time{}
	hardwareCache.Unlock()
	release := make(chan struct{})
	calls := 0
	detectHardware = func(ctx context.Context) (llmfit.Hardware, error) {
		calls++
		if calls == 1 {
			<-release
			return llmfit.Hardware{}, context.DeadlineExceeded
		}
		return llmfit.Hardware{CPUName: "Test CPU", TotalRAMGB: 32}, nil
	}
	// A caller that gives up gets its own error, and the detection goes on.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := MachineHardware(ctx, true); err != context.Canceled {
		t.Fatalf("cancelled caller: %v", err)
	}
	close(release)
	// The failed detection is not kept: the next call detects again.
	deadline := time.Now().Add(5 * time.Second)
	for {
		hw, err := MachineHardware(context.Background(), false)
		if err == nil && hw.CPUName == "Test CPU" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("still failing: %+v %v", hw, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if hw, err := MachineHardware(context.Background(), false); err != nil || hw.CPUName != "Test CPU" || calls != 2 {
		t.Fatalf("cached %+v %v after %d detections", hw, err, calls)
	}
}
