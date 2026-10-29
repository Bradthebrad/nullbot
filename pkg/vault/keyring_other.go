//go:build !windows && !darwin && !linux

package vault

// Other systems have no keychain NullBot knows; vaults there use a passphrase.
func systemKeyring() Keyring { return nil }
