// Package updatetest is a fake GitHub for the tests of the update check and
// upgrade: one signed release served from a local test server. Only tests
// import it; no test ever talks to the real GitHub.
package updatetest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/KKazuhaK/e5-developer-extend/internal/update"
)

// Server serves /repos/<repo>/releases/latest and the release files under /dl/.
type Server struct {
	URL string
	// Key signed the checksums; PublicKey is what a program must trust.
	Key update.SecretKey

	mu   sync.Mutex
	hits map[string]int
	srv  *httptest.Server
}

// New starts a server for release tag with the given files (name -> content).
// SHA256SUMS and its signature are added. Linux standalone archives are made
// with TarGz.
func New(t testing.TB, tag string, files map[string][]byte) *Server {
	t.Helper()
	key, err := update.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Key: key, hits: map[string]int{}}
	served := map[string][]byte{}
	var sums strings.Builder
	for n, b := range files {
		h := sha256.Sum256(b)
		sums.WriteString(hex.EncodeToString(h[:]) + "  " + n + "\n")
		served[n] = b
	}
	served[update.SumsName] = []byte(sums.String())
	served[update.SigName] = key.Sign([]byte(sums.String()), "file:SHA256SUMS")

	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.hits[r.URL.Path]++
		s.mu.Unlock()
		if r.URL.Path == "/repos/"+update.Repo+"/releases/latest" {
			assets := []map[string]any{}
			for n, b := range served {
				assets = append(assets, map[string]any{"name": n, "size": len(b), "browser_download_url": s.URL + "/dl/" + n})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name": tag, "html_url": "https://example.test/release/" + tag, "published_at": "2026-12-01T10:00:00Z", "assets": assets,
			})
			return
		}
		if b, ok := served[strings.TrimPrefix(r.URL.Path, "/dl/")]; ok {
			_, _ = w.Write(b)
			return
		}
		http.NotFound(w, r)
	}))
	s.URL = s.srv.URL
	t.Cleanup(s.srv.Close)
	return s
}

// PublicKey is the key text a program must be given to trust this server's release.
func (s *Server) PublicKey() string { return s.Key.Public().String() }

// Downloads counts the requests made for release files.
func (s *Server) Downloads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for p, c := range s.hits {
		if strings.HasPrefix(p, "/dl/") {
			n += c
		}
	}
	return n
}

// TarGz makes a tar.gz with one program called name.
func TarGz(t testing.TB, name, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	_, _ = tw.Write([]byte(content))
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}
