//go:build windows

package vault

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

func systemKeyring() Keyring { return dpapiKeyring{} }

// dpapiKeyring protects the key with DPAPI for the current Windows user. The
// protected key is kept in the vault header; only this user on this computer
// can unprotect it.
type dpapiKeyring struct{}

var dpapiEntropy = []byte("NullBot vault key v1")

func (dpapiKeyring) Kind() Protection { return DPAPI }
func (dpapiKeyring) Available() bool  { return true }
func (dpapiKeyring) Delete(string) error {
	return nil
}

func (dpapiKeyring) Store(_ string, key []byte) ([]byte, error) { return dpapi(key, true) }
func (dpapiKeyring) Load(_ string, wrapped []byte) ([]byte, error) {
	if len(wrapped) == 0 {
		return nil, ErrCorrupt
	}
	return dpapi(wrapped, false)
}

func dpapi(data []byte, protect bool) ([]byte, error) {
	if len(data) == 0 || len(data) > 64*1024 {
		return nil, ErrCorrupt
	}
	in := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	entropy := windows.DataBlob{Size: uint32(len(dpapiEntropy)), Data: &dpapiEntropy[0]}
	var out windows.DataBlob
	var err error
	// Current-user scope (never CRYPTPROTECT_LOCAL_MACHINE), and never a prompt.
	if protect {
		err = windows.CryptProtectData(&in, nil, &entropy, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	} else {
		err = windows.CryptUnprotectData(&in, nil, &entropy, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	}
	if err != nil {
		return nil, errors.New("DPAPI refused the vault key; it opens only for the Windows user who created it")
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	raw := unsafe.Slice(out.Data, int(out.Size))
	result := append([]byte(nil), raw...)
	clear(raw)
	return result, nil
}
