//go:build ignore

// Command gen-signing-key makes the key pair that signs the checksums of a
// release (docs/design/update.md, section 2). Run it once, as the repository owner:
//
//	go run scripts/release/gen-signing-key.go [-out e5extend-release-signing.key]
//
// It prints the PUBLIC key, which goes into internal/update/pubkey.go, and
// writes the SECRET key to a file for `gh secret set RELEASE_SIGNING_KEY < file`.
// Nothing is committed or uploaded by this program.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/KKazuhaK/e5-developer-extend/internal/update"
)

func main() {
	out := flag.String("out", "e5extend-release-signing.key", "where to write the secret key")
	flag.Parse()

	key, err := update.GenerateKey()
	if err != nil {
		fatal(err)
	}
	// O_EXCL: never overwrite a key that may be the one in use.
	f, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		fatal(err)
	}
	if _, err := f.WriteString(key.Encode()); err != nil {
		_ = f.Close()
		fatal(err)
	}
	if err := f.Close(); err != nil {
		fatal(err)
	}
	pub := key.Public().String()
	fmt.Printf(`Secret key written to %[1]s (mode 0600). Do not commit it.

Public key (minisign format; users can verify with: minisign -Vm SHA256SUMS -P %[2]s):

    %[2]s

Next steps:
  1. In internal/update/pubkey.go set
         const PublicKeyText = "%[2]s"
     (replace the line that says placeholderKey) and commit that change.
  2. Store the secret key as a repository secret, then delete the local file:
         gh secret set RELEASE_SIGNING_KEY < %[1]s
         shred -u %[1]s        (or rm; keep a copy in your password manager)
  3. Tag a release. The Release workflow signs SHA256SUMS and uploads SHA256SUMS.minisig.
`, *out, pub)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "gen-signing-key:", err)
	os.Exit(1)
}
