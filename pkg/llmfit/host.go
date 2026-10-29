package llmfit

import (
	"context"
	"errors"
	"strings"
)

// linuxHost reads RAM from /proc/meminfo and the CPU name from
// /proc/cpuinfo, asking Android's getprop when cpuinfo names no CPU.
func linuxHost(ctx context.Context, d *detector) (hostInfo, error) {
	var h hostInfo
	meminfo, err := d.readFile("proc/meminfo")
	if err != nil {
		return h, err
	}
	h.totalGB, h.availableGB = parseMeminfo(meminfo)
	if h.totalGB <= 0 {
		return h, errors.New("no MemTotal in /proc/meminfo")
	}
	if cpuinfo, err := d.readFile("proc/cpuinfo"); err == nil {
		h.cpuName = parseCPUInfo(cpuinfo)
	}
	if h.cpuName == "" {
		if out, ok := d.text(ctx, "getprop", "ro.soc.model"); ok {
			h.cpuName = strings.TrimSpace(out)
		}
	}
	return h, nil
}
