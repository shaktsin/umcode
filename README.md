# UMCode

A local-first AI workbench for project-scoped chats and software work.

Choose a project folder and UMCode keeps its chats and project instructions together. Start a clean chat or a contextual side chat, choose configured provider/model IDs, or combine models into an ordered pool. When a provider reports a quota or rate limit, the engine can continue with the next configured model. Tool actions stay visible, with approvals for sensitive operations.

![UMCode desktop app](media/umcode-screenshot.png)

---

- **Project-aware chats** — work in a selected folder with its `AGENTS.md` or `CLAUDE.md` instructions
- **Provider and model choice** — use configured models directly or create ordered pools across providers
- **Quota-aware routing** — move to the next model in a pool when a provider reports a retryable quota or rate-limit failure
- **Local controls** — keep chat history, model settings, engine status, and approval workflows in one desktop app
- **Protected credentials** — provider API keys are stored in the macOS Keychain
- **Unified plugins** — install portable Agent Plugins or compatible Codex and Claude plugin folders containing skills, MCP servers, hooks, and metadata

---

## Build and run

Requires Go 1.24+ and, for the desktop app, Node.js. See [GO_ENGINE.md](GO_ENGINE.md) for the full guide.

```bash
make go-build                 # build the engine + CLI → bin/umcode
./bin/umcode engine         # run the engine in the foreground
make app-build                # build UMCode.app (macOS)
make help                     # list all targets
```

## Plugins

Plugins are the only extension unit shown in the desktop app. Open **Plugins**, enter a local folder or Git URL, and choose **Inspect** before **Install**. Inspection shows the package identity, compatibility diagnostics, executable capabilities, requested settings, and whether additional trust is required. UMCode accepts the portable Agent Plugins `plugin.json` format, Codex `.codex-plugin/plugin.json` folders, and Claude `.claude-plugin/plugin.json` folders.

Managed installs copy verified bytes into `~/.umcode/plugins/cache/<source-id>/<plugin>/<version-or-digest>/`, so they keep working if the source disappears. Linked installs track a local source folder and add an explicit **Reload** workflow for development; each active generation still runs from an immutable cached snapshot, and source edits take effect on reload. Installations are user-global; enablement and non-secret settings are per project. Secret settings live in the Keychain or protected secrets file, never SQLite. Mutable plugin data belongs under `~/.umcode/plugins/data/<source-id>/<plugin>/` and is retained on uninstall.

Runtime declarations reference configuration explicitly with `{{setting:name}}` and protected values with `{{secret:name}}`. Plugin MCP servers cannot forward arbitrary host environment variables, and sensitive HTTP headers must use a declared secret reference. Skill-script output and hook diagnostics redact resolved secret values. A plugin with required configuration installs disabled for the selected project; configure it, then enable it after its required components activate successfully.

Hooks run commands from the installed package with a minimal environment and bounded input/output. `TurnStart` and `BeforeToolUse` may block; no hook can bypass UMCode's built-in guards, policy decisions, or approval prompts. Each turn retains one immutable capability snapshot, so reloading or disabling a plugin affects new turns only.

```bash
umcode plugin inspect ./my-plugin
umcode plugin install ./my-plugin --project prj_…
umcode plugin install ./my-plugin --linked --project prj_…
umcode plugin list --project prj_…
umcode plugin enable SOURCE_ID/PLUGIN --project prj_…
umcode plugin reload SOURCE_ID/PLUGIN      # linked installs only
umcode plugin remove SOURCE_ID/PLUGIN --disable-projects
```

Legacy standalone `skill` commands, `skill_dirs`, and `mcp_servers` remain backend-compatible during the migration window, but they are not separate sections in the desktop Plugins screen.
