# UMCode Computer Use helper (native, source-available rewrite)

This replaces the previous vendored, closed-source `UMCode Computer Use.app`
(the one that shipped at `bin/UMCode.app/Contents/Resources/computer/`) with
a small Swift package built from source in this repo, so it can actually be
reviewed, fixed and extended instead of being a black box.

**Status: unverified.** This was written and reasoned about from Apple's
documented APIs, but never compiled, run, or tested — this environment has
no Xcode or macOS SDK. Build it, grant it permissions, and run it against a
real app before trusting it. See "Testing checklist" below.

## Why this exists

The old helper worked, but every click had to bring the target app to the
front first — it used `CGEvent` posted to the global HID event tap plus
`open`/activation to raise the window, so the real app visibly popped in
front of whatever you were doing for every single action. `internal/
computeruse/manager.go` briefly worked around this with a hide/show hack
around each action (see git history on this package if you want it back),
but that's a workaround, not a fix.

The fix is to stop needing the window frontmost at all:

- **Screenshots**: `CGWindowListCreateImage`, keyed to the window's own
  `CGWindowID`, captures its current pixels whether or not it's occluded or
  frontmost. No activation needed.
- **Clicks, typing, keys, scrolling**: `CGEventPostToPid` delivers a
  synthetic event straight to the target process's event queue. It does not
  move the real cursor and does not require the target to be frontmost or
  key. A `fill` additionally tries the Accessibility API's direct
  `AXUIElementSetAttributeValue` first (setting a text field's value
  outright, no keystrokes at all), before falling back to a synthetic
  click + select-all + type.

Both are real, documented Apple APIs — not private or reverse-engineered.
This is also, separately, roughly how the open-source projects referenced
below solve the same problem; this package borrows the *approach*, not
their code (see licenses if you want to actually vendor from them instead):

- [huytieu/computer-harness](https://github.com/huytieu/computer-harness) (MIT) —
  says outright that OpenAI's own ChatGPT/Codex desktop computer use
  (`@oai/sky`) works the same non-disturbing way, and rebuilds it on public
  APIs.
- [echoVic/blade-computer-use](https://github.com/echoVic/blade-computer-use) (MIT) —
  almost exactly UMCode's own shape: TS/MCP layer -> persistent Swift
  helper -> AXUIElement + ScreenCaptureKit + CGEvent over a JSON protocol.
  Its one-shot "revision" token (an observation can be acted on exactly
  once, forcing a fresh observe before the next action) is a good idea this
  package does not yet implement — worth adding later.
- [Sur-Cai/macos-computer-use-kit](https://github.com/Sur-Cai/macos-computer-use-kit) (MIT) —
  AX-first targeting and a built-in deny-list for password fields, Secure
  Event Input, and lock/log-out/force-quit chords. This package has no such
  deny-list yet; `internal/tools/computer.go`'s app allowlist is the only
  guard rail today.

## What did *not* change

The wire protocol is identical on purpose, so this is a drop-in replacement
with **no Go-side changes required**: `<binary> list|inspect|act` with a
JSON object on stdin, matching exactly what `internal/computeruse/
driver_darwin.go`'s `helperDriver` already sends and expects (see
`Protocol.swift`'s doc comment — the two files must be kept in sync by
hand, there's nothing enforcing it). `computer.start`/`open` is still done
by the Go side directly via `/usr/bin/open`; this binary is never asked to
launch anything, only to look up apps that already exist.

## Layout

- `Sources/UMCodeComputerUse/main.swift` — command dispatch (`list` /
  `inspect` / `act`).
- `Protocol.swift` — Codable types mirroring the Go JSON contract exactly.
- `AppList.swift` — lists/resolves apps via `NSWorkspace`. Never calls
  `.activate(options:)` on anything — that would reintroduce the exact
  focus-stealing this rewrite removes.
- `WindowInfo.swift` — finds the target's main on-screen window via
  `CGWindowListCopyWindowInfo`, re-resolved fresh on every single call
  (this binary is short-lived, invoked once per list/inspect/act — no
  daemon, same as before), so a window the person moved or resized between
  actions is picked up correctly.
- `Screenshot.swift` — `CGWindowListCreateImage`-based capture.
- `Input.swift` / `KeyCodes.swift` — `CGEventPostToPid`-based click/type/
  key/scroll, plus the AX direct-set-value fast path for `fill`.

## Build

```bash
cd native/computer-use-helper
./build.sh                # debug, ad-hoc signed — local testing only
./build.sh --release       # release build, still ad-hoc signed
SIGN_IDENTITY="Developer ID Application: You (TEAMID)" ./build.sh --release --sign
```

This drops the assembled `.app` at exactly the path `driver_darwin.go`
looks for: `bin/UMCode.app/Contents/Resources/computer/UMCode Computer
Use.app`. Ad-hoc signing is enough to test locally, but macOS treats an
ad-hoc-signed binary's identity as unstable across rebuilds, so you may get
re-prompted for Accessibility/Screen Recording after every rebuild during
development — normal, not a bug. For anything you actually ship, sign with
your real Developer ID (same identity the rest of `UMCode.app` uses) and
notarize it the same way.

Requires Xcode's command line tools (`xcode-select --install`) and a Swift
5.9+ toolchain — both ship with any recent Xcode.

## Permissions

The binary needs, granted to *this exact bundle* (System Settings > Privacy
& Security):

- **Accessibility** — for `AXUIElementSetAttributeValue` (the `fill` fast
  path) and `AXIsProcessTrusted`/`AXUIElementCopyElementAtPosition`.
- **Screen Recording** — for `CGWindowListCreateImage`. Without it, capture
  either fails outright or returns a blank image depending on macOS
  version; `inspect`'s `permission` field reports which grant, if any, is
  missing, so a caller can surface a real message instead of a mysterious
  black screenshot.

Since clicks/keys now go through `CGEventPostToPid` rather than the global
HID tap, this binary does **not** need the old "Input Monitoring" /
accessibility-for-global-input permission the same way a keylogger-style
tool would — Accessibility covers it.

## Testing checklist (none of this has been run)

- [ ] `./build.sh` completes and produces the `.app` at the expected path.
- [ ] First `list`/`inspect`/`act` call triggers the Accessibility and
      Screen Recording prompts; granting both lets subsequent calls
      through.
- [ ] `inspect` against a normal AppKit app (TextEdit, Notes) returns a
      real, non-blank screenshot without the app coming to the front.
- [ ] `act` with `click` against a button lands correctly (verify against
      the click-marker screenshot the Go side now draws — see
      `internal/computeruse/manager.go`'s `markClick`) without moving the
      real cursor or raising the window.
- [ ] `act` with `fill` on a plain AppKit text field sets its value via the
      AX fast path (no visible typing); on a web view / Electron field,
      confirm the synthetic click+type fallback works instead.
- [ ] `act` with `type`, `key` (try a combo like `"Cmd+A"`), and `scroll`.
- [ ] A window the person drags or resizes mid-conversation is still
      targeted correctly on the next action (this is what re-resolving the
      window on every call, rather than caching bounds, is for).
- [ ] Confirm nothing here needs the App Sandbox entitlement turned on —
      if `UMCode.app` itself is sandboxed and this helper inherits that
      via the same signing identity, cross-process AX actions will likely
      be denied regardless of TCC grants; this helper is written assuming
      it is *not* sandboxed, same as the binary it replaces.

## Known gaps / next steps

- No one-shot "revision" token like blade-computer-use's — a stale
  `computer.act` call using coordinates from an old screenshot can still
  land on the wrong thing if the UI changed since. Worth adding: have
  `inspect`/the post-action screenshot mint an opaque token, and refuse
  `act` calls that don't carry the latest one (would need a matching
  Go-side change to thread it through the tool schema).
- No password-field / Secure Event Input deny-list like
  macos-computer-use-kit's. `internal/tools/computer.go`'s app allowlist
  restricts *which app* Computer Use can touch, but nothing here stops it
  from typing into a password field inside an allowed app.
- Canvas/game-style apps with no accessibility tree and no real window
  content to screenshot via `CGWindowListCreateImage` (e.g. some
  GPU-rendered surfaces) may need a `CGEventPost(.cghidEventTap, ...)`
  foreground fallback, the way computer-harness has an explicit opt-in
  "foreground tier" for exactly this case. Not implemented here.
