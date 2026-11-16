# shellcheck shell=bash
# Sourced by the other scripts in this directory (bash). Sets:
#   ROOT, DIST                 repository root and output directory (dist/)
#   VERSION                    1.2.3 from a v1.2.3 tag, else 0.0.0-dev.g<commit>
#                              (the "g" keeps it valid semver when the hash is all digits)
#   NUM_VERSION                VERSION without a pre-release part (Windows file versions)
#   PKG_VERSION                VERSION as deb/rpm write it ("-" of a pre-release becomes "~")
#   LDFLAGS                    -s -w and the version variables
#   SOURCE_DATE_EPOCH          the commit time, for reproducible archives
# and defines build_cli and make_syso.
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$ROOT"
DIST=${DIST:-$ROOT/dist}
mkdir -p "$DIST"

if [ -z "${VERSION:-}" ]; then
	if [ "${GITHUB_REF_TYPE:-}" = tag ]; then
		VERSION=${GITHUB_REF_NAME#v}
	else
		VERSION=0.0.0-dev.g$(git rev-parse --short=7 HEAD)
	fi
fi
if ! [[ $VERSION =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]]; then
	echo "version $VERSION is not MAJOR.MINOR.PATCH[-PRERELEASE]" >&2
	exit 1
fi
NUM_VERSION=${VERSION%%-*}
PKG_VERSION=${VERSION/-/\~}
COMMIT=$(git rev-parse --short=12 HEAD)
DATE=$(git log -1 --format=%cI)
SOURCE_DATE_EPOCH=$(git log -1 --format=%ct)
export VERSION NUM_VERSION PKG_VERSION SOURCE_DATE_EPOCH
VPKG=github.com/KKazuhaK/e5-renew/internal/version
LDFLAGS="-s -w -X $VPKG.Version=$VERSION -X $VPKG.Commit=$COMMIT -X $VPKG.Date=$DATE"

WAILS3=${WAILS3:-wails3}

# make_syso <arch> <package dir> <description>: puts the icon, version
# information and manifest into the next Windows build of that package. The
# file is removed by clean_syso (call it from a trap).
make_syso() {
	local arch=$1 dir=$2 desc=$3 info
	info=$(mktemp)
	cat >"$info" <<JSON
{
	"fixed": {"file_version": "$NUM_VERSION.0", "product_version": "$NUM_VERSION.0"},
	"info": {"0409": {
		"ProductName": "E5 Renew",
		"FileDescription": "$desc",
		"CompanyName": "KKazuhaK",
		"LegalCopyright": "MIT License",
		"ProductVersion": "$VERSION",
		"FileVersion": "$VERSION"
	}}
}
JSON
	"$WAILS3" generate syso -arch "$arch" -icon packaging/icons/icon.ico \
		-manifest packaging/windows/app.manifest -info "$info" \
		-out "$dir/rsrc_windows_$arch.syso" >/dev/null
	rm -f "$info"
}
clean_syso() { rm -f cmd/e5renew/rsrc_windows_*.syso gui/rsrc_windows_*.syso; }

# build_cli <goos> <goarch> <out dir>: the command-line program, plus the
# console-less e5renew-bg.exe on Windows.
build_cli() {
	local goos=$1 goarch=$2 out=$3
	mkdir -p "$out"
	if [ "$goos" = windows ]; then
		make_syso "$goarch" cmd/e5renew "E5 Renew command line"
		GOOS=$goos GOARCH=$goarch CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o "$out/e5renew.exe" ./cmd/e5renew
		GOOS=$goos GOARCH=$goarch CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS -H=windowsgui" -o "$out/e5renew-bg.exe" ./cmd/e5renew
		clean_syso
	else
		GOOS=$goos GOARCH=$goarch CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o "$out/e5renew" ./cmd/e5renew
	fi
}

# build_frontend: bindings and the web assets, once per job.
build_frontend() {
	"$WAILS3" generate bindings -d gui/frontend/bindings -ts ./gui >/dev/null
	(cd gui/frontend && npm ci --no-audit --no-fund && VITE_APP_VERSION=$VERSION npm run build)
	test -f gui/frontend/dist/index.html
}

# build_gui <goos> <goarch> <out dir>: the desktop app with the real frontend.
# Linux and macOS need CGO for the target, so build them on that system.
build_gui() {
	local goos=$1 goarch=$2 out=$3 ldflags=$LDFLAGS exe=e5renew-gui
	mkdir -p "$out"
	if [ "$goos" = windows ]; then
		make_syso "$goarch" gui "E5 Renew"
		ldflags="$ldflags -H=windowsgui"
		exe=e5renew-gui.exe
	fi
	GOOS=$goos GOARCH=$goarch CGO_ENABLED=$([ "$goos" = windows ] && echo 0 || echo 1) \
		go build -trimpath -tags frontend -ldflags "$ldflags" -o "$out/$exe" ./gui
	clean_syso
}

# plain_names <dir>: replaces "~" in package file names (a pre-release such as
# e5renew_1.2.0~rc.1-1_amd64.deb) with "-". GitHub renames release assets with
# unusual characters, which would break SHA256SUMS. Only the file name changes;
# the version inside the package keeps the "~" so it sorts before the release.
plain_names() {
	local f
	for f in "$1"/*~*; do
		[ -e "$f" ] || continue
		mv -- "$f" "${f//\~/-}"
	done
}

# tar_gz <archive> <dir> <files...>: a reproducible tar.gz of files in dir
# (GNU tar; the Linux jobs use it).
tar_gz() {
	local archive=$1 dir=$2
	shift 2
	tar --sort=name --mtime="@$SOURCE_DATE_EPOCH" --owner=0 --group=0 --numeric-owner \
		-C "$dir" -cf - "$@" | gzip -n -9 >"$archive"
}
