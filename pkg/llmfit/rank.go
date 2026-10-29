package llmfit

import (
	"cmp"
	"runtime"
	"slices"
	"sync"
	"time"
)

// Rank analyzes every model on hw and returns the ones that fit, best
// first: models that don't fit last, then by score, then by name so ties
// order the same way every run.
//
// Like llmfit's recommend command it skips models the hardware can't run
// at all (MLX repos off Apple Silicon, pre-quantized repos without CUDA or
// ROCm), catalog entries that fail llmfit's sanity checks, models below
// opts.MinFit and, when opts.UseCase is set, other use cases. A specific
// opts.Runtime also drops models that runtime can't load, such as AWQ
// repos for Ollama or ternary models for MLX.
func Rank(hw Hardware, models []Model, opts Options) []Fit {
	appleSilicon := hw.Backend == BackendMetal && hw.UnifiedMemory
	minFit := opts.MinFit
	if minFit == "" {
		minFit = FitMarginal
	}
	if opts.Now.IsZero() {
		// One clock reading for the whole ranking.
		opts.Now = time.Now()
	}
	want := opts.Runtime.engine()

	var fits []Fit
	for _, m := range models {
		if !backendCompatible(m, hw) || m.SanityIssue() != "" || (!appleSilicon && m.isMLXOnly()) {
			continue
		}
		// Forcing llama.cpp or MLX onto a pre-quantized repo would size it
		// along a ladder it can't be converted to.
		if (want == RuntimeLlamaCpp || want == RuntimeMLX) && m.isPrequantized() {
			continue
		}
		f := Analyze(m, hw, opts)
		if f.Level.rank() < minFit.rank() || (opts.UseCase != "" && f.UseCase != opts.UseCase) {
			continue
		}
		if want != RuntimeAuto && f.Runtime.engine() != want {
			continue
		}
		fits = append(fits, f)
	}
	SortFits(fits)
	if opts.Limit > 0 && len(fits) > opts.Limit {
		fits = fits[:opts.Limit]
	}
	return fits
}

// SortFits orders fits as Rank does.
func SortFits(fits []Fit) {
	slices.SortStableFunc(fits, func(a, b Fit) int {
		aRuns, bRuns := a.Level != FitTooTight, b.Level != FitTooTight
		if aRuns != bRuns {
			if aRuns {
				return -1
			}
			return 1
		}
		return cmp.Or(cmp.Compare(b.Score, a.Score), cmp.Compare(a.Model.Name, b.Model.Name))
	})
}

// MeasureRAMBandwidth measures achievable system RAM bandwidth in GB/s
// with a short parallel copy sweep (about 100 ms, 64 MiB per thread on up
// to 8 threads), counting bytes read plus written as STREAM does. This is
// what MoE expert streaming sees, typically 60-80% of the spec sheet. It
// returns 0 when the result is implausible, such as on a starved VM.
func MeasureRAMBandwidth() float64 {
	const bufBytes = 32 << 20 // per buffer: well past any L3 cache
	const window = 80 * time.Millisecond
	threads := min(runtime.NumCPU(), 8)

	results := make([]float64, threads)
	var ready, done sync.WaitGroup
	start := make(chan struct{})
	ready.Add(threads)
	done.Add(threads)
	for i := range threads {
		go func() {
			defer done.Done()
			src := make([]byte, bufBytes)
			for j := range src {
				src[j] = 1
			}
			dst := make([]byte, bufBytes)
			copy(dst, src) // fault the pages in before timing
			ready.Done()
			<-start
			begin := time.Now()
			passes := 0
			for time.Since(begin) < window {
				copy(dst, src)
				passes++
			}
			results[i] = float64(passes) * 2 * bufBytes / time.Since(begin).Seconds() / 1e9
		}()
	}
	ready.Wait()
	close(start)
	done.Wait()

	total := 0.0
	for _, r := range results {
		total += r
	}
	// Under 2 GB/s the measurement was starved; over 4000 it hit cache.
	if total < 2 || total > 4000 {
		return 0
	}
	return total
}
