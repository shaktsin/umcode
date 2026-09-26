#!/usr/bin/env bash
# Build a portable Linux desktop folder (not a user-facing CLI).
# Build and run on native Linux; Wails links to the host GTK/WebKit runtime.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$SCRIPT_DIR/../../.." && pwd)"
APP_DIR="$REPO/app"
ARCH="$(uname -m)"
case "$ARCH" in
    x86_64) GOARCH=amd64 ;;
    aarch64|arm64) GOARCH=arm64 ;;
    *) echo "Unsupported Linux architecture: $ARCH" >&2; exit 2 ;;
esac

OUT="${UMCODE_APP_OUT:-${UMCODE_APP_OUT:-$REPO/dist/UMCode-linux-$GOARCH}}"
RUNTIME="${UMCODE_LIBKRUN_BUNDLE:-}"
if [[ -z "$RUNTIME" || ! -x "$RUNTIME/umcode-compute" || ! -d "$RUNTIME/rootfs" ]]; then
    echo "Set UMCODE_LIBKRUN_BUNDLE to a staged Linux private runtime bundle." >&2
    exit 2
fi
if [[ -e "$OUT" ]]; then
    echo "Refusing to overwrite existing app package: $OUT" >&2
    exit 2
fi
mkdir -p "$(dirname "$OUT")"
for tool in go npm pkg-config; do
    command -v "$tool" >/dev/null || { echo "Missing build dependency: $tool" >&2; exit 2; }
done
for pkg in gtk4 webkitgtk-6.0; do
    pkg-config --exists "$pkg" || {
        echo "Missing Wails Linux development dependency: $pkg" >&2
        exit 2
    }
done
if ! pkg-config --atleast-version=4.14 gtk4; then
    echo "GTK 4.14 or newer is required by Wails v3; found $(pkg-config --modversion gtk4)." >&2
    exit 2
fi

VERSION="$(git -C "$REPO" describe --tags --always --dirty 2>/dev/null || echo dev)"
COMMIT="$(git -C "$REPO" rev-parse --short HEAD 2>/dev/null || echo unknown)"
ENGINE_LDFLAGS="-s -w -X github.com/shaktsin/umcode/internal/version.Version=$VERSION -X github.com/shaktsin/umcode/internal/version.Commit=$COMMIT"
APP_LDFLAGS="-s -w -X main.Version=$VERSION"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "==> Frontend"
(cd "$APP_DIR/frontend" && npm install --silent && npm run build --silent)
echo "==> Linux app and engine ($GOARCH)"
(cd "$REPO" && CGO_ENABLED=1 GOOS=linux GOARCH="$GOARCH" go build -trimpath \
    -ldflags "$ENGINE_LDFLAGS" -o "$TMP/umcode-engine" ./cmd/umcode)
(cd "$APP_DIR" && CGO_ENABLED=1 GOOS=linux GOARCH="$GOARCH" go build -trimpath \
    -tags production -ldflags "$APP_LDFLAGS" -o "$TMP/UMCode" .)

mkdir -p "$OUT/compute"
install -m 755 "$TMP/UMCode" "$OUT/UMCode"
install -m 755 "$TMP/umcode-engine" "$OUT/umcode"
cp -R "$RUNTIME/." "$OUT/compute/"
install -m 755 "$SCRIPT_DIR/umcode-launch" "$OUT/umcode-launch"
install -m 755 "$SCRIPT_DIR/install-desktop.sh" "$OUT/install-desktop.sh"
install -m 644 "$APP_DIR/icons/appicon.png" "$OUT/umcode.png"
install -m 644 "$SCRIPT_DIR/umcode.desktop" "$OUT/umcode.desktop"
install -m 644 "$SCRIPT_DIR/README.txt" "$OUT/README.txt"

"$OUT/umcode" version | grep -q '^umcode ' || {
    echo "Bundled engine smoke check failed." >&2
    exit 1
}
test -x "$OUT/compute/umcode-compute"
test -s "$OUT/compute/lib/libkrun.so.1"
test -s "$OUT/compute/lib/libkrunfw.so.5"
echo "==> Portable app folder ready: $OUT"
du -sh "$OUT"
