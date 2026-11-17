package update

// PublicKeyText is the release signing public key (minisign text form, what
// `minisign -P` takes). Every release's SHA256SUMS is signed with the matching
// secret key, stored as the repository secret RELEASE_SIGNING_KEY; the app
// refuses to install anything it cannot verify with it. The pair was made with
// scripts/release/gen-signing-key.go (docs/design/update.md, section 2).
const PublicKeyText = "RWQh2HcJZFOJwdJXpwW2YFjr8hItSVKWgwOQWcMS0lbd7ZJo0I2HTZ41"

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
