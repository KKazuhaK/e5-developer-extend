#!/usr/bin/env bash
# E5 Renew.app (universal) in a dmg. Runs on macOS; needs go, wails3 and npm.
# Signing: ad-hoc unless MACOS_SIGN_IDENTITY names a Developer ID certificate
# in the keychain (not used yet; docs/design/release.md, section 7).
# Output: dist/release/E5-Renew_<version>_macos_universal.dmg
# shellcheck source=scripts/release/common.sh
source "$(dirname "$0")/common.sh"

OUT=$DIST/release
B=$DIST/macos
rm -rf "$B" && mkdir -p "$OUT" "$B"

build_frontend
for arch in amd64 arm64; do
	build_cli darwin "$arch" "$B/$arch"
	build_gui darwin "$arch" "$B/$arch"
done

APP="$B/E5 Renew.app"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
lipo -create -output "$APP/Contents/MacOS/e5renew-gui" "$B/amd64/e5renew-gui" "$B/arm64/e5renew-gui"
lipo -create -output "$APP/Contents/MacOS/e5renew" "$B/amd64/e5renew" "$B/arm64/e5renew"
cp packaging/icons/icons.icns "$APP/Contents/Resources/icons.icns"
sed -e "s/@VERSION@/$VERSION/g" -e "s/@NUM_VERSION@/$NUM_VERSION/g" \
	packaging/darwin/Info.plist >"$APP/Contents/Info.plist"
plutil -lint "$APP/Contents/Info.plist"

# Ad-hoc ("-") lets Apple Silicon run the app; the hardened runtime and a
# timestamp only matter for notarization, so they come with a real identity.
identity=${MACOS_SIGN_IDENTITY:--}
sign=(codesign --force -s "$identity")
if [ "$identity" != - ]; then sign+=(--options runtime --timestamp); fi
# Sign the inner program first, then the bundle (whose main program is e5renew-gui).
"${sign[@]}" "$APP/Contents/MacOS/e5renew"
"${sign[@]}" "$APP"
codesign --verify --deep --strict --verbose=2 "$APP"

stage="$B/dmg"
mkdir -p "$stage"
cp -R "$APP" "$stage/"
ln -s /Applications "$stage/Applications"
dmg="$OUT/E5-Renew_${VERSION}_macos_universal.dmg"
rm -f "$dmg"
hdiutil create -volname "E5 Renew" -srcfolder "$stage" -fs HFS+ -format UDZO -ov "$dmg" >/dev/null
ls -l "$dmg"
