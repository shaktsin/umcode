# Bundled microVM runtime staging

UMCode's desktop app talks to a private `umcode-compute` bridge. It is an
implementation detail of the app bundle, not a supported user command. The
bridge receives one versioned JSON request on stdin (`version`, `rootfs`,
`root`, `dir`, `command`, `network`, `timeoutSeconds`, and an optional single
host/guest port mapping) and streams workload stdout/stderr to its own
stdout/stderr.

`prepare-guest-rootfs.sh` expands the checksum-pinned Alpine minirootfs into a
coding image with Git, ripgrep, Node/npm, Go, Python/pip, compilers, and CA
certificates. It uses Docker only while assembling the release artifact; the
installed UMCode app does not require Docker. Chromium is included by default
for headless browser checks. Set `UMCODE_GUEST_BROWSER=0` only for a reduced image
that intentionally does not support browser verification.

The macOS app builder accepts `UMCODE_LIBKRUN_BUNDLE=/path/to/stage` and copies that
directory to `UMCode.app/Contents/Resources/compute`. The Linux app-folder
builder accepts the same variable and copies it to `compute/` beside the app
and engine executables. Both runtime bundles contain `umcode-compute`, the
matching libkrun/libkrunfw libraries, and the pinned guest root filesystem.

Linux desktop packaging is provided by `app/build/linux/bundle.sh`. It creates
a portable app folder with the engine, private runtime, launcher, and an
optional per-user desktop-menu installer. GTK 4.14+, WebKitGTK 6.0, and KVM are
host requirements. GTK 4.14 is required by APIs used in the Wails v3 binding.
macOS distribution builds must sign the bridge with Apple's
Hypervisor entitlement; the Mac bundle script applies it when `--sign` is used.

The macOS guest smoke tests pass. Linux x86_64 and ARM64 runtimes and portable
app folders build, but guest execution still needs to be verified on native
KVM-capable Linux hosts. Do not claim cross-platform compute is fully verified
until Linux guest boot, workspace isolation, network policy, cancellation, and
app integration smoke tests pass. On Linux, UMCode checks that `/dev/kvm` is
an accessible character device before allowing a project to enable compute and
again before each guest launch; without it, the app shows an actionable error.

When distributing libkrunfw, include the corresponding kernel and library
sources/notices required by their licenses.
