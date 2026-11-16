package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogDropsNamesAndDetails(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{ReplaceAttr: redactAttr}))
	log.Warn("sign-in failed", "account", "work-tenant", "detail", "user@contoso.example", "err", "AADSTS: user@contoso.example", "panic", "boom for user@contoso.example", "n", 3)
	out := buf.String()
	for _, banned := range []string{"work-tenant", "contoso", "AADSTS", "boom"} {
		if strings.Contains(out, banned) {
			t.Errorf("%q in the log: %s", banned, out)
		}
	}
	if !strings.Contains(out, "n=3") {
		t.Errorf("harmless attributes must stay: %s", out)
	}
}

func TestLogRotatesBySizeWhileRunning(t *testing.T) {
	p := filepath.Join(t.TempDir(), "gui.log")
	w, err := openSizeRotating(p)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	chunk := bytes.Repeat([]byte("x"), 256<<10)
	for i := 0; i < 12; i++ { // 3 MB, no restart in between
		if _, err := w.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	fi, err := os.Stat(p)
	if err != nil || fi.Size() > maxLog {
		t.Fatalf("log is %d bytes (err %v)", fi.Size(), err)
	}
	if _, err := os.Stat(p + ".1"); err != nil {
		t.Fatal("no older generation")
	}
}

func TestPageFromArgs(t *testing.T) {
	for args, want := range map[string]string{
		"--window --page health": "health", "--window --page=logs": "logs", "--page nope": "",
		"--page": "", "--window": "", "--page ../x": "", "-page settings": "settings",
	} {
		if got := pageFromArgs(strings.Fields(args)); got != want {
			t.Errorf("%q: %q, want %q", args, got, want)
		}
	}
}

func TestWindowURLOnlyAcceptsKnownPages(t *testing.T) {
	if got := windowURL("health"); got != "/?page=health" {
		t.Errorf("health: %q", got)
	}
	for _, bad := range []string{"", "nope", "../x", "health&x=1"} {
		if got := windowURL(bad); got != "/" {
			t.Errorf("%q: %q", bad, got)
		}
	}
}
