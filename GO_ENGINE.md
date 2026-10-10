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
| Rail | Project switcher, the views (chat, approvals, tasks, plugins, usage, settings), and either the project's chats or its file tree |
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

Each project carries its own defaults (provider, model, complexity, key), compute/network settings, and plugin enablement/configuration. Compute selects the isolated execution environment; network is scoped to that environment. Shell commands run with the project root as the working directory and an environment with no API keys in it; with network off, commands that obviously reach out (`curl`, `git push`, `npm install`, …) are refused with a message rather than failing halfway.

**`UMCODE.md` is the project's instruction file.** Every turn composes the project's `UMCODE.md` → the nearest `UMCODE.md` files in the subtree being worked on. UMCode reads no other instruction files: `AGENTS.md`, `CLAUDE.md` and `~/.umcode/AGENT.md` are not scanned. Files are re-read when they change on disk and capped at 32 KB each; `umcode project instructions ID --composed` prints exactly what the agent receives.

Every file the agent creates, changes or deletes becomes a **`fileChange` item** in the chat with a unified diff, and is recorded with the previous content (up to 1 MB), so `umcode project diff` shows what a turn did and `umcode project revert TURN_ID` puts it back. Answering an approval with `remember: true` stores that exact decision for the chat only.

```sh
umcode project add ~/code/my-app --name my-app        # register a folder
umcode project show prj_…                             # folder, git branch, defaults, UMCODE.md
umcode project instructions prj_… -f UMCODE.md         # write the project's instructions
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
| `plugin inspect / install / list / show / enable / disable / reload / remove` | Inspect and manage portable, Codex, or Claude plugin packages. Installations are global; enablement is per project. |
| `skill ...`, `mcp ...` | Legacy standalone compatibility commands; new extensions should be plugins. |
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

## Plugins and scheduled tasks

**Plugins** are the primary extension model and the only extension type shown in the desktop app. A package may contain skills, MCP servers, hooks, settings, and visible unsupported compatibility metadata. UMCode detects a portable Agent Plugins `plugin.json` first, then Codex `.codex-plugin/plugin.json`, then Claude `.claude-plugin/plugin.json`. The desktop and CLI require inspect-before-install so users review executable capabilities and diagnostics before verified bytes are activated.

Managed packages are copied atomically into `~/.umcode/plugins/cache/<source-id>/<plugin>/<version-or-digest>/`; linked packages load from a local folder and can be explicitly reloaded. Mutable data remains in `~/.umcode/plugins/data/<source-id>/<plugin>/`. Installations are user-global, while enablement and non-secret settings are stored per project. Secrets use `plugin/<plugin-id>/<setting-name>` in the protected secret store and never appear in RPC responses or SQLite settings.

Plugin skills are namespaced as `<plugin>:<skill>`, and plugin MCP servers are namespaced as `<plugin>__<server>`. Hooks cover turn start/completion, tool use/failure, and compaction. Only `TurnStart` and `BeforeToolUse` can block, and built-in guards plus approval policy always run with final authority. A turn holds one immutable snapshot until its completion hook finishes; a failed activation keeps the previous snapshot alive.

**Legacy compatibility.** Standalone skills from `skill_dirs` and MCP servers from `mcp_servers` continue to run through the backend and CLI during migration, but they do not appear as separate desktop extension sections.

**Skills** use the `SKILL.md` format. The system prompt lists each skill's name and description; the agent reads full instructions with `skill.get_instructions` and runs declared scripts with `skill.run_script`, or passes `skill: <name>` to `shell.run` to use the skill's environment. Scripts get `{"input": <args>, "config": <project plugin settings>}` on stdin, run in the skill folder with its runtime environment, and never inherit the engine's own API keys. `risk_level` in `SKILL.md` (default yellow) decides approvals.

**MCP servers** from `mcp_servers` (stdio or Streamable HTTP) start in the background; their tools appear as `mcp_<server>_<tool>`. Risk comes from the server's `risk_level` if set, otherwise from the tool's annotations: read-only is green, destructive is red, anything else yellow. A server that exits is restarted on the next call.

**Scheduled tasks** are stored in the `tasks` table. Each run becomes a turn in the task's own thread (so you can read its history and cost), with the task's provider/model/complexity. Approvals work as in chat. The agent can create, list and cancel tasks itself (`task.create`, `task.list`, `task.cancel`). A failed one-time task is not retried in a loop. Tasks are leased before running, so a task never runs twice.

## Work record

Each chat keeps a quiet record of what a request set out to do: a goal, the verification criteria the agent planned, the files it changed, failed tools, and each verification attempt with its evidence. A work spans turns until it is resolved: it closes when a turn completes (not paused) with no unresolved criterion, stays open after an interrupted, paused or failed turn, and is marked abandoned when the chat is archived. Recording is best-effort and never changes the chat, the prompt, or a tool result. Read it with `work/list` (`threadId`, newest first) and `work/get` (`workId`).

### Designed workflow (opt-in, Phase 5a)

Set `models.designed_workflow: true` to enable the semantic work graph, conditional workflow instructions, and the `work.update` built-in. The flag defaults to false: the existing tool list, prompts, work lifecycle, and request accounting retain their prior behavior. This works in general-purpose chats as well as project chats; an internal graph update needs a thread, not a project or filesystem. It adds no model call.

Workflow depth escalates deterministically and never decreases: Direct for ordinary questions and discovery, Guided after coding/verification or assessed-risk tool activity, and Designed for actionable public-contract or persisted-schema changes, migration/compatibility, security/trust, billing/money, destructive/recovery work, or an explicit validated graph update requesting Designed depth. Explanatory questions about these topics remain Direct. Classification uses local rules and recorded structure, not a model judgment.

Before Guided or Designed implementation, `work.update` records the first sufficient solution rung, with an acceptance-criterion link and supporting inspected evidence:

1. The change does not need to exist.
2. The repository already contains the capability.
3. The standard library satisfies it.
4. The native platform satisfies it.
5. An already-installed dependency satisfies it.
6. Minimum new code is required.

The tool creates bounded batches of requirements, non-goals, options, decisions, tasks, unknowns, and memory candidates; it references existing engine-owned criteria and evidence. It uses work/node revision and status predicates, validates the whole projected graph before an atomic commit, and returns only the new revision and counts. The engine derives task readiness from the selected approved solution, applicable criteria, dependencies, required decisions, and blocking unknowns. It checks graph structure and evidence references; it does not evaluate whether a prose rationale is persuasive. Guided selections can be recorded as approved without a workflow approval when no gate applies. Completion requires a supported approved solution, a task graph, completed required tasks, approved required decisions, resolved or explicitly accepted blocking unknowns, and fresh required verification passes.

Required proposed decisions with gate kinds `public_contract`, `persisted_schema`, `security`, `destructive`, `billing`, `architecture_choice`, or `accepted_risk` require human workflow approval. Explicit acceptance of a blocking unknown as `accepted_risk` also requires approval; merely opening the unknown does not request it. Each workflow approval identifies one exact work, node, and node revision and cannot be remembered, even if the client requests remembering. Approval or denial commits the node outcome, dependent task states, and work revision together before execution continues. A denied decision is not re-prompted; mutation stays `workflow not ready` until a replacement solution is approved and any required implementation task is runnable. After restart, a proposed required decision stays proposed, orphaned approvals expire, and the next mutation obtains a fresh node-specific approval before executing.

Designed mutation fails closed before tool hooks, safety policy, and tool execution. While gates or readiness block mutation, the exact discovery allowance is `file.read`, `file.list`, `file.search`, `web.search`, `web.fetch`, `verification.plan`, `computer.list`, `computer.inspect`, `visual.inspect`, and `work.update`; these tools still enforce their own project and safety restrictions. Unknown plugin/MCP tools are blocked because their side effects are not established. Opening the workflow gate does not bypass ordinary tool approval or safety policy.

With the independent `models.context_compiler: true` flag, eligible compact requests project active semantic state. P0 carries workflow depth/revision, required decisions and unresolved gates, current tasks/dependencies, blocking unknowns, criteria and completion obligations. P1 carries selected solution options/rungs, applicable requirements, and supporting evidence identities/URIs. Rejected and superseded obligations, opaque node content, evidence bodies, and memory-candidate text are omitted. P1 is dropped before P0; P0 stays intact or compilation declines to the transcript fallback. Short threads can also fall back when a packet would not save tokens. Request diagnostics report `p0PacketTokens`, `p1PacketTokens`, `workPacketTokens`, `evidencePacketTokens`, plus `workUpdateSpecTokens`, `workUpdateCallTokens`, and `workUpdateResultTokens`; these are attribution fields within existing totals, not additional charged tokens.

Phase 5a introduced validated memory candidates as internal graph data. Phase 5b can now promote eligible candidates into `UMCODE.md` when explicitly enabled, as described below. Existing user-directed project-instruction editing remains available through normal file tools; no user-project graph file is generated.

### Curated project memory (off by default)

Automatic promotion requires both flags:

```yaml
models:
  designed_workflow: true
memory:
  auto_promote: true
  target_file_bytes: 4096
```

`memory.auto_promote` defaults to `false`. Enabling it without `models.designed_workflow` emits `designed_workflow_disabled` and performs no promotion reads or writes. `target_file_bytes` is a soft ceiling for the complete target file, including user content; invalid limits fail configuration validation. Projectless Work and Work without pending candidates skip promotion.

After successful Work completion commits, the engine synchronously qualifies concise project facts and approved decisions against canonical evidence and final verification freshness. It adds no model call. The next applicable turn receives guidance once through the existing root-to-leaf instruction composition. Promotion does not change completed product criteria or evidence freshness, and a promotion failure never reopens or fails completed Work.

Successful Designed-workflow `verification.run` observations with a matching criterion and workspace fingerprint create engine-owned `verified_command` facts. Their content records `command`, `evidence_id`, and `source_revision`; candidates must use category `command`, the exact canonical text `Use <command> for this repository`, and cite that passing attempt's evidence. The final revision and the observed command must match. Only commands executed at the project root (empty directory or `.`) and unchanged by secret redaction create promotable facts; other verification attempts remain recorded without generating command guidance. Approved decisions project only their canonical title, under category `approved_decision`, and require evidence linked to that decision or its current approval criterion. Other free-form fact/category combinations fail closed; no semantic inference or rewriting occurs. Clients still cannot create observational facts through `work.update`.

Only `UMCODE.md` is eligible. For scoped facts, the target is the nearest existing `UMCODE.md` shared by all scope paths; cross-cutting scopes fall back to the root. The root file may be created, but nested instruction files are never created automatically. Foreign instruction files, including `AGENTS.md`, `CLAUDE.md` and global `AGENT.md`, are never scanned or written.

Entries appear under `## Verified project memory` and `<!-- umcode:generated -->`. SQLite records ownership and provenance. User text outside this section is preserved byte-for-byte; replacement requires an exact unchanged recorded generated bullet in a single well-formed section. External edits, deletion, movement or malformed sections end automatic ownership and conflict without overwriting the file. Replacements preserve the superseded historical row, and normal project diff and turn undo include each automatic write.

Exact duplicates already present in user text activate a row marked `user_owned` without a write or a claim of generated ownership. Repeated duplicate candidates and subsequent unrelated insertions remain supported. Adopted text cannot authorize replacement. If replacement guidance already exists elsewhere, promotion conservatively conflicts and leaves the old row active rather than claiming successful supersession. Indented list continuations and raw HTML structures that prevent proving Markdown ownership also conflict with all bytes preserved.

The safe writer supports Darwin and Linux; other platforms fail closed with `unsupported_platform`. It preserves existing permissions and line endings, writes and syncs a sibling temporary file, checks the target's final hash and inode identity, atomically renames, then syncs the parent directory. The final check and rename are separate filesystem operations: atomic rename prevents partial-file visibility, but does not provide a kernel-level compare-and-swap against an external edit in that remaining interval. UMCode serializes its own promotions per project; arbitrary external writers must still coordinate.

SQLite and filesystem changes use a persisted operation with exact before/after bytes and hashes. Before rename, storage failures leave the target unchanged; size overflow remains pending without eviction or truncation. I/O failures retain a pending-repair operation. After rename, a history or SQLite failure can leave the generated file present while metadata needs repair. Snapshot matching includes file existence: a missing file differs from an existing empty file. Startup recovery retries an exact before state, finalizes an exact after state, and conflicts on a third state without overwriting it. History recording is idempotent by operation ID, so repeated recovery produces one file-change row and one active memory. Undo restores file bytes; it does not delete historical memory rows or reopen completed Work. Later ownership checks detect the missing or changed entry.

Logs expose metadata-only `memory promotion diagnostic` and `memory recovery diagnostic` events: project/Work/candidate/evidence identities, target path, fixed outcome/reason, before/after hashes and byte sizes, inserted/replaced/unchanged/conflict counts, and estimated instruction tokens added/context tokens avoided. Aggregate reports count promoted, rejected, stale, conflicted and pending candidates, plus completed/retried/conflicted/pending-repair recovery operations. Engine failure counters distinguish promotion and recovery failures, including contained panics. Diagnostics omit candidate/generated text, evidence bodies, tool output, secrets and approval rationale. Token estimates use roughly four bytes per token; avoided-context estimates count only duplicate suppression or authoritative replacement. Normal request usage includes promoted instructions and never subtracts estimated savings.

Validate repeated-task quality and cost without network calls or model variance:

```sh
go test ./internal/engine -run 'TestCuratedMemory(E2E|ReducesRepeatedDiscoveryWithoutQualityLoss)' -count=1 -v
```

The deterministic A/B test runs real file tools and repository verification with identical supported completions, answers and artifacts. It reports discovery calls, tool rounds, model calls, and cumulative second-turn input bytes/token estimates, including the promoted bullet's cost. These fixture measurements establish regression behavior, not a prediction of savings for every project.

### Evidence vault and staleness

Full verification output and failed-tool errors are kept in a content-addressed vault under `storage.vault_dir` (`objects/<2 hex>/<sha256>`), with the last 2 KB kept on the evidence row. Secrets (private keys, `sk-`/`ghp_`/`AKIA`/`xox` tokens, `Authorization`/`Bearer` values, `KEY`/`TOKEN`/`SECRET`/`PASSWORD` assignments) are masked **before** hashing and writing, so they never reach disk. Objects over 8 MB are clipped head and tail. If the vault cannot be written, the attempt is still recorded with a summary and the turn is unaffected.

A passing criterion goes `stale` when a file changes after it (including edits made through the shell, detected by a workspace fingerprint taken when an attempt is recorded and when the turn ends) or when the environment (OS, architecture, git HEAD, Go version) changes. A stale criterion keeps the work open. `work/get` accepts `activeOnly` to return only evidence that is still true, and each evidence entry reports `vaultHash`, `availability` (`none`, `available`, `unavailable`) and `envFingerprint`. `vault/stats` returns object count, bytes and bytes eligible for cleanup.

Old objects are removed automatically at startup and daily, never while an open work or active evidence references them. Defaults (optional keys under `storage`): `retention_raw_days` 30, `retention_stale_days` 30, `retention_blob_days` 90 (objects of 1 MiB or more), `retention_redacted_days` 7, and `vault_max_object_bytes` 8388608. None of this asks for approval, shows a prompt or adds chat output; fingerprints and cleanup are engine-internal, time-bounded and only touch vault files, never project files.

### Context compiler (off by default)

With `models.context_compiler: true`, a turn that has an open work record stops replaying the transcript. The model instead receives the current work state — the goal, each criterion with its status, the unresolved failures, the changed files, and the evidence that is still true — one line per check, the newest run only, and nothing for a criterion the packet has already called stale — with a vault reference for each failure's full output — followed by the last ten exchanges. A criterion whose pass no longer holds is shown as needing a re-run, never as a pass, and that is recomputed before every model call, so an edit made earlier in the same turn is reflected immediately. Secrets are masked on the way into the packets.

The packets are capped at 15% of the model's context window and the tail at 25%; passing-check lines are dropped before failures, and the goal and criteria are never dropped. The compiler refuses, and the engine silently replays the transcript as before, whenever the work has no goal, the goal and criteria alone do not fit, the failures that cannot be dropped still exceed the cap, or the compiled request would not be smaller than the transcript — which is the normal outcome for a short thread, since the tail already holds all of it. A read failure or a panic falls back the same way and is counted. The full transcript stays stored and visible in the app either way, and nothing here prompts, approves or adds chat output.

### Scoped retrieval (off by default)

Set `models.context_retrieval: true` together with `models.context_compiler: true` to recover relevant older context without another model call. Retrieval is independent of Designed workflow. It searches only the open current Work and the current thread's older user/assistant/inbound messages. Historical messages are quoted source data, never current decisions or verification authority. Graph traversal is limited to depth two and 128 nodes; lexical queries use at most 16 terms and 64 candidates per source.

Repository excerpts come only from successful built-in `file.read` and `file.search` observations. Each request rechecks workspace identity and the full file hash, with no repository scan or query-derived file paths. Darwin/Linux use descriptor-anchored reads that reject symlinks; other platforms omit excerpts. Secret paths and foreign instruction files, including `UMCODE.md`, are excluded. Validation reads at most four files of 256 KiB each; each excerpt is at most 40 lines/4 KiB.

Retrieval has a 100 ms deadline and fits at most 12 entries within 6,144 tokens or 5% of the model window, whichever is smaller, using space left in the existing 15% compiler packet budget. Mandatory packets and the retained conversation keep their space. Read errors, invalid snapshots, deadline expiry and panic discard retrieval atomically and preserve the base compiler input. Diagnostics contain counts, selected source identities and fixed fallback reasons; `retrievalPacketTokens` attributes tokens already counted in the request. Provider-reported usage remains authoritative.

Run `go test ./internal/engine -run 'TestRetrievalRecovers|TestRetrievalRestart' -count=1 -v` for the deterministic fixture. It performs real file observation, edit and verification: one representative run reduced model requests from 4 to 3 and cumulative estimated input tokens from 23,962 to 15,566, including retrieval overhead, with the same verified artifact. Disabling selection makes the savings assertion fail. These fixture measurements do not predict savings for every project.

### Progressive tool loading (off by default)

Set `models.progressive_tools: true` to send a stable subset of permitted schemas. This flag works independently of the context compiler, retrieval, reducers and Designed workflow. File operations, shell/exec lifecycle, verification, skill tools and the existing gated `work.update` remain available. Explicit request terms, typed pending browser checks and owned active sessions add matching families; retrieved prose cannot activate capabilities.

Use `tools.discover` to search for an omitted capability or load exact canonical/wire names. Discovery returns at most eight entries with redacted descriptions capped at 256 bytes; their schemas appear on the next model request. Queries are limited to 512 bytes and exact names to eight. Pagination cursors are authenticated and bound to the turn, catalog, query and permission revision. Discovery never executes the target, requests approval, runs plugin tool hooks or creates verification facts.

The engine freezes the actual permitted registry winners and acquired plugin snapshot once per turn. Canonical schema order and loaded schemas remain stable; trusted phase changes and discovery add to the union. The next turn prunes unused families while retaining active session lifecycle tools. Current permission revocation takes precedence over that union, and ordinary guessed calls still pass existing guards, workflow gates, scope and approval checks. A disappearing or replaced dynamic implementation returns unavailable rather than silently switching identity.

Selection/internal discovery errors, catalog validation errors and more than 64 additional loaded tools fall back to the full permitted catalog, without an engine-generated model or action call. Invalid discovery arguments return an ordinary bounded error. Catalogs are validated at 4,096 entries, metadata at 4 KiB per string, schemas at 256 KiB, and request classification at 8 KiB; validation failure preserves the existing full catalog. Reserved discovery aliases cannot shadow the engine tool, and ambiguous aliases cannot execute. Permission lookup failures fail closed until the next turn.

`context accounting.toolSelection` reports catalog/exposed counts, original full-catalog and actual schema estimates, discovery-schema and prompt overhead, discovery call/addition counts, hashed phase, bounded fallback reason and elapsed selection time. Full-schema baseline excludes the newly added discovery tool; actual schemas include it. Prompt and result costs are already in existing request totals and are never counted as provider usage credits.

Run `go test ./internal/engine -run 'TestProgressiveCoding|TestProgressiveSpecialist|TestPhase4Flags' -count=1 -v`. The deterministic coding fixture uses real file inspection, editing and a passing `go test ./...`: four model requests/three tool rounds in both arms, cumulative schema tokens 32,404 → 4,832 and total estimated input 50,718 → 23,266, including prompt overhead. Bypassing filtering makes the savings assertion fail. A direct specialist fixture uses two requests in both arms (estimated input 5,594 → 5,598); discovery uses three instead of two (5,592 → 7,671). These results demonstrate reachability and the cost of discovery, rather than universal savings. Neither flag is enabled by default; broad ablations, canary rollout and quiet UI remain Phase 6.

### Tool-result reducers (off by default)

Set `models.tool_result_reducers: true` to send a compact, deterministic projection of oversized owned tool results to the model. This flag is independent of `models.context_compiler` and remains off by default. Verification and browser checks, shell and execution sessions, file and web search, and Visual QA and Computer Use reports have format-aware reducers; explicit file reads and arbitrary MCP/plugin output stay unchanged. The full canonical result remains in thread items, exports, hooks, Work evidence, and the vault. Unknown or malformed formats, insufficient savings, missing required detail, and reducer panics fall back to the canonical result. Per-request diagnostics report final `toolResultTokens`, matching-result `toolResultOriginalTokens`, reducer-only `toolResultSavedTokens`, and `toolResultsReduced`; emergency context trimming is never counted as reducer savings.

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

The `skills` and `mcp_servers` blocks above are legacy compatibility paths. New capabilities should be installed from the Plugins screen or `umcode plugin ...` commands.

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
| Plugins | `plugin/inspect`, `plugin/install`, `plugin/list`, `plugin/get`, `plugin/setEnabled`, `plugin/configure`, `plugin/reload`, `plugin/uninstall` |
| Legacy skills, MCP and tools | `skill/list`, `skill/get`, `skill/install`, `skill/remove`, `mcp/list`, `mcp/restart`, `tool/list` |

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
internal/projects/   projects, UMCODE.md composition, file trees, diffs and undo
internal/pathutil/   the containment rule every tool shares (symlink-safe, /var vs /private/var)
internal/skills/     SKILL.md loader, runtimes/venvs, skill tools, install/remove
internal/mcp/        MCP client (stdio + Streamable HTTP) and tool bridge
internal/plugins/    package adapters, atomic installer, project capability snapshots
internal/hooks/      bounded lifecycle hook runner and audit records
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
