//go:build windows

package llmfit

import (
	"context"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// memoryStatusEx is the Win32 MEMORYSTATUSEX structure.
type memoryStatusEx struct {
	length               uint32
	memoryLoad           uint32
	totalPhys            uint64
	availPhys            uint64
	totalPageFile        uint64
	availPageFile        uint64
	totalVirtual         uint64
	availVirtual         uint64
	availExtendedVirtual uint64
}

var procGlobalMemoryStatusEx = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")

// nativeHost reads RAM from GlobalMemoryStatusEx and the CPU name from the
// registry.
func nativeHost(ctx context.Context, d *detector) (hostInfo, error) {
	var h hostInfo
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\CentralProcessor\0`, registry.QUERY_VALUE); err == nil {
		h.cpuName, _, _ = k.GetStringValue("ProcessorNameString")
		k.Close()
	}
	var ms memoryStatusEx
	ms.length = uint32(unsafe.Sizeof(ms))
	if r, _, err := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&ms))); r == 0 {
		return h, err
	}
	h.totalGB = gib(float64(ms.totalPhys))
	h.availableGB = gib(float64(ms.availPhys))
	return h, nil
}
