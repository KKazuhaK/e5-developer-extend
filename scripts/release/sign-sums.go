//go:build ignore

// Command sign-sums signs a checksum file (SHA256SUMS) in the minisign format
// and writes <file>.minisig next to it. The secret key comes from the
// environment variable RELEASE_SIGNING_KEY (the repository secret), or from
// -key-file; it is never printed.
//
//	RELEASE_SIGNING_KEY=... go run scripts/release/sign-sums.go dist/release/SHA256SUMS
//
// The release workflow runs it only when the secret exists. After signing it
// checks the signature against the public key compiled into the app
// (internal/update/pubkey.go) and warns when they do not match, because a
// release signed with another key cannot be installed by the app.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/update"
)

func main() {
	keyFile := flag.String("key-file", "", "read the secret key from this file instead of $RELEASE_SIGNING_KEY")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: sign-sums [-key-file file] <file to sign>")
		os.Exit(2)
	}
	target := flag.Arg(0)

	text := os.Getenv("RELEASE_SIGNING_KEY")
	if *keyFile != "" {
		b, err := os.ReadFile(*keyFile)
		if err != nil {
			fatal(err)
		}
		text = string(b)
	}
	if text == "" {
		fatal(fmt.Errorf("no signing key: set RELEASE_SIGNING_KEY or use -key-file"))
	}
	key, err := update.ParseSecretKey(text)
	if err != nil {
		fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		fatal(err)
	}
	comment := fmt.Sprintf("timestamp:%d\tfile:%s", time.Now().Unix(), filepath.Base(target))
	sig := key.Sign(data, comment)
	if err := os.WriteFile(target+".minisig", sig, 0o644); err != nil {
		fatal(err)
	}
	fmt.Printf("signed %s -> %s.minisig (key %s)\n", target, filepath.Base(target), key.Public())

	switch {
	case !update.HasPublicKey(update.PublicKeyText):
		fmt.Println("::warning::internal/update/pubkey.go still has the placeholder key; the app cannot verify this signature until the public key above is put there")
	case update.PublicKeyText != key.Public().String():
		fmt.Println("::warning::the signing key does not match internal/update/pubkey.go; the app will refuse to install this release")
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "sign-sums:", err)
	os.Exit(1)
}
