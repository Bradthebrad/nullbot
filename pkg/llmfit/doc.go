// Package llmfit looks at the machine's hardware and ranks which local LLMs
// will run well on it: how much memory each model needs, which quantization
// fits, whether it runs on the GPU, offloaded or on the CPU, roughly how many
// tokens per second it will produce, and an overall score.
//
// It is a Go port of the core of llmfit by Alex Jones
// (https://github.com/AlexsJones/llmfit, MIT licensed, ported from version
// 1.1.18). The memory, speed and scoring formulas, the GPU tables and the
// tool-output parsers follow llmfit-core's fit.rs, models.rs, hardware.rs and
// providers.rs; the license is in LICENSE-llmfit next to this file. The
// llmfit binary is not needed.
//
// The three steps are:
//
//	hw, err := llmfit.Detect(ctx)                  // CPU, RAM, GPUs, backend
//	models := llmfit.Catalog()                     // embedded curated model list
//	fits := llmfit.Rank(hw, models, llmfit.Options{Runtime: llmfit.RuntimeOllama, Limit: 10})
//
// Analyze scores a single model. LoadCatalog reads a full hf_models.json from
// llmfit's repository when the embedded subset is not enough.
//
// Left out of the port: the TUI and CLI, benchmarks and measured or community
// throughput, calibration, sharing, self-update, plan and claim, cluster
// (tensor-parallel) mode, TurboQuant KV estimates, prefill/TTFT estimates,
// Intel GPU and Ascend NPU discovery, and the installed-model scans of each
// runtime.
package llmfit
