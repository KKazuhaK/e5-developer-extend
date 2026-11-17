// Package backup defines the single-file backup format: a header, then an
// AES-256-GCM encrypted, gzip-compressed JSON payload whose key is derived
// from a passphrase with PBKDF2. It only converts between a Payload and bytes;
// reading and writing the application's files is done by internal/ops.
//
// Layout (version 1), all of it before the ciphertext is authenticated:
//
//	magic      8 bytes   "E5RNWBAK"
//	version    1 byte    1
//	kdf        1 byte    1 = PBKDF2-HMAC-SHA256
//	iterations 4 bytes   big endian
//	salt       16 bytes
//	nonce      12 bytes
//	ciphertext rest      AES-256-GCM(payload), the header is the additional data
package backup

import (
	"bytes"
	"compress/gzip"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

const (
	magic        = "E5RNWBAK"
	formatNumber = 1
	kdfPBKDF2    = 1

	saltSize   = 16
	nonceSize  = 12
	headerSize = len(magic) + 1 + 1 + 4 + saltSize + nonceSize

	// MaxFileSize is the largest backup file that is read.
	MaxFileSize = 64 << 20
	// MaxPayload bounds the uncompressed payload. Export refuses to write more and
	// import refuses to decompress more, so a small file cannot expand into memory
	// and a file this program writes is always one it reads back.
	MaxPayload = 96 << 20

	minIterations = 100_000
	maxIterations = 10_000_000

	// MinPassphraseLen is the shortest accepted passphrase, in characters.
	MinPassphraseLen = 8
)

// maxPayload is MaxPayload; tests lower it to exercise the limit without allocating 96 MiB.
var maxPayload = MaxPayload

// Iterations is the PBKDF2 cost used when sealing. Tests lower it; never below the accepted minimum.
var Iterations uint32 = 600_000

// Errors returned by Open. Callers show their text, so none of them carries data from the file.
var (
	ErrNotBackup       = errors.New("not an e5renew backup file")
	ErrTooNew          = errors.New("the backup comes from a newer version of e5renew; upgrade e5renew first")
	ErrBadFile         = errors.New("the backup file is damaged")
	ErrDecrypt         = errors.New("wrong backup passphrase, or the file is damaged")
	ErrTooLarge        = errors.New("the backup file is too large")
	ErrPayloadTooLarge = fmt.Errorf("the backup's content is over the %d MiB limit; leave the call records out and make the backup again", MaxPayload>>20)
	ErrShortPassphrase = fmt.Errorf("the backup passphrase must be at least %d characters", MinPassphraseLen)
)

// Payload is the content of a backup.
type Payload struct {
	Format     int    `json:"format"`
	Created    string `json:"created"`
	AppVersion string `json:"app_version"`
	SourceOS   string `json:"source_os"`
	// Config is the text of config.toml.
	Config string `json:"config"`
	// Vault maps vault key names to their plain values.
	Vault map[string][]byte `json:"vault"`
	// State is state.json and GUI is gui.json, as they are on disk; null when absent.
	State json.RawMessage `json:"state"`
	GUI   json.RawMessage `json:"gui"`
	// Logs maps call log file names to their text; null when not included.
	Logs map[string]string `json:"logs"`
}

// CheckPassphrase reports whether passphrase is long enough.
func CheckPassphrase(passphrase string) error {
	if utf8.RuneCountInString(passphrase) < MinPassphraseLen {
		return ErrShortPassphrase
	}
	return nil
}

// Seal encodes p, encrypts it with a key derived from passphrase and returns the file content.
func Seal(p *Payload, passphrase string) ([]byte, error) {
	if err := CheckPassphrase(passphrase); err != nil {
		return nil, err
	}
	p.Format = formatNumber
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("encode backup: %w", err)
	}
	if len(raw) > maxPayload {
		return nil, ErrPayloadTooLarge
	}
	var zipped bytes.Buffer
	zw := gzip.NewWriter(&zipped)
	if _, err := zw.Write(raw); err != nil {
		return nil, fmt.Errorf("compress backup: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("compress backup: %w", err)
	}
	return seal(zipped.Bytes(), passphrase, Iterations)
}

func seal(plain []byte, passphrase string, iterations uint32) ([]byte, error) {
	hdr := make([]byte, headerSize)
	n := copy(hdr, magic)
	hdr[n], hdr[n+1] = formatNumber, kdfPBKDF2
	binary.BigEndian.PutUint32(hdr[n+2:], iterations)
	if _, err := rand.Read(hdr[n+6:]); err != nil { // salt and nonce
		return nil, fmt.Errorf("random bytes: %w", err)
	}
	gcm, err := deriveGCM(passphrase, hdr[n+6:n+6+saltSize], iterations)
	if err != nil {
		return nil, err
	}
	return gcm.Seal(append([]byte(nil), hdr...), hdr[len(hdr)-nonceSize:], plain, hdr), nil
}

func deriveGCM(passphrase string, salt []byte, iterations uint32) (cipher.AEAD, error) {
	key, err := pbkdf2.Key(sha256.New, passphrase, salt, int(iterations), 32)
	if err != nil {
		return nil, fmt.Errorf("derive key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Open checks the header of data, decrypts and decodes it.
func Open(data []byte, passphrase string) (*Payload, error) {
	if len(data) > MaxFileSize {
		return nil, ErrTooLarge
	}
	if len(data) < len(magic) || string(data[:len(magic)]) != magic {
		return nil, ErrNotBackup
	}
	if len(data) < headerSize+16 { // header plus the GCM tag
		return nil, ErrBadFile
	}
	n := len(magic)
	if data[n] != formatNumber || data[n+1] != kdfPBKDF2 {
		return nil, ErrTooNew
	}
	iterations := binary.BigEndian.Uint32(data[n+2:])
	if iterations < minIterations || iterations > maxIterations {
		return nil, ErrBadFile
	}
	hdr, ct := data[:headerSize], data[headerSize:]
	gcm, err := deriveGCM(passphrase, hdr[n+6:n+6+saltSize], iterations)
	if err != nil {
		return nil, err
	}
	zipped, err := gcm.Open(nil, hdr[headerSize-nonceSize:], ct, hdr)
	if err != nil {
		return nil, ErrDecrypt
	}
	zr, err := gzip.NewReader(bytes.NewReader(zipped))
	if err != nil {
		return nil, ErrBadFile
	}
	raw, err := readCapped(zr)
	if err != nil {
		return nil, err
	}
	var p Payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, ErrBadFile
	}
	if isNull(p.State) {
		p.State = nil
	}
	if isNull(p.GUI) {
		p.GUI = nil
	}
	switch {
	case p.Format > formatNumber:
		return nil, ErrTooNew
	case p.Format != formatNumber:
		return nil, ErrBadFile
	}
	return &p, nil
}

// readCapped reads all of r but never more than MaxPayload+1 bytes from it, so a
// stream that expands without end cannot use up memory.
func readCapped(r io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r, int64(maxPayload)+1))
	switch {
	case len(raw) > maxPayload:
		return nil, ErrPayloadTooLarge
	case err != nil:
		return nil, ErrBadFile
	}
	return raw, nil
}

func isNull(m json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(m), []byte("null")) }
