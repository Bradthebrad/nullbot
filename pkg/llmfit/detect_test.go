package llmfit

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"
)

// fakeRunner answers commands from canned output keyed by the program and
// its first argument; anything else behaves like a missing program.
type fakeRunner struct {
	outputs map[string]string
	calls   atomic.Int32
}

func (f *fakeRunner) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	f.calls.Add(1)
	key := name
	if len(args) > 0 {
		key += " " + args[0]
	}
	if out, ok := f.outputs[key]; ok {
		return []byte(out), nil
	}
	return nil, &exec.Error{Name: name, Err: exec.ErrNotFound}
}

// testDetector replays one platform's tools on any machine.
func testDetector(goos string, run Runner, fsys fstest.MapFS, host hostInfo) *detector {
	d := newDetector(DetectOptions{Run: run, FS: fsys, CommandTimeout: time.Second})
	d.goos, d.goarch = goos, "amd64"
	d.host = func(context.Context, *detector) (hostInfo, error) { return host, nil }
	return d
}

func linuxFS() fstest.MapFS {
	return fstest.MapFS{
		"proc/meminfo": {Data: []byte("MemTotal:       65536000 kB\nMemFree:  1000 kB\nMemAvailable:   49152000 kB\n")},
		"proc/cpuinfo": {Data: []byte("processor\t: 0\nmodel name\t: AMD Ryzen 9 7950X 16-Core Processor\n")},
	}
}

func TestDetectLinuxNvidia(t *testing.T) {
	run := &fakeRunner{outputs: map[string]string{
		"nvidia-smi --query-gpu=addressing_mode,memory.total,memory.free,name": "None, 24564, 20000, NVIDIA GeForce RTX 4090\n",
	}}
	d := newDetector(DetectOptions{Run: run.run, FS: linuxFS()})
	d.goos, d.goarch, d.host = "linux", "amd64", linuxHost
	hw, err := d.detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if hw.CPUName != "AMD Ryzen 9 7950X 16-Core Processor" || !near(hw.TotalRAMGB, 62.5, 1e-9) || !near(hw.AvailableRAMGB, 46.875, 1e-9) {
		t.Fatalf("host: %+v", hw)
	}
	if hw.Backend != BackendCUDA || hw.UnifiedMemory || hw.GPUName() != "NVIDIA GeForce RTX 4090" || hw.GPUVRAMGB() < 23 {
		t.Fatalf("GPU: %+v", hw)
	}
}

func TestDetectLinuxNoTools(t *testing.T) {
	run := &fakeRunner{}
	d := newDetector(DetectOptions{Run: run.run, FS: linuxFS()})
	d.goos, d.goarch, d.host = "linux", "arm64", linuxHost
	hw, err := d.detect(context.Background())
	if err != nil {
		t.Fatalf("missing tools must not be an error: %v", err)
	}
	if hw.HasGPU() || hw.Backend != BackendCPU || hw.TotalRAMGB == 0 {
		t.Fatalf("got %+v", hw)
	}
	if run.calls.Load() == 0 {
		t.Fatal("no probes ran")
	}
}

func TestDetectLinuxAMDWithoutROCm(t *testing.T) {
	fsys := linuxFS()
	fsys["sys/class/drm/card1/device/vendor"] = &fstest.MapFile{Data: []byte("0x1002\n")}
	fsys["sys/class/drm/card1/device/mem_info_vram_total"] = &fstest.MapFile{Data: []byte("17163091968\n")}
	fsys["sys/class/drm/card1/device/uevent"] = &fstest.MapFile{Data: []byte("DRIVER=amdgpu\nPCI_SLOT_NAME=0000:03:00.0\n")}
	fsys["sys/class/drm/card1-DP-1/status"] = &fstest.MapFile{Data: []byte("connected\n")}
	run := &fakeRunner{outputs: map[string]string{
		"lspci -nnD": "0000:03:00.0 VGA compatible controller [0300]: Advanced Micro Devices, Inc. [AMD/ATI] Navi 32 [Radeon RX 7800 XT] [1002:747e]\n",
	}}
	d := newDetector(DetectOptions{Run: run.run, FS: fsys})
	d.goos, d.goarch, d.host = "linux", "amd64", linuxHost
	hw, err := d.detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if hw.GPUName() != "Radeon RX 7800 XT" || hw.Backend != BackendVulkan || !near(hw.GPUVRAMGB(), 15.98, 0.01) {
		t.Fatalf("got %+v", hw)
	}
}

func TestDetectAppleSilicon(t *testing.T) {
	run := &fakeRunner{outputs: map[string]string{
		"system_profiler SPDisplaysDataType": `{"SPDisplaysDataType":[{"sppci_model":"Apple M2 Pro","spdisplays_mtlgpufamilysupport":"Metal 3"}]}`,
	}}
	d := testDetector("darwin", run.run, nil, hostInfo{cpuName: "Apple M2 Pro", totalGB: 32, availableGB: 20})
	hw, err := d.detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if hw.Backend != BackendMetal || !hw.UnifiedMemory || hw.GPUVRAMGB() != 32 || hw.GPUName() != "Apple M2 Pro" {
		t.Fatalf("got %+v", hw)
	}
}

func TestDetectAppleSiliconTextFallback(t *testing.T) {
	// The JSON probe failed; the text report still names an Apple GPU.
	run := &fakeRunner{}
	run.outputs = map[string]string{"system_profiler SPDisplaysDataType": "Graphics/Displays:\n\n    Apple M1:\n\n      Chipset Model: Apple M1\n"}
	d := testDetector("darwin", func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if len(args) > 1 && args[1] == "-json" {
			return nil, errors.New("exit status 1")
		}
		return run.run(ctx, name, args...)
	}, nil, hostInfo{cpuName: "Apple M1", totalGB: 16, availableGB: 8})
	hw, _ := d.detect(context.Background())
	if hw.GPUName() != "Apple M1" || !hw.UnifiedMemory || hw.GPUVRAMGB() != 16 {
		t.Fatalf("got %+v", hw)
	}
}

func TestDetectWindows(t *testing.T) {
	run := &fakeRunner{}
	// The PowerShell probes share their first arguments, so they are told
	// apart by script.
	runner := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "powershell" {
			script := args[len(args)-1]
			switch {
			case strings.Contains(script, "Win32_VideoController"):
				return []byte("Microsoft Basic Display Adapter|0\nIntel(R) UHD Graphics 770|1073741824\nAMD Radeon RX 7900 XTX|4293918720\n"), nil
			case strings.Contains(script, "qwMemorySize"):
				// One unreadable key makes PowerShell exit 1; the output
				// is still good.
				return []byte("AMD Radeon RX 7900 XTX|25753026560\n"), errors.New("exit status 1")
			}
		}
		return run.run(ctx, name, args...)
	}
	d := testDetector("windows", runner, nil, hostInfo{cpuName: "Intel(R) Core(TM) i9-14900K", totalGB: 64, availableGB: 40})
	hw, err := d.detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(hw.GPUs) != 1 || hw.GPUName() != "AMD Radeon RX 7900 XTX" || hw.Backend != BackendVulkan || hw.GPUVRAMGB() != 23.984375 {
		t.Fatalf("got %+v", hw.GPUs)
	}
}

// Issue #964: a 128 GB Strix Halo with 96 GB given to the GPU in the BIOS.
// The OS sees 31 GB and rocm-smi the 96 GB carve-out; the APU really shares
// all 127 GB.
func TestDetectRyzenAIMaxUnified(t *testing.T) {
	run := &fakeRunner{outputs: map[string]string{
		"rocm-smi --showmeminfo":     "GPU[0]          : VRAM Total Memory (B): 103079215104\n",
		"rocm-smi --showproductname": "GPU[0]          : Card Series:            N/A\n",
		"lspci -nnD": "0000:c4:00.0 Display controller [0380]: Advanced Micro Devices, Inc. [AMD/ATI] Strix Halo " +
			"[Radeon Graphics / Radeon 8050S Graphics / Radeon 8060S Graphics] [1002:1586] (rev c1)\n",
	}}
	fsys := fstest.MapFS{
		"sys/class/drm/card1/device/vendor":              {Data: []byte("0x1002\n")},
		"sys/class/drm/card1/device/mem_info_vram_total": {Data: []byte("103079215104\n")},
	}
	d := testDetector("linux", run.run, fsys, hostInfo{cpuName: "AMD Ryzen AI MAX+ 395 w/ Radeon 8060S", totalGB: 31, availableGB: 28})
	hw, err := d.detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if hw.TotalRAMGB != 127 || !hw.UnifiedMemory || hw.GPUVRAMGB() != 127 || !strings.Contains(hw.GPUName(), "Ryzen AI MAX+ 395") {
		t.Fatalf("got %+v", hw)
	}
	if bw, ok := GPUBandwidthGBps(hw.GPUName()); !ok || bw != 256 {
		t.Errorf("Strix Halo bandwidth = %v %v", bw, ok)
	}
}

func TestDetectCommandTimeout(t *testing.T) {
	// A wedged driver makes nvidia-smi hang; detection must give up on it.
	hang := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	d := testDetector("linux", hang, fstest.MapFS{}, hostInfo{cpuName: "Test CPU", totalGB: 16, availableGB: 8})
	d.timeout = 50 * time.Millisecond
	start := time.Now()
	hw, err := d.detect(context.Background())
	if err != nil {
		t.Fatalf("timed-out tools must not be an error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("detection took %v", elapsed)
	}
	if hw.HasGPU() || hw.TotalRAMGB != 16 {
		t.Fatalf("got %+v", hw)
	}
}

func TestDetectCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := testDetector("linux", (&fakeRunner{}).run, fstest.MapFS{}, hostInfo{totalGB: 16})
	if _, err := d.detect(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestDetectHostFailure(t *testing.T) {
	d := testDetector("linux", (&fakeRunner{}).run, fstest.MapFS{}, hostInfo{})
	d.host = linuxHost // no /proc/meminfo in the empty file system
	hw, err := d.detect(context.Background())
	if err == nil {
		t.Fatal("unreadable memory should be reported")
	}
	if hw.CPUName != "Unknown CPU" || hw.CPUCores == 0 {
		t.Fatalf("partial result: %+v", hw)
	}
}

// TestDetectThisMachine only checks that real detection works and is sane;
// what it finds depends on the machine running the tests.
func TestDetectThisMachine(t *testing.T) {
	if testing.Short() {
		t.Skip("runs real tools")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	hw, err := Detect(ctx)
	if err != nil {
		t.Skipf("memory not readable here: %v", err)
	}
	if hw.TotalRAMGB <= 0 || hw.AvailableRAMGB <= 0 || hw.CPUCores <= 0 || hw.Backend == "" {
		t.Fatalf("implausible: %+v", hw)
	}
	t.Logf("%s, %d cores, %.1f GB RAM (%.1f free), backend %s, GPUs %+v",
		hw.CPUName, hw.CPUCores, hw.TotalRAMGB, hw.AvailableRAMGB, hw.Backend, hw.GPUs)
}
