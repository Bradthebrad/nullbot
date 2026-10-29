package llmfit

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fixture reads a file from testdata/hardware, copied from llmfit's
// tests/fixtures/hardware.
func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "hardware", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func findGPU(gpus []GPU, sub string) (GPU, bool) {
	i := slices.IndexFunc(gpus, func(g GPU) bool { return strings.Contains(g.Name, sub) })
	if i < 0 {
		return GPU{}, false
	}
	return gpus[i], true
}

func TestNvidiaSMIList(t *testing.T) {
	// Identical cards are grouped and keep per-card VRAM, never the sum.
	gpus := parseNvidiaSMIList(fixture(t, "nvidia/standard-identical.csv"))
	if len(gpus) != 1 || gpus[0].Count != 2 || gpus[0].VRAMGB < 23 || gpus[0].VRAMGB > 25 {
		t.Fatalf("identical: %+v", gpus)
	}
	gpus = parseNvidiaSMIList(fixture(t, "nvidia/standard-distinct.csv"))
	if len(gpus) != 2 {
		t.Fatalf("distinct: %+v", gpus)
	}
	for _, model := range []string{"4090", "4080"} {
		if g, ok := findGPU(gpus, model); !ok || g.Count != 1 {
			t.Errorf("distinct: missing single %s in %+v", model, gpus)
		}
	}
	gpus = parseNvidiaSMIList(fixture(t, "nvidia/standard-single.csv"))
	if len(gpus) != 1 || gpus[0].Name != "NVIDIA GeForce RTX 4090" || gpus[0].Count != 1 ||
		gpus[0].Backend != BackendCUDA || gpus[0].UnifiedMemory {
		t.Fatalf("single: %+v", gpus)
	}
	empty := fixture(t, "nvidia/empty.txt")
	if len(parseNvidiaSMIList(empty)) != 0 || len(parseNvidiaSMIExtended(empty, 64)) != 0 {
		t.Fatal("empty output should give no GPUs")
	}
	// DGX Spark's GB10 reports 0 MiB; the name table supplies 128 GB.
	if gpus := parseNvidiaSMIList("0, NVIDIA GB10\n"); len(gpus) != 1 || gpus[0].VRAMGB < 100 {
		t.Fatalf("GB10: %+v", gpus)
	}
}

func TestNvidiaSMIExtended(t *testing.T) {
	// Thor reports addressing mode ATS and no VRAM: unified memory sized to
	// system RAM.
	gpus := parseNvidiaSMIExtended(fixture(t, "nvidia/extended-unified.csv"), 128)
	if len(gpus) != 1 || gpus[0].Name != "NVIDIA Thor" || !gpus[0].UnifiedMemory || gpus[0].VRAMGB != 128 {
		t.Fatalf("Thor: %+v", gpus)
	}
	if gpus[0].FreeVRAMGB != nil {
		t.Error("a unified part's pool is system RAM, not free VRAM")
	}

	gpus = parseNvidiaSMIExtended("None, 24564, NVIDIA GeForce RTX 4090\n", 64)
	if len(gpus) != 1 || gpus[0].UnifiedMemory || gpus[0].VRAMGB < 23 || gpus[0].VRAMGB > 25 {
		t.Fatalf("discrete legacy columns: %+v", gpus)
	}
	gpus = parseNvidiaSMIExtended("None, 24564, NVIDIA GeForce RTX 4090\nNone, 24564, NVIDIA GeForce RTX 4090\n", 64)
	if len(gpus) != 1 || gpus[0].Count != 2 || gpus[0].UnifiedMemory {
		t.Fatalf("multi-GPU: %+v", gpus)
	}

	// memory.free, from a 3090 with 23010 MiB held by another process.
	gpus = parseNvidiaSMIExtended("None, 24576, 1112, NVIDIA GeForce RTX 3090\n", 64)
	if len(gpus) != 1 || gpus[0].VRAMGB != 24 || gpus[0].FreeVRAMGB == nil || !near(*gpus[0].FreeVRAMGB, 1112.0/1024, 1e-9) {
		t.Fatalf("memory.free: %+v", gpus)
	}
	gpus = parseNvidiaSMIExtended("None, 24576, 20480, NVIDIA GeForce RTX 4090\nNone, 24576, 4096, NVIDIA GeForce RTX 4090\n", 64)
	if gpus[0].Count != 2 || gpus[0].FreeVRAMGB == nil || *gpus[0].FreeVRAMGB != 24 {
		t.Fatalf("free summed over cards: %+v", gpus)
	}
	// One card without a reading makes the sum meaningless.
	gpus = parseNvidiaSMIExtended("None, 24576, 20480, NVIDIA GeForce RTX 4090\nNone, 24576, [N/A], NVIDIA GeForce RTX 4090\n", 64)
	if gpus[0].Count != 2 || gpus[0].FreeVRAMGB != nil {
		t.Fatalf("partial free: %+v", gpus)
	}
	gpus = parseNvidiaSMIExtended("ATS, [N/A], [N/A], NVIDIA GB10\n", 128)
	if gpus[0].Name != "NVIDIA GB10" || !gpus[0].UnifiedMemory || gpus[0].FreeVRAMGB != nil {
		t.Fatalf("GB10: %+v", gpus)
	}
	// Jetson reports N/A addressing; "(nvgpu)" marks it unified instead.
	gpus = parseNvidiaSMIExtended("N/A, [N/A], Orin (nvgpu)\n", 64)
	if len(gpus) != 1 || gpus[0].Name != "Orin (nvgpu)" || !isNvidiaUnifiedMemoryGPU(gpus[0].Name) {
		t.Fatalf("Jetson: %+v", gpus)
	}
	if isNvidiaUnifiedMemoryGPU("NVIDIA GeForce RTX 4090") {
		t.Error("a discrete GPU is not unified")
	}
}

func TestROCmSMI(t *testing.T) {
	gpus := parseROCmSMI(fixture(t, "rocm/block-vram.txt"), fixture(t, "rocm/block-product.txt"))
	rx, ok1 := findGPU(gpus, "RX 7600")
	r9700, ok2 := findGPU(gpus, "R9700")
	if len(gpus) != 2 || !ok1 || !ok2 || rx.VRAMGB < 7 || rx.VRAMGB > 9 || r9700.VRAMGB < 31 || r9700.VRAMGB > 33 {
		t.Fatalf("block, two models: %+v", gpus)
	}
	if rx.Backend != BackendROCm {
		t.Errorf("backend %s", rx.Backend)
	}

	identicalVRAM := "GPU[0]          : VRAM Total Memory (B): 34208743424\nGPU[0]          : VRAM Total Used Memory (B): 100000\n" +
		"GPU[1]          : VRAM Total Memory (B): 34208743424\nGPU[1]          : VRAM Total Used Memory (B): 200000"
	identicalNames := "GPU[0]          : Card Series:          AMD Radeon AI PRO R9700\nGPU[1]          : Card Series:          AMD Radeon AI PRO R9700"
	if gpus := parseROCmSMI(identicalVRAM, identicalNames); len(gpus) != 1 || gpus[0].Count != 2 {
		t.Fatalf("identical grouped: %+v", gpus)
	}

	// The 512 MB iGPU is dropped next to a discrete card.
	gpus = parseROCmSMI(fixture(t, "rocm/mixed-vram.txt"), fixture(t, "rocm/mixed-product.txt"))
	if len(gpus) != 1 || !strings.Contains(gpus[0].Name, "R9700") {
		t.Fatalf("iGPU filtered: %+v", gpus)
	}

	gpus = parseROCmSMI(fixture(t, "rocm/block-vram.txt"), "")
	if len(gpus) != 1 || gpus[0].Name != "AMD GPU" || gpus[0].Count != 2 || gpus[0].VRAMGB < 31 {
		t.Fatalf("no product names: %+v", gpus)
	}

	// Strix Halo without libdrm: "N/A" must not become the GPU's name.
	gpus = parseROCmSMI("GPU[0]          : VRAM Total Memory (B): 68719476736\n",
		"GPU[0]          : Card Series:            N/A\nGPU[0]          : GFX Version:            gfx1151")
	if len(gpus) != 1 || gpus[0].Name != "AMD GPU" || gpus[0].VRAMGB < 63 {
		t.Fatalf("N/A name: %+v", gpus)
	}

	// The newer table layout, two MI50s.
	gpus = parseROCmSMI(fixture(t, "rocm/tabular-vram.txt"), fixture(t, "rocm/tabular-product.txt"))
	if len(gpus) != 1 || gpus[0].Count != 2 || !strings.Contains(gpus[0].Name, "MI50") || gpus[0].VRAMGB < 31 || gpus[0].VRAMGB > 33 {
		t.Fatalf("table, identical: %+v", gpus)
	}
	gpus = parseROCmSMI("Device  Node  VRAM Total Memory (B)   VRAM Total Used Memory (B)\n0       2     8573157376              60448768\n1       1     34208743424             33732509696",
		"Device  Card Series              Card Model\n0       AMD Radeon RX 7600       0x7480\n1       AMD Radeon AI PRO R9700  0x7551")
	if _, ok := findGPU(gpus, "RX 7600"); len(gpus) != 2 || !ok {
		t.Fatalf("table, two models: %+v", gpus)
	}
	if gpus := parseROCmSMI(fixture(t, "rocm/tabular-vram.txt"), ""); len(gpus) != 1 || gpus[0].Count != 2 || gpus[0].Name != "AMD GPU" {
		t.Fatalf("table without names: %+v", gpus)
	}
	malformed := fixture(t, "rocm/malformed.txt")
	if gpus := parseROCmSMI(malformed, malformed); len(gpus) != 0 {
		t.Fatalf("malformed: %+v", gpus)
	}
}

// Issue #638: a 32 GB MI50 reporting the generic "AMD Radeon Graphics", a
// properly named 16 GB MI50 and a 512 MB Cezanne iGPU.
func TestROCmSMIGenericNamedAccelerator(t *testing.T) {
	vram := "GPU[0]\t\t: VRAM Total Memory (B): 34342961152\nGPU[0]\t\t: VRAM Total Used Memory (B): 25227759616\n" +
		"GPU[1]\t\t: VRAM Total Memory (B): 17163091968\nGPU[1]\t\t: VRAM Total Used Memory (B): 7695077376\n" +
		"GPU[2]\t\t: VRAM Total Memory (B): 536870912\nGPU[2]\t\t: VRAM Total Used Memory (B): 18165760\n"
	product := "GPU[0]\t\t: Card Series: \t\tAMD Radeon Graphics\nGPU[0]\t\t: Card Vendor: \t\tAdvanced Micro Devices, Inc. [AMD/ATI]\nGPU[0]\t\t: GFX Version: \t\tgfx906\n" +
		"GPU[1]\t\t: Card Series: \t\tAMD Instinct MI60 / MI50\nGPU[1]\t\t: GFX Version: \t\tgfx906\n" +
		"GPU[2]\t\t: Card Series: \t\tAMD Radeon Graphics\nGPU[2]\t\t: GFX Version: \t\tgfx90c\n"
	gpus := parseROCmSMI(vram, product)
	big, ok := findGPU(gpus, "(gfx906)")
	if len(gpus) != 2 || !ok || big.Name != "AMD Radeon Graphics (gfx906)" || big.VRAMGB < 30 {
		t.Fatalf("got %+v", gpus)
	}
	if small, ok := findGPU(gpus, "MI60 / MI50"); !ok || small.VRAMGB < 15 || small.VRAMGB > 17 {
		t.Fatalf("16 GB MI50 missing: %+v", gpus)
	}
	if kept := preferDiscreteGPUs(gpus); len(kept) != 2 {
		t.Fatalf("preferDiscreteGPUs dropped a 32 GB accelerator: %+v", kept)
	}
}

func TestMergeROCmAndVulkan(t *testing.T) {
	rocm := parseROCmSMI(fixture(t, "mixed/rocm-vram.txt"), fixture(t, "mixed/rocm-product.txt"))
	gpus := mergeGPUSources(rocm, parseVulkanGPUs(fixture(t, "mixed/vulkan-summary.txt")))
	if len(gpus) != 3 || gpus[0].Name != "AMD Radeon RX 7900 XTX" || gpus[0].VRAMGB != 24 || gpus[0].Count != 1 {
		t.Fatalf("duplicate AMD reports were not merged: %+v", gpus)
	}
	if g, ok := findGPU(gpus, "7800 XT"); !ok || g.Count != 1 {
		t.Errorf("distinct AMD GPU lost: %+v", gpus)
	}
	if g, ok := findGPU(gpus, "RTX 4070"); !ok || g.Name != "NVIDIA GeForce RTX 4070" {
		t.Errorf("distinct NVIDIA GPU lost: %+v", gpus)
	}

	rocm = parseROCmSMI(fixture(t, "mixed/rocm-vram.txt"), fixture(t, "mixed/rocm-product.txt"))
	malformed := parseVulkanGPUs(fixture(t, "mixed/vulkan-malformed.txt"))
	if gpus := mergeGPUSources(rocm, malformed); len(gpus) != 1 || gpus[0].Name != "AMD Radeon RX 7900 XTX" {
		t.Fatalf("invalid Vulkan output removed a valid GPU: %+v", gpus)
	}
	if isSoftwareVulkanDevice("NVIDIA GeForce RTX 4070") || !isSoftwareVulkanDevice("llvmpipe (LLVM 17.0.6, 256 bits)") ||
		!isSoftwareVulkanDevice("AMD Ryzen 7 9800X3D 8-Core Processor (RADV RAPHAEL_MENDOCINO)") {
		t.Error("software Vulkan device detection")
	}
}

func TestAMDSysfs(t *testing.T) {
	sysfs := func(name string) []amdCard {
		names := map[string]string{"card0": "AMD Radeon 780M Graphics", "card1": "AMD Radeon RX 7900 XTX"}
		return scanAMDSysfsCards(os.DirFS(filepath.Join("testdata", "hardware", "sysfs", name)), func(card string, _ []string) string {
			if name == "mixed" {
				return names[card]
			}
			return "AMD Fixture GPU"
		})
	}
	// card0 is a 1 GiB iGPU, card1 a 24 GiB dGPU with 16 GiB used, card2
	// an NVIDIA card the scan must skip.
	gpus := groupAMDSysfsCards(sysfs("mixed"))
	if len(gpus) != 1 {
		t.Fatalf("mixed: %+v", gpus)
	}
	g := gpus[0]
	if g.Name != "AMD Radeon RX 7900 XTX" || g.VRAMGB != 24 || g.Count != 1 || g.Backend != BackendVulkan ||
		g.FreeVRAMGB == nil || *g.FreeVRAMGB != 8 {
		t.Fatalf("mixed: %+v", g)
	}
	for _, dir := range []string{"missing-vram", "malformed-vram"} {
		if gpus := groupAMDSysfsCards(sysfs(dir)); len(gpus) != 1 || gpus[0].Name != "AMD Fixture GPU" || gpus[0].VRAMGB != 0 {
			t.Errorf("%s: %+v", dir, gpus)
		}
	}

	// Strix Halo with 96 GiB given to the GPU: the carve-out is added back.
	cards := scanAMDSysfsCards(os.DirFS(filepath.Join("testdata", "hardware", "sysfs", "strix-halo")),
		func(string, []string) string { return "Strix Halo [Radeon 8060S] (integrated)" })
	if carveout, ok := apuCarveoutGB(cards); !ok || carveout != 96 {
		t.Fatalf("carve-out = %v %v", carveout, ok)
	}
	if _, ok := apuCarveoutGB(append(cards, cards...)); ok {
		t.Error("two integrated cards are ambiguous")
	}
}

func TestLspciNames(t *testing.T) {
	lspci := "0000:01:00.0 VGA compatible controller [0300]: NVIDIA Corporation AD102 [GeForce RTX 4090] [10de:2684] (rev a1)\n" +
		"0000:03:00.0 Display controller [0380]: Advanced Micro Devices, Inc. [AMD/ATI] Navi 31 [Radeon RX 7900 XT/7900 XTX] [1002:744c] (rev c8)\n"
	if got := lspciGPUName(lspci, []string{"0000:03:00.0"}, isAMDLspciLine); got != "Radeon RX 7900 XT/7900 XTX" {
		t.Errorf("by slot: %q", got)
	}
	// Without a slot the AMD line must still win over "NVIDIA Corporation",
	// whose "ati" llmfit's vendor check would match.
	if got := lspciGPUName(lspci, nil, isAMDLspciLine); got != "Radeon RX 7900 XT/7900 XTX" {
		t.Errorf("fallback: %q", got)
	}
}

func TestMacOSSystemProfiler(t *testing.T) {
	if !isAppleGPUReport(fixture(t, "macos/apple-a18-pro.txt")) {
		t.Error("A18 Pro text report")
	}
	for _, name := range []string{"Apple M1", "Apple M4 Pro", "Apple M5 Max", "Apple GPU", "APPLE A18 PRO"} {
		if !isAppleGPUReport("Chipset Model: " + name + "\n") {
			t.Errorf("%s should be Apple Silicon", name)
		}
	}
	for _, name := range []string{"Intel HD Graphics 630", "AMD Radeon Pro 560", "Apple Accelerator", "Apple Monitor"} {
		if isAppleGPUReport("Chipset Model: " + name + "\n") {
			t.Errorf("%s is not Apple Silicon", name)
		}
	}

	// Both Metal schemas identify the same unified device.
	json := fixture(t, "macos/apple-a18-pro.json")
	legacy := strings.NewReplacer("spdisplays_metal", "spdisplays_mtlgpufamilysupport", "spdisplays_supported", "Metal 3").Replace(json)
	for _, data := range []string{json, legacy} {
		gpus := parseMacDisplaysJSON([]byte(data), 8)
		if len(gpus) != 1 || gpus[0].Name != "Apple A18 Pro" || gpus[0].Backend != BackendMetal || gpus[0].VRAMGB != 8 || !gpus[0].UnifiedMemory {
			t.Fatalf("A18 Pro JSON: %+v", gpus)
		}
	}

	gpus := parseMacDisplaysJSON([]byte(fixture(t, "macos/intel-amd.json")), 16)
	if len(gpus) != 2 || gpus[0].Name != "Intel HD Graphics 630" || gpus[0].VRAMGB != 1.5 || gpus[0].UnifiedMemory ||
		gpus[1].Name != "Radeon Pro 560" || gpus[1].VRAMGB != 4 || gpus[1].Backend != BackendMetal {
		t.Fatalf("Intel Mac: %+v", gpus)
	}
	// Apple Silicon's pool is all of RAM, not the reported shared figure.
	gpus = parseMacDisplaysJSON([]byte(fixture(t, "macos/apple-silicon.json")), 32)
	if len(gpus) != 1 || gpus[0].Name != "Apple M2" || !gpus[0].UnifiedMemory || gpus[0].VRAMGB != 32 {
		t.Fatalf("M2: %+v", gpus)
	}
	if gpus := parseMacDisplaysJSON([]byte(fixture(t, "macos/missing-metal.json")), 16); len(gpus) != 0 {
		t.Fatalf("no Metal: %+v", gpus)
	}
	gpus = parseMacDisplaysJSON([]byte(fixture(t, "macos/missing-vram.json")), 16)
	if len(gpus) != 1 || gpus[0].Name != "Radeon Pro 560" || gpus[0].VRAMGB != 0 {
		t.Fatalf("no VRAM field: %+v", gpus)
	}
	for _, f := range []string{"macos/invalid.json", "macos/missing-array.json"} {
		if gpus := parseMacDisplaysJSON([]byte(fixture(t, f)), 16); len(gpus) != 0 {
			t.Fatalf("%s: %+v", f, gpus)
		}
	}
	for status, want := range map[string]int{"spdisplays_supported": 1, "spdisplays_unsupported": 0, "": 0} {
		data := `{"SPDisplaysDataType":[{"sppci_model":"Radeon Pro 560","spdisplays_metal":"` + status + `","spdisplays_vram":"4 GB"}]}`
		if gpus := parseMacDisplaysJSON([]byte(data), 16); len(gpus) != want {
			t.Errorf("metal status %q: %+v", status, gpus)
		}
	}
}

func TestWindowsVideoControllers(t *testing.T) {
	gpus := parseWindowsGPUList(fixture(t, "windows/win32-video-controller.txt"))
	var names []string
	for _, g := range gpus {
		names = append(names, g.Name)
	}
	want := []string{"NVIDIA GeForce RTX 4070", "AMD Radeon 780M Graphics", "AMD Radeon AI PRO R9700", "AMD Radeon PRO V710"}
	if !slices.Equal(names, want) {
		t.Fatalf("names %q, want %q", names, want)
	}
	if gpus[0].Backend != BackendCUDA || gpus[0].VRAMGB != 12 {
		t.Errorf("RTX 4070: %+v", gpus[0])
	}
	if gpus[1].Backend != BackendVulkan || gpus[1].VRAMGB != 2 || !isIntegratedGPU(gpus[1].Name, gpus[1].VRAMGB) {
		t.Errorf("780M: %+v", gpus[1])
	}
	// AdapterRAM caps at 4 GB; the name table knows the R9700 has 32.
	if gpus[2].VRAMGB != 32 || isIntegratedGPU(gpus[2].Name, gpus[2].VRAMGB) {
		t.Errorf("R9700: %+v", gpus[2])
	}

	// Old Intel HD Graphics must not count as a GPU.
	if gpus := parseWindowsGPUList("Intel(R) HD Graphics 4000|2158112768\n"); len(gpus) != 0 {
		t.Fatalf("legacy Intel: %+v", gpus)
	}
	gpus = parseWindowsGPUList("Intel(R) HD Graphics 4000|2158112768\nNVIDIA GeForce RTX 4090|4293918720\n")
	if len(gpus) != 1 || gpus[0].Backend != BackendCUDA || gpus[0].VRAMGB != 24 {
		t.Fatalf("legacy Intel next to an RTX 4090: %+v", gpus)
	}
}

func TestWindowsRegistryVRAM(t *testing.T) {
	reg := parseWindowsRegistryVRAM(fixture(t, "windows/registry-vram.txt"))
	if len(reg) != 2 || reg["AMD Radeon PRO V710"] != 30064771072 || reg["NVIDIA GeForce RTX 4070"] != 12884901888 {
		t.Fatalf("registry: %v", reg)
	}
	if len(parseWindowsRegistryVRAM("AMD Radeon RX 7900 XTX|8192\n")) != 0 {
		t.Error("a megabyte figure must be dropped, not read as 8 KB")
	}
	gpus := applyRegistryVRAM(parseWindowsGPUList(fixture(t, "windows/win32-video-controller.txt")), reg)
	if v710, _ := findGPU(gpus, "V710"); v710.VRAMGB != 28 {
		t.Errorf("V710 should take the registry's 28 GB: %+v", v710)
	}
	// Trademark decoration differs between WMI and the registry.
	if b, ok := matchRegistryVRAM("AMD Radeon(TM) RX 7900 XTX", map[string]uint64{"AMD Radeon RX 7900 XTX": 24 << 30}); !ok || b != 24<<30 {
		t.Error("trademark marks should not block a match")
	}
	// An ambiguous partial match binds to nothing.
	if _, ok := matchRegistryVRAM("NVIDIA GeForce RTX 4090", map[string]uint64{
		"NVIDIA GeForce RTX 4090 D": 24 << 30, "NVIDIA GeForce RTX 4090 Laptop GPU": 16 << 30,
	}); ok {
		t.Error("ambiguous partial match was accepted")
	}
}

func TestIntegratedGPUNames(t *testing.T) {
	integrated := []string{"Intel(R) UHD Graphics 770", "Intel(R) Graphics", "Intel Arc Graphics 130V/140V (integrated)",
		"AMD Radeon(TM) Graphics", "AMD Radeon 780M Graphics", "Radeon 890M"}
	discrete := []string{"Intel(R) Arc(TM) A770 Graphics", "Intel Arc A370M Graphics", "Intel Arc Pro B60",
		"AMD Radeon RX 7900 XTX", "NVIDIA GeForce RTX 4090", "AMD Radeon Pro W7900"}
	for _, n := range integrated {
		if !isIntegratedGPUName(n) {
			t.Errorf("%s should be integrated", n)
		}
	}
	for _, n := range discrete {
		if isIntegratedGPUName(n) {
			t.Errorf("%s should be discrete", n)
		}
	}
	gpus := preferDiscreteGPUs([]GPU{{Name: "Intel(R) UHD Graphics 770", VRAMGB: 1}, {Name: "NVIDIA GeForce RTX 4060", VRAMGB: 8}})
	if len(gpus) != 1 || gpus[0].Name != "NVIDIA GeForce RTX 4060" {
		t.Errorf("preferDiscreteGPUs: %+v", gpus)
	}
	if gpus := preferDiscreteGPUs([]GPU{{Name: "AMD Radeon(TM) Graphics", VRAMGB: 0.5}}); len(gpus) != 1 {
		t.Error("an iGPU-only machine keeps its iGPU")
	}
}

func TestAMDUnifiedAPU(t *testing.T) {
	cpu := "AMD Ryzen AI MAX+ 395 w/ Radeon 8060S"
	gpus := applyAMDUnifiedAPU([]GPU{{Name: "AMD GPU", VRAMGB: 96, Count: 1, Backend: BackendROCm}}, cpu, 128)
	if len(gpus) != 1 || !gpus[0].UnifiedMemory || gpus[0].VRAMGB != 128 || gpus[0].Name != cpu+" (integrated)" {
		t.Fatalf("override: %+v", gpus)
	}
	gpus = applyAMDUnifiedAPU(nil, cpu, 128)
	if len(gpus) != 1 || gpus[0].Backend != BackendVulkan || !gpus[0].UnifiedMemory {
		t.Fatalf("synthesized: %+v", gpus)
	}
	if gpus := applyAMDUnifiedAPU(nil, "AMD Ryzen AI 9 HX 370 w/ Radeon 890M", 32); len(gpus) != 0 {
		t.Fatal("regular Ryzen AI parts are not unified-memory APUs")
	}
	if !isAMDAPU("AMD Ryzen 7 8845HS w/ Radeon 780M Graphics") || isAMDAPU("AMD Ryzen 9 7950X 16-Core Processor") {
		t.Error("isAMDAPU")
	}
}

func TestGPUTables(t *testing.T) {
	for name, want := range map[string]float64{
		"NVIDIA GeForce RTX 4090": 1008, "NVIDIA GeForce RTX 3060": 360, "Tesla T4": 320, "NVIDIA H100 SXM": 3350,
		"NVIDIA A100": 1555, "Apple M1 Max": 400, "Apple M4 Pro": 273, "Apple M5": 153.6,
		"AMD Radeon RX 7900 XTX": 960, "AMD Instinct MI300X": 5300, "NVIDIA GeForce RTX 4070 Ti SUPER": 672,
	} {
		if got, ok := GPUBandwidthGBps(name); !ok || got != want {
			t.Errorf("bandwidth %s = %v %v, want %v", name, got, ok, want)
		}
	}
	// Laptop parts don't share the desktop card's memory bus.
	for _, name := range []string{"Some Random GPU", "", "NVIDIA GeForce RTX 5070 Laptop GPU", "NVIDIA GeForce RTX 2080 Max-Q"} {
		if _, ok := GPUBandwidthGBps(name); ok {
			t.Errorf("%q should be unknown", name)
		}
	}
	for name, want := range map[string]float64{
		"NVIDIA GB10": 128, "NVIDIA GB20": 128, "NVIDIA RTX A6000": 48, "NVIDIA RTX A4500": 20,
		"AMD Radeon AI PRO R9700": 32, "AMD Radeon RX 9070 XT": 16, "AMD Radeon RX 9060 XT": 16,
		"NVIDIA GeForce RTX 5070 Laptop GPU": 8, // generic RTX estimate, not the desktop 12
	} {
		if got := estimateVRAMFromName(name); got != want {
			t.Errorf("VRAM %s = %v, want %v", name, got, want)
		}
	}
	if cc, ok := gpuComputeCapability("Tesla V100-PCIE-32GB"); !ok || cc != (computeCapability{7, 0}) {
		t.Errorf("V100 cc = %v", cc)
	}
	if quantKernelSupport("AWQ-4bit", "Tesla V100") != kernelUnsupported ||
		quantKernelSupport("AWQ-4bit", "NVIDIA GeForce RTX 3090") != kernelNative ||
		quantKernelSupport("NVFP4", "NVIDIA GeForce RTX 4090") != kernelFallback ||
		quantKernelSupport("NVFP4", "NVIDIA GeForce RTX 5090") != kernelNative ||
		quantKernelSupport("Q4_K_M", "NVIDIA GeForce RTX 4090") != kernelUnknown {
		t.Error("kernel support")
	}
}

func TestHostParsers(t *testing.T) {
	if got := parseCPUInfo("processor   : 0\nmodel name  : Qualcomm Kryo 680\nHardware    : Qualcomm Technologies, Inc SM8350\n"); got != "Qualcomm Kryo 680" {
		t.Errorf("model name: %q", got)
	}
	if got := parseCPUInfo("processor   : 0\nHardware    : Qualcomm Technologies, Inc SM8650\n"); got != "Qualcomm Technologies, Inc SM8650" {
		t.Errorf("Hardware fallback: %q", got)
	}
	if got := parseCPUInfo("processor\t: 0\nBogoMIPS\t: 243.75\n"); got != "" {
		t.Errorf("a processor index is not a name: %q", got)
	}
	total, avail := parseMeminfo("MemTotal:       65536000 kB\nMemFree:         1000000 kB\nMemAvailable:   32768000 kB\n")
	if !near(total, 62.5, 1e-9) || !near(avail, 31.25, 1e-9) {
		t.Errorf("meminfo = %v, %v", total, avail)
	}
	if _, avail := parseMeminfo("MemTotal: 2097152 kB\nMemFree: 524288 kB\nBuffers: 0 kB\nCached: 524288 kB\n"); avail != 1 {
		t.Errorf("old-kernel available = %v", avail)
	}
	vmstat := "Mach Virtual Memory Statistics: (page size of 16384 bytes)\nPages free:                               65536.\n" +
		"Pages active:                            100000.\nPages inactive:                           65536.\nPages purgeable:                          0.\n"
	if got := parseVMStat(vmstat); got != 2 {
		t.Errorf("vm_stat available = %v GiB, want 2", got)
	}
	for in, want := range map[string]float64{"32G": 32, "32gb": 32, "1536 MB": 1.5, "2T": 2048, "16": 16, " 8 GiB ": 8} {
		if got, ok := parseMemorySize(in); !ok || got != want {
			t.Errorf("parseMemorySize(%q) = %v %v", in, got, ok)
		}
	}
	for _, in := range []string{"", "lots", "32X"} {
		if _, ok := parseMemorySize(in); ok {
			t.Errorf("parseMemorySize(%q) accepted", in)
		}
	}
}
