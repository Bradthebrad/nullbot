package vault

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeKeyring keeps keys in memory, like a keychain.
type fakeKeyring struct {
	keys    map[string][]byte
	wrap    bool // behave like DPAPI: return the key "wrapped" for the header
	missing bool
}

func (k *fakeKeyring) Kind() Protection {
	if k.wrap {
		return DPAPI
	}
	return Keychain
}
func (k *fakeKeyring) Available() bool { return !k.missing }
func (k *fakeKeyring) Store(id string, key []byte) ([]byte, error) {
	if k.wrap {
		out := append([]byte("wrapped:"), key...)
		return out, nil
	}
	if k.keys == nil {
		k.keys = map[string][]byte{}
	}
	k.keys[id] = append([]byte(nil), key...)
	return nil, nil
}
func (k *fakeKeyring) Load(id string, wrapped []byte) ([]byte, error) {
	if k.wrap {
		return append([]byte(nil), bytes.TrimPrefix(wrapped, []byte("wrapped:"))...), nil
	}
	key, ok := k.keys[id]
	if !ok {
		return nil, errors.New("no such item")
	}
	return append([]byte(nil), key...), nil
}
func (k *fakeKeyring) Delete(id string) error { delete(k.keys, id); return nil }

func noPassphrase() (string, bool) { return "", false }

func newTestVault(t *testing.T, kr Keyring, pass func() (string, bool)) *Vault {
	t.Helper()
	if pass == nil {
		pass = noPassphrase
	}
	return New(filepath.Join(t.TempDir(), "vault", "vault.json"), Options{Keyring: kr, Passphrase: pass, Iterations: 1000})
}

func TestPassphraseVaultRoundTrip(t *testing.T) {
	v := newTestVault(t, &fakeKeyring{missing: true}, nil)
	if err := v.Create(Auto, ""); !errors.Is(err, ErrNoKeychain) {
		t.Fatalf("auto without keychain or passphrase: %v", err)
	}
	if err := v.Create(Passphrase, "short"); err == nil {
		t.Fatal("a short passphrase was accepted")
	}
	if err := v.CreateWith(Passphrase, "correct horse battery", []byte(`{"a":"sk-very-secret-value"}`)); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(v.Path())
	if bytes.Contains(raw, []byte("sk-very-secret-value")) || bytes.Contains(raw, []byte("correct horse")) {
		t.Fatal("the vault file holds plain text")
	}
	info, _ := os.Stat(v.Path())
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("vault file mode %v", info.Mode())
	}

	// A new process has neither the key nor the passphrase.
	other := New(v.Path(), Options{Keyring: &fakeKeyring{missing: true}, Passphrase: noPassphrase})
	if _, err := other.Read(); !errors.Is(err, ErrLocked) {
		t.Fatalf("read without passphrase: %v", err)
	}
	if st := other.Status(); !st.Exists || st.Unlocked || st.Protection != Passphrase {
		t.Fatalf("status %+v", st)
	}
	if err := other.Unlock("wrong passphrase!"); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("wrong passphrase: %v", err)
	}
	if err := other.Unlock("correct horse battery"); err != nil {
		t.Fatal(err)
	}
	got, err := other.Read()
	if err != nil || string(got) != `{"a":"sk-very-secret-value"}` {
		t.Fatalf("read %q %v", got, err)
	}
	if err := other.Update(func(p []byte) ([]byte, error) { return []byte(`{"b":1}`), nil }); err != nil {
		t.Fatal(err)
	}
	// The passphrase from the environment opens it too.
	env := New(v.Path(), Options{Keyring: &fakeKeyring{missing: true}, Passphrase: func() (string, bool) { return "correct horse battery", true }})
	got, err = env.Read()
	if err != nil || string(got) != `{"b":1}` {
		t.Fatalf("env read %q %v", got, err)
	}
	other.Lock()
	if _, err := other.Read(); !errors.Is(err, ErrLocked) {
		t.Fatalf("read after lock: %v", err)
	}
}

func TestKeychainVaultAndTampering(t *testing.T) {
	kr := &fakeKeyring{}
	v := newTestVault(t, kr, nil)
	if err := v.CreateWith(Auto, "", []byte(`{"token":"ghp_abcdefghijklmnop"}`)); err != nil {
		t.Fatal(err)
	}
	if st := v.Status(); st.Protection != Keychain || !st.Unlocked {
		t.Fatalf("status %+v", st)
	}
	if err := v.Create(Auto, ""); !errors.Is(err, ErrExists) {
		t.Fatalf("second create: %v", err)
	}
	// A fresh handle reads the key from the keychain.
	fresh := New(v.Path(), Options{Keyring: kr, Passphrase: noPassphrase})
	if got, err := fresh.Read(); err != nil || !strings.Contains(string(got), "ghp_") {
		t.Fatalf("fresh read %q %v", got, err)
	}

	// Swapping the protection in the header breaks the authentication.
	var h map[string]any
	raw, _ := os.ReadFile(v.Path())
	_ = json.Unmarshal(raw, &h)
	h["updated"] = "2020-01-01T00:00:00Z" // harmless metadata
	data, _ := json.Marshal(h)
	_ = os.WriteFile(v.Path(), data, 0o600)
	if _, err := fresh.Read(); err != nil {
		t.Fatalf("metadata change broke the vault: %v", err)
	}
	h["data"] = flipBase64(h["data"].(string))
	data, _ = json.Marshal(h)
	_ = os.WriteFile(v.Path(), data, 0o600)
	if _, err := fresh.Read(); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("tampered data: %v", err)
	}
}

func flipBase64(s string) string {
	b := []byte(s)
	if b[5] == 'A' {
		b[5] = 'B'
	} else {
		b[5] = 'A'
	}
	return string(b)
}

func TestReprotectMovesBetweenPassphraseAndKeychain(t *testing.T) {
	kr := &fakeKeyring{}
	v := newTestVault(t, kr, nil)
	if err := v.CreateWith(Passphrase, "first passphrase", []byte(`{"x":"secret-one"}`)); err != nil {
		t.Fatal(err)
	}
	if err := v.Reprotect(Auto, ""); err != nil {
		t.Fatal(err)
	}
	if st := v.Status(); st.Protection != Keychain {
		t.Fatalf("after reprotect: %+v", st)
	}
	if len(kr.keys) != 1 {
		t.Fatalf("keychain holds %d keys", len(kr.keys))
	}
	if err := v.Reprotect(Passphrase, "second passphrase"); err != nil {
		t.Fatal(err)
	}
	if len(kr.keys) != 0 {
		t.Fatal("the old keychain item was left behind")
	}
	fresh := New(v.Path(), Options{Keyring: kr, Passphrase: noPassphrase})
	if err := fresh.Unlock("first passphrase"); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("old passphrase: %v", err)
	}
	if err := fresh.Unlock("second passphrase"); err != nil {
		t.Fatal(err)
	}
	if got, _ := fresh.Read(); string(got) != `{"x":"secret-one"}` {
		t.Fatalf("payload %q", got)
	}
	if err := fresh.Destroy(); err != nil || fresh.Exists() {
		t.Fatalf("destroy: %v", err)
	}
}

func TestDPAPIStyleKeyringKeepsTheWrappedKeyInTheHeader(t *testing.T) {
	kr := &fakeKeyring{wrap: true}
	v := newTestVault(t, kr, nil)
	if err := v.Create(Auto, ""); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(v.Path())
	if !bytes.Contains(raw, []byte(`"protection": "dpapi"`)) || !bytes.Contains(raw, []byte(`"wrapped_key"`)) {
		t.Fatalf("header: %s", raw)
	}
	fresh := New(v.Path(), Options{Keyring: kr, Passphrase: noPassphrase})
	if got, err := fresh.Read(); err != nil || string(got) != "{}" {
		t.Fatalf("read %q %v", got, err)
	}
}

func TestVaultRefusesLinks(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	_ = os.MkdirAll(real, 0o700)
	link := filepath.Join(dir, "vault")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symlinks unavailable")
	}
	v := New(filepath.Join(link, "vault.json"), Options{Keyring: &fakeKeyring{}, Passphrase: noPassphrase})
	if err := v.Create(Auto, ""); err == nil {
		t.Fatal("created a vault through a linked folder")
	}
}

func TestEnvPassphraseIsTakenOutOfTheEnvironment(t *testing.T) {
	t.Setenv(PassphraseEnv, "from the environment")
	TakeEnvPassphrase()
	t.Cleanup(func() { envMu.Lock(); envCaptured = ""; envMu.Unlock() })
	if _, ok := os.LookupEnv(PassphraseEnv); ok {
		t.Fatal("the passphrase is still in the environment")
	}
	if p, ok := EnvPassphrase(); !ok || p != "from the environment" {
		t.Fatalf("captured %q %v", p, ok)
	}
	file := filepath.Join(t.TempDir(), "pass")
	_ = os.WriteFile(file, []byte("from a file\n"), 0o600)
	envMu.Lock()
	envCaptured = ""
	envMu.Unlock()
	t.Setenv(PassphraseFileEnv, file)
	if p, ok := EnvPassphrase(); !ok || p != "from a file" {
		t.Fatalf("file passphrase %q %v", p, ok)
	}
}

// The command-line keychains must never pass the key as an argument.
func TestCommandKeychainsKeepTheKeyOffTheCommandLine(t *testing.T) {
	key := bytes.Repeat([]byte{0xab}, keySize)
	hexKey := hex.EncodeToString(key)
	id := strings.Repeat("0f", 16)
	var stored string
	var calls [][]string
	run := func(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		for _, a := range args {
			if strings.Contains(a, hexKey) {
				t.Fatalf("key on the command line: %v", args)
			}
		}
		switch {
		case name == "security" && len(args) == 1 && args[0] == "-i":
			if !strings.Contains(string(stdin), "-w "+hexKey) || !strings.Contains(string(stdin), "-a "+id) {
				t.Fatalf("security stdin %q", stdin)
			}
			stored = hexKey
		case name == "security" && args[0] == "find-generic-password":
			return []byte(stored + "\n"), nil
		case name == "secret-tool" && args[0] == "store":
			stored = string(stdin)
		case name == "secret-tool" && args[0] == "lookup":
			return []byte(stored), nil
		}
		return nil, nil
	}
	found := func(string) (string, error) { return "/usr/bin/x", nil }
	mac := &macKeychain{run: run, lookup: found, service: "NullBot Vault"}
	if _, err := mac.Store(id, key); err != nil {
		t.Fatal(err)
	}
	if got, err := mac.Load(id, nil); err != nil || !bytes.Equal(got, key) {
		t.Fatalf("mac load %x %v", got, err)
	}
	stored = ""
	ss := &secretService{run: run, lookup: found, getenv: func(k string) string {
		if k == "DBUS_SESSION_BUS_ADDRESS" {
			return "unix:path=/run/user/1000/bus"
		}
		return ""
	}, stat: os.Stat}
	if !ss.Available() {
		t.Fatal("secret service unavailable with a bus")
	}
	if _, err := ss.Store(id, key); err != nil {
		t.Fatal(err)
	}
	if got, err := ss.Load(id, nil); err != nil || !bytes.Equal(got, key) {
		t.Fatalf("secret-tool load %x %v", got, err)
	}
	if len(calls) < 4 {
		t.Fatalf("calls %v", calls)
	}
	noBus := &secretService{run: run, lookup: found, getenv: func(string) string { return "" }, stat: os.Stat}
	if noBus.Available() {
		t.Fatal("secret service available without a session bus")
	}
}

// brokenKeyring says it is available but cannot store anything, like a bus
// with no Secret Service behind it.
type brokenKeyring struct{ fakeKeyring }

func (k *brokenKeyring) Store(string, []byte) ([]byte, error) { return nil, errors.New("no provider") }

func TestABrokenKeychainCountsAsNone(t *testing.T) {
	v := newTestVault(t, &brokenKeyring{}, nil)
	if err := v.Create(Auto, ""); !errors.Is(err, ErrNoKeychain) {
		t.Fatalf("create with a broken keychain: %v", err)
	}
	if st := v.Status(); st.Keychain != "" || st.Exists {
		t.Fatalf("status %+v", st)
	}
	if err := v.Create(Passphrase, "a passphrase that works"); err != nil {
		t.Fatal(err)
	}
}

// Two handles stand in for two NullBot processes sharing one vault.
func TestConcurrentWritersDoNotLoseUpdates(t *testing.T) {
	kr := &fakeKeyring{wrap: true}
	path := filepath.Join(t.TempDir(), "vault", "vault.json")
	a := New(path, Options{Keyring: kr, Passphrase: noPassphrase})
	b := New(path, Options{Keyring: kr, Passphrase: noPassphrase})
	if err := a.CreateWith(Auto, "", []byte("0")); err != nil {
		t.Fatal(err)
	}
	if err := b.Create(Auto, ""); !errors.Is(err, ErrExists) {
		t.Fatalf("second create: %v", err)
	}
	var wg sync.WaitGroup
	for _, v := range []*Vault{a, b} {
		wg.Add(1)
		go func(v *Vault) {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				if err := v.Update(func(p []byte) ([]byte, error) {
					n, _ := strconv.Atoi(string(p))
					return []byte(strconv.Itoa(n + 1)), nil
				}); err != nil {
					t.Error(err)
					return
				}
			}
		}(v)
	}
	wg.Wait()
	got, _ := a.Read()
	if string(got) != "80" {
		t.Fatalf("after 80 updates the count is %s", got)
	}
	if vals := a.RedactionValues(); len(vals) != 3 || len(vals[0]) != 64 {
		t.Fatalf("redaction values %v", vals)
	}
}
