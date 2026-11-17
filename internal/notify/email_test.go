package notify

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"io"
	"math/big"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
)

// fakeSMTP is a minimal SMTP server that records what it receives.
type fakeSMTP struct {
	ln       net.Listener
	tlsConf  *tls.Config
	pool     *x509.CertPool
	implicit bool   // TLS from the first byte
	starttls bool   // offer STARTTLS
	user     string // the only accepted user ("" = no AUTH offered)
	pass     string
	authKind string // "PLAIN" or "LOGIN"
	reject   string // reply to MAIL FROM instead of 250, e.g. "451 try later"

	mu       sync.Mutex
	from     string
	rcpts    []string
	data     string
	authUsed string
	sessions int
}

func newFakeSMTP(t *testing.T, mode string) *fakeSMTP {
	t.Helper()
	s := &fakeSMTP{authKind: "PLAIN"}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	s.pool = x509.NewCertPool()
	s.pool.AddCert(cert)
	s.tlsConf = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	s.implicit, s.starttls = mode == "tls", mode == "starttls"
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if s.implicit {
		ln = tls.NewListener(ln, s.tlsConf)
	}
	s.ln = ln
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	return s
}

func (s *fakeSMTP) port() int { return s.ln.Addr().(*net.TCPAddr).Port }

func (s *fakeSMTP) serve(c net.Conn) {
	defer c.Close()
	s.mu.Lock()
	s.sessions++
	s.mu.Unlock()
	tc := textproto.NewConn(c)
	_ = tc.PrintfLine("220 fake ESMTP")
	secure := s.implicit
	for {
		line, err := tc.ReadLine()
		if err != nil {
			return
		}
		cmd := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(cmd, "EHLO"):
			_ = tc.PrintfLine("250-fake")
			if s.starttls && !secure {
				_ = tc.PrintfLine("250-STARTTLS")
			}
			if s.user != "" {
				_ = tc.PrintfLine("250-AUTH %s", s.authKind)
			}
			_ = tc.PrintfLine("250 8BITMIME")
		case cmd == "STARTTLS":
			_ = tc.PrintfLine("220 go ahead")
			tlsc := tls.Server(c, s.tlsConf)
			if tlsc.Handshake() != nil {
				return
			}
			c, tc, secure = tlsc, textproto.NewConn(tlsc), true
		case strings.HasPrefix(cmd, "AUTH PLAIN"):
			raw, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(line[len("AUTH PLAIN"):]))
			parts := strings.Split(string(raw), "\x00")
			if len(parts) == 3 && parts[1] == s.user && parts[2] == s.pass {
				s.mu.Lock()
				s.authUsed = "PLAIN"
				s.mu.Unlock()
				_ = tc.PrintfLine("235 ok")
			} else {
				_ = tc.PrintfLine("535 5.7.8 bad credentials")
			}
		case cmd == "AUTH LOGIN":
			_ = tc.PrintfLine("334 %s", base64.StdEncoding.EncodeToString([]byte("Username:")))
			u, _ := tc.ReadLine()
			_ = tc.PrintfLine("334 %s", base64.StdEncoding.EncodeToString([]byte("Password:")))
			p, _ := tc.ReadLine()
			du, _ := base64.StdEncoding.DecodeString(u)
			dp, _ := base64.StdEncoding.DecodeString(p)
			if string(du) == s.user && string(dp) == s.pass {
				s.mu.Lock()
				s.authUsed = "LOGIN"
				s.mu.Unlock()
				_ = tc.PrintfLine("235 ok")
			} else {
				_ = tc.PrintfLine("535 5.7.8 bad credentials")
			}
		case strings.HasPrefix(cmd, "MAIL FROM:"):
			if s.reject != "" {
				_ = tc.PrintfLine("%s", s.reject)
				continue
			}
			s.mu.Lock()
			s.from = strings.Trim(strings.Fields(line[len("MAIL FROM:"):])[0], "<>")
			s.mu.Unlock()
			_ = tc.PrintfLine("250 ok")
		case strings.HasPrefix(cmd, "RCPT TO:"):
			s.mu.Lock()
			s.rcpts = append(s.rcpts, strings.Trim(strings.Fields(line[len("RCPT TO:"):])[0], "<>"))
			s.mu.Unlock()
			_ = tc.PrintfLine("250 ok")
		case cmd == "DATA":
			_ = tc.PrintfLine("354 go")
			b, _ := io.ReadAll(tc.DotReader())
			s.mu.Lock()
			s.data = string(b)
			s.mu.Unlock()
			_ = tc.PrintfLine("250 queued")
		case cmd == "QUIT":
			_ = tc.PrintfLine("221 bye")
			return
		default:
			_ = tc.PrintfLine("250 ok")
		}
	}
}

func (s *fakeSMTP) deliverer() *Deliverer {
	d := testDeliverer(nil)
	d.TLSConfig = &tls.Config{RootCAs: s.pool}
	return d
}

func (s *fakeSMTP) target(security string) Target {
	return Target{Channel: config.Channel{ID: "mail", Type: config.ChannelEmail, SMTPHost: "127.0.0.1", SMTPPort: s.port(), Security: security,
		From: "E5 Alerts <alerts@example.com>", To: []string{"a@example.com", "Bob <b@example.com>"}, Username: s.user, Language: "zh"},
		Secrets: map[string]string{"password": s.pass}}
}

// parsed is a received message split into its MIME parts.
type parsed struct {
	msg   *mail.Message
	parts map[string]string // content type -> decoded body
}

func (s *fakeSMTP) received(t *testing.T) parsed {
	t.Helper()
	s.mu.Lock()
	data := s.data
	s.mu.Unlock()
	if data == "" {
		t.Fatal("the server received no message")
	}
	m, err := mail.ReadMessage(strings.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	mt, params, err := mime.ParseMediaType(m.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/alternative" {
		t.Fatalf("content type %q: %v", m.Header.Get("Content-Type"), err)
	}
	if strings.Count(data, "Content-Transfer-Encoding: quoted-printable") != 2 {
		t.Error("both parts must be quoted-printable")
	}
	p := parsed{msg: m, parts: map[string]string{}}
	mr := multipart.NewReader(m.Body, params["boundary"])
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		ct, cp, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
		if cp["charset"] != "utf-8" {
			t.Errorf("part %s: charset %q", ct, cp["charset"])
		}
		// mime/multipart already undoes the quoted-printable encoding.
		b, _ := io.ReadAll(part)
		p.parts[ct] = string(b)
	}
	return p
}

func TestEmailModes(t *testing.T) {
	for _, mode := range []string{"none", "starttls", "tls"} {
		t.Run(mode, func(t *testing.T) {
			s := newFakeSMTP(t, mode)
			s.user, s.pass = "alerts@example.com", "APP-PASSWORD-1"
			if err := s.deliverer().Deliver(context.Background(), s.target(mode), sampleEv(FailureSpike)); err != nil {
				t.Fatal(err)
			}
			p := s.received(t)
			s.mu.Lock()
			from, rcpts, auth := s.from, s.rcpts, s.authUsed
			s.mu.Unlock()
			if from != "alerts@example.com" || len(rcpts) != 2 || rcpts[0] != "a@example.com" || rcpts[1] != "b@example.com" || auth != "PLAIN" {
				t.Errorf("envelope: from %q rcpts %v auth %q", from, rcpts, auth)
			}
			if got := p.msg.Header.Get("Subject"); got == "" || !strings.HasPrefix(got, "=?UTF-8?") {
				t.Errorf("subject is not encoded: %q", got)
			}
			subj, _ := new(mime.WordDecoder).DecodeHeader(p.msg.Header.Get("Subject"))
			if subj != "[E5 Developer Extend] 警告：调用失败率过高（demo）" {
				t.Errorf("subject: %q", subj)
			}
			if p.msg.Header.Get("From") != `"E5 Alerts" <alerts@example.com>` || !strings.Contains(p.msg.Header.Get("To"), "b@example.com") ||
				p.msg.Header.Get("MIME-Version") != "1.0" || p.msg.Header.Get("Date") == "" || p.msg.Header.Get("Message-ID") == "" {
				t.Errorf("headers: %v", p.msg.Header)
			}
			text, html := p.parts["text/plain"], p.parts["text/html"]
			mustContain(t, "text part", text, "调用失败率过高", "失败率 75%", "dev-box")
			mustContain(t, "html part", html, "<html>", "调用失败率过高", "demo")
			mustNotContain(t, "message", s.data, "APP-PASSWORD-1")
			if strings.Contains(text, "<html>") || strings.Contains(text, "<b>") {
				t.Errorf("plain part has markup:\n%s", text)
			}
		})
	}
}

func TestEmailLoginAuthAndWrongPassword(t *testing.T) {
	s := newFakeSMTP(t, "starttls")
	s.user, s.pass, s.authKind = "u@example.com", "APP-PASSWORD-1", "LOGIN"
	if err := s.deliverer().Deliver(context.Background(), s.target("starttls"), sampleEv(Test)); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	used := s.authUsed
	s.mu.Unlock()
	if used != "LOGIN" {
		t.Errorf("auth: %q", used)
	}
	bad := s.target("starttls")
	bad.Secrets = map[string]string{"password": "WRONG-PASSWORD-9"}
	err := s.deliverer().Deliver(context.Background(), bad, sampleEv(Test))
	if err == nil || !strings.Contains(err.Error(), "535") {
		t.Fatalf("got %v", err)
	}
	var de *DeliveryError
	if !errors.As(err, &de) || de.Temporary {
		t.Errorf("a bad password must not be retried: %v", err)
	}
	mustNotContain(t, "error", err.Error(), "WRONG-PASSWORD-9", "APP-PASSWORD-1")
}

func TestEmailTemporaryRefusalIsRetriedAndNoAuthWorks(t *testing.T) {
	s := newFakeSMTP(t, "none")
	s.reject = "451 4.3.0 try later"
	d := s.deliverer()
	d.Backoff = []time.Duration{time.Millisecond, time.Millisecond}
	err := d.Deliver(context.Background(), s.target("none"), sampleEv(Test))
	var de *DeliveryError
	if err == nil || !errors.As(err, &de) || !de.Temporary {
		t.Fatalf("got %v", err)
	}
	s.mu.Lock()
	n := s.sessions
	s.mu.Unlock()
	if n != 3 {
		t.Errorf("sessions: %d, want 3 (one try and two retries)", n)
	}
}

func TestEmailStartTLSRequiredAndConnectError(t *testing.T) {
	s := newFakeSMTP(t, "none") // offers no STARTTLS
	err := s.deliverer().Deliver(context.Background(), s.target("starttls"), sampleEv(Test))
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Errorf("starttls without server support: %v", err)
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	tg := Target{Channel: config.Channel{ID: "m", Type: config.ChannelEmail, SMTPHost: "127.0.0.1", SMTPPort: port, Security: "none", From: "a@b.co", To: []string{"c@d.co"}}}
	err = testDeliverer(nil).Deliver(context.Background(), tg, sampleEv(Test))
	if err == nil || !strings.Contains(err.Error(), "cannot connect") {
		t.Errorf("connect error: %v", err)
	}
}

func TestEmailHeaderInjectionIsBlocked(t *testing.T) {
	s := newFakeSMTP(t, "none")
	tg := s.target("none")
	tg.Channel.Template = "{{define \"title\"}}hello\r\nBcc: evil@example.com{{end}}{{define \"body\"}}x{{end}}"
	if err := s.deliverer().Deliver(context.Background(), tg, sampleEv(Test)); err != nil {
		t.Fatal(err)
	}
	p := s.received(t)
	if p.msg.Header.Get("Bcc") != "" {
		t.Error("a template injected a header")
	}
}

func TestEmailPresetsAndPorts(t *testing.T) {
	for name, want := range map[string]struct {
		host string
		port int
		sec  string
	}{"gmail": {"smtp.gmail.com", 587, "starttls"}, "outlook": {"smtp-mail.outlook.com", 587, "starttls"},
		"qq": {"smtp.qq.com", 465, "tls"}, "163": {"smtp.163.com", 465, "tls"}} {
		p, ok := EmailPresetByName(name)
		if !ok || p.Host != want.host || p.Port != want.port || p.Security != want.sec {
			t.Errorf("%s: %+v", name, p)
		}
	}
	if _, ok := EmailPresetByName("nope"); ok {
		t.Error("unknown preset found")
	}
	for sec, port := range map[string]int{"": 587, "starttls": 587, "tls": 465, "none": 25} {
		if got := (config.Channel{Security: sec}).SMTPPortOrDefault(); got != port {
			t.Errorf("%q: %d", sec, got)
		}
	}
}
