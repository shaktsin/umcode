#!/usr/bin/env bash
# Build the app-private libkrun bridge. libkrun itself and a prepared guest
# rootfs must be built/staged for the target host before calling this script.
set -euo pipefail

REPO="$(cd "$(dirname "$0")/../../.." && pwd)"
PREFIX="${UMCODE_LIBKRUN_PREFIX:-}"
GUEST_ROOTFS="${UMCODE_GUEST_ROOTFS:-}"
OUT="${UMCODE_LIBKRUN_BUNDLE_OUT:-}"

if [[ -z "$PREFIX" || ! -f "$PREFIX/include/libkrun.h" ]]; then
    echo "Set UMCODE_LIBKRUN_PREFIX to a staged libkrun/libkrunfw install prefix." >&2
    exit 2
fi
if [[ -z "$GUEST_ROOTFS" || ! -d "$GUEST_ROOTFS" || ( ! -e "$GUEST_ROOTFS/bin/sh" && ! -L "$GUEST_ROOTFS/bin/sh" ) || ( ! -e "$GUEST_ROOTFS/bin/mount" && ! -L "$GUEST_ROOTFS/bin/mount" ) ]]; then
    echo "Set UMCODE_GUEST_ROOTFS to a Linux rootfs containing /bin/sh and /bin/mount." >&2
    exit 2
fi
if [[ -z "$OUT" ]]; then
    echo "Set UMCODE_LIBKRUN_BUNDLE_OUT to an empty staging directory." >&2
    exit 2
fi
if [[ -e "$OUT" ]]; then
    echo "Refusing to overwrite runtime stage: $OUT" >&2
    exit 2
fi
if [[ "$(uname -s)" != "Darwin" && "$(uname -s)" != "Linux" ]]; then
    echo "libkrun desktop bridge supports macOS and Linux builds only." >&2
    exit 2
fi
case "$(uname -s)" in
    Darwin) FW_NAME="libkrunfw.5.dylib" ;;
    Linux) FW_NAME="libkrunfw.so.5" ;;
esac
if [[ ! -f "$PREFIX/lib/$FW_NAME" && ! -f "$PREFIX/lib64/$FW_NAME" ]]; then
    echo "The staged runtime is missing $FW_NAME; include the matching libkrunfw kernel bundle." >&2
    exit 2
fi

mkdir -p "$OUT/lib"
if [[ -d "$PREFIX/lib" ]]; then
    cp -R "$PREFIX/lib/." "$OUT/lib/"
elif [[ -d "$PREFIX/lib64" ]]; then
    cp -R "$PREFIX/lib64/." "$OUT/lib/"
else
    echo "No lib or lib64 directory found in $PREFIX." >&2
    exit 2
fi
if [[ "$(uname -s)" == "Darwin" ]]; then
    cp -cRp "$GUEST_ROOTFS" "$OUT/rootfs"
else
    cp -Rp --reflink=auto "$GUEST_ROOTFS" "$OUT/rootfs"
fi

if [[ "$(uname -s)" == "Darwin" ]]; then
    # Upstream's macOS libkrun install name is a bare `libkrun.1.dylib`.
    # Give it an app-relative runpath so the bridge loads only the bundled copy.
    KRUN_DYLIB="$(find "$OUT/lib" -maxdepth 1 -name 'libkrun.*.dylib' -type f | head -n1)"
    if [[ -z "$KRUN_DYLIB" ]]; then
        echo "No versioned libkrun dylib found in $OUT/lib." >&2
        exit 2
    fi
    install_name_tool -id '@rpath/libkrun.1.dylib' "$KRUN_DYLIB"
    export CGO_CFLAGS="${CGO_CFLAGS:-} -I$PREFIX/include"
    export CGO_LDFLAGS="${CGO_LDFLAGS:-} -L$OUT/lib -Wl,-rpath,@loader_path/lib -lkrun"
else
    export CGO_CFLAGS="${CGO_CFLAGS:-} -I$PREFIX/include"
    export CGO_LDFLAGS="${CGO_LDFLAGS:-} -L$OUT/lib -Wl,-rpath,\$ORIGIN/lib -lkrun"
fi
export CGO_ENABLED=1

echo "==> Building private libkrun bridge for $(uname -s)/$(uname -m)"
(cd "$REPO" && go build -trimpath -tags=libkrun -o "$OUT/umcode-compute" ./cmd/umcode-compute)
chmod 755 "$OUT/umcode-compute"
echo "==> Staged private runtime: $OUT"
