package backup

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
)

const pass = "correct horse battery"

func TestMain(m *testing.M) {
	Iterations = minIterations // keep the tests quick; Open accepts this as the lowest value
	m.Run()
}

func samplePayload() *Payload {
	return &Payload{
		Created: "2026-11-16T17:00:00-08:00", AppVersion: "0.2.0", SourceOS: "linux",
		Config: "version = 1\n",
		Vault:  map[string][]byte{"secret/main": []byte("s3cret"), "tokencache/main": {0, 1, 2, 255}},
		State:  []byte(`{"version":1}`),
		Logs:   map[string]string{"calls-2026-11-16.jsonl": "{\"a\":1}\n"},
	}
}

func TestRoundTrip(t *testing.T) {
	data, err := Seal(samplePayload(), pass)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("s3cret")) || !bytes.HasPrefix(data, []byte("E5RNWBAK\x01\x01")) {
		t.Fatal("header wrong or content not encrypted")
	}
	got, err := Open(data, pass)
	if err != nil {
		t.Fatal(err)
	}
	want := samplePayload()
	if got.Format != 1 || got.Config != want.Config || got.SourceOS != "linux" || got.AppVersion != "0.2.0" ||
		string(got.Vault["secret/main"]) != "s3cret" || !bytes.Equal(got.Vault["tokencache/main"], want.Vault["tokencache/main"]) ||
		string(got.State) != `{"version":1}` || got.GUI != nil ||
		got.Logs["calls-2026-11-16.jsonl"] != want.Logs["calls-2026-11-16.jsonl"] {
		t.Fatalf("payload changed: %+v", got)
	}
	// Two backups of the same content differ (fresh salt and nonce).
	again, _ := Seal(samplePayload(), pass)
	if bytes.Equal(data, again) {
		t.Fatal("salt or nonce reused")
	}
}

func TestWrongPassphrase(t *testing.T) {
	data, _ := Seal(samplePayload(), pass)
	if _, err := Open(data, pass+"x"); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("got %v", err)
	}
}

func TestShortPassphraseRejected(t *testing.T) {
	if _, err := Seal(samplePayload(), "1234567"); !errors.Is(err, ErrShortPassphrase) {
		t.Fatalf("got %v", err)
	}
	// Characters are counted, not bytes.
	if err := CheckPassphrase("密码密码密码密码"); err != nil {
		t.Fatalf("8 characters rejected: %v", err)
	}
	if err := CheckPassphrase("密码密码密码"); err == nil {
		t.Fatal("6 characters accepted")
	}
}

func TestEveryHeaderByteAndSampledCiphertextAreAuthenticated(t *testing.T) {
	data, _ := Seal(samplePayload(), pass)
	// Every byte checked before key derivation (magic, version, kdf, iterations), then the ends of the
	// salt and nonce and a spread of ciphertext bytes including the GCM tag. Each of those derives a key,
	// so the whole ciphertext is not walked.
	idx := make([]int, 0, 24)
	for i := 0; i < 14; i++ {
		idx = append(idx, i)
	}
	n := len(data)
	idx = append(idx, 14, 29, 30, headerSize-1, headerSize, n/2, n-16, n-1)
	for _, i := range idx {
		bad := bytes.Clone(data)
		bad[i] ^= 0x01
		_, err := Open(bad, pass)
		if err == nil {
			t.Fatalf("flipping byte %d went unnoticed", i)
		}
		if i >= 14 && !errors.Is(err, ErrDecrypt) {
			t.Fatalf("byte %d (salt, nonce or ciphertext): got %v", i, err)
		}
	}
}

func TestNotABackup(t *testing.T) {
	for _, in := range [][]byte{nil, []byte("hello"), []byte("E5RNWBA"), bytes.Repeat([]byte{'x'}, 200)} {
		if _, err := Open(in, pass); !errors.Is(err, ErrNotBackup) {
			t.Errorf("%q: got %v", in, err)
		}
	}
	if _, err := Open([]byte("E5RNWBAK\x01\x01"), pass); !errors.Is(err, ErrBadFile) {
		t.Errorf("truncated header: %v", err)
	}
}

func TestUnknownVersionAndKDF(t *testing.T) {
	data, _ := Seal(samplePayload(), pass)
	for _, idx := range []int{8, 9} {
		bad := bytes.Clone(data)
		bad[idx] = 2
		if _, err := Open(bad, pass); !errors.Is(err, ErrTooNew) {
			t.Errorf("byte %d: got %v", idx, err)
		}
	}
}

func TestIterationsOutOfRange(t *testing.T) {
	data, _ := Seal(samplePayload(), pass)
	for _, it := range []uint32{0, 1, minIterations - 1, maxIterations + 1, 1 << 31} {
		bad := bytes.Clone(data)
		binary.BigEndian.PutUint32(bad[10:], it)
		if _, err := Open(bad, pass); !errors.Is(err, ErrBadFile) {
			t.Errorf("iterations %d: got %v", it, err)
		}
	}
}

func TestOversizedFile(t *testing.T) {
	big := make([]byte, MaxFileSize+1)
	copy(big, magic)
	if _, err := Open(big, pass); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("got %v", err)
	}
}

func TestEmptyAndMalformedPayloads(t *testing.T) {
	var zipped bytes.Buffer
	zw := gzip.NewWriter(&zipped)
	_, _ = zw.Write([]byte(`{}`))
	_ = zw.Close()
	cases := map[string][]byte{
		"empty plaintext":    nil,
		"not gzip":           []byte("plain"),
		"empty json object":  zipped.Bytes(),
		"gzip of non-json":   gz(t, "not json"),
		"gzip of empty":      gz(t, ""),
		"newer payload":      gz(t, `{"format":2}`),
		"zero-format object": gz(t, `{"format":0,"config":"x"}`),
	}
	for name, plain := range cases {
		data, err := seal(plain, pass, minIterations)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Open(data, pass); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	data, _ := seal(gz(t, `{"format":2}`), pass, minIterations)
	if _, err := Open(data, pass); !errors.Is(err, ErrTooNew) {
		t.Errorf("newer payload: %v", err)
	}
}

// Every way Open can fail is fed a file that holds a secret in the place the
// failure is about; none of the errors may repeat it, nor the passphrase.
func TestErrorsCarryNothingFromTheFile(t *testing.T) {
	const secret = "TOPSECRET-VALUE-9f3a"
	const wrongPass = "the wrong passphrase 77"

	sealed := func(plain []byte) []byte {
		t.Helper()
		data, err := seal(plain, pass, minIterations)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	withHeader := func(version, kdf byte, iterations uint32, rest string) []byte {
		b := append([]byte(magic), version, kdf, 0, 0, 0, 0)
		binary.BigEndian.PutUint32(b[len(magic)+2:], iterations)
		return append(b, rest...)
	}
	good, err := Seal(&Payload{Config: secret, Vault: map[string][]byte{"secret/main": []byte(secret)}, Logs: map[string]string{"x": secret}}, pass)
	if err != nil {
		t.Fatal(err)
	}
	tampered := append([]byte(nil), good...)
	tampered[len(tampered)-1] ^= 1

	cases := []struct {
		name string
		data []byte
		pass string
		want error
	}{
		{"not a backup", []byte(secret + " is the first line of some other file"), pass, ErrNotBackup},
		{"truncated after the magic", append([]byte(magic), secret...), pass, ErrBadFile},
		{"newer format", withHeader(9, kdfPBKDF2, minIterations, strings.Repeat(secret, 8)), pass, ErrTooNew},
		{"unknown kdf", withHeader(formatNumber, 9, minIterations, strings.Repeat(secret, 8)), pass, ErrTooNew},
		{"iterations too low", withHeader(formatNumber, kdfPBKDF2, 1, strings.Repeat(secret, 8)), pass, ErrBadFile},
		{"iterations too high", withHeader(formatNumber, kdfPBKDF2, maxIterations+1, strings.Repeat(secret, 8)), pass, ErrBadFile},
		{"wrong passphrase", good, wrongPass, ErrDecrypt},
		{"tampered ciphertext", tampered, pass, ErrDecrypt},
		{"not gzip", sealed([]byte(secret)), pass, ErrBadFile},
		{"gzip of text", sealed(gz(t, secret)), pass, ErrBadFile},
		{"json of the wrong shape", sealed(gz(t, `{"format":"`+secret+`"}`)), pass, ErrBadFile},
		{"truncated json", sealed(gz(t, `{"format":1,"config":"`+secret)), pass, ErrBadFile},
		{"newer payload", sealed(gz(t, `{"format":2,"config":"`+secret+`"}`)), pass, ErrTooNew},
		{"missing format", sealed(gz(t, `{"config":"`+secret+`"}`)), pass, ErrBadFile},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Open(c.data, c.pass)
			if !errors.Is(err, c.want) {
				t.Fatalf("got %v, want %v", err, c.want)
			}
			for _, leak := range []string{secret, wrongPass, pass} {
				if strings.Contains(err.Error(), leak) {
					t.Fatalf("the error repeats %q: %v", leak, err)
				}
			}
		})
	}

	// Sealing: the short passphrase is not repeated either.
	_, err = Seal(&Payload{Config: secret}, "short")
	if !errors.Is(err, ErrShortPassphrase) || strings.Contains(err.Error(), "short\"") {
		t.Errorf("seal: %v", err)
	}
	// And every fixed error text is free of what a test file could hold.
	for _, e := range []error{ErrNotBackup, ErrTooNew, ErrBadFile, ErrDecrypt, ErrTooLarge, ErrPayloadTooLarge, ErrShortPassphrase} {
		if strings.Contains(e.Error(), secret) || strings.Contains(e.Error(), pass) {
			t.Errorf("%v", e)
		}
	}
}

func lowerCap(t *testing.T, n int) {
	t.Helper()
	old := maxPayload
	maxPayload = n
	t.Cleanup(func() { maxPayload = old })
}

func TestSealRefusesMoreThanMaxPayload(t *testing.T) {
	if MaxPayload != 96<<20 {
		t.Fatalf("MaxPayload = %d", MaxPayload)
	}
	lowerCap(t, 4<<20)
	p := &Payload{Config: "version = 1\n", Logs: map[string]string{"calls-2026-01-01.jsonl": strings.Repeat("a", maxPayload)}}
	_, err := Seal(p, pass)
	if !errors.Is(err, ErrPayloadTooLarge) || !strings.Contains(err.Error(), "call records") {
		t.Fatalf("got %v", err)
	}
	// Just under the cap is accepted and read back.
	p.Logs["calls-2026-01-01.jsonl"] = strings.Repeat("a", maxPayload-1<<20)
	data, err := Seal(p, pass)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Open(data, pass)
	if err != nil || len(got.Logs["calls-2026-01-01.jsonl"]) != maxPayload-1<<20 {
		t.Fatalf("read back: %v", err)
	}
}

// A small file that expands past the cap is refused with its own error.
func TestOpenRefusesAnExpandingFile(t *testing.T) {
	lowerCap(t, 4<<20)
	var zipped bytes.Buffer
	zw := gzip.NewWriter(&zipped)
	chunk := make([]byte, 1<<20)
	for i := 0; i < maxPayload>>20+8; i++ { // a little over the cap, as zeros
		if _, err := zw.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	_ = zw.Close()
	if zipped.Len() > 1<<20 {
		t.Fatalf("the crafted file is not small: %d bytes", zipped.Len())
	}
	data, err := seal(zipped.Bytes(), pass, minIterations)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(data, pass); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("got %v", err)
	}
}

// countingReader serves zeros and counts how many were taken.
type countingReader struct{ n, limit int64 }

func (r *countingReader) Read(p []byte) (int, error) {
	if r.n >= r.limit {
		return 0, io.EOF
	}
	k := int64(len(p))
	if r.n+k > r.limit {
		k = r.limit - r.n
	}
	clear(p[:k])
	r.n += k
	return int(k), nil
}

// Whatever the stream holds, no more than the cap (plus one byte) is ever read from it.
func TestReadCappedStopsAtTheCap(t *testing.T) {
	src := &countingReader{limit: 4 * MaxPayload}
	_, err := readCapped(src)
	if !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("got %v", err)
	}
	if src.n > MaxPayload+1<<20 { // io.ReadAll asks in chunks; the limit reader still stops at cap+1
		t.Fatalf("read %d bytes from the stream, cap is %d", src.n, MaxPayload)
	}
}

func gz(t *testing.T, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	_, _ = zw.Write([]byte(s))
	_ = zw.Close()
	return b.Bytes()
}
