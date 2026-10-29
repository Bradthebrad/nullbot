// Package vault keeps NullBot's secrets in one encrypted file.
//
// The file holds a JSON header and the secrets encrypted with AES-256-GCM
// under a random 32-byte data key. The data key never touches the disk in
// the clear. It is protected by one of:
//
//   - dpapi: Windows DPAPI for the current user; the protected key is kept in
//     the header.
//   - keychain: the macOS login keychain, through the security tool.
//   - secret-service: the Linux Secret Service (GNOME Keyring, KWallet),
//     through secret-tool.
//   - passphrase: a key derived from a passphrase with PBKDF2-SHA256, for
//     servers without a keychain. The passphrase comes from
//     NULLBOT_VAULT_PASSPHRASE, NULLBOT_VAULT_PASSPHRASE_FILE or Unlock.
//
// The package only stores bytes; what the bytes mean is up to the caller.
package vault

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Protection names how the data key is protected.
type Protection string

const (
	DPAPI         Protection = "dpapi"
	Keychain      Protection = "keychain"
	SecretService Protection = "secret-service"
	Passphrase    Protection = "passphrase"
	// Auto picks the passphrase when one is configured in the environment,
	// otherwise this system's keychain.
	Auto Protection = ""
)

const (
	format       = "nullbot-vault"
	version      = 1
	keySize      = 32
	maxFileBytes = 8 << 20
	// MaxPayload is the largest payload Write accepts.
	MaxPayload = 4 << 20
	// PBKDF2Iterations follows the OWASP recommendation for PBKDF2-HMAC-SHA256.
	PBKDF2Iterations = 600_000
)

var (
	// ErrNotFound: there is no vault file yet.
	ErrNotFound = errors.New("vault: no vault has been created")
	// ErrLocked: the vault is protected by a passphrase that has not been given.
	ErrLocked = errors.New("vault: locked; set NULLBOT_VAULT_PASSPHRASE or unlock it with the passphrase")
	// ErrWrongPassphrase: the passphrase does not open the vault.
	ErrWrongPassphrase = errors.New("vault: the passphrase is wrong")
	// ErrNoKeychain: this system has no keychain NullBot can use, so a
	// passphrase is needed.
	ErrNoKeychain = errors.New("vault: this system has no keychain NullBot can use; set NULLBOT_VAULT_PASSPHRASE (or NULLBOT_VAULT_PASSPHRASE_FILE) to protect the vault with a passphrase")
	// ErrCorrupt: the file is not a vault, was changed, or its key is gone.
	ErrCorrupt = errors.New("vault: the vault file is damaged or was changed")
	// ErrExists: Create was called for a vault that already exists.
	ErrExists = errors.New("vault: a vault already exists")
	errBusy   = errors.New("vault: another NullBot is changing the vault; try again in a moment")
	// ErrKeychainFailed: this system has a keychain, but it would not store
	// the vault key (a declined prompt, no provider behind the bus). The
	// remedy is a passphrase; unlike ErrNoKeychain it never means "fall back
	// to plain text".
	ErrKeychainFailed = errors.New("vault: the system keychain would not store the vault key; set NULLBOT_VAULT_PASSPHRASE (or NULLBOT_VAULT_PASSPHRASE_FILE) to use a passphrase instead")
)

// header is the file format. Everything but Data is metadata; Data is the
// payload sealed with the data key, with the header's identity as additional
// data so a header cannot be swapped onto another payload.
type header struct {
	Format     string     `json:"format"`
	Version    int        `json:"version"`
	ID         string     `json:"id"`
	Protection Protection `json:"protection"`
	// WrappedKey is the data key protected by DPAPI, or sealed with the
	// passphrase key (nonce then ciphertext). Empty for keychains, which hold
	// the key themselves.
	WrappedKey string     `json:"wrapped_key,omitempty"`
	KDF        *kdfParams `json:"kdf,omitempty"`
	Nonce      string     `json:"nonce"`
	Data       string     `json:"data"`
	Updated    time.Time  `json:"updated"`
}

type kdfParams struct {
	Name       string `json:"name"`
	Iterations int    `json:"iterations"`
	Salt       string `json:"salt"`
}

// Status describes a vault without opening its payload.
type Status struct {
	Path       string     `json:"path"`
	Exists     bool       `json:"exists"`
	Protection Protection `json:"protection,omitempty"`
	Unlocked   bool       `json:"unlocked"`
	// Error explains why the vault cannot be opened, when it cannot.
	Error   string    `json:"error,omitempty"`
	Updated time.Time `json:"updated,omitempty"`
	// Keychain is the protection this system offers, empty when none.
	Keychain Protection `json:"keychain,omitempty"`
}

// Options configure a Vault. Zero values use the system's keychain and
// passphrases from the environment.
type Options struct {
	// Keyring overrides the system keychain (tests).
	Keyring Keyring
	// Passphrase returns a passphrase from configuration, if any. Defaults to
	// EnvPassphrase.
	Passphrase func() (string, bool)
	// Iterations overrides PBKDF2Iterations for new passphrase keys (tests).
	Iterations int
}

// Vault is one vault file. It is safe for concurrent use in one process.
// Every write rereads the file first, so separate processes sharing a vault
// lose an update only when they write within the same instant.
type Vault struct {
	path       string
	keyring    Keyring
	passphrase func() (string, bool)
	iterations int

	// writeMu queues this process's writers before the file lock, which
	// only has to keep other processes out.
	writeMu sync.Mutex
	mu      sync.Mutex
	key     []byte // cached data key for id
	id      string
	// given is a passphrase handed to Unlock; it lives only in memory.
	given string
	// keyringFailed is set when the keychain said it was available but
	// could not store a key; Auto then asks for a passphrase.
	keyringFailed bool
}

// New returns the vault stored at path. Nothing is read until it is used.
func New(path string, opts Options) *Vault {
	v := &Vault{path: path, keyring: opts.Keyring, passphrase: opts.Passphrase, iterations: opts.Iterations}
	if v.keyring == nil {
		v.keyring = systemKeyring()
	}
	if v.passphrase == nil {
		v.passphrase = EnvPassphrase
	}
	if v.iterations <= 0 {
		v.iterations = PBKDF2Iterations
	}
	return v
}

// Path is the vault file.
func (v *Vault) Path() string { return v.path }

// Status reports whether the vault exists, how it is protected and whether
// it opens. It may consult the keychain.
func (v *Vault) Status() Status {
	st := Status{Path: v.path}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.keyringUsable() {
		st.Keychain = v.keyring.Kind()
	}
	h, err := v.readHeader()
	if errors.Is(err, ErrNotFound) {
		return st
	}
	st.Exists = true
	if err != nil {
		st.Error = err.Error()
		return st
	}
	st.Protection, st.Updated = h.Protection, h.Updated
	if _, err := v.dataKey(h); err != nil {
		st.Error = err.Error()
		return st
	}
	st.Unlocked = true
	return st
}

// Exists reports whether a vault file is present.
func (v *Vault) Exists() bool {
	_, err := os.Lstat(v.path)
	return err == nil
}

// Create makes an empty vault protected as asked. With Auto, a passphrase
// from the environment wins, then the system keychain; without either it
// fails with ErrNoKeychain. passphrase is used only for Passphrase and may
// be empty to take it from the environment.
func (v *Vault) Create(protection Protection, passphrase string) error {
	return v.CreateWith(protection, passphrase, nil)
}

// CreateWith makes a vault holding payload; see Create.
func (v *Vault) CreateWith(protection Protection, passphrase string, payload []byte) error {
	if v.Exists() {
		return ErrExists
	}
	// The file lock is taken before v.mu, so reads in this process never
	// wait behind another process's write.
	v.writeMu.Lock()
	defer v.writeMu.Unlock()
	unlock, err := v.fileLock()
	if err != nil {
		return err
	}
	defer unlock()
	v.mu.Lock()
	defer v.mu.Unlock()
	// Another process may have made it while this one waited.
	if v.Exists() {
		return ErrExists
	}
	return v.create(protection, passphrase, payload)
}

// fileLock serializes changes between NullBot processes sharing the vault.
func (v *Vault) fileLock() (func(), error) {
	dir := filepath.Dir(v.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := rejectLinks(v.path); err != nil {
		return nil, err
	}
	return lockPath(v.path + ".lock")
}

func (v *Vault) keyringUsable() bool {
	return v.keyring != nil && !v.keyringFailed && v.keyring.Available()
}

func (v *Vault) create(protection Protection, passphrase string, payload []byte) error {
	auto := protection == Auto
	if auto {
		if p, ok := v.passphrase(); ok && p != "" {
			protection, passphrase = Passphrase, p
		} else if v.keyringUsable() {
			protection = v.keyring.Kind()
		} else if v.keyringFailed {
			return ErrKeychainFailed
		} else {
			return ErrNoKeychain
		}
	}
	id := randomHex(16)
	key := make([]byte, keySize)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	h := header{Format: format, Version: version, ID: id, Protection: protection}
	if err := v.protectKey(&h, key, passphrase); err != nil {
		if auto && protection != Passphrase {
			// The keychain claimed to work but did not (no Secret Service
			// provider behind the bus, a locked login keychain over SSH):
			// treat it as absent from now on.
			v.keyringFailed = true
			return fmt.Errorf("%w (%v)", ErrKeychainFailed, err)
		}
		return err
	}
	if payload == nil {
		payload = []byte("{}")
	}
	if err := v.writeLocked(h, key, payload); err != nil {
		if h.Protection != DPAPI && h.Protection != Passphrase && v.keyring != nil {
			_ = v.keyring.Delete(id)
		}
		return err
	}
	v.id, v.key = id, key
	if protection == Passphrase {
		v.given = passphrase
	}
	return nil
}

// protectKey fills the header fields that protect key.
func (v *Vault) protectKey(h *header, key []byte, passphrase string) error {
	switch h.Protection {
	case Passphrase:
		if passphrase == "" {
			p, ok := v.passphrase()
			if !ok || p == "" {
				return ErrLocked
			}
			passphrase = p
		}
		if len(passphrase) < 8 {
			return errors.New("vault: use a passphrase of at least 8 characters")
		}
		salt := make([]byte, 16)
		if _, err := rand.Read(salt); err != nil {
			return err
		}
		h.KDF = &kdfParams{Name: "pbkdf2-sha256", Iterations: v.iterations, Salt: base64.StdEncoding.EncodeToString(salt)}
		kek, err := deriveKey(passphrase, salt, v.iterations)
		if err != nil {
			return err
		}
		defer clear(kek)
		sealed, err := seal(kek, key, []byte("nullbot-vault-key/"+h.ID))
		if err != nil {
			return err
		}
		h.WrappedKey = base64.StdEncoding.EncodeToString(sealed)
		return nil
	case DPAPI, Keychain, SecretService:
		if v.keyring == nil || v.keyring.Kind() != h.Protection || !v.keyring.Available() {
			return fmt.Errorf("vault: %s is not available on this system", h.Protection)
		}
		wrapped, err := v.keyring.Store(h.ID, key)
		if err != nil {
			return fmt.Errorf("vault: storing the key in %s: %w", h.Protection, err)
		}
		if len(wrapped) > 0 {
			h.WrappedKey = base64.StdEncoding.EncodeToString(wrapped)
		}
		return nil
	default:
		return fmt.Errorf("vault: unknown protection %q", h.Protection)
	}
}

// Unlock opens a passphrase vault with passphrase and remembers it in
// memory for this process. It is a no-op check for other protections.
func (v *Vault) Unlock(passphrase string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	h, err := v.readHeader()
	if err != nil {
		return err
	}
	if h.Protection != Passphrase {
		_, err := v.dataKey(h)
		return err
	}
	key, err := v.unwrapPassphrase(h, passphrase)
	if err != nil {
		return err
	}
	v.id, v.key, v.given = h.ID, key, passphrase
	return nil
}

// Lock forgets the cached key and any passphrase given to Unlock.
func (v *Vault) Lock() {
	v.mu.Lock()
	defer v.mu.Unlock()
	clear(v.key)
	v.key, v.id, v.given = nil, "", ""
}

// Read returns the decrypted payload. The caller should clear it when done.
func (v *Vault) Read() ([]byte, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.readLocked()
}

func (v *Vault) readLocked() ([]byte, error) {
	h, err := v.readHeader()
	if err != nil {
		return nil, err
	}
	key, err := v.dataKey(h)
	if err != nil {
		return nil, err
	}
	nonce, err1 := base64.StdEncoding.DecodeString(h.Nonce)
	data, err2 := base64.StdEncoding.DecodeString(h.Data)
	if err1 != nil || err2 != nil {
		return nil, ErrCorrupt
	}
	plain, err := open(key, append(nonce, data...), aad(h))
	if err != nil {
		return nil, ErrCorrupt
	}
	return plain, nil
}

// ErrNoChange, returned by an Update function, leaves the vault as it is;
// Update then returns nil.
var ErrNoChange = errors.New("vault: no change")

// Update rereads the payload, passes it to fn and writes what fn returns,
// all under the vault's lock. fn's input is cleared afterwards.
func (v *Vault) Update(fn func(payload []byte) ([]byte, error)) error {
	v.writeMu.Lock()
	defer v.writeMu.Unlock()
	unlock, err := v.fileLock()
	if err != nil {
		return err
	}
	defer unlock()
	v.mu.Lock()
	defer v.mu.Unlock()
	h, err := v.readHeader()
	if err != nil {
		return err
	}
	plain, err := v.readLocked()
	if err != nil {
		return err
	}
	defer clear(plain)
	next, err := fn(plain)
	if errors.Is(err, ErrNoChange) {
		return nil
	}
	if err != nil {
		return err
	}
	defer clear(next)
	key, err := v.dataKey(h)
	if err != nil {
		return err
	}
	return v.writeLocked(h, key, next)
}

// Reprotect moves the data key to another protection, for example from a
// passphrase to the keychain, or to a new passphrase. The payload is
// re-encrypted under a fresh data key.
func (v *Vault) Reprotect(protection Protection, passphrase string) error {
	v.writeMu.Lock()
	defer v.writeMu.Unlock()
	unlock, err := v.fileLock()
	if err != nil {
		return err
	}
	defer unlock()
	v.mu.Lock()
	defer v.mu.Unlock()
	old, err := v.readHeader()
	if err != nil {
		return err
	}
	plain, err := v.readLocked()
	if err != nil {
		return err
	}
	defer clear(plain)
	if protection == Auto {
		if !v.keyringUsable() {
			return ErrNoKeychain
		}
		protection = v.keyring.Kind()
	}
	id := randomHex(16)
	key := make([]byte, keySize)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	h := header{Format: format, Version: version, ID: id, Protection: protection}
	if err := v.protectKey(&h, key, passphrase); err != nil {
		return err
	}
	if err := v.writeLocked(h, key, plain); err != nil {
		if (protection == Keychain || protection == SecretService) && v.keyring != nil {
			_ = v.keyring.Delete(id)
		}
		return err
	}
	if old.Protection == Keychain || old.Protection == SecretService {
		if v.keyring != nil {
			_ = v.keyring.Delete(old.ID)
		}
	}
	clear(v.key)
	v.id, v.key, v.given = id, key, ""
	if protection == Passphrase {
		v.given = passphrase
	}
	return nil
}

// Destroy removes the vault file and its keychain entry. Its secrets are
// gone for good.
func (v *Vault) Destroy() error {
	v.writeMu.Lock()
	defer v.writeMu.Unlock()
	unlock, err := v.fileLock()
	if err != nil {
		return err
	}
	defer unlock()
	v.mu.Lock()
	defer v.mu.Unlock()
	h, err := v.readHeader()
	if errors.Is(err, ErrCorrupt) {
		// Still find the keychain entry to remove, if the id is readable.
		if data, rerr := os.ReadFile(v.path); rerr == nil && len(data) <= maxFileBytes {
			var loose header
			if json.Unmarshal(data, &loose) == nil && validID(loose.ID) {
				h = loose
			}
		}
	} else if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	if err := os.Remove(v.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	if h.ID != "" && (h.Protection == Keychain || h.Protection == SecretService) && v.keyring != nil {
		_ = v.keyring.Delete(h.ID)
	}
	clear(v.key)
	v.key, v.id, v.given = nil, "", ""
	return nil
}

// dataKey returns the data key for h, from cache or by unwrapping it.
func (v *Vault) dataKey(h header) ([]byte, error) {
	if v.key != nil && v.id == h.ID {
		return v.key, nil
	}
	var key []byte
	var err error
	switch h.Protection {
	case Passphrase:
		pass := v.given
		if pass == "" {
			p, ok := v.passphrase()
			if !ok || p == "" {
				return nil, ErrLocked
			}
			pass = p
		}
		key, err = v.unwrapPassphrase(h, pass)
	case DPAPI, Keychain, SecretService:
		if v.keyring == nil || v.keyring.Kind() != h.Protection {
			return nil, fmt.Errorf("vault: this vault is protected by %s, which this system does not offer; set it up again with a passphrase on the computer that made it", h.Protection)
		}
		var wrapped []byte
		if h.WrappedKey != "" {
			wrapped, err = base64.StdEncoding.DecodeString(h.WrappedKey)
			if err != nil {
				return nil, ErrCorrupt
			}
		}
		key, err = v.keyring.Load(h.ID, wrapped)
		if err != nil {
			return nil, fmt.Errorf("vault: %s could not open the vault key: %w", h.Protection, err)
		}
	default:
		return nil, ErrCorrupt
	}
	if err != nil {
		return nil, err
	}
	if len(key) != keySize {
		clear(key)
		return nil, ErrCorrupt
	}
	clear(v.key)
	v.id, v.key = h.ID, key
	return key, nil
}

func (v *Vault) unwrapPassphrase(h header, passphrase string) ([]byte, error) {
	// A lowered iteration count cannot weaken the stored key: it would only
	// derive a different key that fails to open it. The ceiling bounds work.
	if h.KDF == nil || h.KDF.Name != "pbkdf2-sha256" || h.KDF.Iterations < 1 || h.KDF.Iterations > 50_000_000 {
		return nil, ErrCorrupt
	}
	salt, err := base64.StdEncoding.DecodeString(h.KDF.Salt)
	if err != nil || len(salt) < 16 {
		return nil, ErrCorrupt
	}
	sealed, err := base64.StdEncoding.DecodeString(h.WrappedKey)
	if err != nil {
		return nil, ErrCorrupt
	}
	kek, err := deriveKey(passphrase, salt, h.KDF.Iterations)
	if err != nil {
		return nil, err
	}
	defer clear(kek)
	key, err := open(kek, sealed, []byte("nullbot-vault-key/"+h.ID))
	if err != nil {
		return nil, ErrWrongPassphrase
	}
	return key, nil
}

func (v *Vault) readHeader() (header, error) {
	var h header
	if err := rejectLinks(v.path); err != nil {
		return h, err
	}
	f, err := os.Open(v.path)
	if os.IsNotExist(err) {
		return h, ErrNotFound
	}
	if err != nil {
		return h, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxFileBytes {
		return h, ErrCorrupt
	}
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil || len(data) > maxFileBytes {
		return h, ErrCorrupt
	}
	if json.Unmarshal(data, &h) != nil || h.Format != format || h.Version != version || len(h.ID) != 32 {
		return header{}, ErrCorrupt
	}
	return h, nil
}

func (v *Vault) writeLocked(h header, key, payload []byte) error {
	if len(payload) > MaxPayload {
		return fmt.Errorf("vault: the secrets are larger than %d MiB", MaxPayload>>20)
	}
	h.Updated = time.Now().UTC().Truncate(time.Second)
	sealed, err := seal(key, payload, aad(h))
	if err != nil {
		return err
	}
	nonceSize := 12
	h.Nonce = base64.StdEncoding.EncodeToString(sealed[:nonceSize])
	h.Data = base64.StdEncoding.EncodeToString(sealed[nonceSize:])
	data, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(v.path, append(data, '\n'))
}

// aad binds the payload to the header fields that matter.
func aad(h header) []byte {
	return []byte(fmt.Sprintf("%s/%d/%s/%s", h.Format, h.Version, h.ID, h.Protection))
}

func seal(key, plain, additional []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plain, additional), nil
}

func open(key, sealed, additional []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(sealed) < gcm.NonceSize()+gcm.Overhead() {
		return nil, ErrCorrupt
	}
	return gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], additional)
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// writeAtomic replaces path with data through a temporary file in the same
// folder, so a crash leaves the old vault or the new one, never half of one.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := rejectLinks(path); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".vault-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := rejectLinks(path); err != nil {
		return err
	}
	if err := renameReplace(tmp, path); err != nil {
		return err
	}
	syncDir(dir)
	return nil
}

// rejectLinks refuses a vault file, or vault folder, that is a symbolic
// link, so a planted link cannot redirect the vault elsewhere. Links higher
// up (a home folder that is a link, say) are the user's own layout.
func rejectLinks(path string) error {
	for _, p := range []string{path, filepath.Dir(path)} {
		if info, err := os.Lstat(p); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("vault: %s is a link; the vault must be a real file in a real folder", p)
		}
	}
	return nil
}

// EnvPassphrase reads the passphrase from NULLBOT_VAULT_PASSPHRASE, or from
// the file named by NULLBOT_VAULT_PASSPHRASE_FILE (for example a systemd
// credential). A passphrase captured by TakeEnvPassphrase is returned first.
func EnvPassphrase() (string, bool) {
	envMu.Lock()
	captured := envCaptured
	envMu.Unlock()
	if captured != "" {
		return captured, true
	}
	return readEnvPassphrase()
}

func readEnvPassphrase() (string, bool) {
	if p := os.Getenv(PassphraseEnv); p != "" {
		return p, true
	}
	if file := strings.TrimSpace(os.Getenv(PassphraseFileEnv)); file != "" {
		data, err := os.ReadFile(file)
		if err != nil || len(data) > 4096 {
			return "", false
		}
		p := string(bytes.TrimRight(data, "\r\n"))
		return p, p != ""
	}
	return "", false
}

const (
	PassphraseEnv     = "NULLBOT_VAULT_PASSPHRASE"
	PassphraseFileEnv = "NULLBOT_VAULT_PASSPHRASE_FILE"
)

var (
	envMu       sync.Mutex
	envCaptured string
)

// TakeEnvPassphrase reads the passphrase from the environment, keeps it in
// memory and removes NULLBOT_VAULT_PASSPHRASE from this process's
// environment, so programs NullBot starts never inherit it. Call it once at
// startup, before starting other goroutines.
func TakeEnvPassphrase() {
	p, ok := readEnvPassphrase()
	envMu.Lock()
	if ok {
		envCaptured = p
	}
	envMu.Unlock()
	// Neither the passphrase nor where to find it reaches child processes.
	_ = os.Unsetenv(PassphraseEnv)
	_ = os.Unsetenv(PassphraseFileEnv)
}

// RedactionValues are forms of the vault's own key material that must never
// reach a model: the data key in hex, and the protected key in the header.
// Empty until the vault has been opened in this process.
func (v *Vault) RedactionValues() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.key == nil {
		return nil
	}
	out := []string{hex.EncodeToString(v.key), strings.ToUpper(hex.EncodeToString(v.key))}
	if h, err := v.readHeader(); err == nil && h.WrappedKey != "" {
		out = append(out, h.WrappedKey)
	}
	return out
}
