//go:build darwin

package llmfit

import (
	"context"

	"golang.org/x/sys/unix"
)

// nativeHost reads total RAM and the CPU name from sysctl, and available
// RAM from vm_stat.
func nativeHost(ctx context.Context, d *detector) (hostInfo, error) {
	var h hostInfo
	h.cpuName, _ = unix.Sysctl("machdep.cpu.brand_string")
	total, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return h, err
	}
	h.totalGB = gib(float64(total))
	h.availableGB = d.availableFromVMStat(ctx)
	return h, nil
}
