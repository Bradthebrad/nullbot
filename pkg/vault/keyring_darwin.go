//go:build darwin

package vault

func systemKeyring() Keyring { return newMacKeychain() }
