#!/usr/bin/env bash
# Command-line archives for every platform, and the Linux deb/rpm packages of
# the command-line program. Runs on Linux; needs go, wails3 (syso, lipo), nfpm,
# zip and GNU tar. Output: dist/release/.
# shellcheck source=scripts/release/common.sh
source "$(dirname "$0")/common.sh"

OUT=$DIST/release
B=$DIST/cli
rm -rf "$B" && mkdir -p "$OUT"
trap clean_syso EXIT

for t in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
	build_cli "${t%/*}" "${t#*/}" "$B/${t%/*}_${t#*/}"
done
mkdir -p "$B/darwin_universal"
"$WAILS3" tool lipo -output "$B/darwin_universal/e5renew" \
	-input "$B/darwin_amd64/e5renew" -input "$B/darwin_arm64/e5renew" >/dev/null

for d in linux_amd64 linux_arm64 darwin_universal windows_amd64 windows_arm64; do
	cp LICENSE README.md "$B/$d/"
done
for d in linux_amd64 linux_arm64 darwin_universal; do
	tar_gz "$OUT/e5renew_${VERSION}_$d.tar.gz" "$B/$d" e5renew LICENSE README.md
done
for d in windows_amd64 windows_arm64; do
	rm -f "$OUT/e5renew_${VERSION}_$d.zip"
	(cd "$B/$d" && TZ=UTC find . -exec touch -d "@$SOURCE_DATE_EPOCH" {} + &&
		zip -q -X "$OUT/e5renew_${VERSION}_$d.zip" e5renew.exe e5renew-bg.exe LICENSE README.md)
done

# deb and rpm of the command-line program. nfpm reads dist/stage/.
for arch in amd64 arm64; do
	rm -rf "$DIST/stage" && mkdir -p "$DIST/stage"
	cp "$B/linux_$arch/e5renew" "$DIST/stage/"
	for fmt in deb rpm; do
		GOARCH=$arch nfpm package -f packaging/linux/nfpm-cli.yaml -p "$fmt" -t "$OUT/" >/dev/null
	done
done
rm -rf "$DIST/stage"
plain_names "$OUT"
ls -l "$OUT"
