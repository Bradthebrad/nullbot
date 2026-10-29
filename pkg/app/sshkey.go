package app

// SSH keys: generating an Ed25519 key in OpenSSH format, and reading the
// public half of a private key the user imports. Standard library only.

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strings"
)

type sshPublicKey struct {
	keyType string
	blob    []byte
	comment string
}

func (k sshPublicKey) authorizedKey(comment string) string {
	line := k.keyType + " " + base64.StdEncoding.EncodeToString(k.blob)
	if comment = strings.TrimSpace(comment); comment != "" {
		line += " " + comment
	}
	return line
}

func (k sshPublicKey) fingerprint() string {
	sum := sha256.Sum256(k.blob)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

var errSSHPassphrase = errors.New("this key is protected by a passphrase; NullBot runs commands with no one there to type it, so use a key without one (NullBot can generate one)")

// generateSSHKey makes an Ed25519 key and returns it in OpenSSH format.
func generateSSHKey(comment string) (string, sshPublicKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", sshPublicKey{}, err
	}
	pubBlob := sshString(nil, []byte("ssh-ed25519"))
	pubBlob = sshString(pubBlob, pub)

	var check [4]byte
	if _, err := rand.Read(check[:]); err != nil {
		return "", sshPublicKey{}, err
	}
	var section []byte
	section = append(section, check[:]...)
	section = append(section, check[:]...)
	section = sshString(section, []byte("ssh-ed25519"))
	section = sshString(section, pub)
	section = sshString(section, priv)
	section = sshString(section, []byte(comment))
	for i := byte(1); len(section)%8 != 0; i++ {
		section = append(section, i)
	}
	defer clear(section)

	var out []byte
	out = append(out, "openssh-key-v1\x00"...)
	out = sshString(out, []byte("none"))
	out = sshString(out, []byte("none"))
	out = sshString(out, nil)
	out = binary.BigEndian.AppendUint32(out, 1)
	out = sshString(out, pubBlob)
	out = sshString(out, section)
	defer clear(out)
	clear(priv)
	text := string(pem.EncodeToMemory(&pem.Block{Type: "OPENSSH PRIVATE KEY", Bytes: out}))
	return text, sshPublicKey{keyType: "ssh-ed25519", blob: pubBlob, comment: comment}, nil
}

// sshPublicKeyFromPrivate reads the public key from an OpenSSH, PKCS#1,
// PKCS#8 or SEC 1 private key. Keys with a passphrase are refused.
func sshPublicKeyFromPrivate(text string) (sshPublicKey, error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(text)))
	if block == nil {
		return sshPublicKey{}, errors.New("this is not a private key file; paste the whole key, including its BEGIN and END lines")
	}
	defer clear(block.Bytes)
	if strings.Contains(block.Headers["Proc-Type"], "ENCRYPTED") || block.Type == "ENCRYPTED PRIVATE KEY" {
		return sshPublicKey{}, errSSHPassphrase
	}
	switch block.Type {
	case "OPENSSH PRIVATE KEY":
		return parseOpenSSHPrivate(block.Bytes)
	case "RSA PRIVATE KEY":
		key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return sshPublicKey{}, fmt.Errorf("the RSA key could not be read: %v", err)
		}
		return sshPublicFromCrypto(&key.PublicKey)
	case "EC PRIVATE KEY":
		key, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return sshPublicKey{}, fmt.Errorf("the EC key could not be read: %v", err)
		}
		return sshPublicFromCrypto(&key.PublicKey)
	case "PRIVATE KEY":
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return sshPublicKey{}, fmt.Errorf("the key could not be read: %v", err)
		}
		switch k := key.(type) {
		case *rsa.PrivateKey:
			return sshPublicFromCrypto(&k.PublicKey)
		case *ecdsa.PrivateKey:
			return sshPublicFromCrypto(&k.PublicKey)
		case ed25519.PrivateKey:
			return sshPublicFromCrypto(k.Public())
		}
		return sshPublicKey{}, errors.New("this kind of key is not supported; use Ed25519, ECDSA or RSA")
	}
	return sshPublicKey{}, fmt.Errorf("a %q block is not an SSH private key", block.Type)
}

func sshPublicFromCrypto(pub any) (sshPublicKey, error) {
	switch k := pub.(type) {
	case ed25519.PublicKey:
		blob := sshString(sshString(nil, []byte("ssh-ed25519")), k)
		return sshPublicKey{keyType: "ssh-ed25519", blob: blob}, nil
	case *rsa.PublicKey:
		if k.N.BitLen() < 2048 {
			return sshPublicKey{}, errors.New("RSA keys shorter than 2048 bits are refused; generate a new key")
		}
		blob := sshString(nil, []byte("ssh-rsa"))
		blob = sshMPInt(blob, big.NewInt(int64(k.E)))
		blob = sshMPInt(blob, k.N)
		return sshPublicKey{keyType: "ssh-rsa", blob: blob}, nil
	case *ecdsa.PublicKey:
		var curve string
		switch k.Curve {
		case elliptic.P256():
			curve = "nistp256"
		case elliptic.P384():
			curve = "nistp384"
		case elliptic.P521():
			curve = "nistp521"
		default:
			return sshPublicKey{}, errors.New("this elliptic curve is not supported by SSH")
		}
		ecdhKey, err := k.ECDH()
		if err != nil {
			return sshPublicKey{}, err
		}
		point := ecdhKey.Bytes()
		keyType := "ecdsa-sha2-" + curve
		blob := sshString(nil, []byte(keyType))
		blob = sshString(blob, []byte(curve))
		blob = sshString(blob, point)
		return sshPublicKey{keyType: keyType, blob: blob}, nil
	}
	return sshPublicKey{}, errors.New("this kind of key is not supported")
}

func parseOpenSSHPrivate(data []byte) (sshPublicKey, error) {
	r := sshReader{data: data}
	if !bytes.HasPrefix(data, []byte("openssh-key-v1\x00")) {
		return sshPublicKey{}, errors.New("the OpenSSH key is damaged")
	}
	r.data = r.data[len("openssh-key-v1\x00"):]
	cipher := string(r.str())
	kdf := string(r.str())
	r.str() // kdf options
	n := r.u32()
	if r.err != nil || n != 1 {
		return sshPublicKey{}, errors.New("the OpenSSH key is damaged or holds more than one key")
	}
	if cipher != "none" || kdf != "none" {
		return sshPublicKey{}, errSSHPassphrase
	}
	blob := append([]byte(nil), r.str()...)
	section := r.str()
	if r.err != nil {
		return sshPublicKey{}, errors.New("the OpenSSH key is damaged")
	}
	pr := sshReader{data: section}
	keyType := string(sshReader{data: blob}.peekStr())
	if strings.HasPrefix(keyType, "sk-") {
		return sshPublicKey{}, errors.New("security-key (sk-) keys need the hardware key at hand; use an Ed25519 key instead")
	}
	if pr.u32() != pr.u32() || pr.err != nil {
		return sshPublicKey{}, errors.New("the OpenSSH key is damaged")
	}
	if string(pr.str()) != keyType {
		return sshPublicKey{}, errors.New("the OpenSSH key is damaged")
	}
	// Skip the key material to reach the comment.
	fields := map[string][]byte{"ssh-ed25519": {'s', 's'}, "ssh-rsa": {'m', 'm', 'm', 'm', 'm', 'm'}}
	spec, ok := fields[keyType]
	if strings.HasPrefix(keyType, "ecdsa-sha2-") {
		spec, ok = []byte{'s', 's', 'm'}, true
	}
	comment := ""
	if ok {
		for range spec {
			pr.str()
		}
		comment = string(pr.str())
		if pr.err != nil {
			comment = ""
		}
	}
	return sshPublicKey{keyType: keyType, blob: blob, comment: comment}, nil
}

type sshReader struct {
	data []byte
	err  error
}

func (r *sshReader) u32() uint32 {
	if r.err != nil || len(r.data) < 4 {
		r.err = errors.New("short")
		return 0
	}
	v := binary.BigEndian.Uint32(r.data)
	r.data = r.data[4:]
	return v
}

func (r *sshReader) str() []byte {
	n := r.u32()
	if r.err != nil || uint64(n) > uint64(len(r.data)) {
		r.err = errors.New("short")
		return nil
	}
	v := r.data[:n]
	r.data = r.data[n:]
	return v
}

func (r sshReader) peekStr() []byte { return r.str() }

func sshString(b, s []byte) []byte {
	b = binary.BigEndian.AppendUint32(b, uint32(len(s)))
	return append(b, s...)
}

func sshMPInt(b []byte, n *big.Int) []byte {
	if n.Sign() == 0 {
		return sshString(b, nil)
	}
	raw := n.Bytes()
	if raw[0]&0x80 != 0 {
		raw = append([]byte{0}, raw...)
	}
	return sshString(b, raw)
}

// GenerateSSHKey makes an Ed25519 key, stores it as a secret and returns it
// without the private half.
func GenerateSSHKey(config Config, name, host, description string, agentUse bool) (SecretInfo, error) {
	name = strings.TrimSpace(name)
	if !secretNamePattern.MatchString(name) {
		return SecretInfo{}, errors.New("name the key with lowercase letters, digits, dots, dashes or underscores")
	}
	payload, err := loadSecretsPayload(config)
	if err == nil && secretIndex(payload, name) >= 0 {
		return SecretInfo{}, fmt.Errorf("a secret named %s already exists", name)
	}
	private, _, err := generateSSHKey(name + "@nullbot")
	if err != nil {
		return SecretInfo{}, err
	}
	return SaveSecret(config, SecretInput{
		Name: name, Kind: SecretSSHKey, Description: description, AgentUse: agentUse,
		Values: map[string]string{"private_key": private},
		Public: map[string]string{"host": host},
	})
}
