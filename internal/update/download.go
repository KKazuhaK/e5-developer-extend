package update

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// maxDownload bounds one downloaded file; the largest release file is far smaller.
const maxDownload = 400 << 20

// Limits of one downloaded file: it fails when no byte arrives for this long,
// and when it takes longer than the overall cap.
const (
	defaultStall   = 60 * time.Second
	defaultMaxTime = 30 * time.Minute
)

// errStalled is the cause of a download that stopped receiving bytes.
var errStalled = errors.New("no data received for too long")

// Errors a download or its verification ends with.
var (
	// ErrNoSignature means the release carries no signature of its checksums
	// (it was built without a signing key).
	ErrNoSignature = errors.New("this release has no signature, so it cannot be verified")
	// ErrChecksum means a downloaded file does not match the signed checksum list.
	ErrChecksum = errors.New("the downloaded file does not match its checksum")
)

// downloadPrefix is where release files are served from; anything else is refused.
var downloadPrefix = "https://github.com/" + Repo + "/releases/download/"

// Downloader fetches release files.
type Downloader struct {
	// HTTP carries the proxy of the configuration; a plain client when nil.
	HTTP *http.Client
	// Allow decides which addresses may be downloaded from; the release
	// download address of this repository when nil. Tests allow their own server.
	Allow func(url string) bool
	// Stall is how long a download may receive nothing before it fails (60 s
	// when zero); MaxTime caps one file altogether (30 minutes when zero).
	Stall, MaxTime time.Duration
}

func (d *Downloader) allowed(url string) bool {
	if d.Allow != nil {
		return d.Allow(url)
	}
	return strings.HasPrefix(url, downloadPrefix)
}

// Fetch downloads a to dir/<name> and returns the path. progress, when not
// nil, is called with the bytes done and the size (0 when unknown).
func (d *Downloader) Fetch(ctx context.Context, a Asset, dir string, progress func(done, total int64)) (string, error) {
	if a.Name == "" || a.Name != filepath.Base(a.Name) || strings.ContainsAny(a.Name, `\/`) || a.Name == "." || a.Name == ".." {
		return "", fmt.Errorf("refusing the file name %q", a.Name)
	}
	if !d.allowed(a.URL) {
		return "", fmt.Errorf("refusing to download %s from an address outside the project's releases", a.Name)
	}
	if a.Size > maxDownload {
		return "", fmt.Errorf("%s is %d bytes, more than the %d allowed", a.Name, a.Size, int64(maxDownload))
	}
	client := http.Client{}
	if d.HTTP != nil {
		client = *d.HTTP
	}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" {
			return errors.New("refusing a redirect to a non-https address")
		}
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		return nil
	}
	stall, maxTime := d.Stall, d.MaxTime
	if stall <= 0 {
		stall = defaultStall
	}
	if maxTime <= 0 {
		maxTime = defaultMaxTime
	}
	ctx, stop := context.WithTimeout(ctx, maxTime)
	defer stop()
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	timer := time.AfterFunc(stall, func() { cancel(errStalled) })
	defer timer.Stop()
	fail := func(err error) error {
		switch {
		case errors.Is(context.Cause(ctx), errStalled):
			err = fmt.Errorf("%w (%s)", errStalled, stall)
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			err = fmt.Errorf("it took longer than %s", maxTime)
		}
		return fmt.Errorf("download %s: %w", a.Name, urlErrorText(err))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fail(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: GitHub answered %s", a.Name, resp.Status)
	}
	total := resp.ContentLength
	if total < 0 {
		total = a.Size
	}
	if total > maxDownload {
		return "", fmt.Errorf("download %s: the file is larger than %d bytes", a.Name, int64(maxDownload))
	}
	path := filepath.Join(dir, a.Name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	timer.Reset(stall)
	n, err := io.Copy(f, &progressReader{r: io.LimitReader(resp.Body, maxDownload+1), total: total, cb: progress, alive: func() { timer.Reset(stall) }})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > maxDownload {
		err = errors.New("the file is larger than allowed")
	}
	if err == nil && total > 0 && n != total {
		err = fmt.Errorf("received %d of %d bytes", n, total)
	}
	if err != nil {
		_ = os.Remove(path)
		return "", fail(err)
	}
	return path, nil
}

type progressReader struct {
	r     io.Reader
	done  int64
	total int64
	cb    func(done, total int64)
	last  int64
	alive func() // called whenever bytes arrive
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.done += int64(n)
	if n > 0 && p.alive != nil {
		p.alive()
	}
	// Report at most about 200 times per file.
	if p.cb != nil && (err != nil || p.done-p.last >= 1<<18 || p.done == p.total) {
		p.last = p.done
		p.cb(p.done, p.total)
	}
	return n, err
}

// ParseSums reads a sha256sum list into a map from file name to lowercase hex digest.
func ParseSums(b []byte) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		sum, name, ok := strings.Cut(line, " ")
		name = strings.TrimPrefix(strings.TrimLeft(name, " "), "*")
		if !ok || len(sum) != 64 || name == "" {
			return nil, fmt.Errorf("SHA256SUMS: cannot read the line %q", line)
		}
		if _, err := hex.DecodeString(sum); err != nil {
			return nil, fmt.Errorf("SHA256SUMS: cannot read the line %q", line)
		}
		out[name] = strings.ToLower(sum)
	}
	return out, sc.Err()
}

// FileSum is the SHA-256 digest of a file, in hex.
func FileSum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// VerifyBundle checks the files named by names, all in dir, against the signed
// checksum list in dir (SHA256SUMS and SHA256SUMS.minisig). Nothing in dir may
// be used before it has passed. pubKey is PublicKeyText.
func VerifyBundle(pubKey, dir string, names []string) error {
	if !HasPublicKey(pubKey) {
		return ErrNoPublicKey
	}
	key, err := ParsePublicKey(pubKey)
	if err != nil {
		return err
	}
	sums, err := os.ReadFile(filepath.Join(dir, SumsName))
	if err != nil {
		return fmt.Errorf("read %s: %w", SumsName, err)
	}
	sig, err := os.ReadFile(filepath.Join(dir, SigName))
	if errors.Is(err, os.ErrNotExist) {
		return ErrNoSignature
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", SigName, err)
	}
	if err := VerifyFile(key, sums, sig); err != nil {
		return fmt.Errorf("%s: %w", SigName, err)
	}
	list, err := ParseSums(sums)
	if err != nil {
		return err
	}
	for _, n := range names {
		want, ok := list[n]
		if !ok {
			return fmt.Errorf("%w: %s is not in the signed list", ErrChecksum, n)
		}
		got, err := FileSum(filepath.Join(dir, n))
		if err != nil {
			return err
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			return fmt.Errorf("%w: %s", ErrChecksum, n)
		}
	}
	return nil
}
