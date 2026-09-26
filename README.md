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

---

For the current Go engine and desktop app build instructions, see [GO_ENGINE.md](GO_ENGINE.md). The rest of this README covers the legacy Python assistant.

## Legacy Python app quick start

**Requirements:** Python 3.11+, a Telegram bot token (from [@BotFather](https://t.me/BotFather)), and an API key for Claude, OpenAI, or Gemini.

```bash
git clone https://github.com/shaktsin/umcode
cd umcode
make install     # create venv, install deps
make init        # interactive setup wizard
make run         # start in foreground (Ctrl+C to stop)
```

`make init` walks you through:
1. Choosing your AI provider and model
2. Setting up your control panel (web or Telegram bot)
3. Connecting integrations (Google Workspace for Gmail/Calendar)
4. Adding message connectors (Telegram account, Discord, or Gmail IMAP)
5. Configuring workspaces (sandboxed directories the agent can work in)
6. Installing skills

After setup, run `make doctor` to verify everything is wired up correctly.

---

## Running

| Command | What it does |
|---------|-------------|
| `make run` | Start in foreground with web panel (Ctrl+C to stop) |
| `make start` | Start as a background daemon |
| `make stop` | Stop the daemon |
| `make restart` | Restart the daemon |
| `make status` | Check if it's running |
| `make logs` | Tail the live log |
| `make reload` | Hot-reload config without restart |

Run `make help` for the full command list.

---

## Connectors

Connectors are split into two roles that determine how messages flow:

| Role | Types | Behaviour |
|------|-------|-----------|
| **listener** | `gmail_imap`, `telegram_user`, `discord` | Inbound-only. PII-filtered. Summaries forwarded to your control panel for review. |
| **admin** | `telegram_bot`, web panel | Bidirectional. Your private control interface. Receives all notifications and approval requests. |

The role is assigned automatically from the connector `type` — you never configure it manually.

### Listener connectors

```yaml
connectors:
  # Watch your Gmail inbox via IMAP IDLE (no GCP required)
  - name: gmail_imap
    type: gmail_imap
    mailbox: INBOX          # defaults to INBOX

  # Read all your personal Telegram chats
  - name: my_account
    type: telegram_user
    api_id: null
    api_hash: null
```

When a new email or message arrives:
1. PII (email addresses, phone numbers, SSNs) is masked before storage
2. A lightweight LLM call classifies importance and suggests an action
3. Low-importance noise is silently discarded — no LLM cost
4. Everything else is summarised and sent to **all** your admin panels simultaneously
5. If a reply is appropriate, a draft is prepared for your review before sending

### Admin connectors (control panel)

```yaml
control_panel:
  enabled: true
  ui_type: telegram        # telegram | web
  connector: my_bot
  chat_id: "123456789"     # your personal Telegram ID
```

You can run multiple admin panels simultaneously (e.g. web panel at home + Telegram on mobile):

```yaml
control_panels:
  - enabled: true
    ui_type: web
    web_host: 127.0.0.1
    web_port: 8080
  - enabled: true
    ui_type: telegram
    connector: my_bot
    chat_id: "123456789"
```

---

## Configuration

The Python runtime keeps its configuration in UMCode's per-user configuration directory. The easiest way to generate it is `make init`.

To see a fully-annotated example of every option:

```bash
cat config.example.yaml
```

**Key sections:**

| Section | Purpose |
|---------|---------|
| `llm` | AI provider, model, API key |
| `control_panel` | Your private UI for approvals |
| `connectors` | Chat channels the bot listens on |
| `tools.workspaces` | Sandboxed directories with per-dir ACLs |
| `skills` | Per-skill env vars and node/python overrides |
| `skill_dirs` | Directories scanned for skills at startup |
| `agents` | Orchestrator + worker model, iteration limits |
| `security` | Role-based tool access, SSRF protection |
| `policy` | Approval strictness + declarative ACL rules (`rules` / `rules_file`) |

**Secrets** are never stored in `config.yaml`. They're kept in macOS Keychain (automatic) or read from the supported environment variables for your installation.

---

## Skills

Skills are packaged capabilities — a folder with a `SKILL.md` manifest and scripts in Python, Bash, or Node.js. The bot discovers them automatically at startup.

**Install a skill:**
```bash
make skill-add SKILL=./path/to/skill-folder
make skill-add SKILL=https://github.com/<owner>/<skill-repository>
```

**List loaded skills:**
```bash
make skills
```

**Add a skill directory** (all sub-folders with `SKILL.md` are loaded):
```yaml
# UMCode configuration
skill_dirs:
  - ~/projects/skills/skills
```

**Example SKILL.md:**
```yaml
---
name: web_search
version: 1.0.0
description: Search the web and return results
runtime:
  type: python
  timeout_seconds: 30
---
```

Skills run in isolated subprocesses with their own virtualenv. They can only use tools explicitly allowlisted in their manifest.

---

## Security

UMCode has a layered security model so you stay in control of what the bot does.

### Tool risk tiers

Every tool has a risk level:

| Tier | Examples | Behaviour |
|------|---------|-----------|
| 🟢 **GREEN** | file.read, web search | Runs automatically |
| 🟡 **YELLOW** | file.write, API calls | Runs automatically (can require approval in strict mode) |
| 🔴 **RED** | shell.run, file.delete | **Requires your explicit approval** |

When a RED tool is triggered, you get a message on your control panel like:

```
⚠️ Approval needed
Tool: shell.run
Command: rm -rf ~/old-project

Reply: YES abc123def456ghij
```

The token is single-use with 128-bit entropy. If you don't respond, it times out and the action is cancelled.

### Secrets

- API keys and tokens are **never written to `config.yaml`** — stored in Keychain or env vars
- Secrets are masked in logs (`***last4`)
- `config.yaml` and `*.session` files are git-ignored

### Workspaces

Agents only operate inside configured workspace directories. Each workspace has a fine-grained ACL:

```yaml
tools:
  workspaces:
    - name: builds
      path: ~/umcode-workspace
      acl:
        read: true
        write: true
        create_files: true
        delete_files: false   # agents cannot delete files here
        shell: true
```

### Declarative ACL Rules

For connector-agnostic inbound/outbound policy, use `policy.rules` (inline) or
`policy.rules_file` (external YAML). Rules can:
- block or require confirmation for tools (`apply.tool`)
- decide whether inbound listener messages are sent to LLM (`apply.ingest_to_llm`)
- override listener intent (`apply.set_action`, `apply.set_importance`, `apply.set_needs_admin`)

```yaml
policy:
  rules_file: ~/umcode-policies/default.yaml
  rules:
    - id: gmail-search-explicit-admin
      priority: 20
      match:
        tools: ["gmail.search"]
        admin_explicit: false
      apply:
        tool: deny
        reason: "gmail.search requires explicit admin request."
```

---

## Scheduled Tasks

You can schedule tasks directly from chat:

```
task daily 09:00 summarize my inbox and send me the highlights
task weekly mon 08:30 pull my calendar and prepare a weekly brief
task once 2026-04-01T10:00 remind me to file quarterly taxes
tasks list
tasks cancel 3
```

Results are sent to your control panel.

---

## Further Reading

For internals, architecture diagrams, connector protocol, and deployment guides see [ARCHITECTURE.md](ARCHITECTURE.md).
