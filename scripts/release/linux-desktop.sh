#!/usr/bin/env bash
# Desktop deb/rpm for the architecture of this machine (the app needs CGO and
# GTK4/WebKitGTK 6.0 headers, so it is built natively). Needs go, wails3, npm
# and nfpm. Output: dist/release/.
# shellcheck source=scripts/release/common.sh
source "$(dirname "$0")/common.sh"

arch=$(go env GOARCH)
OUT=$DIST/release
mkdir -p "$OUT"
trap clean_syso EXIT

build_frontend
build_gui linux "$arch" "$DIST/gui/linux_$arch"

rm -rf "$DIST/stage" && mkdir -p "$DIST/stage"
cp "$DIST/gui/linux_$arch/e5renew-gui" "$DIST/stage/"
for fmt in deb rpm; do
	GOARCH=$arch nfpm package -f packaging/linux/nfpm-desktop.yaml -p "$fmt" -t "$OUT/" >/dev/null
done
rm -rf "$DIST/stage"
plain_names "$OUT"
ls -l "$OUT"/e5renew-desktop*
