package update

// PublicKeyText is the release signing public key (minisign text form, what
// `minisign -P` takes). Every release's SHA256SUMS is signed with the matching
// secret key; the app refuses to install anything it cannot verify with it.
//
// PLACEHOLDER: the repository owner replaces this once. Run
//
//	go run scripts/release/gen-signing-key.go
//
// paste the public key it prints below, and store the secret key file with
// `gh secret set RELEASE_SIGNING_KEY < file` (docs/design/update.md, section 2).
// While the placeholder is in place the app can still tell that a newer
// version exists, but it will only offer to open the release page.
const PublicKeyText = placeholderKey

// placeholderKey is the value of PublicKeyText before the owner sets a real key.
const placeholderKey = "PLACEHOLDER-run-scripts-release-gen-signing-key.go-and-paste-the-public-key-here"

// HasPublicKey reports whether text is a real public key and not the placeholder.
func HasPublicKey(text string) bool {
	if text == "" || text == placeholderKey {
		return false
	}
	_, err := ParsePublicKey(text)
	return err == nil
}
