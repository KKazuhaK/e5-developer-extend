package update

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testKey(t *testing.T) SecretKey {
	t.Helper()
	k, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestSignVerifyRoundTrip(t *testing.T) {
	k := testKey(t)
	msg := []byte("abc  file\n")
	sig := k.Sign(msg, "timestamp:1 file:SHA256SUMS")
	pub, err := ParsePublicKey(k.Public().String())
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyFile(pub, msg, sig); err != nil {
		t.Fatalf("good signature rejected: %v", err)
	}
	// The format is minisign's: four lines, base64 payloads of the documented sizes.
	lines := strings.Split(strings.TrimSpace(string(sig)), "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[0], "untrusted comment: ") || !strings.HasPrefix(lines[2], "trusted comment: ") {
		t.Fatalf("layout: %q", lines)
	}
	raw, _ := base64.StdEncoding.DecodeString(lines[1])
	if len(raw) != 74 || string(raw[:2]) != "Ed" {
		t.Fatalf("signature line: %d bytes, algorithm %q", len(raw), raw[:2])
	}
	pk, _ := base64.StdEncoding.DecodeString(k.Public().String())
	if len(pk) != 42 || !strings.HasPrefix(k.Public().String(), "RW") {
		t.Fatalf("public key: %d bytes, text %q", len(pk), k.Public().String())
	}
	// A whole minisign public key file works too.
	file := "untrusted comment: minisign public key: ABCD\n" + k.Public().String() + "\n"
	if _, err := ParsePublicKey(file); err != nil {
		t.Fatalf("key file: %v", err)
	}
}

func TestVerifyFileRejects(t *testing.T) {
	k, other := testKey(t), testKey(t)
	msg := []byte("the checksums\n")
	sig := k.Sign(msg, "tc")
	pub := k.Public()

	cases := map[string]struct {
		pub PublicKey
		msg []byte
		sig []byte
	}{
		"changed message":  {pub, []byte("the checksums\nmore\n"), sig},
		"another key":      {other.Public(), msg, sig},
		"changed comment":  {pub, msg, []byte(strings.Replace(string(sig), "trusted comment: tc", "trusted comment: evil", 1))},
		"not a signature":  {pub, msg, []byte("hello")},
		"empty":            {pub, msg, nil},
		"damaged base64":   {pub, msg, []byte(strings.Replace(string(sig), "\n", "!\n", 2))},
		"truncated":        {pub, msg, sig[:len(sig)/2]},
		"prehashed format": {pub, msg, bytes.Replace(sig, []byte(base64Prefix(sig, "Ed")), []byte(base64Prefix(sig, "ED")), 1)},
	}
	for name, c := range cases {
		if err := VerifyFile(c.pub, c.msg, c.sig); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A signature of the right key over other bytes is a mismatch, not a format error.
	if err := VerifyFile(other.Public(), msg, sig); !errors.Is(err, ErrBadSignature) {
		t.Errorf("another key: %v", err)
	}
}

// base64Prefix returns the first characters of the signature line, which
// encode the two algorithm bytes, for alg ("Ed" or "ED") given the other bytes.
func base64Prefix(sig []byte, alg string) string {
	line := strings.Split(string(sig), "\n")[1]
	raw, _ := base64.StdEncoding.DecodeString(line)
	raw[0], raw[1] = alg[0], alg[1]
	return base64.StdEncoding.EncodeToString(raw)
}

func TestVerifyFileAcceptsKnownEd25519Signature(t *testing.T) {
	// An independent check of the signed payloads: build the file by hand from
	// crypto/ed25519 following the minisign format description.
	seed := bytes.Repeat([]byte{7}, ed25519.SeedSize)
	priv := ed25519.NewKeyFromSeed(seed)
	id := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	msg := []byte("data")
	sig := ed25519.Sign(priv, msg)
	global := ed25519.Sign(priv, append(append([]byte(nil), sig...), "tc"...))
	file := "untrusted comment: x\n" + base64.StdEncoding.EncodeToString(append(append([]byte("Ed"), id...), sig...)) + "\ntrusted comment: tc\n" + base64.StdEncoding.EncodeToString(global) + "\n"
	pub := base64.StdEncoding.EncodeToString(append(append([]byte("Ed"), id...), priv.Public().(ed25519.PublicKey)...))
	key, err := ParsePublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyFile(key, msg, []byte(file)); err != nil {
		t.Fatal(err)
	}
	// Windows line endings do not matter.
	if err := VerifyFile(key, msg, []byte(strings.ReplaceAll(file, "\n", "\r\n"))); err != nil {
		t.Fatal(err)
	}
}

func TestSecretKeyEncoding(t *testing.T) {
	k := testKey(t)
	for _, text := range []string{k.Encode(), lastLine(k.Encode())} {
		back, err := ParseSecretKey(text)
		if err != nil || back != k {
			t.Fatalf("round trip of %q: %v", text, err)
		}
	}
	if _, err := ParseSecretKey("nonsense"); err == nil {
		t.Fatal("accepted nonsense")
	}
	if _, err := ParsePublicKey("nonsense"); err == nil {
		t.Fatal("accepted a bad public key")
	}
}

func TestHasPublicKey(t *testing.T) {
	if !HasPublicKey(PublicKeyText) {
		t.Fatal("the committed release key must be a real minisign public key")
	}
	if HasPublicKey("") || HasPublicKey(placeholderKey) || HasPublicKey("RWQnotbase64!!") {
		t.Fatal("accepted a non-key")
	}
	if !HasPublicKey(testKey(t).Public().String()) {
		t.Fatal("rejected a real key")
	}
}

// signedDir makes a folder with the given files, SHA256SUMS of them and, when
// sign is set, its signature by key.
func signedDir(t *testing.T, key SecretKey, files map[string]string, sign bool) string {
	t.Helper()
	dir := t.TempDir()
	var sums strings.Builder
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		sum, _ := FileSum(p)
		sums.WriteString(sum + "  " + name + "\n")
	}
	if err := os.WriteFile(filepath.Join(dir, SumsName), []byte(sums.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if sign {
		if err := os.WriteFile(filepath.Join(dir, SigName), key.Sign([]byte(sums.String()), "file:SHA256SUMS"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestVerifyBundle(t *testing.T) {
	key := testKey(t)
	pub := key.Public().String()
	files := map[string]string{"a.bin": "alpha", "b.bin": "beta"}

	dir := signedDir(t, key, files, true)
	if err := VerifyBundle(pub, dir, []string{"a.bin", "b.bin"}); err != nil {
		t.Fatalf("good bundle: %v", err)
	}

	// Checksum mismatch: a file changed after the checksums were made.
	dir = signedDir(t, key, files, true)
	_ = os.WriteFile(filepath.Join(dir, "a.bin"), []byte("tampered"), 0o600)
	if err := VerifyBundle(pub, dir, []string{"a.bin"}); !errors.Is(err, ErrChecksum) {
		t.Fatalf("tampered file: %v", err)
	}

	// A file the signed list does not name.
	dir = signedDir(t, key, files, true)
	_ = os.WriteFile(filepath.Join(dir, "extra.bin"), []byte("x"), 0o600)
	if err := VerifyBundle(pub, dir, []string{"extra.bin"}); !errors.Is(err, ErrChecksum) {
		t.Fatalf("unlisted file: %v", err)
	}

	// Missing signature.
	dir = signedDir(t, key, files, false)
	if err := VerifyBundle(pub, dir, []string{"a.bin"}); !errors.Is(err, ErrNoSignature) {
		t.Fatalf("no signature: %v", err)
	}

	// Checksums replaced after signing: the signature no longer matches.
	dir = signedDir(t, key, files, true)
	sum, _ := FileSum(filepath.Join(dir, "a.bin"))
	_ = os.WriteFile(filepath.Join(dir, SumsName), []byte(sum+"  a.bin\n"), 0o600)
	if err := VerifyBundle(pub, dir, []string{"a.bin"}); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("edited checksums: %v", err)
	}

	// Signed by another key.
	dir = signedDir(t, testKey(t), files, true)
	if err := VerifyBundle(pub, dir, []string{"a.bin"}); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("wrong key: %v", err)
	}

	// The placeholder key can verify nothing.
	dir = signedDir(t, key, files, true)
	if err := VerifyBundle(placeholderKey, dir, []string{"a.bin"}); !errors.Is(err, ErrNoPublicKey) {
		t.Fatalf("placeholder: %v", err)
	}
	if err := VerifyBundle("", dir, []string{"a.bin"}); !errors.Is(err, ErrNoPublicKey) {
		t.Fatalf("empty key: %v", err)
	}
}

func TestParseSums(t *testing.T) {
	h := strings.Repeat("ab", 32)
	m, err := ParseSums([]byte(h + "  one.txt\n" + strings.ToUpper(h) + " *two.bin\n\n"))
	if err != nil || m["one.txt"] != h || m["two.bin"] != h {
		t.Fatalf("%v %v", m, err)
	}
	for _, bad := range []string{"short  x\n", h + "\n", strings.Repeat("zz", 32) + "  x\n"} {
		if _, err := ParseSums([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

// TestVerifyFileWithMinisignOutput checks a signature made by the real
// minisign tool (minisign 0.12, `minisign -l -S`) with a throwaway key.
func TestVerifyFileWithMinisignOutput(t *testing.T) {
	const pub = "RWRpZO8foXrdCvTZj+JiDJH4x4B7cSB4ys0UC5KdlHpU9CM/k/RGyhVw"
	const sig = "untrusted comment: signature from minisign secret key\n" +
		"RWRpZO8foXrdCmX/f2aSEfpbEpPTW4bzvYO2jV4xWykkV6m4iL2114f36rjzd6GpeH9VKs0B+zgqaCVMltVAScwEMH4sa2IJGwg=\n" +
		"trusted comment: timestamp:1794902323\tfile:m.txt\n" +
		"/d+mi3AQZwAU0emBuyCunVO2Nl3x97qaEgQTbzT3V8JtKS7dYABKWF+4p1NJfFv5vn0WebSwQhiPsmF2qQgeDA==\n"
	key, err := ParsePublicKey("untrusted comment: minisign public key 0ADD7AA11FEF6469\n" + pub + "\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyFile(key, []byte("hello\n"), []byte(sig)); err != nil {
		t.Fatalf("a signature made by minisign was rejected: %v", err)
	}
	if err := VerifyFile(key, []byte("hello"), []byte(sig)); err == nil {
		t.Fatal("accepted another message")
	}
}
