#!/usr/bin/env bash
# NSIS installers for amd64 and arm64. The Windows app needs no CGO, so this
# also runs on Linux (with the nsis package); CI runs it on Windows so the
# installer can be tested there. Needs go, wails3, npm and makensis.
# Output: dist/release/E5-Developer-Extend_<version>_windows_<arch>_setup.exe
# shellcheck source=scripts/release/common.sh
source "$(dirname "$0")/common.sh"

OUT=$DIST/release
B=$DIST/windows
rm -rf "$B" && mkdir -p "$OUT" "$B"
trap clean_syso EXIT

# makensis on Windows wants native paths (Git Bash has cygpath).
native() { if command -v cygpath >/dev/null; then cygpath -w "$1"; else echo "$1"; fi; }

build_frontend
for arch in amd64 arm64; do
	src=$B/$arch
	build_cli windows "$arch" "$src"
	build_gui windows "$arch" "$src"
	cp LICENSE "$src/LICENSE.txt"
	"$WAILS3" generate webview2bootstrapper -dir "$src" >/dev/null
	test -f "$src/MicrosoftEdgeWebview2Setup.exe"
	out="$OUT/E5-Developer-Extend_${VERSION}_windows_${arch}_setup.exe"
	makensis -V2 -INPUTCHARSET UTF8 \
		"-DVERSION=$NUM_VERSION" "-DDISPLAY_VERSION=$VERSION" "-DARCH=$arch" \
		"-DSRC=$(native "$src")" "-DOUTFILE=$(native "$out")" \
		"$(native packaging/windows/installer.nsi)"
done
ls -l "$OUT"/E5-Developer-Extend_*_setup.exe
