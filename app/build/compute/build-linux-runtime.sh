#!/usr/bin/env bash
# Build and stage an app-private Linux libkrun runtime on a native Linux host.
# The script is a release/build helper; it does not install anything system-wide.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$SCRIPT_DIR/../../.." && pwd)"
# shellcheck disable=SC1091
source "$SCRIPT_DIR/sources.lock"

OUT="${UMCODE_LIBKRUN_BUNDLE_OUT:-}"
if [[ -z "$OUT" ]]; then
    echo "Set UMCODE_LIBKRUN_BUNDLE_OUT to a new runtime staging directory." >&2
    exit 2
fi
if [[ "$OUT" != /* ]]; then
    OUT="$PWD/$OUT"
fi
if [[ -e "$OUT" ]]; then
    echo "Refusing to overwrite runtime stage: $OUT" >&2
    exit 2
fi
if [[ "$(uname -s)" != Linux ]]; then
    echo "Build the Linux runtime on Linux; this script does not cross-compile." >&2
    exit 2
fi

case "$(uname -m)" in
    x86_64)
        guest_arch=x86_64
        guest_sha="$guest_rootfs_x86_64_sha256"
        ;;
    aarch64|arm64)
        guest_arch=aarch64
        guest_sha="$guest_rootfs_aarch64_sha256"
        ;;
    *)
        echo "Unsupported Linux runtime architecture: $(uname -m)" >&2
        exit 2
        ;;
esac

for tool in git make cargo curl tar sha256sum go python3 gcc bison flex xz; do
    if ! command -v "$tool" >/dev/null; then
        echo "Missing build dependency: $tool" >&2
        exit 2
    fi
done
if [[ "${CGO_ENABLED:-1}" != 1 ]]; then
    echo "The compute bridge requires CGO_ENABLED=1." >&2
    exit 2
fi
if ! python3 -c 'import elftools' >/dev/null 2>&1; then
    echo "Missing Python build dependency: pyelftools." >&2
    exit 2
fi

jobs="${UMCODE_LIBKRUN_BUILD_JOBS:-$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 4)}"
work_dir="$(mktemp -d "${TMPDIR:-/tmp}/umcode-runtime-build.XXXXXX")"
trap 'rm -rf "$work_dir"' EXIT
prefix="$work_dir/prefix/usr/local"

git clone --depth 1 --branch "$libkrun_tag" "$libkrun_url" "$work_dir/libkrun"
actual_commit="$(git -C "$work_dir/libkrun" rev-parse HEAD)"
if [[ "$actual_commit" != "$libkrun_commit" ]]; then
    echo "Unexpected libkrun commit: $actual_commit" >&2
    exit 2
fi
make -C "$work_dir/libkrun" -j"$jobs"
make -C "$work_dir/libkrun" PREFIX=/usr/local DESTDIR="$work_dir/prefix" install

git clone --depth 1 --branch "$libkrunfw_tag" "$libkrunfw_url" "$work_dir/libkrunfw"
actual_commit="$(git -C "$work_dir/libkrunfw" rev-parse HEAD)"
if [[ "$actual_commit" != "$libkrunfw_commit" ]]; then
    echo "Unexpected libkrunfw commit: $actual_commit" >&2
    exit 2
fi
if [[ -n "${UMCODE_LIBKRUNFW_KERNEL_C:-}" ]]; then
    if [[ "$guest_arch" != aarch64 ]]; then
        echo "The pinned prebuilt kernel bundle is ARM64-only; omit UMCODE_LIBKRUNFW_KERNEL_C on x86_64." >&2
        exit 2
    fi
    actual_sha="$(sha256sum "$UMCODE_LIBKRUNFW_KERNEL_C" | awk '{print $1}')"
    if [[ "$actual_sha" != "$guest_kernel_c_bundle_aarch64_sha256" ]]; then
        echo "Unexpected ARM64 kernel bundle checksum: $actual_sha" >&2
        exit 2
    fi
    cp "$UMCODE_LIBKRUNFW_KERNEL_C" "$work_dir/libkrunfw/kernel.c"
fi
# libkrunfw passes MAKEFLAGS as positional arguments into its kernel sub-make.
# Avoid `make -C` here: GNU make adds a bare `w` (print-directory) flag for -C,
# which this upstream Makefile misinterprets as a kernel target.
(cd "$work_dir/libkrunfw" && make -j"$jobs")
make -C "$work_dir/libkrunfw" PREFIX=/usr/local DESTDIR="$work_dir/prefix" install

rootfs_archive="$work_dir/alpine-minirootfs-${guest_rootfs_version}-${guest_arch}.tar.gz"
rootfs_url="https://dl-cdn.alpinelinux.org/alpine/v${guest_rootfs_version%.*}/releases/${guest_arch}/$(basename "$rootfs_archive")"
curl -fL --output "$rootfs_archive" "$rootfs_url"
printf '%s  %s\n' "$guest_sha" "$rootfs_archive" | sha256sum --check --status || {
    echo "Guest rootfs checksum mismatch: $rootfs_archive" >&2
    exit 2
}
guest_rootfs="$work_dir/guest-rootfs"
UMCODE_GUEST_ROOTFS_ARCHIVE="$rootfs_archive" UMCODE_GUEST_ROOTFS_OUT="$guest_rootfs" UMCODE_GUEST_ARCH="$guest_arch" \
    "$SCRIPT_DIR/prepare-guest-rootfs.sh"

CGO_ENABLED=1 UMCODE_LIBKRUN_PREFIX="$prefix" UMCODE_GUEST_ROOTFS="$guest_rootfs" \
    UMCODE_LIBKRUN_BUNDLE_OUT="$OUT" "$SCRIPT_DIR/build-bridge.sh"

echo "Linux runtime staged at: $OUT"
