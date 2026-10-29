package app

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Bradthebrad/nullbot/pkg/llmfit"
	"github.com/Bradthebrad/nullbot/pkg/localmodels"
)

// Which local models run well here: this computer's hardware, and
// llmfit's ranking of the model catalog against it (pkg/llmfit, a Go port
// of github.com/AlexsJones/llmfit).

// NullBotContext is the context a model should have to work as an agent:
// NullBot's instructions and tool list take several thousand tokens.
const NullBotContext = 16384

var hardwareCache struct {
	sync.Mutex
	hw llmfit.Hardware
	at time.Time // of the last detection that succeeded
	// running is closed when the detection in progress ends; last and
	// lastErr are what it found.
	running chan struct{}
	last    llmfit.Hardware
	lastErr error
}

// detectHardware finds this computer's hardware; tests replace it.
var detectHardware = llmfit.Detect

// MachineHardware reports this computer's CPU, memory and GPUs, checked at
// most every ten minutes unless refresh is set. One detection runs at a
// time, on its own 30-second limit, so a caller that gives up neither stops
// it nor leaves a failure cached; only a detection that succeeded is kept.
func MachineHardware(ctx context.Context, refresh bool) (llmfit.Hardware, error) {
	hardwareCache.Lock()
	if !refresh && !hardwareCache.at.IsZero() && time.Since(hardwareCache.at) < 10*time.Minute {
		defer hardwareCache.Unlock()
		return hardwareCache.hw, nil
	}
	done := hardwareCache.running
	if done == nil {
		done = make(chan struct{})
		hardwareCache.running = done
		go func() {
			dctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			hw, err := detectHardware(dctx)
			hardwareCache.Lock()
			hardwareCache.running = nil
			hardwareCache.last, hardwareCache.lastErr = hw, err
			if err == nil {
				hardwareCache.hw, hardwareCache.at = hw, time.Now()
			}
			hardwareCache.Unlock()
			close(done)
		}()
	}
	hardwareCache.Unlock()
	select {
	case <-done:
	case <-ctx.Done():
		return llmfit.Hardware{}, ctx.Err()
	}
	hardwareCache.Lock()
	defer hardwareCache.Unlock()
	return hardwareCache.last, hardwareCache.lastErr
}

// ModelFitOptions narrow a ranking.
type ModelFitOptions struct {
	UseCase       string `json:"use_case,omitempty"` // general, coding, reasoning, chat, multimodal
	ContextLength int    `json:"context_length,omitempty"`
	Runtime       string `json:"runtime,omitempty"` // ollama, llama.cpp, mlx, vllm; empty picks
	Limit         int    `json:"limit,omitempty"`
	Refresh       bool   `json:"refresh,omitempty"`
	// IncludeSmall keeps models under 1.5B parameters, which rarely follow
	// tool instructions well enough to work as agents.
	IncludeSmall bool `json:"include_small,omitempty"`
}

const minAgentParamsB = 1.5

// ModelFits is a ranking for this computer.
type ModelFits struct {
	Hardware llmfit.Hardware `json:"hardware"`
	Options  ModelFitOptions `json:"options"`
	Fits     []llmfit.Fit    `json:"fits"`
}

// SuggestModels ranks the catalog's models for this computer, best first.
// With an Ollama server set up and no runtime given, it plans for Ollama.
func (a *App) SuggestModels(ctx context.Context, opts ModelFitOptions) (ModelFits, error) {
	hw, err := MachineHardware(ctx, opts.Refresh)
	if err != nil {
		return ModelFits{}, err
	}
	var useCase llmfit.UseCase
	if strings.TrimSpace(opts.UseCase) != "" {
		if useCase, err = llmfit.ParseUseCase(opts.UseCase); err != nil {
			return ModelFits{}, err
		}
	}
	if opts.Runtime == "" {
		for _, e := range a.Config().LocalEndpoints {
			if e.Kind == string(localmodels.Ollama) && !e.Disabled {
				opts.Runtime = "ollama"
			}
		}
	}
	runtime, err := llmfit.ParseRuntime(opts.Runtime)
	if err != nil {
		return ModelFits{}, err
	}
	if opts.ContextLength <= 0 {
		opts.ContextLength = NullBotContext
	}
	if opts.Limit <= 0 {
		opts.Limit = 20
	}
	ranked := llmfit.Rank(hw, llmfit.Catalog(), llmfit.Options{UseCase: useCase, ContextLength: opts.ContextLength, Runtime: runtime})
	// Only what NullBot can install, and, unless asked, nothing too small
	// to follow an agent's tool instructions.
	fits := []llmfit.Fit{}
	for _, f := range ranked {
		if f.OllamaTag == "" && f.GGUFRepo == "" {
			continue
		}
		if !opts.IncludeSmall && f.Model.ParamsB() < minAgentParamsB {
			continue
		}
		fits = append(fits, f)
		if len(fits) == opts.Limit {
			break
		}
	}
	return ModelFits{Hardware: hw, Options: opts, Fits: fits}, nil
}

// MachineSummary says in a line what this computer has, for people.
func MachineSummary(hw llmfit.Hardware) string {
	parts := []string{}
	if hw.CPUName != "" {
		parts = append(parts, strings.TrimSpace(hw.CPUName))
	}
	if hw.TotalRAMGB > 0 {
		parts = append(parts, formatGiB(hw.TotalRAMGB)+" RAM")
	}
	if g, ok := hw.PrimaryGPU(); ok {
		gpu := g.Name
		if g.Count > 1 {
			gpu = strings.TrimSpace(gpu + " ×" + strconv.Itoa(g.Count))
		}
		if g.UnifiedMemory {
			gpu += " (shared memory)"
		} else if g.VRAMGB > 0 {
			gpu += ", " + formatGiB(g.VRAMGB) + " VRAM"
		}
		parts = append(parts, gpu)
	} else {
		parts = append(parts, "no GPU found")
	}
	return strings.Join(parts, " · ")
}

func formatGiB(gb float64) string {
	if gb >= 10 {
		return strconv.Itoa(int(gb+0.5)) + " GB"
	}
	return strconv.FormatFloat(gb, 'f', 1, 64) + " GB"
}
