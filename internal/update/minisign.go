package update

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// The release signature is a minisign signature
// (https://jedisct1.github.io/minisign/), so anyone can check SHA256SUMS with
//
//	minisign -Vm SHA256SUMS -P <public key>
//
// Only the unhashed ("Ed") form is made and accepted: it signs the file
// itself, which is small, and needs nothing but crypto/ed25519 to verify. The
// prehashed form ("ED") needs BLAKE2b, which the standard library lacks.

const (
	sigAlgLegacy = "Ed"
	sigAlgHashed = "ED"
	keyIDLen     = 8
)

// ErrNoPublicKey means this build carries no real public key (the placeholder
// of pubkey.go is still in place), so no signature can be checked.
var ErrNoPublicKey = errors.New("this build has no release signing key, so a download cannot be verified")

// ErrBadSignature means the signature does not match.
var ErrBadSignature = errors.New("the signature does not match")

// PublicKey is a minisign public key.
type PublicKey struct {
	ID  [keyIDLen]byte
	Key ed25519.PublicKey
}

// ParsePublicKey reads the base64 text minisign prints for a public key
// ("RWQ..."), or a whole minisign public key file.
func ParsePublicKey(s string) (PublicKey, error) {
	line := lastLine(s)
	raw, err := base64.StdEncoding.DecodeString(line)
	if err != nil {
		return PublicKey{}, fmt.Errorf("public key: %w", err)
	}
	if len(raw) != 2+keyIDLen+ed25519.PublicKeySize || string(raw[:2]) != sigAlgLegacy {
		return PublicKey{}, errors.New("public key: not a minisign Ed25519 public key")
	}
	var k PublicKey
	copy(k.ID[:], raw[2:2+keyIDLen])
	k.Key = ed25519.PublicKey(append([]byte(nil), raw[2+keyIDLen:]...))
	return k, nil
}

// String is the text minisign -P takes.
func (k PublicKey) String() string {
	raw := append([]byte(sigAlgLegacy), k.ID[:]...)
	return base64.StdEncoding.EncodeToString(append(raw, k.Key...))
}

// lastLine returns the last non-empty line, which is the key itself in a key file.
func lastLine(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}

// VerifyFile checks that sig (the text of a .minisig file) is a signature of
// msg made by key. The trusted comment is verified too, as minisign does.
func VerifyFile(key PublicKey, msg, sig []byte) error {
	lines := strings.Split(strings.ReplaceAll(string(sig), "\r\n", "\n"), "\n")
	if len(lines) < 4 || !strings.HasPrefix(lines[0], "untrusted comment:") || !strings.HasPrefix(lines[2], "trusted comment:") {
		return errors.New("signature file: not a minisign signature")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lines[1]))
	if err != nil || len(raw) != 2+keyIDLen+ed25519.SignatureSize {
		return errors.New("signature file: damaged signature line")
	}
	switch string(raw[:2]) {
	case sigAlgLegacy:
	case sigAlgHashed:
		return errors.New("signature file: a prehashed (ED) signature is not supported; sign with: minisign -l -S")
	default:
		return errors.New("signature file: unknown signature algorithm")
	}
	if !bytes.Equal(raw[2:2+keyIDLen], key.ID[:]) {
		return fmt.Errorf("%w: it was made by another key", ErrBadSignature)
	}
	sigBytes := raw[2+keyIDLen:]
	if !ed25519.Verify(key.Key, msg, sigBytes) {
		return ErrBadSignature
	}
	comment := strings.TrimPrefix(lines[2], "trusted comment:")
	comment = strings.TrimPrefix(comment, " ")
	global, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lines[3]))
	if err != nil || len(global) != ed25519.SignatureSize {
		return errors.New("signature file: damaged trusted comment signature")
	}
	if !ed25519.Verify(key.Key, append(append([]byte(nil), sigBytes...), comment...), global) {
		return fmt.Errorf("%w: the trusted comment was changed", ErrBadSignature)
	}
	return nil
}

// SecretKey is the private half of a release signing key.
type SecretKey struct {
	ID   [keyIDLen]byte
	Seed [ed25519.SeedSize]byte
}

// GenerateKey makes a new signing key.
func GenerateKey() (SecretKey, error) {
	var k SecretKey
	if _, err := rand.Read(k.ID[:]); err != nil {
		return k, err
	}
	if _, err := rand.Read(k.Seed[:]); err != nil {
		return k, err
	}
	return k, nil
}

// Public returns the matching public key.
func (k SecretKey) Public() PublicKey {
	priv := ed25519.NewKeyFromSeed(k.Seed[:])
	return PublicKey{ID: k.ID, Key: priv.Public().(ed25519.PublicKey)}
}

const secretComment = "untrusted comment: e5extend release signing key (SECRET: store it as the repository secret RELEASE_SIGNING_KEY, never commit it)"

// Encode is the text of the key file, and of the secret RELEASE_SIGNING_KEY.
func (k SecretKey) Encode() string {
	return secretComment + "\n" + base64.StdEncoding.EncodeToString(append(k.ID[:], k.Seed[:]...)) + "\n"
}

// ParseSecretKey reads what Encode wrote (the comment line is optional).
func ParseSecretKey(s string) (SecretKey, error) {
	raw, err := base64.StdEncoding.DecodeString(lastLine(s))
	if err != nil || len(raw) != keyIDLen+ed25519.SeedSize {
		return SecretKey{}, errors.New("signing key: not a key written by gen-signing-key.go")
	}
	var k SecretKey
	copy(k.ID[:], raw[:keyIDLen])
	copy(k.Seed[:], raw[keyIDLen:])
	return k, nil
}

// Sign makes the text of a .minisig file for msg (legacy, unhashed form).
// trustedComment is covered by the signature too.
func (k SecretKey) Sign(msg []byte, trustedComment string) []byte {
	priv := ed25519.NewKeyFromSeed(k.Seed[:])
	sig := ed25519.Sign(priv, msg)
	global := ed25519.Sign(priv, append(append([]byte(nil), sig...), trustedComment...))
	head := append([]byte(sigAlgLegacy), k.ID[:]...)
	var b strings.Builder
	b.WriteString("untrusted comment: signature from e5extend release key\n")
	b.WriteString(base64.StdEncoding.EncodeToString(append(head, sig...)) + "\n")
	b.WriteString("trusted comment: " + trustedComment + "\n")
	b.WriteString(base64.StdEncoding.EncodeToString(global) + "\n")
	return []byte(b.String())
}
