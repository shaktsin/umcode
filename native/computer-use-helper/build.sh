#!/usr/bin/env bash
# Builds UMCodeComputerUse and assembles it into a .app bundle at the exact
# path driver_darwin.go expects (computerHelperPath()):
#   bin/UMCode.app/Contents/Resources/computer/UMCode Computer Use.app
#
# Usage:
#   ./build.sh                 # debug build, ad-hoc signed, for local testing
#   ./build.sh --release        # release build
#   SIGN_IDENTITY="Developer ID Application: Your Name (TEAMID)" ./build.sh --release --sign
#
# Ad-hoc signing (the default) is enough to test locally: macOS will still
# prompt for Accessibility and Screen Recording and remember the grant, but
# an ad-hoc identity is not stable across rebuilds, and macOS may treat a
# rebuilt binary as "a new app" and ask again. For anything you'll actually
# ship, sign with your real Developer ID and notarize it, the same as the
# rest of UMCode.app.
set -euo pipefail
cd "$(dirname "$0")"

CONFIG="debug"
DO_SIGN="adhoc"
for arg in "$@"; do
  case "$arg" in
    --release) CONFIG="release" ;;
    --sign) DO_SIGN="identity" ;;
  esac
done

echo "==> swift build ($CONFIG)"
swift build -c "$CONFIG"

BIN_PATH=".build/$CONFIG/UMCodeComputerUse"
if [ ! -f "$BIN_PATH" ]; then
  echo "build output not found at $BIN_PATH" >&2
  exit 1
fi

OUT_APP="../../bin/UMCode.app/Contents/Resources/computer/UMCode Computer Use.app"
echo "==> assembling $OUT_APP"
rm -rf "$OUT_APP"
mkdir -p "$OUT_APP/Contents/MacOS"
cp Info.plist "$OUT_APP/Contents/Info.plist"
cp "$BIN_PATH" "$OUT_APP/Contents/MacOS/UMCode Computer Use"
chmod +x "$OUT_APP/Contents/MacOS/UMCode Computer Use"

if [ "$DO_SIGN" = "identity" ]; then
  if [ -z "${SIGN_IDENTITY:-}" ]; then
    echo "SIGN_IDENTITY is not set; export it or drop --sign to ad-hoc sign instead" >&2
    exit 1
  fi
  echo "==> codesign (--sign \"$SIGN_IDENTITY\", hardened runtime)"
  codesign --force --deep --options runtime --timestamp \
    --sign "$SIGN_IDENTITY" "$OUT_APP"
else
  echo "==> codesign (ad-hoc, local testing only)"
  codesign --force --deep --sign - "$OUT_APP"
fi

echo "==> done: $OUT_APP"
echo "First run will prompt for Accessibility and Screen Recording — grant"
echo "both to this exact bundle. 'System Settings > Privacy & Security' if"
echo "it does not prompt automatically."
