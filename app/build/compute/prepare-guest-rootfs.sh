#!/usr/bin/env bash
# Turn the pinned Alpine minirootfs into UMCode's coding guest image. Docker is
# used only at build time so packages can be installed for the target Linux
# architecture from macOS or Linux; the shipped app does not require Docker.
set -euo pipefail

ARCHIVE="${UMCODE_GUEST_ROOTFS_ARCHIVE:-}"
OUT="${UMCODE_GUEST_ROOTFS_OUT:-}"
ARCH="${UMCODE_GUEST_ARCH:-$(uname -m)}"

if [[ -z "$ARCHIVE" || ! -f "$ARCHIVE" ]]; then
    echo "Set UMCODE_GUEST_ROOTFS_ARCHIVE to the pinned Alpine minirootfs archive." >&2
    exit 2
fi
if [[ -z "$OUT" || "$OUT" != /* ]]; then
    echo "Set UMCODE_GUEST_ROOTFS_OUT to a new absolute output directory." >&2
    exit 2
fi
if [[ -e "$OUT" ]]; then
    echo "Refusing to overwrite guest rootfs: $OUT" >&2
    exit 2
fi
if ! command -v docker >/dev/null; then
    echo "Docker is required to assemble the cross-platform coding guest image." >&2
    exit 2
fi
if ! command -v python3 >/dev/null; then
    echo "Python 3 is required to normalize guest symlinks for app signing." >&2
    exit 2
fi

case "$ARCH" in
    arm64|aarch64) PLATFORM="linux/arm64" ;;
    amd64|x86_64) PLATFORM="linux/amd64" ;;
    *) echo "Unsupported guest architecture: $ARCH" >&2; exit 2 ;;
esac

suffix="$$-${RANDOM}"
image="umcode-guest-base:$suffix"
container="umcode-guest-build-$suffix"
cleanup() {
    docker rm -f "$container" >/dev/null 2>&1 || true
    docker image rm "$image" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "==> Importing pinned Alpine rootfs for $PLATFORM"
docker import --platform "$PLATFORM" "$ARCHIVE" "$image" >/dev/null

packages=(
    bash build-base ca-certificates coreutils curl findutils git go
    nodejs npm openssh-client python3 py3-pip ripgrep
)
if [[ "${UMCODE_GUEST_BROWSER:-1}" == 1 ]]; then
    packages+=(chromium)
fi

echo "==> Installing coding and verification toolchains"
docker create --platform "$PLATFORM" --name "$container" "$image" /bin/sh -lc \
    "apk add --no-cache ${packages[*]} && update-ca-certificates && printf '%s\n' '${packages[*]}' > /etc/umcode-packages" >/dev/null
docker start -a "$container"

mkdir -p "$OUT"
docker export "$container" | tar -xf - -C "$OUT"

# Docker export can preserve a restrictive temporary-directory mode from the
# build container. Guest tools run without host privileges and Chromium needs
# a conventional world-writable sticky temp directory for shared memory.
mkdir -p "$OUT/tmp" "$OUT/var/tmp"
chmod 1777 "$OUT/tmp" "$OUT/var/tmp"

# Alpine uses absolute links such as /bin/sh -> /bin/busybox. They are valid in
# the guest but look broken to macOS codesign because it resolves them against
# the host root. Relative links preserve the same guest target and let the app
# bundle's sealed-resource verification succeed.
python3 - "$OUT" <<'PY'
import os
import sys

root = os.path.realpath(sys.argv[1])
for directory, subdirs, files in os.walk(root, followlinks=False):
    for name in [*subdirs, *files]:
        path = os.path.join(directory, name)
        if not os.path.islink(path):
            continue
        target = os.readlink(path)
        if not target.startswith("/"):
            continue
        guest_target = os.path.join(root, target.lstrip("/"))
        relative = os.path.relpath(guest_target, directory)
        os.unlink(path)
        os.symlink(relative, path)
PY

# /etc/mtab points at /proc/mounts. The kernel mounts procfs over this empty
# placeholder in the guest; the placeholder only keeps the signed app bundle
# free of broken resource symlinks before boot.
mkdir -p "$OUT/proc"
: > "$OUT/proc/mounts"

for required in bin/sh bin/mount usr/bin/git usr/bin/go usr/bin/node usr/bin/npm usr/bin/python3 usr/bin/rg; do
    if [[ ! -e "$OUT/$required" && ! -L "$OUT/$required" ]]; then
        echo "Coding guest is missing $required." >&2
        exit 1
    fi
done

echo "==> Coding guest ready: $OUT ($(du -sh "$OUT" | awk '{print $1}'))"
