package llmfit

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
)

// Backend is the acceleration path inference would use.
type Backend string

const (
	BackendCUDA   Backend = "cuda"
	BackendROCm   Backend = "rocm"
	BackendMetal  Backend = "metal"
	BackendVulkan Backend = "vulkan" // AMD without ROCm, and other GPUs
	BackendSYCL   Backend = "sycl"   // Intel GPUs (oneAPI)
	BackendCPU    Backend = "cpu"
)

// GPU is one detected GPU model. Identical cards are grouped with a Count.
type GPU struct {
	Name string `json:"name"`
	// VRAMGB is per card; 0 when unknown. For unified memory it is the
	// shared system pool.
	VRAMGB float64 `json:"vram_gb"`
	// FreeVRAMGB is free right now, summed over the cards, when the driver
	// reports it for every card (nvidia-smi, amdgpu sysfs); nil otherwise.
	FreeVRAMGB    *float64 `json:"free_vram_gb,omitempty"`
	Count         int      `json:"count"`
	Backend       Backend  `json:"backend"`
	UnifiedMemory bool     `json:"unified_memory,omitempty"`
}

// Hardware describes the machine the models would run on.
type Hardware struct {
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	CPUName  string `json:"cpu_name"`
	CPUCores int    `json:"cpu_cores"`
	// TotalRAMGB and AvailableRAMGB are GiB, like every *GB field here.
	TotalRAMGB     float64 `json:"total_ram_gb"`
	AvailableRAMGB float64 `json:"available_ram_gb"`
	// GPUs lists the usable GPUs, the one with the most VRAM first.
	GPUs []GPU `json:"gpus,omitempty"`
	// Backend is the primary GPU's backend, or BackendCPU.
	Backend Backend `json:"backend"`
	// UnifiedMemory is set when the primary GPU shares system RAM (Apple
	// Silicon, AMD Ryzen AI MAX, NVIDIA GB10 and Jetson).
	UnifiedMemory bool `json:"unified_memory,omitempty"`
	// RAMBandwidthGBps is measured system RAM bandwidth, or 0 when not
	// measured (see DetectOptions.MeasureRAMBandwidth).
	RAMBandwidthGBps float64 `json:"ram_bandwidth_gbps,omitempty"`
}

// HasGPU reports whether any GPU was found.
func (h Hardware) HasGPU() bool { return len(h.GPUs) > 0 }

// PrimaryGPU returns the GPU with the most VRAM, if any.
func (h Hardware) PrimaryGPU() (GPU, bool) {
	if len(h.GPUs) == 0 {
		return GPU{}, false
	}
	return h.GPUs[0], true
}

// GPUName is the primary GPU's name, or "".
func (h Hardware) GPUName() string {
	g, _ := h.PrimaryGPU()
	return g.Name
}

// GPUVRAMGB is the primary GPU's per-card VRAM, or 0.
func (h Hardware) GPUVRAMGB() float64 {
	g, _ := h.PrimaryGPU()
	return g.VRAMGB
}

// TotalVRAMGB sums VRAM over every card. llama.cpp and vLLM split a model
// across cards, so this is the pool a discrete-GPU fit is graded against.
func (h Hardware) TotalVRAMGB() float64 {
	total := 0.0
	for _, g := range h.GPUs {
		total += g.VRAMGB * float64(g.Count)
	}
	return total
}

// gpuFitPoolGB is the VRAM free for weights: free VRAM when every discrete
// card reports it, else the total. Unified memory keeps the whole pool.
func (h Hardware) gpuFitPoolGB() float64 {
	total := h.TotalVRAMGB()
	if total == 0 {
		total = h.GPUVRAMGB()
	}
	if h.UnifiedMemory || len(h.GPUs) == 0 {
		return total
	}
	free := 0.0
	for _, g := range h.GPUs {
		if g.FreeVRAMGB == nil {
			return total
		}
		free += *g.FreeVRAMGB
	}
	return min(free, total)
}

// Runner runs an external program and returns its standard output. When
// the program exits with an error it should still return what was
// written, along with the error. A missing program is just an error.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// DetectOptions adjusts Detect. The zero value detects the real machine.
type DetectOptions struct {
	// Run runs helper programs (nvidia-smi, rocm-smi, system_profiler,
	// powershell, ...). Nil runs them with os/exec.
	Run Runner
	// FS is the root file system, read for /proc and /sys on Linux. Nil
	// means the real one.
	FS fs.FS
	// CommandTimeout bounds each helper program. Zero means 8 seconds.
	CommandTimeout time.Duration
	// MeasureRAMBandwidth runs MeasureRAMBandwidth and stores the result in
	// Hardware.RAMBandwidthGBps. It takes about 100 ms and up to 512 MiB
	// of short-lived buffers.
	MeasureRAMBandwidth bool
}

// Detect inspects this machine: CPU, RAM, GPUs and the backend inference
// would use. Missing GPU tools are not errors; it returns an error only when
// ctx ends or system memory can't be read, and even then fills in what it
// could.
func Detect(ctx context.Context) (Hardware, error) {
	return DetectWith(ctx, DetectOptions{})
}

// DetectWith is Detect with options, mainly to replay recorded tool output
// in tests.
func DetectWith(ctx context.Context, opts DetectOptions) (Hardware, error) {
	d := newDetector(opts)
	return d.detect(ctx)
}

const defaultCommandTimeout = 8 * time.Second

// detector holds one detection run. Helper programs run at most once each,
// and the ones every run of this OS needs start in parallel up front.
type detector struct {
	run     Runner
	fsys    fs.FS
	timeout time.Duration
	goos    string
	goarch  string
	// host reads total and available RAM and the CPU name from the OS.
	host    func(ctx context.Context, d *detector) (hostInfo, error)
	measure bool

	mu   sync.Mutex
	runs map[string]*cmdResult
}

type cmdResult struct {
	done chan struct{}
	out  []byte
	err  error
}

type hostInfo struct {
	cpuName              string
	totalGB, availableGB float64
}

func newDetector(opts DetectOptions) *detector {
	d := &detector{
		run:     opts.Run,
		fsys:    opts.FS,
		timeout: opts.CommandTimeout,
		goos:    runtime.GOOS,
		goarch:  runtime.GOARCH,
		host:    nativeHost,
		measure: opts.MeasureRAMBandwidth,
		runs:    map[string]*cmdResult{},
	}
	if d.run == nil {
		d.run = execRunner
	}
	if d.fsys == nil {
		d.fsys = os.DirFS("/")
	}
	if d.timeout <= 0 {
		d.timeout = defaultCommandTimeout
	}
	return d
}

// execRunner runs a program for real.
func execRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	hideWindow(cmd)
	// Don't wait forever on pipes held open by a grandchild once the
	// program itself has been killed.
	cmd.WaitDelay = time.Second
	return cmd.Output()
}

// output runs a helper program once per detection, with a timeout, and
// returns its standard output.
func (d *detector) output(ctx context.Context, name string, args ...string) ([]byte, error) {
	key := name + "\x00" + strings.Join(args, "\x00")
	d.mu.Lock()
	r, ok := d.runs[key]
	if !ok {
		r = &cmdResult{done: make(chan struct{})}
		d.runs[key] = r
		d.mu.Unlock()
		go func() {
			defer close(r.done)
			cctx, cancel := context.WithTimeout(ctx, d.timeout)
			defer cancel()
			r.out, r.err = d.run(cctx, name, args...)
		}()
	} else {
		d.mu.Unlock()
	}
	select {
	case <-r.done:
		return r.out, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// prefetch starts programs in the background so their output is ready
// when the detection logic asks for it.
func (d *detector) prefetch(ctx context.Context, cmds ...[]string) {
	for _, c := range cmds {
		go func() { _, _ = d.output(ctx, c[0], c[1:]...) }()
	}
}

// text returns a program's output as a string, or "" if it failed.
func (d *detector) text(ctx context.Context, name string, args ...string) (string, bool) {
	out, err := d.output(ctx, name, args...)
	if err != nil {
		return "", false
	}
	return string(out), true
}

func (d *detector) readFile(name string) (string, error) {
	b, err := fs.ReadFile(d.fsys, name)
	return string(b), err
}

// Commands whose output detection parses.
var (
	cmdNvidiaExtended = []string{"nvidia-smi", "--query-gpu=addressing_mode,memory.total,memory.free,name", "--format=csv,noheader,nounits"}
	cmdNvidiaList     = []string{"nvidia-smi", "--query-gpu=memory.total,name", "--format=csv,noheader,nounits"}
	cmdROCmVRAM       = []string{"rocm-smi", "--showmeminfo", "vram"}
	cmdROCmProduct    = []string{"rocm-smi", "--showproductname"}
	cmdVulkanSummary  = []string{"vulkaninfo", "--summary"}
	cmdLspci          = []string{"lspci", "-nnD"}
	cmdMacDisplays    = []string{"system_profiler", "SPDisplaysDataType", "-json"}
	cmdMacDisplaysTxt = []string{"system_profiler", "SPDisplaysDataType"}
	cmdVMStat         = []string{"vm_stat"}
	cmdWinVideo       = []string{"powershell", "-NoProfile", "-Command",
		"Get-CimInstance Win32_VideoController | Select-Object Name,AdapterRAM | ForEach-Object { $_.Name + '|' + $_.AdapterRAM }"}
	cmdWinRegistryVRAM = []string{"powershell", "-NoProfile", "-Command", windowsRegistryVRAMScript}
	cmdWinPhysicalRAM  = []string{"powershell", "-NoProfile", "-Command",
		"(Get-CimInstance Win32_PhysicalMemory | Measure-Object -Property Capacity -Sum).Sum"}
)

// windowsRegistryVRAMScript prints "DriverDesc|bytes" for each display
// adapter from the 64-bit size the driver writes to the registry, because
// WMI's AdapterRAM is 32-bit and caps at 4 GB.
const windowsRegistryVRAMScript = `Get-ItemProperty -Path 'HKLM:\SYSTEM\CurrentControlSet\Control\Class\{4d36e968-e325-11ce-bfc1-08002be10318}\*' -ErrorAction SilentlyContinue | ForEach-Object { $d = $_.DriverDesc; $m = $_.'HardwareInformation.qwMemorySize'; if ($null -eq $m) { $m = $_.'HardwareInformation.MemorySize' }; if ($m -is [byte[]]) { if ($m.Length -ge 8) { $m = [System.BitConverter]::ToUInt64($m, 0) } elseif ($m.Length -ge 4) { $m = [System.BitConverter]::ToUInt32($m, 0) } else { $m = $null } } elseif ($m -is [int] -and $m -lt 0) { $m = [long]$m + 4294967296 }; if ($d -and $m) { $d + '|' + $m } }`

func (d *detector) detect(ctx context.Context) (Hardware, error) {
	switch d.goos {
	case "linux":
		d.prefetch(ctx, cmdNvidiaExtended, cmdROCmVRAM, cmdROCmProduct, cmdVulkanSummary)
	case "windows":
		d.prefetch(ctx, cmdNvidiaExtended, cmdWinVideo, cmdWinRegistryVRAM, cmdVulkanSummary)
	case "darwin":
		d.prefetch(ctx, cmdMacDisplays, cmdVMStat)
	}

	hw := Hardware{OS: d.goos, Arch: d.goarch, CPUCores: runtime.NumCPU()}
	var errs []error
	host, err := d.host(ctx, d)
	if err != nil {
		errs = append(errs, fmt.Errorf("reading system memory: %w", err))
	}
	hw.CPUName = cmp.Or(strings.TrimSpace(host.cpuName), "Unknown CPU")
	hw.TotalRAMGB, hw.AvailableRAMGB = host.totalGB, host.availableGB
	if hw.AvailableRAMGB <= 0 && hw.TotalRAMGB > 0 {
		// Same last resort as llmfit: assume most of it is free.
		hw.AvailableRAMGB = hw.TotalRAMGB * 0.8
	}

	// A BIOS carve-out for an AMD APU's GPU hides that memory from the OS;
	// add it back so the unified pool is graded against what is installed.
	if isAMDAPU(hw.CPUName) {
		hw.TotalRAMGB = d.apuTotalRAMGB(ctx, hw.TotalRAMGB)
	}

	hw.GPUs = d.detectGPUs(ctx, hw.TotalRAMGB, hw.CPUName)
	hw.Backend = BackendCPU
	if g, ok := hw.PrimaryGPU(); ok {
		hw.Backend = g.Backend
		hw.UnifiedMemory = g.UnifiedMemory
	}
	if d.measure {
		hw.RAMBandwidthGBps = MeasureRAMBandwidth()
	}
	if err := ctx.Err(); err != nil {
		errs = append(errs, err)
	}
	return hw, errors.Join(errs...)
}

// detectGPUs runs the probes for this OS and returns the usable GPUs, the
// one with the most VRAM first.
func (d *detector) detectGPUs(ctx context.Context, totalRAMGB float64, cpuName string) []GPU {
	var gpus []GPU
	switch d.goos {
	case "darwin":
		if out, ok := d.text(ctx, cmdMacDisplays[0], cmdMacDisplays[1:]...); ok {
			gpus = parseMacDisplaysJSON([]byte(out), totalRAMGB)
		}
		// The text report is the fallback when the JSON names no Apple GPU.
		if !slices.ContainsFunc(gpus, func(g GPU) bool { return g.Backend == BackendMetal && g.UnifiedMemory }) {
			if out, ok := d.text(ctx, cmdMacDisplaysTxt[0], cmdMacDisplaysTxt[1:]...); ok && isAppleGPUReport(out) {
				name := "Apple Silicon"
				if strings.Contains(strings.ToLower(cpuName), "apple") {
					name = cpuName
				}
				gpus = append(gpus, GPU{Name: name, VRAMGB: totalRAMGB, Count: 1, Backend: BackendMetal, UnifiedMemory: true})
			}
		}
		// macOS lists both built-in GPUs on dual-GPU Intel Macs, as
		// llama.cpp's Metal device list does, so nothing is dropped here.
		sortGPUs(gpus)
		return gpus

	case "linux", "windows":
		gpus = append(gpus, d.nvidiaGPUs(ctx, totalRAMGB)...)
		if d.goos == "linux" {
			gpus = append(gpus, d.amdLinuxGPUs(ctx)...)
		} else {
			for _, g := range d.windowsGPUs(ctx) {
				// Skip adapters a vendor tool already reported.
				dominated := slices.ContainsFunc(gpus, func(e GPU) bool {
					el, wl := strings.ToLower(e.Name), strings.ToLower(g.Name)
					return strings.Contains(el, wl) || strings.Contains(wl, el)
				})
				if !dominated {
					gpus = append(gpus, g)
				}
			}
		}
		gpus = applyAMDUnifiedAPU(gpus, cpuName, totalRAMGB)
		// GB10 and Jetson share system RAM; nvidia-smi may report 0 or a
		// small dedicated slice for them.
		for i := range gpus {
			if isNvidiaUnifiedMemoryGPU(gpus[i].Name) {
				gpus[i].UnifiedMemory = true
				gpus[i].VRAMGB = totalRAMGB
				gpus[i].FreeVRAMGB = nil
			}
		}
		// Vulkan catches GPUs without a vendor tool; earlier sources win
		// because they report better names and VRAM.
		gpus = mergeGPUSources(gpus, d.vulkanGPUs(ctx))
		gpus = preferDiscreteGPUs(gpus)
		sortGPUs(gpus)
		return gpus

	default:
		gpus = d.nvidiaGPUs(ctx, totalRAMGB)
		sortGPUs(gpus)
		return gpus
	}
}

// sortGPUs puts the GPU with the most VRAM first; it is the primary.
func sortGPUs(gpus []GPU) {
	slices.SortStableFunc(gpus, func(a, b GPU) int { return cmp.Compare(b.VRAMGB, a.VRAMGB) })
}

func (d *detector) nvidiaGPUs(ctx context.Context, totalRAMGB float64) []GPU {
	// The extended query reports unified memory (addressing mode ATS) and
	// free VRAM; older drivers reject it, so fall back to the basic one.
	if out, ok := d.text(ctx, cmdNvidiaExtended[0], cmdNvidiaExtended[1:]...); ok {
		return parseNvidiaSMIExtended(out, totalRAMGB)
	}
	if out, ok := d.text(ctx, cmdNvidiaList[0], cmdNvidiaList[1:]...); ok {
		return parseNvidiaSMIList(out)
	}
	return nil
}

func (d *detector) amdLinuxGPUs(ctx context.Context) []GPU {
	if vram, ok := d.text(ctx, cmdROCmVRAM[0], cmdROCmVRAM[1:]...); ok {
		product, _ := d.text(ctx, cmdROCmProduct[0], cmdROCmProduct[1:]...)
		if gpus := parseROCmSMI(vram, product); len(gpus) > 0 {
			return gpus
		}
	}
	// Without ROCm, amdgpu's sysfs still reports each card's VRAM.
	drm, err := fs.Sub(d.fsys, "sys/class/drm")
	if err != nil {
		return nil
	}
	return groupAMDSysfsCards(d.scanAMDSysfs(ctx, drm))
}

// scanAMDSysfs reads the AMD cards under a /sys/class/drm tree, naming each
// from lspci.
func (d *detector) scanAMDSysfs(ctx context.Context, drm fs.FS) []amdCard {
	var lspci *string
	return scanAMDSysfsCards(drm, func(_ string, slots []string) string {
		if lspci == nil {
			out, _ := d.text(ctx, cmdLspci[0], cmdLspci[1:]...)
			lspci = &out
		}
		return lspciGPUName(*lspci, slots, isAMDLspciLine)
	})
}

func (d *detector) windowsGPUs(ctx context.Context) []GPU {
	out, ok := d.text(ctx, cmdWinVideo[0], cmdWinVideo[1:]...)
	if !ok {
		return nil
	}
	gpus := parseWindowsGPUList(out)
	// The registry probe can exit non-zero over one unreadable key while
	// its output is still good, so the output is used either way.
	reg, _ := d.output(ctx, cmdWinRegistryVRAM[0], cmdWinRegistryVRAM[1:]...)
	gpus = applyRegistryVRAM(gpus, parseWindowsRegistryVRAM(string(reg)))
	return preferDiscreteGPUs(gpus)
}

func (d *detector) vulkanGPUs(ctx context.Context) []GPU {
	out, ok := d.text(ctx, cmdVulkanSummary[0], cmdVulkanSummary[1:]...)
	if !ok {
		// Old vulkaninfo has no --summary.
		if out, ok = d.text(ctx, "vulkaninfo"); !ok {
			return nil
		}
	}
	return parseVulkanGPUs(out)
}

// apuTotalRAMGB recovers RAM an AMD APU's BIOS carve-out hides from the OS:
// on Windows from the installed DIMM total, on Linux by adding back the
// integrated GPU's VRAM from sysfs. Anything uncertain keeps the OS figure.
func (d *detector) apuTotalRAMGB(ctx context.Context, osTotalGB float64) float64 {
	switch d.goos {
	case "windows":
		out, ok := d.text(ctx, cmdWinPhysicalRAM[0], cmdWinPhysicalRAM[1:]...)
		if !ok {
			return osTotalGB
		}
		var bytes uint64
		if _, err := fmt.Sscan(strings.TrimSpace(out), &bytes); err != nil || bytes == 0 {
			return osTotalGB
		}
		physical := float64(bytes) / (1 << 30)
		// Ordinary reserved memory is well under 1 GB; carve-outs start at 1.
		if physical-osTotalGB >= 1 {
			return physical
		}
		return osTotalGB
	case "linux":
		drm, err := fs.Sub(d.fsys, "sys/class/drm")
		if err != nil {
			return osTotalGB
		}
		if carveout, ok := apuCarveoutGB(d.scanAMDSysfs(ctx, drm)); ok {
			return osTotalGB + carveout
		}
	}
	return osTotalGB
}

// availableFromVMStat reads macOS free memory: free + inactive + purgeable
// pages.
func (d *detector) availableFromVMStat(ctx context.Context) float64 {
	out, ok := d.text(ctx, cmdVMStat[0], cmdVMStat[1:]...)
	if !ok {
		return 0
	}
	return parseVMStat(out)
}
