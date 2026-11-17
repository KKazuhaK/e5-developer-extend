package notify

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
)

// EmailPreset is the server settings of a well-known mail provider.
type EmailPreset struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Security string `json:"security"`
	// Note says what the password has to be, in English; the UIs translate it.
	Note string `json:"note"`
}

// EmailPresets lists the providers the CLI and the app can fill in.
func EmailPresets() []EmailPreset {
	return []EmailPreset{
		{"gmail", "smtp.gmail.com", 587, config.SecurityStartTLS, "an app password (2-step verification must be on)"},
		{"outlook", "smtp-mail.outlook.com", 587, config.SecurityStartTLS, "an app password, or SMTP AUTH enabled for the mailbox"},
		{"qq", "smtp.qq.com", 465, config.SecurityTLS, "the SMTP authorization code, not the login password"},
		{"163", "smtp.163.com", 465, config.SecurityTLS, "the SMTP authorization code, not the login password"},
	}
}

// EmailPresetByName finds a preset.
func EmailPresetByName(name string) (EmailPreset, bool) {
	for _, p := range EmailPresets() {
		if p.Name == strings.ToLower(name) {
			return p, true
		}
	}
	return EmailPreset{}, false
}

// smtpTimeout bounds one whole mail transaction when no timeout is set.
const smtpTimeout = 30 * time.Second

func (d *Deliverer) sendEmail(ctx context.Context, t Target, r Rendered) error {
	c := t.Channel
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = smtpTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	from, err := mail.ParseAddress(c.From)
	if err != nil {
		return &DeliveryError{msg: "channel " + c.ID + ": the from address is not valid"}
	}
	var to []string
	for _, a := range c.To {
		pa, err := mail.ParseAddress(a)
		if err != nil {
			return &DeliveryError{msg: "channel " + c.ID + ": a to address is not valid"}
		}
		to = append(to, pa.Address)
	}
	msg, err := d.buildMail(from, c.To, r)
	if err != nil {
		return &DeliveryError{msg: "build the message: " + err.Error()}
	}
	password := t.Secrets["password"]
	secret := []string{password}
	clean := func(err error) error {
		s := err.Error()
		for _, v := range secret {
			if v != "" {
				s = strings.ReplaceAll(s, v, "<secret>")
			}
		}
		return &DeliveryError{Temporary: isTemporarySMTP(err), msg: "email: " + s}
	}

	// A password must not cross the network unencrypted by accident: with
	// security none it goes only to this machine unless allow_plain_auth says otherwise.
	if c.Username != "" && c.SecurityOrDefault() == config.SecurityNone && !c.AllowPlainAuth && !IsLoopback(c.SMTPHost) {
		return &DeliveryError{msg: "email: refusing to send the password without encryption; use starttls or tls (or set allow_plain_auth)"}
	}
	host, port := c.SMTPHost, c.SMTPPortOrDefault()
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	tlsConf := d.tlsConfig(host)
	dialer := &net.Dialer{}
	var conn net.Conn
	if c.SecurityOrDefault() == config.SecurityTLS {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: tlsConf}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return &DeliveryError{Temporary: true, msg: "email: cannot connect to the mail server: " + shortNetErr(err)}
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	cl, err := smtp.NewClient(conn, host)
	if err != nil {
		return clean(err)
	}
	defer cl.Close()
	if err := cl.Hello(localName()); err != nil {
		return clean(err)
	}
	if c.SecurityOrDefault() == config.SecurityStartTLS {
		if ok, _ := cl.Extension("STARTTLS"); !ok {
			return &DeliveryError{msg: "email: the server does not offer STARTTLS; choose another security mode"}
		}
		if err := cl.StartTLS(tlsConf); err != nil {
			return clean(err)
		}
	}
	if c.Username != "" {
		ext, params := cl.Extension("AUTH")
		if !ext {
			return &DeliveryError{msg: "email: the server does not offer authentication"}
		}
		var auth smtp.Auth
		if strings.Contains(strings.ToUpper(params), "PLAIN") {
			auth = plainAuth{c.Username, password}
		} else if strings.Contains(strings.ToUpper(params), "LOGIN") {
			auth = loginAuth{c.Username, password}
		} else {
			return &DeliveryError{msg: "email: the server offers no supported sign-in method (PLAIN or LOGIN)"}
		}
		if err := cl.Auth(auth); err != nil {
			return clean(err)
		}
	}
	if err := cl.Mail(from.Address); err != nil {
		return clean(err)
	}
	for _, a := range to {
		if err := cl.Rcpt(a); err != nil {
			return clean(err)
		}
	}
	w, err := cl.Data()
	if err != nil {
		return clean(err)
	}
	if _, err := w.Write(msg); err != nil {
		return clean(err)
	}
	if err := w.Close(); err != nil {
		return clean(err)
	}
	_ = cl.Quit()
	return nil
}

func (d *Deliverer) tlsConfig(host string) *tls.Config {
	var cfg *tls.Config
	if d.TLSConfig != nil {
		cfg = d.TLSConfig.Clone()
	} else {
		cfg = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	cfg.ServerName = host
	return cfg
}

func localName() string { return "localhost" }

// isTemporarySMTP treats 4xx answers and network errors as worth retrying.
func isTemporarySMTP(err error) bool {
	var pe *textproto.Error
	if errors.As(err, &pe) {
		return pe.Code >= 400 && pe.Code < 500
	}
	var ne net.Error
	return errors.As(err, &ne) || errors.Is(err, io.EOF)
}

func shortNetErr(err error) string {
	var oe *net.OpError
	if errors.As(err, &oe) && oe.Err != nil {
		return oe.Err.Error()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timed out"
	}
	return err.Error()
}

// buildMail makes the RFC 5322 message: multipart/alternative with a plain
// text and an HTML part, both quoted-printable UTF-8.
func (d *Deliverer) buildMail(from *mail.Address, to []string, r Rendered) ([]byte, error) {
	var b bytes.Buffer
	boundary := make([]byte, 12)
	if _, err := rand.Read(boundary); err != nil {
		return nil, err
	}
	bd := "e5x-" + hex.EncodeToString(boundary)
	msgID := hex.EncodeToString(boundary)
	domain := "localhost"
	if i := strings.LastIndexByte(from.Address, '@'); i >= 0 {
		domain = from.Address[i+1:]
	}
	var tos []string
	for _, a := range to {
		pa, err := mail.ParseAddress(a)
		if err != nil {
			return nil, err
		}
		tos = append(tos, pa.String())
	}
	subject := strings.Join(strings.Fields(r.Title), " ") // no line breaks in a header
	hdr := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	hdr("From", from.String())
	hdr("To", strings.Join(tos, ", "))
	hdr("Subject", mime.QEncoding.Encode("UTF-8", subject))
	hdr("Date", d.now().Format(time.RFC1123Z))
	hdr("Message-ID", "<"+msgID+"@"+domain+">")
	hdr("MIME-Version", "1.0")
	hdr("X-Auto-Response-Suppress", "All")
	hdr("Content-Type", `multipart/alternative; boundary="`+bd+`"`)
	b.WriteString("\r\n")

	part := func(ctype, body string) error {
		b.WriteString("--" + bd + "\r\n")
		b.WriteString("Content-Type: " + ctype + "; charset=utf-8\r\n")
		b.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")
		qw := quotedprintable.NewWriter(&b)
		if _, err := qw.Write([]byte(body)); err != nil {
			return err
		}
		if err := qw.Close(); err != nil {
			return err
		}
		b.WriteString("\r\n")
		return nil
	}
	if err := part("text/plain", r.Body); err != nil {
		return nil, err
	}
	html := r.HTML
	if html == "" {
		html = "<pre>" + strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(r.Body) + "</pre>"
	}
	if err := part("text/html", html); err != nil {
		return nil, err
	}
	b.WriteString("--" + bd + "--\r\n")
	return b.Bytes(), nil
}

// plainAuth is AUTH PLAIN without net/smtp's refusal to send a password over
// a connection that is not encrypted: choosing security "none" is the user's decision.
type plainAuth struct{ user, pass string }

func (a plainAuth) Start(*smtp.ServerInfo) (string, []byte, error) {
	return "PLAIN", []byte("\x00" + a.user + "\x00" + a.pass), nil
}
func (a plainAuth) Next(_ []byte, more bool) ([]byte, error) {
	if more {
		return nil, errors.New("unexpected server challenge")
	}
	return nil, nil
}

// loginAuth is AUTH LOGIN, which some providers offer instead of PLAIN.
type loginAuth struct{ user, pass string }

func (a loginAuth) Start(*smtp.ServerInfo) (string, []byte, error) { return "LOGIN", nil, nil }
func (a loginAuth) Next(from []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimRight(string(from), ": ")) {
	case "username":
		return []byte(a.user), nil
	case "password":
		return []byte(a.pass), nil
	}
	return nil, fmt.Errorf("unexpected server challenge")
}
