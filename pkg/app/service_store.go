package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var serviceStoreMu sync.Mutex

// Test seam: production always uses the OS known-folder lookup, never an env override.
var serviceDataDir = serviceLocalDataDir
var errServiceStore = errors.New("service account secure storage is unavailable or invalid")
var errServicePlatform = errors.New("service account secure storage requires Windows DPAPI")

const maxServiceStoreBytes = 4 * 1024 * 1024

// The vault is outside AppDir and workspace config, under the OS local profile.
// The config root digest isolates independent installations without persisting paths.
func serviceStorePath(config Config) (string, error) {
	root, err := serviceDataDir()
	if err != nil {
		return "", errServicePlatform
	}
	appDir, err := filepath.Abs(config.AppDir)
	if err != nil || config.AppDir == "" {
		return "", errServiceStore
	}
	hash := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(appDir))))
	return filepath.Join(root, "NullBot", "ServiceAccounts", hex.EncodeToString(hash[:16]), "accounts.dpapi"), nil
}

func loadServiceAccounts(config Config) ([]ServiceAccountInput, error) {
	serviceStoreMu.Lock()
	defer serviceStoreMu.Unlock()
	return readServiceAccounts(config)
}
func readServiceAccounts(config Config) ([]ServiceAccountInput, error) {
	path, err := serviceStorePath(config)
	if err != nil {
		return nil, err
	}
	if err := rejectCredentialLinks(path); err != nil {
		return nil, errServiceStore
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return []ServiceAccountInput{}, nil
	}
	if err != nil {
		return nil, errServiceStore
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxServiceStoreBytes {
		return nil, errServiceStore
	}
	sealed, err := io.ReadAll(io.LimitReader(f, maxServiceStoreBytes+1))
	if err != nil || len(sealed) > maxServiceStoreBytes {
		return nil, errServiceStore
	}
	plain, err := unprotectServiceData(sealed)
	if err != nil {
		return nil, errServiceStore
	}
	defer clear(plain)
	var accounts []ServiceAccountInput
	if json.Unmarshal(plain, &accounts) != nil || len(accounts) > 100 {
		return nil, errServiceStore
	}
	seen := map[string]bool{}
	for _, account := range accounts {
		if validateServiceAccount(account) != nil || seen[account.ID] {
			return nil, errServiceStore
		}
		seen[account.ID] = true
	}
	return accounts, nil
}
func writeServiceAccounts(config Config, accounts []ServiceAccountInput) error {
	path, err := serviceStorePath(config)
	if err != nil {
		return err
	}
	plain, err := json.Marshal(accounts)
	if err != nil || len(plain) > maxServiceStoreBytes/2 {
		return errServiceStore
	}
	defer clear(plain)
	sealed, err := protectServiceData(plain)
	if err != nil {
		return errServiceStore
	}
	if rejectCredentialLinks(path) != nil {
		return errServiceStore
	}
	if os.MkdirAll(filepath.Dir(path), 0700) != nil {
		return errServiceStore
	}
	if rejectCredentialLinks(path) != nil {
		return errServiceStore
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".accounts-*.dpapi")
	if err != nil {
		return errServiceStore
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if f.Chmod(0600) != nil {
		f.Close()
		return errServiceStore
	}
	if _, err := f.Write(sealed); err != nil {
		f.Close()
		return errServiceStore
	}
	if f.Sync() != nil {
		f.Close()
		return errServiceStore
	}
	if f.Close() != nil || rejectCredentialLinks(path) != nil {
		return errServiceStore
	}
	if os.Rename(tmp, path) != nil {
		return errServiceStore
	}
	return nil
}

// Reject links in every ancestor, including Windows junctions and other reparse
// tags. This is defense-in-depth, not an OS sandbox against same-user TOCTOU.
func rejectCredentialLinks(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return errServiceStore
	}
	if err := rejectProjectReparsePoints(abs); err != nil {
		return errServiceStore
	}
	for p := abs; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil && !os.IsNotExist(err) {
			return errServiceStore
		}
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return errServiceStore
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	return nil
}
