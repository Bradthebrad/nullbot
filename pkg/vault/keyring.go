package vault

import (
	"bytes"
	"context"
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Keyring protects a vault's data key with the operating system.
type Keyring interface {
	Kind() Protection
	// Available reports whether the keyring can be used right now.
	Available() bool
	// Store protects key for vault id. It returns bytes to keep in the vault
	// header (DPAPI), or nil when the keyring keeps the key itself.
	Store(id string, key []byte) ([]byte, error)
	// Load returns the key for vault id; wrapped is what Store returned.
	Load(id string, wrapped []byte) ([]byte, error)
	// Delete removes the key for vault id, if the keyring holds it.
	Delete(id string) error
}

func deriveKey(passphrase string, salt []byte, iterations int) ([]byte, error) {
	return pbkdf2.Key(sha256.New, passphrase, salt, iterations, keySize)
}

// runner runs a helper program with stdin and returns its standard output.
type runner func(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error)

func runCommand(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// Helpers get no extra environment beyond what a desktop session has;
	// the passphrase variable was removed at startup.
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 300 {
			msg = msg[:300]
		}
		if msg != "" {
			return nil, fmt.Errorf("%s: %w: %s", filepath.Base(name), err, msg)
		}
		return nil, fmt.Errorf("%s: %w", filepath.Base(name), err)
	}
	return stdout.Bytes(), nil
}

const keyringTimeout = 2 * time.Minute

// macKeychain keeps the key in the login keychain with the security tool.
// The key is written through security's interactive mode on standard input,
// so it never appears in a process list.
type macKeychain struct {
	run     runner
	lookup  func(string) (string, error)
	service string
	// bin is the security tool's fixed path, so a planted one on PATH is
	// never asked for the key.
	bin string
}

func (k *macKeychain) tool() string {
	if k.bin != "" {
		return k.bin
	}
	return "security"
}

func newMacKeychain() *macKeychain {
	return &macKeychain{run: runCommand, lookup: exec.LookPath, service: "NullBot Vault", bin: "/usr/bin/security"}
}

func (k *macKeychain) Kind() Protection { return Keychain }

func (k *macKeychain) Available() bool {
	_, err := k.lookup(k.tool())
	return err == nil
}

func (k *macKeychain) Store(id string, key []byte) ([]byte, error) {
	if !validID(id) {
		return nil, ErrCorrupt
	}
	ctx, cancel := context.WithTimeout(context.Background(), keyringTimeout)
	defer cancel()
	line := fmt.Sprintf("add-generic-password -U -a %s -s %q -l %q -w %s\n", id, k.service, "NullBot vault key", hex.EncodeToString(key))
	stdin := []byte(line)
	defer clear(stdin)
	_, err := k.run(ctx, stdin, k.tool(), "-i")
	if err != nil {
		return nil, err
	}
	// Confirm it reads back: interactive mode reports some failures only on
	// its own output.
	got, err := k.Load(id, nil)
	if err != nil {
		return nil, err
	}
	defer clear(got)
	if !bytes.Equal(got, key) {
		return nil, errors.New("the keychain returned a different key")
	}
	return nil, nil
}

func (k *macKeychain) Load(id string, _ []byte) ([]byte, error) {
	if !validID(id) {
		return nil, ErrCorrupt
	}
	ctx, cancel := context.WithTimeout(context.Background(), keyringTimeout)
	defer cancel()
	out, err := k.run(ctx, nil, k.tool(), "find-generic-password", "-a", id, "-s", k.service, "-w")
	if err != nil {
		return nil, err
	}
	defer clear(out)
	return decodeKey(out)
}

func (k *macKeychain) Delete(id string) error {
	if !validID(id) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), keyringTimeout)
	defer cancel()
	_, err := k.run(ctx, nil, k.tool(), "delete-generic-password", "-a", id, "-s", k.service)
	return err
}

// secretService keeps the key in the Secret Service with secret-tool, which
// reads the secret from standard input.
type secretService struct {
	run    runner
	lookup func(string) (string, error)
	getenv func(string) string
	stat   func(string) (os.FileInfo, error)
	bin    string
}

func newSecretService() *secretService {
	k := &secretService{run: runCommand, lookup: exec.LookPath, getenv: os.Getenv, stat: os.Stat}
	// Prefer the system's copy over whatever is first on PATH.
	for _, p := range []string{"/usr/bin/secret-tool", "/bin/secret-tool"} {
		if _, err := os.Stat(p); err == nil {
			k.bin = p
			break
		}
	}
	return k
}

func (k *secretService) tool() string {
	if k.bin != "" {
		return k.bin
	}
	return "secret-tool"
}

func (k *secretService) Kind() Protection { return SecretService }

// Available needs secret-tool and a session bus; servers usually have
// neither, and use a passphrase.
func (k *secretService) Available() bool {
	if _, err := k.lookup(k.tool()); err != nil {
		return false
	}
	if k.getenv("DBUS_SESSION_BUS_ADDRESS") != "" {
		return true
	}
	if dir := k.getenv("XDG_RUNTIME_DIR"); dir != "" {
		if _, err := k.stat(filepath.Join(dir, "bus")); err == nil {
			return true
		}
	}
	return false
}

func (k *secretService) Store(id string, key []byte) ([]byte, error) {
	if !validID(id) {
		return nil, ErrCorrupt
	}
	ctx, cancel := context.WithTimeout(context.Background(), keyringTimeout)
	defer cancel()
	secret := []byte(hex.EncodeToString(key))
	defer clear(secret)
	if _, err := k.run(ctx, secret, k.tool(), "store", "--label=NullBot vault key", "application", "nullbot-vault", "id", id); err != nil {
		return nil, err
	}
	got, err := k.Load(id, nil)
	if err != nil {
		return nil, err
	}
	defer clear(got)
	if !bytes.Equal(got, key) {
		return nil, errors.New("the Secret Service returned a different key")
	}
	return nil, nil
}

func (k *secretService) Load(id string, _ []byte) ([]byte, error) {
	if !validID(id) {
		return nil, ErrCorrupt
	}
	ctx, cancel := context.WithTimeout(context.Background(), keyringTimeout)
	defer cancel()
	out, err := k.run(ctx, nil, k.tool(), "lookup", "application", "nullbot-vault", "id", id)
	if err != nil {
		return nil, err
	}
	defer clear(out)
	return decodeKey(out)
}

func (k *secretService) Delete(id string) error {
	if !validID(id) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), keyringTimeout)
	defer cancel()
	_, err := k.run(ctx, nil, k.tool(), "clear", "application", "nullbot-vault", "id", id)
	return err
}

func decodeKey(out []byte) ([]byte, error) {
	text := bytes.TrimSpace(out)
	if len(text) != keySize*2 {
		return nil, errors.New("the stored vault key is missing or malformed")
	}
	key := make([]byte, keySize)
	if _, err := hex.Decode(key, text); err != nil {
		return nil, errors.New("the stored vault key is malformed")
	}
	return key, nil
}

func validID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

// SystemKeychain is the protection this system offers, or empty.
func SystemKeychain() Protection {
	k := systemKeyring()
	if k == nil || !k.Available() {
		return ""
	}
	return k.Kind()
}
