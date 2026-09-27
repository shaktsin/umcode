# UMCode roadmap: agent core, sandbox, compute

Derived from a review of UMCode's engine, compute and computer-use code against
OpenAI Codex (`codex-rs`). Facts below were checked in the code; sizes marked
"estimate" were not measured.

Legend: `[x]` done · `[ ]` open · **P0** do first · **P1** next · **P2** later.

## How the two projects differ (context)

- Codex runs commands on the host with the user's own toolchains and confines
  them with an OS sandbox (Seatbelt on macOS, bubblewrap + Landlock on Linux):
  read anywhere, write only to workspace roots and temp, network denied unless
  a domain-allowlist proxy or an approval says otherwise. It has no per-language
  provisioning; for a specific stack it runs in a remote/container environment.
- UMCode's host `shell.run` had no OS sandbox (path checks on file tools only),
  so "network off" meant "refuse to run a shell unless the microVM is on". The
  microVM guest is a fixed Alpine image (Git, ripgrep, Node, Go, Python, gcc,
  Chromium), rebuilt from a disposable copy for every command, so package
  caches are lost and any project outside that toolset is unsupported.

## Phase 1 — this branch (P0)

- [x] **1. Sandbox the host shell.** New `internal/sandbox` package: Seatbelt
  profile on macOS, bubblewrap on Linux. Writes confined to the project root,
  temp dirs and (when network is on) package-manager caches; network denied
  unless the project allows it; credential directories (`~/.ssh`, `~/.aws`,
  ...) unreadable; `.git/hooks` and `.git/config` read-only. `shell.run` uses it
  when available, which also removes the "no shell while network is off unless
  the VM is on" restriction. Config: `tools.host_sandbox: auto | off`.
  Falls back to the previous behaviour when no sandbox is available.
  *Linux (bubblewrap) is tested end to end: writes outside the project and
  network are blocked. macOS Seatbelt is covered by profile-generation tests
  and compiles, but `go test ./internal/sandbox` has not been run on a Mac
  yet — do that before relying on it.*
- [x] **2. `file.edit` tool.** Exact-match replace (`old_string` →
  `new_string`, optional `replace_all`), CRLF-aware, refuses ambiguous or
  missing matches with actionable errors, recorded for diff/undo like
  `file.write`. Creates a file when `old_string` is empty and the file is new.
- [x] **3. `file.search` tool.** Native regex/literal content search and
  file-name globbing inside the project. Works with the shell off and network
  off, skips VCS/vendor dirs, binaries, large files and symlinks, and returns
  bounded `path:line: text` output with optional context lines.
- [x] Update the system prompt to prefer `file.search` / `file.edit`.

## Phase 2 — agent loop (P1)

- [x] **4. Command-aware approvals.** Parse commands into argv (handle
  `sh -c`, pipes, `&&`), keep a built-in safe list (`ls`, `cat`, `rg`,
  `git status|diff|log`), persistent allow/forbid prefix rules, and forbid
  interpreter one-liners and `sudo` by default (compare Codex `execpolicy`).
- [x] **5. Auto-compaction and token-based truncation.** `CompactThread` is
  manual, history is capped at 60 messages and tool output is clipped by
  characters. Compact at a token threshold, keep recent turns verbatim, and
  truncate tool output head+tail by tokens.
- [x] **6. Long-running / interactive commands.** `shell.run` is one-shot
  (600 s cap, no stdin, no persistent cwd/env). Add exec sessions:
  `exec.start`, `exec.write_stdin`, `exec.poll` with yielded output and PTY.
- [ ] **7. Parallel read-only tool calls.** Run `file.read`, `file.list`,
  `file.search`, `web.*` concurrently within one model round.
- [ ] **8. Plan/todo tool** for long tasks, surfaced in the UI.
- [ ] **9. `apply_patch`-style multi-file edit** (streaming parser) and a
  batch `edits: []` on `file.edit`.
- [ ] **10. Honor `.gitignore` in `file.search`** (currently a fixed skip list).
- [ ] **11. `skill:` environments inside compute** (currently errors).

## Phase 3 — dynamic language support (P1)

- [ ] **12. Detect the stack.** Extend `verification.go` (Node/Go/Rust/Python
  manifests) with version files: `.nvmrc`, `.tool-versions`, `mise.toml`,
  `.python-version`, `rust-toolchain.toml`, `go.mod`, `package.json#engines`,
  `requires-python`, `devcontainer.json`, `Dockerfile`.
- [ ] **13. Host mode uses the user's toolchains** via a login-shell
  environment snapshot (as Codex's `shell_snapshot`), with a filtered env.
- [ ] **14. Install missing runtimes on demand** into `~/.umcode/toolchains`
  (`mise` / `uv`), behind an approval, and put them on `PATH` for sandboxed
  commands.
- [ ] **15. Network allowlist instead of on/off.** Local proxy with a default
  registry allowlist (npm, PyPI, crates.io, Go proxy, GitHub) and audit log.
  *Verify first whether libkrun 1.19 TSI can be restricted to a proxy; if not,
  use virtio-net with host-side filtering for the VM.*
- [ ] **16. Persistent package caches** per project mounted into the VM
  (`~/.npm`, pip, cargo, Go modules) so installs happen once.

## Phase 4 — compute and bundle size (P1/P2)

- [ ] **17. Ship the guest as a download, not in the app.** App + engine is
  ~25 MB; the guest image now adds Go, gcc, Node, Python and Chromium (the
  63 MB figure in `IMPLEMENTATION_PLAN.md` predates that; estimate ~1 GB
  unpacked, measure with `du -sh`). Fetch a checksummed runtime pack on first
  use.
- [ ] **18. Layered images.** Small glibc base (Debian-slim or Wolfi, not
  Alpine/musl), optional per-language layers cached in `~/.umcode`, no
  Chromium by default.
- [ ] **19. Stop copying the rootfs per command.** Boot from a read-only image
  with a tmpfs/overlay upper layer (Linux `copyTree` copies the whole image
  every run; macOS uses APFS clonefile).
- [ ] **20. Per-architecture guest packs** (no universal guest); compress.
- [ ] **21. Honor a project's own `devcontainer.json` / OCI image** in the VM.

## Phase 5 — computer use (P2)

Reviewed only the Go manager and tool wrappers, not the macOS helper.

- [ ] **22. Per-app grants** and a deny list for sensitive apps (keychain,
  password managers, banking, Terminal).
- [ ] **23. Prefer accessibility controls over pixel coordinates.**
- [ ] **24. Action budget, audit log and a user kill switch.**

## Done log

- Phase 1 implemented on branch `feat/agent-core-sandbox-edit-search`: `go vet`, `gofmt` and `go test -race ./...` pass on Linux with bubblewrap installed; darwin build and `go vet` pass. Not yet exercised on macOS or in the desktop app.
- Items 4 to 6 implemented on branch `feat/approvals-compaction-exec`: command-aware approvals (`internal/tools/shellpolicy.go`, `policy.shell_forbid_commands`), head+tail output truncation with old tool-result trimming and automatic compaction at turn start (`internal/engine/context.go`), and `exec.start`/`exec.write`/`exec.stop` sessions (`internal/tools/execsession.go`). `gofmt`, `go vet` and `go test -race` pass on Linux; darwin `go vet` passes. Follow-ups: exec sessions use pipes, not a PTY; they are host-only (not available with the microVM); compaction thresholds are fixed fractions of the model's context window.
- Subscription sign-in (Codex / Claude Code) removed from the engine, protocol and app; only API keys remain.
