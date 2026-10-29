package llmfit

import "strings"

// The tables match lowercase substrings of the GPU name, first match wins,
// so more specific names ("4070 ti super") come before shorter ones
// ("4070"). Values are from llmfit's hardware.rs.

type nameValue struct {
	match string
	value float64
}

func lookupName(table []nameValue, lower string) (float64, bool) {
	for _, e := range table {
		if strings.Contains(lower, e.match) {
			return e.value, true
		}
	}
	return 0, false
}

// isMobileGPUName: laptop parts share model numbers with desktop cards but
// not their memory bus (an RTX 5070 Laptop is 128-bit, the desktop card
// 192-bit), so the model-number tables don't apply to them.
func isMobileGPUName(lower string) bool {
	return strings.Contains(lower, "laptop") || strings.Contains(lower, "mobile") || strings.Contains(lower, "max-q")
}

// gpuBandwidthTable is memory bandwidth in GB/s. Token generation reads
// every active weight once per token, so this sets the speed ceiling.
var gpuBandwidthTable = []nameValue{
	// NVIDIA GeForce RTX 50 (Blackwell)
	{"5090", 1792}, {"5080", 960}, {"5070 ti", 896}, {"5070", 672}, {"5060 ti", 448}, {"5060", 256},
	// RTX 40 (Ada Lovelace)
	{"4090", 1008}, {"4080 super", 736}, {"4080", 717}, {"4070 ti super", 672}, {"4070 ti", 504},
	{"4070 super", 504}, {"4070", 504}, {"4060 ti", 288}, {"4060", 272},
	// RTX 30 (Ampere)
	{"3090 ti", 1008}, {"3090", 936}, {"3080 ti", 912}, {"3080", 760}, {"3070 ti", 608}, {"3070", 448},
	{"3060 ti", 448}, {"3060", 360},
	// RTX 20 and GTX 16 (Turing)
	{"2080 ti", 616}, {"2080 super", 496}, {"2080", 448}, {"2070 super", 448}, {"2070", 448},
	{"2060 super", 448}, {"2060", 336},
	{"1660 ti", 288}, {"1660 super", 336}, {"1660", 192}, {"1650 super", 192}, {"1650", 128},
	// NVIDIA data center and professional
	{"h100 sxm", 3350}, {"h100", 2039}, {"h200", 4800}, {"a100 sxm", 2039}, {"a100", 1555},
	{"l40s", 864}, {"l40", 864}, {"l4", 300}, {"a10g", 600}, {"a10", 600}, {"t4", 320},
	{"v100 sxm", 900}, {"v100", 897}, {"a6000", 768}, {"a5000", 768}, {"a4000", 448},
	// AMD Ryzen AI MAX (Strix Halo): 256-bit LPDDR5X-8000
	{"8060s", 256}, {"8050s", 256}, {"strix halo", 256}, {"ryzen ai max", 256},
	// AMD Radeon RX 9000 / 7000 / 6000
	{"9070 xt", 624}, {"9070", 488},
	{"7900 xtx", 960}, {"7900 xt", 800}, {"7900 gre", 576}, {"7800 xt", 624}, {"7700 xt", 432}, {"7600", 288},
	{"6950 xt", 576}, {"6900 xt", 512}, {"6800 xt", 512}, {"6800", 512}, {"6700 xt", 384},
	{"6600 xt", 256}, {"6600", 224},
	// AMD Instinct (CDNA)
	{"mi300x", 5300}, {"mi300", 5300}, {"mi250x", 3277}, {"mi250", 3277}, {"mi210", 1638}, {"mi100", 1229},
	// Apple Silicon unified memory
	{"m5 max", 614}, {"m5 pro", 307}, {"m5", 153.6},
	{"m4 ultra", 819}, {"m4 max", 546}, {"m4 pro", 273}, {"m4", 120},
	{"m3 ultra", 800}, {"m3 max", 400}, {"m3 pro", 150}, {"m3", 100},
	{"m2 ultra", 800}, {"m2 max", 400}, {"m2 pro", 200}, {"m2", 100},
	{"m1 ultra", 800}, {"m1 max", 400}, {"m1 pro", 200}, {"m1", 68},
}

// GPUBandwidthGBps returns a GPU's memory bandwidth in GB/s from its name,
// or false for unknown and laptop GPUs. Speed estimates then fall back to a
// per-backend constant.
func GPUBandwidthGBps(name string) (float64, bool) {
	lower := strings.ToLower(name)
	if isMobileGPUName(lower) {
		return 0, false
	}
	return lookupName(gpuBandwidthTable, lower)
}

// vramTable is VRAM in GiB by name, for tools that report 0 or a capped
// figure.
var vramTable = []nameValue{
	{"5090", 32}, {"5080", 16}, {"5070 ti", 16}, {"5070", 12}, {"5060 ti", 16}, {"5060", 8},
	{"4090", 24}, {"4080", 16}, {"4070 ti", 12}, {"4070", 12}, {"4060 ti", 16}, {"4060", 8},
	{"3090", 24}, {"3080 ti", 12}, {"3080", 10}, {"3070", 8}, {"3060 ti", 8}, {"3060", 12},
	{"h100", 80}, {"a100", 80}, {"l40", 48},
	// RTX professional (Ampere) before the broad "a10".
	{"a6000", 48}, {"a5500", 24}, {"a5000", 24}, {"a4500", 20}, {"a4000", 16}, {"a2000", 12},
	{"a10", 24}, {"t4", 16},
	// NVIDIA Grace / DGX Spark; 2e12 is GB10's PCI device ID.
	{"gb10", 128}, {"2e12", 128}, {"gb20", 128},
	// Radeon AI PRO R9700 before the RX 9000 entries.
	{"r9700", 32},
	{"9070 xt", 16}, {"9070", 12}, {"9060 xt", 16}, {"9060", 8},
	{"7900 xtx", 24}, {"7900", 20}, {"7800", 16}, {"7700", 12}, {"7600", 8},
	{"6950", 16}, {"6900", 16}, {"6800", 16}, {"6750", 12}, {"6700", 12}, {"6650", 8}, {"6600", 8}, {"6500", 4},
	{"5700 xt", 8}, {"5700", 8}, {"5600", 6}, {"5500", 4},
	// Ryzen AI iGPUs, for name-only detection.
	{"8060s", 32}, {"8050s", 24}, {"8060", 16}, {"8050", 12},
	{"890m", 16}, {"880m", 12}, {"870m", 8}, {"860m", 8},
}

// estimateVRAMFromName guesses VRAM in GiB from a GPU name, or 0.
func estimateVRAMFromName(name string) float64 {
	lower := strings.ToLower(name)
	if !isMobileGPUName(lower) {
		if v, ok := lookupName(vramTable, lower); ok {
			return v
		}
	}
	return estimateVRAMGeneric(lower)
}

// estimateVRAMGeneric is the per-family default when the model number is
// unknown or belongs to a laptop part.
func estimateVRAMGeneric(lower string) float64 {
	// An AMD iGPU ("AMD Radeon(TM) Graphics") has almost no dedicated VRAM.
	if (strings.Contains(lower, "radeon") || strings.Contains(lower, "amd")) &&
		!strings.Contains(lower, "rx ") && !strings.Contains(lower, "hd ") &&
		!strings.Contains(lower, " r5 ") && !strings.Contains(lower, " r7 ") && !strings.Contains(lower, " r9 ") &&
		!strings.Contains(lower, "8060") && !strings.Contains(lower, "8050") &&
		(strings.Contains(lower, "graphics") || strings.Contains(lower, "igpu")) {
		return 0.5
	}
	switch {
	case strings.Contains(lower, "rtx"):
		return 8
	case strings.Contains(lower, "gtx"):
		return 4
	case strings.Contains(lower, "rx ") || strings.Contains(lower, "radeon"):
		return 8
	}
	return 0
}

// computeCapability is an NVIDIA GPU's CUDA compute capability, used to tell
// whether vLLM can run a pre-quantized format on it.
type computeCapability struct{ major, minor int }

func (c computeCapability) less(o computeCapability) bool {
	return c.major < o.major || c.major == o.major && c.minor < o.minor
}

var computeCapabilityTable = []struct {
	matches []string
	cc      computeCapability
}{
	{[]string{"5090", "5080", "5070", "5060", "b200", "b100", "gb200", "gb100"}, computeCapability{10, 0}},
	{[]string{"h100", "h200"}, computeCapability{9, 0}},
	{[]string{"4090", "4080", "4070", "4060", "l40", "l4"}, computeCapability{8, 9}},
	{[]string{"a100"}, computeCapability{8, 0}},
	{[]string{"3090", "3080", "3070", "3060", "a10", "a6000", "a5000", "a4000", "a2000", "a16"}, computeCapability{8, 6}},
	{[]string{"2080", "2070", "2060", "1660", "1650", "t4"}, computeCapability{7, 5}},
	{[]string{"v100", "titan v"}, computeCapability{7, 0}},
	{[]string{"p100", "1080", "1070", "1060", "1050", "p40", "p4"}, computeCapability{6, 1}},
}

func gpuComputeCapability(name string) (computeCapability, bool) {
	lower := strings.ToLower(name)
	for _, e := range computeCapabilityTable {
		for _, m := range e.matches {
			if strings.Contains(lower, m) {
				return e.cc, true
			}
		}
	}
	return computeCapability{}, false
}

func (c computeCapability) label() string {
	switch {
	case c.major == 10 || c.major == 12:
		return "Blackwell"
	case c.major == 9:
		return "Hopper"
	case c.major == 8 && c.minor == 9:
		return "Ada Lovelace"
	case c.major == 8:
		return "Ampere"
	case c.major == 7 && c.minor == 5:
		return "Turing"
	case c.major == 7:
		return "Volta"
	case c.major == 6:
		return "Pascal"
	}
	return "an older generation"
}

// quantMinCC is the oldest GPU vLLM can run a pre-quantized format on.
func quantMinCC(format string) (computeCapability, bool) {
	switch format {
	case "AWQ-4bit", "AWQ-8bit", "GPTQ-Int4", "GPTQ-Int8", "NVFP4":
		return computeCapability{7, 5}, true
	case "MXFP4":
		// vLLM's Marlin MXFP4 kernels exclude Turing.
		return computeCapability{8, 0}, true
	}
	return computeCapability{}, false
}

// quantNativeMinCC: FP4 formats load from Turing/Ampere on, but only run on
// their own fast kernels from Blackwell.
func quantNativeMinCC(format string) (computeCapability, bool) {
	switch format {
	case "NVFP4", "MXFP4":
		return computeCapability{10, 0}, true
	}
	return computeCapability{}, false
}

// kernelSupport says how a GPU runs a pre-quantized format under vLLM.
type kernelSupport int

const (
	kernelUnknown kernelSupport = iota
	kernelNative
	kernelFallback    // loads, but through a slower dequantizing kernel
	kernelUnsupported // no kernel on this GPU runs it
)

// effectiveKernelFormat: a repo named "...-NVFP4-AWQ" holds NVFP4 tensors
// whatever its catalog label says, and that decides the kernels.
func effectiveKernelFormat(quant, modelName string) string {
	lower := strings.ToLower(modelName)
	switch {
	case strings.Contains(lower, "nvfp4"):
		return "NVFP4"
	case strings.Contains(lower, "mxfp4"):
		return "MXFP4"
	}
	return quant
}

func quantKernelSupport(format, gpuName string) kernelSupport {
	minCC, ok := quantMinCC(format)
	if !ok {
		return kernelUnknown
	}
	cc, ok := gpuComputeCapability(gpuName)
	if !ok {
		return kernelUnknown
	}
	if cc.less(minCC) {
		return kernelUnsupported
	}
	if native, ok := quantNativeMinCC(format); ok && cc.less(native) {
		return kernelFallback
	}
	return kernelNative
}
