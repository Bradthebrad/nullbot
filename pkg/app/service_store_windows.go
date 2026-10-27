//go:build windows

package app

import (
	"golang.org/x/sys/windows"
	"unsafe"
)

func serviceLocalDataDir() (string, error) {
	return windows.KnownFolderPath(windows.FOLDERID_LocalAppData, 0)
}
func protectServiceData(plain []byte) ([]byte, error)    { return serviceDPAPI(plain, true) }
func unprotectServiceData(sealed []byte) ([]byte, error) { return serviceDPAPI(sealed, false) }
func serviceDPAPI(data []byte, protect bool) ([]byte, error) {
	if len(data) == 0 || len(data) > maxServiceStoreBytes {
		return nil, errServiceStore
	}
	in := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	var out windows.DataBlob
	var err error
	// Current-user DPAPI, never CRYPTPROTECT_LOCAL_MACHINE; never display prompts.
	if protect {
		err = windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	} else {
		err = windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	}
	if err != nil {
		return nil, errServiceStore
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	if out.Size > maxServiceStoreBytes {
		return nil, errServiceStore
	}
	raw := unsafe.Slice(out.Data, int(out.Size))
	result := append([]byte(nil), raw...)
	clear(raw)
	return result, nil
}
