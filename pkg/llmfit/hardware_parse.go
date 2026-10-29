package llmfit

import (
	"encoding/json"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// gib converts bytes to GiB.
func gib(bytes float64) float64 { return bytes / (1 << 30) }

func ptr(v float64) *float64 { return &v }

// sortedKeys returns a map's keys in order, so grouped GPUs come out in a
// stable order as they do from llmfit's BTreeMap.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// gpuGroup accumulates identical cards.
type gpuGroup struct {
	count   int
	vram    float64 // max per card, GiB; 0 = unknown
	unified bool
	free    float64 // summed over cards, GiB
	freeOK  bool    // every card so far reported free memory
}

// addFree folds one card's free-memory reading into the group: the sum
// becomes unknown as soon as one card lacks a reading, since a partial sum
// would understate what is free.
func (g *gpuGroup) addFree(first bool, free float64, ok bool) {
	switch {
	case first:
		g.free, g.freeOK = free, ok
	case g.freeOK && ok:
		g.free += free
	default:
		g.freeOK = false
	}
}

// parseNvidiaSMIExtended parses
// nvidia-smi --query-gpu=addressing_mode,memory.total,memory.free,name
// --format=csv,noheader,nounits. Addressing mode "ATS" marks unified
// memory (Grace, Thor), whose pool is system RAM. Output from before
// memory.free was queried (three columns) still parses.
func parseNvidiaSMIExtended(text string, totalRAMGB float64) []GPU {
	groups := map[string]*gpuGroup{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ",", 4)
		if len(parts) < 3 {
			continue
		}
		unified := strings.EqualFold(strings.TrimSpace(parts[0]), "ATS")
		third := strings.TrimSpace(parts[2])
		_, thirdErr := strconv.ParseFloat(third, 64)
		hasFree := len(parts) == 4 && (thirdErr == nil || strings.HasPrefix(third, "["))
		var name string
		freeMB, freeOK := 0.0, false
		if hasFree {
			if v, err := strconv.ParseFloat(third, 64); err == nil && v >= 0 {
				freeMB, freeOK = v, true
			}
			name = strings.TrimSpace(parts[3])
		} else {
			name = strings.TrimSpace(strings.Join(parts[2:], ","))
		}
		if name == "" {
			name = "NVIDIA GPU"
		}
		vramMB, _ := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		if vramMB <= 0 {
			if unified {
				vramMB = totalRAMGB * 1024
			} else {
				vramMB = estimateVRAMFromName(name) * 1024
			}
		}
		g, seen := groups[name]
		if !seen {
			g = &gpuGroup{}
			groups[name] = g
		}
		g.count++
		g.vram = max(g.vram, vramMB/1024)
		g.unified = g.unified || unified
		g.addFree(!seen, freeMB/1024, freeOK)
	}
	var gpus []GPU
	for _, name := range sortedKeys(groups) {
		g := groups[name]
		gpu := GPU{Name: name, VRAMGB: g.vram, Count: g.count, Backend: BackendCUDA, UnifiedMemory: g.unified}
		// A unified part's pool is system RAM, already counted as RAM.
		if !g.unified && g.freeOK {
			gpu.FreeVRAMGB = ptr(g.free)
		}
		gpus = append(gpus, gpu)
	}
	return gpus
}

// parseNvidiaSMIList parses nvidia-smi --query-gpu=memory.total,name
// --format=csv,noheader,nounits, grouping identical cards and keeping
// per-card VRAM (never summed).
func parseNvidiaSMIList(text string) []GPU {
	groups := map[string]*gpuGroup{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		mem, name, _ := strings.Cut(line, ",")
		name = strings.TrimSpace(name)
		if name == "" {
			name = "NVIDIA GPU"
		}
		vramMB, _ := strconv.ParseFloat(strings.TrimSpace(mem), 64)
		if vramMB <= 0 {
			vramMB = estimateVRAMFromName(name) * 1024
		}
		g := groups[name]
		if g == nil {
			g = &gpuGroup{}
			groups[name] = g
		}
		g.count++
		g.vram = max(g.vram, vramMB/1024)
	}
	var gpus []GPU
	for _, name := range sortedKeys(groups) {
		g := groups[name]
		gpus = append(gpus, GPU{Name: name, VRAMGB: g.vram, Count: g.count, Backend: BackendCUDA})
	}
	return gpus
}

// parseROCmVRAMBytes reads per-GPU VRAM totals from rocm-smi --showmeminfo
// vram, in either the block layout ("GPU[0] : VRAM Total Memory (B): N")
// or the newer table with one row per device.
func parseROCmVRAMBytes(text string) []uint64 {
	var out []uint64
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		lower := strings.ToLower(line)
		if !strings.Contains(lower, "total") || strings.Contains(lower, "used") {
			continue
		}
		fields := strings.Fields(line)
		for i := len(fields) - 1; i >= 0; i-- {
			if v, err := strconv.ParseUint(fields[i], 10, 64); err == nil {
				if v > 0 {
					out = append(out, v)
				}
				break
			}
		}
	}
	if len(out) > 0 {
		return out
	}
	// Table rows start with the device index; the first number big enough
	// to be VRAM (64 MiB) is the Total column, which precedes Used.
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if _, err := strconv.ParseUint(fields[0], 10, 32); err != nil {
			continue
		}
		for _, f := range fields {
			if v, err := strconv.ParseUint(f, 10, 64); err == nil && v >= 64<<20 {
				out = append(out, v)
				break
			}
		}
	}
	return out
}

// parseROCmProductNames reads one name per device from rocm-smi
// --showproductname, block or table layout.
func parseROCmProductNames(text string) []string {
	var block, gfx []string
	for _, line := range strings.Split(text, "\n") {
		lower := strings.ToLower(line)
		if !strings.Contains(line, ":") {
			continue
		}
		value := strings.TrimSpace(line[strings.LastIndexByte(line, ':')+1:])
		switch {
		case strings.Contains(lower, "card series") && value != "":
			// rocm-smi prints N/A when it can't read the marketing name;
			// keep the slot so names stay aligned with VRAM.
			if isPlaceholderGPUName(value) {
				value = "AMD GPU"
			}
			block = append(block, value)
		case strings.Contains(lower, "gfx version") && value != "":
			gfx = append(gfx, value)
		}
	}
	if len(block) > 0 {
		// Some accelerators (Instinct MI50) report the generic "AMD Radeon
		// Graphics" an APU's iGPU also reports; the GFX version keeps them
		// apart.
		if len(gfx) == len(block) {
			for i := range block {
				if isIntegratedGPUName(block[i]) {
					block[i] += " (" + gfx[i] + ")"
				}
			}
		}
		return block
	}

	// Table layout: slice the "Card Series" column out of each device row.
	var out []string
	var header string
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(strings.ToLower(line), "card series") {
			header = line
			break
		}
	}
	if header == "" {
		return nil
	}
	lowerHeader := strings.ToLower(header)
	start := strings.Index(lowerHeader, "card series")
	end := len(header)
	for _, h := range []string{"card model", "card vendor", "card sku", "card partition"} {
		if i := strings.Index(lowerHeader, h); i > start && i < end {
			end = i
		}
	}
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if _, err := strconv.ParseUint(fields[0], 10, 32); err != nil {
			continue
		}
		name := ""
		if len(line) > start {
			name = strings.TrimSpace(line[start:min(end, len(line))])
		}
		if name == "" || isPlaceholderGPUName(name) {
			name = "AMD GPU"
		}
		out = append(out, name)
	}
	return out
}

// parseROCmSMI combines rocm-smi's VRAM and product output into one GPU per
// model. Integrated GPUs (2 GiB or less) are dropped when a discrete card
// is present, so an APU's iGPU doesn't inflate the count.
func parseROCmSMI(vramText, productText string) []GPU {
	vram := parseROCmVRAMBytes(vramText)
	names := parseROCmProductNames(productText)
	const igpuMax = 2 << 30
	hasDiscrete := slices.ContainsFunc(vram, func(v uint64) bool { return v > igpuMax })
	groups := map[string]*gpuGroup{}
	for i := range max(len(vram), len(names)) {
		var bytes uint64
		if i < len(vram) {
			bytes = vram[i]
		}
		if hasDiscrete && bytes <= igpuMax {
			continue
		}
		name := "AMD GPU"
		if i < len(names) {
			name = names[i]
		}
		g := groups[name]
		if g == nil {
			g = &gpuGroup{}
			groups[name] = g
		}
		g.count++
		g.vram = max(g.vram, gib(float64(bytes)))
	}
	var gpus []GPU
	for _, name := range sortedKeys(groups) {
		g := groups[name]
		vramGB := g.vram
		if vramGB == 0 {
			vramGB = estimateVRAMFromName(name)
		}
		gpus = append(gpus, GPU{Name: name, VRAMGB: vramGB, Count: g.count, Backend: BackendROCm})
	}
	return gpus
}

// amdCard is one AMD card read from /sys/class/drm/cardN/device.
type amdCard struct {
	name   string
	vramGB float64  // mem_info_vram_total; 0 when unreadable
	freeGB *float64 // total minus mem_info_vram_used, when both read
}

// scanAMDSysfsCards lists the AMD (vendor 0x1002) cards of a
// /sys/class/drm tree. VRAM comes only from mem_info_vram_total, never from
// a name guess, because the APU carve-out arithmetic relies on it being a
// hardware reading. name resolves a card's marketing name ("" if unknown)
// from its directory name and PCI slots.
func scanAMDSysfsCards(drm fs.FS, name func(card string, slots []string) string) []amdCard {
	entries, err := fs.ReadDir(drm, ".")
	if err != nil {
		return nil
	}
	read := func(p string) (string, bool) {
		b, err := fs.ReadFile(drm, p)
		return strings.TrimSpace(string(b)), err == nil
	}
	var cards []amdCard
	for _, e := range entries {
		card := e.Name()
		// cardN only, not connectors such as card0-DP-1.
		if !strings.HasPrefix(card, "card") || strings.Contains(card, "-") {
			continue
		}
		dev := path.Join(card, "device")
		if vendor, ok := read(path.Join(dev, "vendor")); !ok || vendor != "0x1002" {
			continue
		}
		var c amdCard
		if s, ok := read(path.Join(dev, "mem_info_vram_total")); ok {
			if total, err := strconv.ParseUint(s, 10, 64); err == nil && total > 0 {
				c.vramGB = gib(float64(total))
				if s, ok := read(path.Join(dev, "mem_info_vram_used")); ok {
					if used, err := strconv.ParseUint(s, 10, 64); err == nil {
						c.freeGB = ptr(gib(float64(total - min(used, total))))
					}
				}
			}
		}
		var slots []string
		if uevent, ok := read(path.Join(dev, "uevent")); ok {
			for _, l := range strings.Split(uevent, "\n") {
				if slot, ok := strings.CutPrefix(strings.TrimSpace(l), "PCI_SLOT_NAME="); ok {
					slots = append(slots, slot)
				}
			}
		}
		c.name = name(card, slots)
		if c.name == "" {
			c.name = "AMD GPU"
		}
		cards = append(cards, c)
	}
	return cards
}

// groupAMDSysfsCards groups sysfs cards by model (estimating VRAM from the
// name where sysfs had none) and drops integrated GPUs when a discrete one
// is present. AMD without ROCm runs through Vulkan.
func groupAMDSysfsCards(cards []amdCard) []GPU {
	groups := map[string]*gpuGroup{}
	for _, c := range cards {
		vram := c.vramGB
		if vram == 0 {
			vram = estimateVRAMFromName(c.name)
		}
		g, seen := groups[c.name]
		if !seen {
			g = &gpuGroup{}
			groups[c.name] = g
		}
		g.count++
		g.vram = max(g.vram, vram)
		free := 0.0
		if c.freeGB != nil {
			free = *c.freeGB
		}
		g.addFree(!seen, free, c.freeGB != nil)
	}
	// A card whose VRAM is unknown gets the benefit of the doubt: only a
	// known small VRAM marks a card as integrated-class.
	hasDiscrete := false
	for name, g := range groups {
		if !isIntegratedGPUName(name) && g.vram > 2 {
			hasDiscrete = true
		}
	}
	var gpus []GPU
	for _, name := range sortedKeys(groups) {
		g := groups[name]
		if hasDiscrete && (isIntegratedGPUName(name) || (g.vram > 0 && g.vram <= 2)) {
			continue
		}
		gpu := GPU{Name: name, VRAMGB: g.vram, Count: g.count, Backend: BackendVulkan}
		if g.freeOK {
			gpu.FreeVRAMGB = ptr(g.free)
		}
		gpus = append(gpus, gpu)
	}
	return gpus
}

// apuCarveoutGB picks the integrated GPU's measured VRAM, which on an APU
// is the BIOS carve-out. Exactly one integrated card with a reading is
// required, since the figure is added straight onto the RAM total.
func apuCarveoutGB(cards []amdCard) (float64, bool) {
	found, carveout := 0, 0.0
	for _, c := range cards {
		if isIntegratedGPUName(c.name) && c.vramGB > 0 {
			found++
			carveout = c.vramGB
		}
	}
	return carveout, found == 1
}

// isAMDLspciLine matches AMD/ATI display controllers by vendor. llmfit
// matches the substring "ati", which "NVIDIA Corporation" also contains, so
// this checks AMD's PCI vendor ID instead.
func isAMDLspciLine(lower string) bool {
	return strings.Contains(lower, "amd") || strings.Contains(lower, "[1002:")
}

// lspciGPUName finds a GPU's name in lspci -nnD output, preferring the line
// of one of the card's PCI slots.
func lspciGPUName(lspci string, slots []string, vendor func(lower string) bool) string {
	lines := strings.Split(lspci, "\n")
	isGPU := func(line string) bool {
		lower := strings.ToLower(line)
		return (strings.Contains(lower, "vga") || strings.Contains(lower, "3d") || strings.Contains(lower, "display")) && vendor(lower)
	}
	for _, slot := range slots {
		for _, line := range lines {
			if strings.HasPrefix(line, slot) && isGPU(line) {
				if name := lspciModel(line); name != "" {
					return name
				}
			}
		}
	}
	// Headless cards (Instinct) are "Display controller", not "VGA".
	for _, line := range lines {
		if isGPU(line) {
			if name := lspciModel(line); name != "" {
				return name
			}
		}
	}
	return ""
}

// lspciModel extracts a model name from an lspci line, preferring the
// longest readable bracketed token such as "[Radeon RX 7900 XT/7900 XTX]".
func lspciModel(line string) string {
	best := ""
	rest := line
	for {
		open := strings.IndexByte(rest, '[')
		if open < 0 {
			break
		}
		after := rest[open+1:]
		end := strings.IndexByte(after, ']')
		if end < 0 {
			break
		}
		token := strings.TrimSpace(after[:end])
		usable := token != "" && !strings.Contains(token, ":") && strings.Trim(token, "0123456789") != ""
		if usable && len(token) > len(best) {
			best = token
		}
		rest = after[end+1:]
	}
	if best != "" {
		return best
	}
	if _, right, ok := strings.Cut(line, ": "); ok {
		return strings.TrimSpace(right)
	}
	return ""
}

// parseWindowsGPUList parses "Name|AdapterRAM" lines from
// Win32_VideoController.
func parseWindowsGPUList(text string) []GPU {
	var gpus []GPU
	for _, line := range strings.Split(text, "\n") {
		name, raw, ok := strings.Cut(strings.TrimSpace(line), "|")
		if !ok {
			continue
		}
		bytes, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
		if err != nil {
			continue
		}
		if g, ok := windowsGPUFromWMI(strings.TrimSpace(name), bytes); ok {
			gpus = append(gpus, g)
		}
	}
	return gpus
}

// windowsGPUFromWMI turns one adapter into a GPU, skipping display-only
// adapters (Microsoft Basic Display, virtual adapters, old Intel HD).
func windowsGPUFromWMI(name string, rawVRAM uint64) (GPU, bool) {
	lower := strings.ToLower(name)
	if lower == "" || strings.Contains(lower, "microsoft") || strings.Contains(lower, "basic") ||
		strings.Contains(lower, "virtual") || isLegacyIntelIGPUName(name) {
		return GPU{}, false
	}
	return GPU{Name: name, VRAMGB: resolveWMIVRAM(rawVRAM, name), Count: 1, Backend: inferGPUBackend(name)}, true
}

// resolveWMIVRAM: AdapterRAM is a 32-bit field, so cards of 4 GB and more
// report about 4 GB; the name table gives a better figure then.
func resolveWMIVRAM(rawBytes uint64, name string) float64 {
	vram := gib(float64(rawBytes))
	if est := estimateVRAMFromName(name); vram < 0.1 || (vram <= 4.1 && est > 4.1) {
		if est > 0 {
			vram = est
		}
	}
	return vram
}

// inferGPUBackend guesses the backend from a Windows adapter name. AMD on
// Windows runs through Vulkan, since ROCm support there is limited.
func inferGPUBackend(name string) Backend {
	l := strings.ToLower(name)
	switch {
	case strings.Contains(l, "nvidia") || strings.Contains(l, "geforce") || strings.Contains(l, "quadro") ||
		strings.Contains(l, "tesla") || strings.Contains(l, "rtx"):
		return BackendCUDA
	case strings.Contains(l, "amd") || strings.Contains(l, "radeon") || strings.Contains(l, "ati"):
		return BackendVulkan
	case strings.Contains(l, "intel") || strings.Contains(l, "arc"):
		return BackendSYCL
	}
	return BackendVulkan
}

// parseWindowsRegistryVRAM parses "DriverDesc|bytes" lines. Values under
// 64 MiB are dropped: a driver that wrote megabytes would otherwise replace
// a good estimate with a few kilobytes.
func parseWindowsRegistryVRAM(text string) map[string]uint64 {
	out := map[string]uint64{}
	for _, line := range strings.Split(text, "\n") {
		desc, raw, ok := strings.Cut(strings.TrimSpace(line), "|")
		if !ok {
			continue
		}
		desc = strings.TrimSpace(desc)
		bytes, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
		if err != nil || desc == "" || bytes < 64<<20 {
			continue
		}
		if _, dup := out[desc]; !dup {
			out[desc] = bytes
		}
	}
	return out
}

// applyRegistryVRAM replaces WMI's capped VRAM with the registry's exact
// figure, matched by driver description.
func applyRegistryVRAM(gpus []GPU, registry map[string]uint64) []GPU {
	for i := range gpus {
		if bytes, ok := matchRegistryVRAM(gpus[i].Name, registry); ok {
			gpus[i].VRAMGB = gib(float64(bytes))
		}
	}
	return gpus
}

// matchRegistryVRAM prefers an exact (normalized) name match; a partial
// match counts only when it is the single one, so "RTX 4090" never binds to
// an "RTX 4090 Ti" entry by accident.
func matchRegistryVRAM(name string, registry map[string]uint64) (uint64, bool) {
	target := normalizeGPUNameForMatch(name)
	if target == "" {
		return 0, false
	}
	var partial []uint64
	for _, desc := range sortedKeys(registry) {
		d := normalizeGPUNameForMatch(desc)
		if d == target {
			return registry[desc], true
		}
		if d != "" && (strings.Contains(d, target) || strings.Contains(target, d)) {
			partial = append(partial, registry[desc])
		}
	}
	if len(partial) == 1 {
		return partial[0], true
	}
	return 0, false
}

// normalizeGPUNameForMatch lowercases and drops trademark marks and
// punctuation, since WMI and the registry decorate the same adapter
// differently ("Radeon(TM)" vs "Radeon").
func normalizeGPUNameForMatch(name string) string {
	s := strings.NewReplacer("(tm)", " ", "(r)", " ", "(c)", " ").Replace(strings.ToLower(name))
	s = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return ' '
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// isLegacyIntelIGPUName reports Intel HD/UHD-era iGPUs, which run LLMs no
// faster than the CPU beside them and whose "VRAM" is just shared RAM.
// Iris Xe and Arc iGPUs are kept.
func isLegacyIntelIGPUName(name string) bool {
	l := strings.ToLower(name)
	containsAny := func(subs ...string) bool {
		return slices.ContainsFunc(subs, func(s string) bool { return strings.Contains(l, s) })
	}
	legacyCodename := strings.Contains(l, "integrated graphics controller") &&
		containsAny("sandybridge", "sandy bridge", "ivybridge", "ivy bridge", "haswell", "broadwell")
	legacyGeneration := strings.Contains(l, "graphics controller") &&
		containsAny("2nd generation core processor", "2nd gen core processor", "3rd gen core processor", "4th gen core processor")
	return strings.Contains(l, "intel") && (strings.Contains(l, "hd graphics") || legacyCodename || legacyGeneration)
}

// preferDiscreteGPUs drops integrated GPUs when a discrete one is present,
// so the discrete GPU becomes primary.
func preferDiscreteGPUs(gpus []GPU) []GPU {
	var discrete []GPU
	for _, g := range gpus {
		if !isIntegratedGPU(g.Name, g.VRAMGB) {
			discrete = append(discrete, g)
		}
	}
	if len(discrete) == 0 {
		return gpus
	}
	return discrete
}

// isIntegratedGPU adds VRAM to the name check: datacenter cards such as the
// Instinct MI50 can report the generic "AMD Radeon Graphics" name, but no
// iGPU has 8 GB of dedicated VRAM.
func isIntegratedGPU(name string, vramGB float64) bool {
	if !isIntegratedGPUName(name) {
		return false
	}
	if isAMDMobileIGPUName(name) {
		return true
	}
	l := strings.ToLower(name)
	amdGeneric := strings.Contains(l, "radeon") && !strings.Contains(l, "(integrated)")
	return !(amdGeneric && vramGB >= 8)
}

// isIntegratedGPUName matches integrated GPU naming: anything tagged
// "(integrated)", AMD mobile iGPUs, Intel without a discrete Arc product
// code, and "Radeon Graphics" without a discrete series.
func isIntegratedGPUName(name string) bool {
	l := strings.ToLower(name)
	switch {
	case strings.Contains(l, "(integrated)"), isAMDMobileIGPUName(name):
		return true
	case strings.Contains(l, "intel"):
		return !hasIntelArcProductCode(l)
	case strings.Contains(l, "radeon") && strings.Contains(l, "graphics"):
		discreteTag := strings.Contains(l, "rx ") || strings.Contains(l, "pro ") || strings.Contains(l, "vega") ||
			strings.Contains(l, " vii") || strings.Contains(l, " w")
		return !discreteTag
	}
	return false
}

// asciiTokens splits on anything that is not an ASCII letter or digit.
func asciiTokens(lower string) []string {
	return strings.FieldsFunc(lower, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9')
	})
}

// hasIntelArcProductCode finds a discrete Arc code such as A770, B580 or
// the mobile A370M. No Intel iGPU name carries one.
func hasIntelArcProductCode(lower string) bool {
	for _, tok := range asciiTokens(lower) {
		if len(tok) < 3 || len(tok) > 5 {
			continue
		}
		code := strings.TrimSuffix(tok, "m")
		if (code[0] == 'a' || code[0] == 'b') && strings.Trim(code[1:], "0123456789") == "" {
			return true
		}
	}
	return false
}

// isAMDMobileIGPUName matches "Radeon 780M"-style iGPU names; discrete
// mobile parts carry an RX prefix.
func isAMDMobileIGPUName(name string) bool {
	l := strings.ToLower(name)
	if !strings.Contains(l, "radeon") || strings.Contains(l, "rx") {
		return false
	}
	for _, tok := range asciiTokens(l) {
		if len(tok) == 4 && tok[3] == 'm' && strings.Trim(tok[:3], "0123456789") == "" {
			return true
		}
	}
	return false
}

// isPlaceholderGPUName is a name a tool printed when it could not read one.
func isPlaceholderGPUName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "n/a", "na", "n-a", `n\a`, "unknown", "none", "null", "-":
		return true
	}
	return false
}

// isGenericAMDGPUName is a name too vague to identify the model.
func isGenericAMDGPUName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "amd gpu", "amd/ati", "radeon graphics", "amd radeon graphics":
		return true
	}
	return false
}

// isAMDUnifiedMemoryAPU: only Ryzen AI MAX (Strix Halo) shares a large pool
// with its GPU the way Apple Silicon does.
func isAMDUnifiedMemoryAPU(cpuName string) bool {
	return strings.Contains(strings.ToLower(cpuName), "ryzen ai max")
}

// isAMDAPU: any Ryzen whose brand string names its Radeon iGPU, the
// population that can carry a BIOS memory carve-out.
func isAMDAPU(cpuName string) bool {
	l := strings.ToLower(cpuName)
	return strings.Contains(l, "ryzen") && strings.Contains(l, "radeon")
}

// applyAMDUnifiedAPU marks a Ryzen AI MAX's Radeon as unified memory sized
// to the whole RAM pool, creating it when no probe found it. Per-card
// probes only ever see the BIOS carve-out.
func applyAMDUnifiedAPU(gpus []GPU, cpuName string, poolGB float64) []GPU {
	if !isAMDUnifiedMemoryAPU(cpuName) {
		return gpus
	}
	for i := range gpus {
		l := strings.ToLower(gpus[i].Name)
		if strings.Contains(l, "amd") || strings.Contains(l, "radeon") {
			gpus[i].UnifiedMemory = true
			gpus[i].VRAMGB = poolGB
			gpus[i].FreeVRAMGB = nil
			if isGenericAMDGPUName(gpus[i].Name) {
				gpus[i].Name = cpuName + " (integrated)"
			}
			return gpus
		}
	}
	return append(gpus, GPU{Name: cpuName + " (integrated)", VRAMGB: poolGB, Count: 1, Backend: BackendVulkan, UnifiedMemory: true})
}

// isNvidiaUnifiedMemoryGPU: GB10/GB20 (DGX Spark) and Jetson ("(nvgpu)")
// share system RAM. 2e12 is GB10's PCI device ID, shown inside containers.
func isNvidiaUnifiedMemoryGPU(name string) bool {
	l := strings.ToLower(name)
	return strings.Contains(l, "gb10") || strings.Contains(l, "gb20") || strings.Contains(l, "2e12") || strings.Contains(l, "nvgpu")
}

// parseVulkanDeviceNames reads device names from vulkaninfo, either
// "deviceName = X" fields or "GPU id = 0 (X)" lines.
func parseVulkanDeviceNames(text string) []string {
	var names []string
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if key, value, ok := strings.Cut(t, "="); ok && strings.EqualFold(strings.TrimSpace(key), "deviceName") {
			if v := strings.TrimSpace(value); v != "" {
				names = append(names, v)
			}
			continue
		}
		if rest, ok := strings.CutPrefix(t, "GPU id"); ok {
			open, close := strings.IndexByte(rest, '('), strings.LastIndexByte(rest, ')')
			if open >= 0 && close > open+1 {
				if v := strings.TrimSpace(rest[open+1 : close]); v != "" {
					names = append(names, v)
				}
			}
		}
	}
	return names
}

// isSoftwareVulkanDevice: software rasterizers and CPUs exposed as Vulkan
// devices are not inference GPUs.
func isSoftwareVulkanDevice(name string) bool {
	l := strings.ToLower(name)
	for _, s := range []string{"llvmpipe", "lavapipe", "swiftshader", "software rasterizer", "core processor"} {
		if strings.Contains(l, s) {
			return true
		}
	}
	return false
}

// parseVulkanGPUs groups vulkaninfo devices by name. Vulkan reports no
// VRAM here, so these mainly fill in GPUs no vendor tool found.
func parseVulkanGPUs(text string) []GPU {
	counts := map[string]int{}
	for _, name := range parseVulkanDeviceNames(text) {
		if !isSoftwareVulkanDevice(name) && !isLegacyIntelIGPUName(name) {
			counts[name]++
		}
	}
	var gpus []GPU
	for _, name := range sortedKeys(counts) {
		gpus = append(gpus, GPU{Name: name, Count: counts[name], Backend: BackendVulkan})
	}
	return gpus
}

// mergeGPUSources adds fallback GPUs not already in primary. A duplicate
// only contributes VRAM the primary entry lacked.
func mergeGPUSources(primary, fallback []GPU) []GPU {
	for _, c := range fallback {
		i := slices.IndexFunc(primary, func(g GPU) bool { return isSameGPUName(g.Name, c.Name) })
		if i < 0 {
			primary = append(primary, c)
			continue
		}
		if !primary[i].UnifiedMemory && primary[i].VRAMGB == 0 && c.VRAMGB > 0 {
			primary[i].VRAMGB = c.VRAMGB
		}
	}
	sortGPUs(primary)
	return primary
}

// isSameGPUName decides whether two tools describe the same GPU. ROCm gives
// family names ("Radeon RX 7700S/7600/7600S") where Vulkan gives the exact
// model plus a driver codename, so AMD and Intel names also match on a
// shared model number.
func isSameGPUName(a, b string) bool {
	if normalizeGPUNameForDedupe(a) == normalizeGPUNameForDedupe(b) {
		return true
	}
	al, bl := strings.ToLower(a), strings.ToLower(b)
	isAMD := func(s string) bool {
		return strings.Contains(s, "radeon") || strings.HasPrefix(s, "amd ") || strings.Contains(s, " amd ")
	}
	shared := func(x, y []string) bool {
		return slices.ContainsFunc(x, func(n string) bool { return slices.Contains(y, n) })
	}
	if isAMD(al) && isAMD(bl) && shared(gpuModelNumbers(al), gpuModelNumbers(bl)) {
		return true
	}
	if strings.Contains(al, "intel") && strings.Contains(bl, "intel") {
		an, bn := gpuModelNumbers(al), gpuModelNumbers(bl)
		at, bt := arcModelTokens(al), arcModelTokens(bl)
		if shared(an, bn) || shared(at, bt) {
			return true
		}
		// A platform has one Intel iGPU, so an integrated entry matches a
		// Vulkan Intel device that names no model at all.
		if (strings.Contains(al, "(integrated)") && len(bn) == 0 && len(bt) == 0) ||
			(strings.Contains(bl, "(integrated)") && len(an) == 0 && len(at) == 0) {
			return true
		}
	}
	return false
}

// gpuModelNumbers extracts 3 to 5 digit runs such as "7600".
func gpuModelNumbers(name string) []string {
	var out []string
	run := ""
	flush := func() {
		if len(run) >= 3 && len(run) <= 5 {
			out = append(out, run)
		}
		run = ""
	}
	for _, r := range name {
		if r >= '0' && r <= '9' {
			run += string(r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

// arcModelTokens extracts Intel Arc model tokens such as "a770" or "b70".
func arcModelTokens(name string) []string {
	var out []string
	for _, tok := range asciiTokens(name) {
		if len(tok) >= 3 && len(tok) <= 5 && strings.ContainsRune("abcd", rune(tok[0])) &&
			strings.Trim(tok[1:], "0123456789") == "" {
			out = append(out, tok)
		}
	}
	return out
}

// normalizeGPUNameForDedupe lowercases and collapses punctuation runs to
// single spaces.
func normalizeGPUNameForDedupe(name string) string {
	var b strings.Builder
	sep := true
	for _, r := range strings.ToLower(name) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			sep = false
		} else if !sep {
			b.WriteByte(' ')
			sep = true
		}
	}
	return strings.TrimSpace(b.String())
}

// parseMacDisplaysJSON reads system_profiler SPDisplaysDataType -json.
// Apple Silicon GPUs get the whole RAM pool as VRAM; Intel and AMD GPUs in
// Intel Macs keep the VRAM the profiler reports. Entries without Metal are
// skipped.
func parseMacDisplaysJSON(data []byte, totalRAMGB float64) []GPU {
	var doc struct {
		Displays []map[string]any `json:"SPDisplaysDataType"`
	}
	if json.Unmarshal(data, &doc) != nil {
		return nil
	}
	str := func(e map[string]any, keys ...string) (string, bool) {
		for _, k := range keys {
			if v, present := e[k]; present {
				s, ok := v.(string)
				return s, ok
			}
		}
		return "", false
	}
	var gpus []GPU
	for _, e := range doc.Displays {
		name, ok := str(e, "sppci_model", "_name")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		unified := isAppleSiliconGPUName(name)
		family, _ := str(e, "spdisplays_mtlgpufamilysupport")
		metalStatus, _ := str(e, "spdisplays_metal")
		metal := strings.TrimSpace(family) != "" || metalStatus == "spdisplays_supported"
		if !metal && !unified {
			continue
		}
		g := GPU{Name: name, Count: 1, Backend: BackendMetal, UnifiedMemory: unified}
		if unified {
			// The pool is all of RAM, not a reported shared-VRAM figure.
			g.VRAMGB = totalRAMGB
		} else if s, ok := str(e, "spdisplays_vram", "_spdisplays_vram", "spdisplays_vram_shared"); ok {
			g.VRAMGB, _ = parseMemorySize(s)
		}
		gpus = append(gpus, g)
	}
	return gpus
}

// isAppleSiliconGPUName matches "Apple M2 Pro", "Apple A18 Pro" and
// "Apple GPU", but not other Apple-branded devices.
func isAppleSiliconGPUName(name string) bool {
	l := strings.ToLower(strings.TrimRight(strings.TrimSpace(name), ":"))
	if l == "apple gpu" {
		return true
	}
	for _, p := range []string{"apple m", "apple a"} {
		if rest, ok := strings.CutPrefix(l, p); ok && rest != "" && rest[0] >= '0' && rest[0] <= '9' {
			return true
		}
	}
	return false
}

// isAppleGPUReport scans the plain-text system_profiler report for an
// Apple Silicon GPU heading or chipset line.
func isAppleGPUReport(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "Chipset Model:"); ok {
			line = rest
		}
		if isAppleSiliconGPUName(line) {
			return true
		}
	}
	return false
}

// parseMemorySize reads sizes such as "16 GB", "1536 MB" or "1T" as GiB.
func parseMemorySize(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	end := strings.IndexFunc(s, func(r rune) bool { return !(r >= '0' && r <= '9' || r == '.') })
	if end < 0 {
		end = len(s)
	}
	v, err := strconv.ParseFloat(s[:end], 64)
	if err != nil || v < 0 {
		return 0, false
	}
	switch strings.ToLower(strings.TrimSpace(s[end:])) {
	case "g", "gb", "gib", "":
		return v, true
	case "m", "mb", "mib":
		return v / 1024, true
	case "t", "tb", "tib":
		return v * 1024, true
	}
	return 0, false
}

// parseMeminfo reads MemTotal and MemAvailable (GiB) from /proc/meminfo,
// estimating available from free + buffers + cache on old kernels.
func parseMeminfo(text string) (total, available float64) {
	kb := map[string]float64{}
	for _, line := range strings.Split(text, "\n") {
		key, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if f := strings.Fields(rest); len(f) > 0 {
			if v, err := strconv.ParseFloat(f[0], 64); err == nil {
				kb[key] = v
			}
		}
	}
	total = kb["MemTotal"] / (1 << 20)
	if v, ok := kb["MemAvailable"]; ok {
		available = v / (1 << 20)
	} else {
		available = (kb["MemFree"] + kb["Buffers"] + kb["Cached"]) / (1 << 20)
	}
	return total, available
}

// parseCPUInfo finds a CPU name in /proc/cpuinfo, trying the keys x86, ARM
// boards and other architectures use. A bare number (the "processor" index
// on ARM servers) is not a name, which llmfit would return.
func parseCPUInfo(text string) string {
	lines := strings.Split(text, "\n")
	for _, key := range []string{"model name", "hardware", "processor", "cpu model", "model"} {
		for _, line := range lines {
			lhs, rhs, ok := strings.Cut(line, ":")
			if !ok || !strings.EqualFold(strings.TrimSpace(lhs), key) {
				continue
			}
			v := strings.TrimSpace(rhs)
			if v != "" && !strings.EqualFold(v, "unknown") && strings.Trim(v, "0123456789") != "" {
				return v
			}
		}
	}
	return ""
}

// parseVMStat computes macOS available memory (GiB) from vm_stat as free +
// inactive + purgeable pages.
func parseVMStat(text string) float64 {
	lines := strings.Split(text, "\n")
	pageSize := 16384.0 // Apple Silicon's page size
	if len(lines) > 0 {
		if _, after, ok := strings.Cut(lines[0], "page size of "); ok {
			if f := strings.Fields(after); len(f) > 0 {
				if v, err := strconv.ParseFloat(f[0], 64); err == nil && v > 0 {
					pageSize = v
				}
			}
		}
	}
	pages := 0.0
	for _, line := range lines {
		for _, key := range []string{"Pages free", "Pages inactive", "Pages purgeable"} {
			if rest, ok := strings.CutPrefix(line, key+":"); ok {
				if v, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(rest), "."), 64); err == nil {
					pages += v
				}
			}
		}
	}
	return gib(pages * pageSize)
}
