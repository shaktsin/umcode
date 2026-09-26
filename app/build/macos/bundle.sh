#!/usr/bin/env bash
# Build UMCode.app: the Wails shell, the engine binary and the LaunchAgent
# that runs the engine in the background. Run this on macOS.
#
#   app/build/macos/bundle.sh [--universal] [--sign "Developer ID Application: …"]
#
# Without --sign the bundle is ad-hoc signed, which is enough to run it
# locally; notarization needs a real Developer ID certificate.
set -euo pipefail

cd "$(dirname "$0")/../../.."   # repo root
REPO="$PWD"
APP_DIR="$REPO/app"
OUT="${UMCODE_APP_OUT:-$REPO/bin/UMCode.app}"
VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
BUILD_ID="$(uuidgen | tr -d '-')"
UNIVERSAL=0
SIGN_ID=""

while [[ $# -gt 0 ]]; do
    case "$1" in
        --universal) UNIVERSAL=1; shift ;;
        --sign) SIGN_ID="$2"; shift 2 ;;
        *) echo "unknown option: $1" >&2; exit 2 ;;
    esac
done

if [[ "$(uname -s)" != "Darwin" ]]; then
    echo "This builds a macOS app bundle; run it on a Mac." >&2
    exit 1
fi

echo "==> Frontend"
(cd "$APP_DIR/frontend" && npm install --silent && npm run build --silent)

ENGINE_LDFLAGS="-s -w -X github.com/shaktsin/umcode/internal/version.Version=$VERSION -X github.com/shaktsin/umcode/internal/version.Commit=$COMMIT -X github.com/shaktsin/umcode/internal/version.BuildID=$BUILD_ID"
APP_LDFLAGS="-s -w -X main.Version=$VERSION -X main.BuildID=$BUILD_ID"

build_engine() { # $1=arch $2=out
    GOOS=darwin GOARCH="$1" CGO_ENABLED=1 go build -trimpath -ldflags "$ENGINE_LDFLAGS" -o "$2" ./cmd/umcode
}
build_app() { # $1=arch $2=out
    (cd "$APP_DIR" && GOOS=darwin GOARCH="$1" CGO_ENABLED=1 go build -trimpath -tags production \
        -ldflags "$APP_LDFLAGS" -o "$2" .)
}
build_computer_helper() { # $1=arch $2=out
    local clang_arch="$1"
    [[ "$clang_arch" == "amd64" ]] && clang_arch="x86_64"
    xcrun clang -arch "$clang_arch" -fobjc-arc -mmacosx-version-min=14.0 \
        -framework AppKit -framework ApplicationServices -framework CoreGraphics -framework ImageIO \
        "$APP_DIR/build/macos/computer-use/main.m" -o "$2"
}

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "==> Engine and app binaries ($( ((UNIVERSAL)) && echo "arm64 + amd64" || echo "$(uname -m)" ))"
if ((UNIVERSAL)); then
    build_engine arm64 "$TMP/engine-arm64"
    build_engine amd64 "$TMP/engine-amd64"
    lipo -create -output "$TMP/engine-bin" "$TMP/engine-arm64" "$TMP/engine-amd64"
    build_app arm64 "$TMP/app-arm64"
    build_app amd64 "$TMP/app-amd64"
    lipo -create -output "$TMP/app-bin" "$TMP/app-arm64" "$TMP/app-amd64"
    build_computer_helper arm64 "$TMP/computer-arm64"
    build_computer_helper amd64 "$TMP/computer-amd64"
    lipo -create -output "$TMP/computer-bin" "$TMP/computer-arm64" "$TMP/computer-amd64"
else
    ARCH="$(uname -m)"; [[ "$ARCH" == "x86_64" ]] && ARCH=amd64 || ARCH=arm64
    # Never use names that differ only by case here. The default macOS file
    # system is case-insensitive, so the app and engine executables need distinct names.
    build_engine "$ARCH" "$TMP/engine-bin"
    build_app "$ARCH" "$TMP/app-bin"
    build_computer_helper "$ARCH" "$TMP/computer-bin"
fi

echo "==> Bundle"
rm -rf "$OUT"
mkdir -p "$OUT/Contents/MacOS" "$OUT/Contents/Resources" "$OUT/Contents/Library/LaunchAgents"
cp "$TMP/app-bin" "$OUT/Contents/MacOS/UMCode"
# The engine goes in Resources, not next to the app binary: the Mac's file
# system ignores case, so Contents/MacOS/UMCode and Contents/MacOS/umcode would
# be the same file and the engine would overwrite the app.
cp "$TMP/engine-bin" "$OUT/Contents/Resources/umcode"
# Computer Use is a separate helper bundle so macOS grants Screen Recording
# and Accessibility access to the smallest component that needs it.
COMPUTER_APP="$OUT/Contents/Resources/computer/UMCode Computer Use.app"
mkdir -p "$COMPUTER_APP/Contents/MacOS"
cp "$TMP/computer-bin" "$COMPUTER_APP/Contents/MacOS/UMCode Computer Use"
sed "s/__VERSION__/${VERSION#v}/g" "$APP_DIR/build/macos/computer-use/Info.plist" > "$COMPUTER_APP/Contents/Info.plist"
printf 'APPL????' > "$COMPUTER_APP/Contents/PkgInfo"
# Release builds may stage UMCode's private libkrun bridge and its runtime
# assets here. This is an app-internal executable, not a user-facing CLI.
if [[ -n "${UMCODE_LIBKRUN_BUNDLE:-}" ]]; then
    if [[ ! -x "$UMCODE_LIBKRUN_BUNDLE/umcode-compute" ]]; then
        echo "UMCODE_LIBKRUN_BUNDLE must contain an executable umcode-compute bridge." >&2
        exit 1
    fi
    mkdir -p "$OUT/Contents/Resources/compute"
    cp -Rp "$UMCODE_LIBKRUN_BUNDLE/." "$OUT/Contents/Resources/compute/"
else
    echo "NOTICE: no libkrun runtime staged; isolated compute will be unavailable in this app build."
fi
# Visual QA is a first-party opt-in plugin. Release builders stage a pinned,
# signed Chromium.app here; it is never replaced by the user's browser profile.
if [[ -n "${UMCODE_CHROMIUM_BUNDLE:-}" ]]; then
	BROWSER_EXECUTABLE="$UMCODE_CHROMIUM_BUNDLE/Contents/MacOS/Chromium"
	[[ -x "$BROWSER_EXECUTABLE" ]] || BROWSER_EXECUTABLE="$UMCODE_CHROMIUM_BUNDLE/Contents/MacOS/Google Chrome"
    if [[ ! -x "$BROWSER_EXECUTABLE" ]]; then
        echo "UMCODE_CHROMIUM_BUNDLE must point to a Chromium-compatible .app with a Chromium or Google Chrome executable." >&2
        exit 1
    fi
    mkdir -p "$OUT/Contents/Resources/browser"
    cp -Rp "$UMCODE_CHROMIUM_BUNDLE" "$OUT/Contents/Resources/browser/Chromium.app"
else
    echo "NOTICE: no Chromium runtime staged; the opt-in Visual QA plugin will report not run."
fi
sed "s/__VERSION__/${VERSION#v}/g" "$APP_DIR/build/macos/Info.plist" > "$OUT/Contents/Info.plist"
cp "$APP_DIR/build/macos/com.umcode.engine.plist" "$OUT/Contents/Library/LaunchAgents/"
printf 'APPL????' > "$OUT/Contents/PkgInfo"

# sips can write a valid .icns directly. This is also more portable across
# macOS releases: some iconutil versions reject otherwise canonical iconsets.
if command -v sips >/dev/null; then
    sips -s format icns "$APP_DIR/icons/appicon.png" --out "$OUT/Contents/Resources/appicon.icns" >/dev/null
fi

# Guard against the case-insensitivity trap coming back.
if ! cmp -s "$TMP/app-bin" "$OUT/Contents/MacOS/UMCode"; then
    echo "The app binary in the bundle is not the app; check where the engine was copied." >&2
    exit 1
fi
if cmp -s "$OUT/Contents/MacOS/UMCode" "$OUT/Contents/Resources/umcode"; then
    echo "The app and engine binaries are identical; the bundle is invalid." >&2
    exit 1
fi
if ! "$OUT/Contents/Resources/umcode" version | grep -q '^umcode '; then
    echo "The bundled engine does not identify itself as the umcode CLI." >&2
    exit 1
fi

echo "==> Signing"
if [[ -n "$SIGN_ID" ]]; then
    if [[ -x "$OUT/Contents/Resources/compute/umcode-compute" ]]; then
        # The private bridge is the process that creates the VM and therefore
        # must carry Apple's hypervisor entitlement in signed distribution builds.
        while IFS= read -r -d '' library; do
            codesign --force --options runtime --timestamp --sign "$SIGN_ID" "$library"
        done < <(find "$OUT/Contents/Resources/compute" \( -type f -o -type d \) \( -name '*.dylib' -o -name '*.framework' \) -print0)
        codesign --force --options runtime --timestamp --entitlements "$APP_DIR/build/macos/hypervisor.entitlements" \
            --sign "$SIGN_ID" "$OUT/Contents/Resources/compute/umcode-compute"
    fi
    codesign --force --options runtime --timestamp --sign "$SIGN_ID" "$COMPUTER_APP"
    codesign --force --options runtime --timestamp --sign "$SIGN_ID" "$OUT/Contents/Resources/umcode"
    codesign --force --options runtime --timestamp --sign "$SIGN_ID" "$OUT"
    echo "Signed with: $SIGN_ID"
    echo "Notarize with: xcrun notarytool submit --keychain-profile <profile> --wait <zip> && xcrun stapler staple '$OUT'"
else
    codesign --force --sign - "$COMPUTER_APP"
    codesign --force --deep --sign - "$OUT"
    echo "Ad-hoc signed. SMAppService login items need a Developer ID signature to register."
fi

if [[ -x "$OUT/Contents/Resources/compute/umcode-compute" ]]; then
    # Re-apply the VM entitlement after outer-bundle signing. `--deep` in the
    # ad-hoc path above otherwise re-signs nested code without this entitlement.
    if [[ -n "$SIGN_ID" ]]; then
        while IFS= read -r -d '' library; do
            codesign --force --options runtime --timestamp --sign "$SIGN_ID" "$library"
        done < <(find "$OUT/Contents/Resources/compute" -type f -name '*.dylib' -print0)
        codesign --force --options runtime --timestamp --entitlements "$APP_DIR/build/macos/hypervisor.entitlements" \
            --sign "$SIGN_ID" "$OUT/Contents/Resources/compute/umcode-compute"
        codesign --force --options runtime --timestamp --sign "$SIGN_ID" "$OUT"
    else
        while IFS= read -r -d '' library; do
            codesign --force --sign - "$library"
        done < <(find "$OUT/Contents/Resources/compute" -type f -name '*.dylib' -print0)
        codesign --force --entitlements "$APP_DIR/build/macos/hypervisor.entitlements" --sign - \
            "$OUT/Contents/Resources/compute/umcode-compute"
        codesign --force --sign - "$OUT"
    fi
fi

echo "==> Done: $OUT"
