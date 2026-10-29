//go:build linux

package vault

func systemKeyring() Keyring { return newSecretService() }
