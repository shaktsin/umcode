UMCode Linux desktop bundle

Run ./umcode-launch to start the app. Run ./install-desktop.sh to add a
launcher to this user's desktop application menu. No CLI or browser UI is
installed.

This portable folder includes the UMCode app, its private engine, and the
private libkrun microVM runtime. It does not install system packages. The host
must provide GTK 4.14+ and WebKitGTK 6.0 runtime libraries (the matching -dev
packages are needed only to build), and must be x86_64 or ARM64 Linux with KVM
available to use isolated compute. Without KVM the rest of the app can launch,
but compute will report unavailable.

Keep the folder together when moving/updating the app. Removing it removes the
app bundle; the desktop entry can be removed from
~/.local/share/applications/umcode.desktop (displayed as UMCode).
