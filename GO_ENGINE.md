# UMCode Go engine

The Go engine is the new core of UMCode: one binary (`umcode`) that is both the always-on engine and the CLI. The Mac app and other clients talk to it through the engine protocol described below. State lives in `~/.umcode` (SQLite database, config, run socket and token).

Status: milestones M0–M5 of the [rewrite plan](https://claude.ai/code/artifact/259caf02-238a-46e2-ad53-ee346d97c407) (engine, agent loop, skills, MCP, scheduled tasks, projects and the sandbox), plus the Mac app (M5) and the engine side of M6 (chats, models, complexity, API keys and usage). The former Python assistant (connectors, Google Workspace tools, agent teams, web control panel) has been removed.

## Quick start

```sh
make go-build                       # → bin/umcode
./bin/umcode engine               # run in the foreground (Ctrl-C to stop)
./bin/umcode service install      # or: start at login (launchd / systemd --user)

./bin/umcode key add --provider claude --label personal   # prompts for the key, then tests it
./bin/umcode project add ~/code/my-app                    # the folder the agent may work in
./bin/umcode chat --project prj_… "add a test for the parser"
./bin/umcode chat                 # interactive, no project: read-only
```

API keys go to the macOS Keychain (service `com.umcode`); on Linux they are kept in `~/.umcode/secrets.json` (mode 0600). On first start the engine imports `api_key` values from `config.yaml` (or the `UMCODE_LLM_*` environment variables) for any provider with no key yet; you can then remove them from `config.yaml`.

## The Mac app

`UMCode.app` is a [Wails v3](https://v3.wails.io) shell around a Svelte UI that speaks the engine protocol over the WebSocket — the same protocol the CLI uses, so the app is only a client.

```sh
make app-build            # → bin/UMCode.app (macOS; add app-build-universal for arm64 + x86_64)
make app-dev              # the UI in a browser against a running engine, with hot reload
make app-check            # svelte-check + the UI's unit tests
```

The window is a rail plus up to three columns:

| Column | What it holds |
| --- | --- |
| Rail | Project switcher, the views (chat, approvals, tasks, skills & MCP, usage, settings), and either the project's chats or its file tree |
| 1 · Chat | The conversation: streaming replies, tool calls, file changes with inline diffs, approvals in place, and a composer with the complexity dial and model picker |
| 2 · Side chat | “Ask about this” on any message, file or diff opens a child chat in the same project; several stack as tabs, and *Promote* hands its answer back to the main composer |
| 3 · Inspector | A file, a diff or a long tool result, read-only, with *Undo* for a change and *Ask about this* to spin off a side chat |

The shell itself does the native parts: a menu-bar item with the engine's state, the pending-approval count and the project list; approval notifications with Approve and Deny buttons; starting the engine (as a `SMAppService` login item when the app is installed, otherwise as a child process); *Open at Login*; and installing the `umcode` command-line tool. The UI reaches the engine through `/__umcode/connection`, which the shell answers with the WebSocket URL and the token from `~/.umcode/run/token`; `make app-dev` answers the same path from the Vite dev server, which is why the UI runs unchanged in a browser.

The theme is warm rather than cold — paper and ink with one clay accent, and sage/rust diffs — and follows the system's light or dark setting.

## Projects

A **project** is a folder the agent may work in, and it is the sandbox: `file.read`, `file.list`, `file.write` and `shell.run` resolve every path against the project root — symlinks included — and refuse anything outside it. A write outside a project fails; it is never offered as an approval. A chat without a project still answers questions, but its file and shell tools are off.

**File tools.** `file.read`, `file.list`, `file.write`, `file.edit` (exact-match replace; prefer it over `file.write` for existing files) and `file.search` (regex or literal content search and file-name globs; works with the shell and network off) all resolve paths inside the project.

**Approvals and auto-approve.** First-party tools are available without project/global enable switches. Risky actions ask in chat by default. Each chat can choose Ask every time, Auto-approve workspace, or Auto-approve all; these choices are stored on that chat, never its project. Auto-approve workspace covers red-risk actions confined to the workspace; Auto-approve all also covers Computer Use in host applications. Listener-triggered red-risk actions always ask, and hard-forbidden shell commands are refused in every mode. “Always allow in this chat” remembers the exact approved tool arguments only for that chat.

Shell commands are parsed per pipeline segment (`&&`, `||`, `;`, `|`). Known read-only commands (`ls`, `cat`, `rg`, `git status/diff/log/show`, `find` without `-exec`/`-delete`, version checks, ...) run without asking when their arguments stay inside the project; anything the parser cannot fully understand (command substitution, variable expansion, redirection to files, background jobs) asks. Destructive commands (`sudo`, `rm -rf /` or `~`, `curl | sh`, `dd of=/dev/...`, fork bombs) and prefixes in `policy.shell_forbid_commands` are refused without an approval prompt.

**Long-running commands.** `exec.start` runs a command that keeps going (dev server, REPL, prompting installer) and returns a session id and first output; `exec.write` sends stdin or polls; `exec.stop` ends it. Sessions belong to a chat, use the same sandbox and approval rules as `shell.run`, keep the last 256 KB of output, idle out after 30 minutes, are capped at 8 per chat and are killed on engine shutdown. They run on the host only and use pipes rather than a terminal.

**Context budget.** Shell output over 64 KB keeps its head and its tail. When a request nears 75% of the model's context window, older large tool results are replaced with a short stub (the newest six are kept), and a new turn starts by summarizing earlier conversation when history is above about 70% of the window (the same summary as the manual compact action).

**Host shell sandbox.** On the host, `shell.run` runs confined when the platform allows: Seatbelt (`sandbox-exec`) on macOS, bubblewrap (`bwrap`) on Linux. Writes are limited to the project folder, temp folders and — only when the project's network is on — package-manager caches (`~/.npm`, `~/.cache`, `~/go/pkg`, ...); `~/.ssh`, `~/.aws`, `~/.gnupg`, `~/.kube` and similar are unreadable; `.git/hooks` and `.git/config` are read-only; and there is no network unless the project allows it. With a sandbox available, a project with network off can still run shell commands. Without one (for example bubblewrap is not installed or user namespaces are disabled) the earlier behaviour applies: shell commands need the microVM when network is off. Set `tools.host_sandbox: off` to run commands unconfined.

Each project carries its own defaults (provider, model, complexity, key), compute/network settings, and optionally the MCP servers it may use. Compute selects the isolated execution environment; network is scoped to that environment. Shell commands run with the project root as the working directory and an environment with no API keys in it; with network off, commands that obviously reach out (`curl`, `git push`, `npm install`, …) are refused with a message rather than failing halfway.

**`AGENT.md` is the project's system prompt.** Every turn composes `~/.umcode/AGENT.md` (your standing instructions) → the project's `AGENT.md` → the nearest `AGENT.md` in the subtree being worked on. `AGENTS.md` (Codex) and `CLAUDE.md` (Claude Code) are read as fallbacks, so a repo set up for either works unchanged. Files are re-read when they change on disk and capped at 32 KB each; `umcode project instructions ID --composed` prints exactly what the agent receives.

Every file the agent creates, changes or deletes becomes a **`fileChange` item** in the chat with a unified diff, and is recorded with the previous content (up to 1 MB), so `umcode project diff` shows what a turn did and `umcode project revert TURN_ID` puts it back. Answering an approval with `remember: true` stores that exact decision for the chat only.

```sh
umcode project add ~/code/my-app --name my-app        # register a folder
umcode project show prj_…                             # folder, git branch, defaults, AGENT.md
umcode project instructions prj_… -f AGENT.md         # write the project's instructions
umcode project diff prj_…                             # what the agent changed
umcode project revert trn_…                           # undo one turn's edits
umcode project set prj_… --network on -m claude-opus-5
```

## CLI

| Command | What it does |
| --- | --- |
| `chat [--project ID] [-t THREAD] [-p PROVIDER] [-m MODEL] [-c LEVEL] [-k KEY] [MESSAGE]` | Send a message, streaming the reply. Asks `[y/N]` for approvals. No message = interactive. |
| `project list / add / show / instructions / files / diff / revert / set / archive / remove` | Projects: the folders the agent may work in. |
| `thread list / show / search / rename / pin / unpin / archive / unarchive / delete / fork / export` | Chat history. Old Python sessions appear as threads with ids `legacy-s<N>`. |
| `thread set ID [-p] [-m] [-c] [-k]` | Save a thread's provider, model, complexity and key. |
| `key list / add / test / default / enable / disable / fallback / rotate / delete / budget` | API keys per provider, monthly budgets (`--hard-stop` blocks requests at 100%). |
| `model list [-p P] [--all] / hide / show / price / refresh` | Model catalog: bundled models + models the provider reports, hidden flags, price overrides. |
| `complexity [show] / default LEVEL` | Complexity presets and the default level. |
| `usage [--by credential\|model\|thread\|role\|day] [--days N] [--key ID]` | Tokens and cost. |
| `approvals / approve ID / deny ID` | Answer approvals from another terminal. |
| `task list [--all] / add / cancel ID / run ID / runs ID` | Scheduled tasks. `add` takes `--at 2026-09-20T09:00`, `--daily 09:00`, `--weekly mon@09:00`, `--hourly 15` or `--cron "0 9 * * mon-fri"`, plus `--tz`, `-p/-m/-c`. |
| `skill list / show NAME / install PATH_OR_GIT_URL / remove NAME` | Skills in `./skills`, `~/.umcode/skills` and `skill_dirs`. |
| `mcp [list] / mcp restart NAME` | MCP servers from `mcp_servers` and their status. |
| `tools` | Every tool the agent can call, with its source. |
| `status`, `service install\|uninstall\|status`, `version` | Engine management. |

## Model selection and complexity

For each turn the engine resolves provider → model → API key → complexity, first match wins:

1. The turn override (`-p/-m/-c/-k`, or the composer pickers in the app).
2. The thread's saved settings.
3. Defaults: `llm.provider` / `llm.model`, `llm.providers.<p>.default_model`, then the bundled default.

The key is the requested one, else the provider's default key, else its oldest enabled key. With `fallback` enabled on other keys, a rate-limited request is retried with the next one.

| Level | Reasoning | Max tool steps | Output tokens |
| --- | --- | --- | --- |
| quick | off | 5 | 4,096 |
| standard | medium | 15 | 16,384 |
| deep | high | 40 | 32,768 |
| auto (default) | picks quick / standard / deep per message with a local heuristic; the turn records what it picked | | |

Reasoning maps to Claude adaptive thinking + `output_config.effort` (or a thinking budget for Haiku 4.5), OpenAI `reasoning_effort`, and Gemini `thinkingLevel` (or `thinkingBudget` for 2.5). Models without reasoning ignore it.

## Skills, MCP and scheduled tasks

**Skills** use the `SKILL.md` format and are found in `./skills`, `~/.umcode/skills` and any `skill_dirs`. The system prompt lists each skill's name and description; the agent reads full instructions with `skill.get_instructions` and runs declared scripts with `skill.run_script`, or passes `skill: <name>` to `shell.run` to use the skill's environment. Scripts get `{"input": <args>, "config": <skills.<name>.config>}` on stdin (the `skill-template` contract), run in the skill folder with its `.venv` (created with `uv` when available), `extra_path`, `env` and `SKILL_DIR`, and never inherit the engine's own API keys. `risk_level` in `SKILL.md` (default yellow) decides approvals.

**MCP servers** from `mcp_servers` (stdio or Streamable HTTP) start in the background; their tools appear as `mcp_<server>_<tool>`. Risk comes from the server's `risk_level` if set, otherwise from the tool's annotations: read-only is green, destructive is red, anything else yellow. A server that exits is restarted on the next call.

**Scheduled tasks** are stored in the `tasks` table. Each run becomes a turn in the task's own thread (so you can read its history and cost), with the task's provider/model/complexity. Approvals work as in chat. The agent can create, list and cancel tasks itself (`task.create`, `task.list`, `task.cancel`). A failed one-time task is not retried in a loop. Tasks are leased before running, so a task never runs twice.

## Usage and cost

Every model request writes one row to `llm_usage`: key, provider, model, thread, turn, role (`chat`, `title`), input / cached / output / reasoning tokens, cost, latency and status. Cost uses the bundled price table (`internal/models/catalog.json`, list prices checked 2026-09-18) or your override (`umcode model price`). Models without a known price cost $0 and show `?`. When a provider reports no usage, tokens are estimated and flagged.

## New config keys

```yaml
runtime:
  engine_ws_port: 8766          # protocol WebSocket (0 = off); separate from ws_port
  socket_path: ~/.umcode/run/engine.sock
policy:
  approval_timeout_minutes: 30  # unanswered approvals count as denied
models:
  default_complexity: auto      # auto | quick | standard | deep
  complexity:                   # optional overrides per level
    deep: {max_tool_steps: 60}
  roles:
    title: {provider: claude, model: claude-haiku-4-5-20251001}
llm:
  providers:
    openai_compatible: {base_url: http://localhost:11434/v1, default_model: llama3.3}
skills:
  weather-fetch:
    config: {api_key_env: WEATHER_KEY}   # passed to scripts as "config"
mcp_servers:
  - name: github
    command: npx
    args: ["-y", "@modelcontextprotocol/server-github"]
    env_vars: [GITHUB_TOKEN]            # forwarded from the engine's environment
    risk_level: yellow                  # optional override for every tool on this server
```

## Protocol

JSON-RPC 2.0, one message per line on the Unix socket `~/.umcode/run/engine.sock` (0600), or one per text frame on `ws://127.0.0.1:8766/ws` with `Authorization: Bearer <~/.umcode/run/token>` (or `?token=`). The token is regenerated at each engine start. Browser clients may connect from the Mac app's webview (`wails://…`) or a local dev server; other origins are refused. Clients must call `initialize` first (`protocolVersion` major must match; `admin: true` to receive and answer approvals).

Types are in `internal/protocol`. Methods:

| Group | Methods |
| --- | --- |
| Session | `initialize`, `engine/status`, `events/subscribe` (`{all: true}` or `{threadIds: [...]}`) |
| Projects | `project/list`, `project/create`, `project/open`, `project/update`, `project/delete`, `project/instructions`, `project/files`, `project/readFile`, `project/diff`, `project/revertTurn` |
| Threads | `thread/start` (`projectId`), `thread/list`, `thread/read`, `thread/rename`, `thread/pin`, `thread/archive`, `thread/delete`, `thread/fork`, `thread/search`, `thread/export`, `thread/setSettings` |
| Turns | `turn/start` (`text`, `attachments`, `override`), `turn/interrupt` |
| Approvals | `approval/list`, `approval/respond` |
| Providers, models | `provider/list`, `model/list`, `model/setHidden`, `model/setPrice`, `model/refresh` |
| API keys | `credential/list`, `credential/add`, `credential/test`, `credential/update`, `credential/rotate`, `credential/delete` |
| ChatGPT sign-in (OpenAI) | `chatgpt/signin/start`, `chatgpt/signin/cancel`; notification `chatgpt/signin/completed`. A credential has `kind`: `api_key` or `chatgpt` |
| Usage | `usage/summary`, `usage/setBudget` |
| Complexity | `complexity/getDefaults`, `complexity/setDefaults` |
| Tasks | `task/list`, `task/create`, `task/cancel`, `task/runNow`, `task/runs` |
| Skills | `skill/list`, `skill/get`, `skill/install`, `skill/remove` |
| MCP and tools | `mcp/list`, `mcp/restart`, `tool/list` |

Item kinds are `userMessage`, `agentMessage`, `reasoning`, `toolCall`, `fileChange` (path, action, diff, counts, the turn that made it), `inboundEvent` and `error`.

Notifications: `turn/started`, `turn/completed` (with `usage`), `item/started`, `item/delta`, `item/completed`, `thread/updated`, `approval/request`, `approval/resolved`, `usage/budgetWarning`, `project/updated` and `task/updated` (admin clients). A client receives thread events for threads it started, read or sent a turn to, or all threads after `events/subscribe {all: true}`.

Approvals are a notification plus `approval/respond` rather than a server-to-client request: the first admin answer wins, later ones get error `-32005`, and everyone gets `approval/resolved`.

## Layout

```
cmd/umcode/        CLI + engine entry point
internal/protocol/   protocol types and method names
internal/server/     Unix socket + WebSocket transports (e2e tests live here)
internal/client/     Go protocol client (used by the CLI)
internal/engine/     threads, turns, agent loop, approvals, titles
internal/llm/        Claude, OpenAI(-compatible) and Gemini streaming adapters (plain HTTP)
internal/models/     catalog, prices, cost, complexity presets and Auto classifier
internal/credentials/ API keys, Keychain, budgets, fallback
internal/tools/      tool registry, built-in tools (file.read/list/write, shell.run), project scope + workspace ACL
internal/projects/   projects, AGENT.md composition, file trees, diffs and undo
internal/pathutil/   the containment rule every tool shares (symlink-safe, /var vs /private/var)
internal/skills/     SKILL.md loader, runtimes/venvs, skill tools, install/remove
internal/mcp/        MCP client (stdio + Streamable HTTP) and tool bridge
internal/tasks/      schedules (incl. cron), scheduler loop, task tools
internal/procutil/   subprocess cleanup (process groups, timeouts)
internal/policy/     approval policy
internal/store/      SQLite (pure Go, WASM build of SQLite) + migrations
internal/config/     config.yaml loader
internal/secrets/    Keychain (macOS) / file store
```

### App layout

```
app/                 Wails v3 shell (its own Go module; needs Go 1.25)
  main.go            window, menu bar, engine lifecycle
  shell.go           /__umcode/* endpoints, tray menu, login item, CLI install
  watcher.go         admin connection: approval notifications, counts, projects
  engine.go          finds and supervises the engine (SMAppService or child process)
  build/macos/       Info.plist, the engine LaunchAgent, bundle.sh
                     (the engine sits in Contents/Resources: macOS ignores
                     case, so Contents/MacOS/umcode would be the app itself)
  frontend/          Svelte 5 + Vite UI (svelte-check, vitest)
```

## Building without golang.org access

`go.mod` fetches `golang.org/x/*` from their GitHub mirrors through `replace` directives. Where `golang.org` is reachable you can drop the `replace` block and run `go mod tidy`.
