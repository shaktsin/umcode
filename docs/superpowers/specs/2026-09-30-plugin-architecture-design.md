# UMCode Plugin Architecture Design

**Date:** 2026-09-30
**Status:** Approved in conversation; awaiting written-spec review

## Purpose

UMCode will make a plugin the only user-facing installation and management unit for extensions. A plugin may contribute skills, MCP servers, hooks, and future capability types. Existing standalone skills and MCP server configuration remain backend-compatible during migration, but the desktop Plugins screen will no longer expose them as separate products.

UMCode will prefer the portable Agent Plugins package format and will install compatible Codex and Claude Code plugin folders through adapters. All supported source formats normalize into one internal model before validation or activation.

## Goals

- Add a first-class plugin layer above the existing skill and MCP runtimes.
- Support portable Agent Plugin, legacy Codex, and Claude Code plugin folders.
- Provide managed, atomic, versioned installation with an explicit linked development mode.
- Install plugins globally and enable or disable them per project.
- Implement hooks from manifest parsing through turn and tool execution, diagnostics, and audit records.
- Present only plugins as installable extensions in the desktop UI.
- Test installation, activation, skill use, MCP use, hooks, project enablement, rollback, and removal through the backend RPC boundary.

## Non-goals

- Replacing the existing skill or MCP execution engines.
- Turning built-in tools such as Visual QA or Computer Use into plugins.
- Executing every capability declared by Claude or Codex in the first release. Unsupported components remain visible through compatibility diagnostics.
- Automatically installing plugins declared by a project for other collaborators.
- Running install-time scripts from third-party packages.

## Package Format Strategy

The preferred authoring format is a portable root `plugin.json` using a supported Agent Plugins schema. Portable components use conventional root locations such as `skills/`, `mcp.json`, and `hooks/`. UMCode-specific metadata belongs under `extensions.dev.umcode`; UMCode will not introduce a separate public manifest format.

Format detection uses this precedence:

1. A root `plugin.json` with a supported Agent Plugins schema.
2. `.codex-plugin/plugin.json`.
3. `.claude-plugin/plugin.json`.
4. Rejection with a diagnostic listing the manifests UMCode checked.

When a portable package also contains a Codex overlay, root identity and portable components remain canonical. The overlay contributes only supported Codex-specific presentation or runtime metadata. The folder is one plugin, not two.

Each adapter only detects, parses, and normalizes its source format. It does not copy files, start processes, mutate configuration, or activate capabilities.

## Normalized Plugin Model

Every adapter produces a `PluginPackage` containing:

- stable plugin identity, display metadata, and version;
- detected source format and provenance;
- normalized skill roots;
- normalized MCP server declarations;
- normalized hook declarations;
- typed supported and unsupported component inventory;
- plugin configuration schema and secure-secret references;
- required versus optional component status;
- compatibility diagnostics;
- resolved package-relative resources that have passed containment checks.

Plugin identities namespace contributed skills, MCP servers, hooks, and future named capabilities. Namespacing prevents collisions between two enabled plugins without merging their implementation runtimes.

Unknown or unsupported components must never disappear silently. They produce structured compatibility diagnostics and remain visible in the plugin detail view.

## Ownership and Boundaries

`internal/plugins` owns source materialization, format detection, adapters, package validation, inspection, installation, updates, global installation records, per-project enablement, capability snapshot composition, reload, and uninstall.

The existing `internal/skills` package continues to own skill parsing, instruction loading, environments, and script execution. Plugin skills enter it through plugin-owned skill roots.

The existing `internal/mcp` package continues to own MCP transports, processes, connections, tools, and restart behavior. Plugin MCP declarations enter it through namespaced server configurations.

A new `internal/hooks` package owns normalized hook declarations, matching, subprocess execution, input/output envelopes, limits, result validation, and hook outcome reporting.

Built-in tools remain core capabilities registered outside the plugin manager.

Legacy standalone skill directories and configured MCP servers enter their existing runtimes through legacy contributors. They do not appear as installable plugins and are not shown on the Plugins screen.

## Installation and Storage

Plugins are installed once for the current user and enabled independently per project.

Normal installation accepts a local folder or Git URL and follows this sequence:

1. Materialize the source in a temporary directory when necessary.
2. Detect exactly one source format.
3. Normalize the source into `PluginPackage`.
4. Validate identity, schema versions, containment, symlinks, component schemas, executable declarations, configuration references, and duplicate names.
5. Return a pre-install report containing the detected format, component inventory, executable processes, requested configuration, unsupported components, warnings, and approval requirements.
6. Install only from a valid inspection token bound to the inspected content digest. A changed source requires a new inspection.
7. Copy the package to a staging directory, revalidate the staged copy, and atomically activate it.
8. Record version, source provenance, content digest, active state, and installation mode.
9. Enable the plugin for the selected project only after activation succeeds.

Managed packages live under:

```text
~/.umcode/plugins/cache/<source-id>/<plugin-name>/<version-or-content-digest>/
```

`source-id` is the first 16 lowercase hexadecimal characters of the SHA-256 digest of the canonical source locator. A valid manifest version is used as the final segment; an absent or path-unsafe version is replaced by the first 16 hexadecimal characters of the inspected content digest.

Mutable plugin data lives separately under:

```text
~/.umcode/plugins/data/<source-id>/<plugin-name>/
```

An update installs and validates a new version before switching the active installation. Activation failure preserves the previous version. Obsolete versions may be removed only after the new version becomes active and no capability snapshot references the old version.

Uninstall refuses while any project enables the plugin unless the request explicitly disables those project references. Uninstall removes managed package code. Mutable plugin data is retained by default and may be removed only through a separate explicit operation.

### Linked development mode

Linked mode is explicit and accepts only a local folder. It never becomes the default merely because a source is local. The UI labels linked plugins and explains that source edits affect execution.

Reload re-runs detection, normalization, and validation, then atomically replaces the active capability snapshot. A failed reload leaves the prior valid snapshot active.

## Configuration and Secrets

Global installation records package identity and provenance. Project configuration records plugin enablement and validated non-secret plugin settings.

Installation, active-version, project-enablement, settings, component-health, and hook-audit records are persisted in the existing SQLite store through new migrations. The filesystem cache contains package payloads only and is not the authority for enablement.

Secret values are stored through UMCode's protected credential mechanism. Manifests and project settings contain references, never secret values. Hook logs, compatibility reports, audit records, and model-visible context redact secret values.

Plugins containing executable hooks or subprocess MCP servers require explicit review during installation. UMCode does not support third-party install scripts in this release.

## Capability Snapshots

When a project opens or its enabled plugin set changes, `PluginManager` builds an immutable capability snapshot. Each turn captures one snapshot at start. Installing, updating, reloading, disabling, or uninstalling a plugin cannot change capabilities midway through a turn.

Disabling a plugin removes its capabilities from new snapshots. Plugin MCP processes shut down after active snapshots release them. A plugin update may coexist temporarily with its previous version while active turns finish.

A required component failure prevents activation and preserves the previous working snapshot. An optional component failure marks that component unavailable while allowing other plugin components to activate.

## Skills and MCP Integration

Plugin skill roots are contributed to the existing skill registry. Skill identities are namespaced by plugin. The short skill catalog remains lazily expanded through the existing instruction-loading flow, and skill scripts continue to use the existing runtime and approval model.

Plugin MCP declarations are contributed to the existing MCP manager with namespaced server IDs. The MCP manager continues to expose server tools through the tool registry, apply tool risk policy, and own connection lifecycle. Plugin disablement and snapshot retirement close only the connections owned by that plugin version.

## Hook Runtime

The normalized hook runtime supports these events in the first release:

- `TurnStart`
- `BeforeToolUse`
- `AfterToolUse`
- `ToolUseFailed`
- `TurnComplete`
- `BeforeCompaction`
- `AfterCompaction`

Codex and Claude adapters map compatible source events to these names. Source events without a safe UMCode equivalent produce compatibility diagnostics.

### Hook input

Each hook receives a versioned JSON envelope on standard input containing:

- plugin identity and version;
- project, thread, and turn identity;
- normalized event name;
- project working directory;
- event timestamp;
- bounded tool name, arguments, output, or error when applicable.

The first envelope version is integer `1`. Tool arguments, output, and errors are each limited to 64 KiB in the envelope; truncation is explicit in the envelope metadata.

### Hook output

A hook returns a versioned JSON result with these optional effects:

- `continue`: continue normal execution;
- `block`: stop the pending turn or tool action with a user-visible reason;
- `context`: add bounded text to the current model context;
- `warning`: emit a user-visible non-blocking diagnostic.

The first result version is integer `1`. A block reason is limited to 4 KiB, a warning to 8 KiB, and injected context to 32 KiB. Results that exceed a field limit are invalid rather than silently truncated.

Only `TurnStart` and `BeforeToolUse` hooks may block. After-events cannot rewrite a completed result. `BeforeToolUse` runs after built-in tool safety guards and before UMCode's approval decision. A hook may block or increase scrutiny, but it cannot bypass a policy denial or required approval.

### Hook execution

- Hooks run as subprocesses from the project root.
- They receive a minimal environment plus declared non-secret plugin settings and explicit secure references.
- Each declaration defaults to a 10-second timeout and may request at most 60 seconds. Standard output is limited to 256 KiB and standard error to 64 KiB.
- Blocking-event hooks execute in deterministic plugin-identity order and then manifest declaration order.
- Observational after-event hooks may execute concurrently, with at most four hook processes running for one event.
- A required before-hook timeout, crash, malformed response, or oversized output fails closed.
- An optional before-hook failure emits a warning and continues.
- An after-hook failure is recorded but never changes the completed tool or turn outcome.
- Every invocation and outcome is recorded in the audit log and attributed to its plugin, project, thread, and turn.

## Backend Protocol

The desktop app uses only plugin-focused RPC methods:

- `plugin/inspect`
- `plugin/install`
- `plugin/list`
- `plugin/get`
- `plugin/setEnabled`
- `plugin/configure`
- `plugin/reload`
- `plugin/uninstall`

`plugin/inspect` is non-mutating and returns a random, single-use inspection token bound to the source locator, content digest, detected adapter, and requested installation mode. The token expires after 15 minutes and is invalidated after a successful install. `plugin/install` requires that token and fails if the source content no longer matches.

The legacy `skill/*` and `mcp/*` routes remain temporarily available for CLI and migration compatibility. They are deprecated and unused by the desktop Plugins screen.

Plugin errors include a stable code, operation phase, safe user-facing message, remediation, and plugin/component identity when known. Phases are detect, adapt, validate, inspect, install, activate, configure, hook, and uninstall.

Plugin health states are healthy, compatibility warning, configuration required, partially unavailable, and activation failed.

## Desktop UI

The current Extensions screen becomes a plugin-only screen.

The installation flow accepts a local directory or Git URL and requires inspection before installation. Its review dialog shows format, identity, version, installation mode, grouped components, executable processes, requested settings, unsupported components, compatibility warnings, and approval requirements.

Installed plugin cards show plugin identity, version, origin, managed or linked status, current-project enablement, and aggregate health. Actions include configure, enable or disable, update, reload for linked plugins, and uninstall.

The detail view may show skills, MCP servers, hooks, and other capabilities only as nested components of the owning plugin. It also shows component health, compatibility diagnostics, and hook failures.

The Plugins screen removes:

- standalone Skills installation and inventory;
- standalone MCP server inventory and restart controls;
- Built-in plugins cards for Visual QA and Computer Use;
- the generic tools-available-to-the-agent inventory.

Visual QA and Computer Use keep their existing project settings.

## Compatibility and Migration

Existing standalone skills and MCP configuration continue working after the plugin layer lands. No automatic destructive conversion occurs.

The desktop UI stops offering standalone installation and management. Existing CLI commands remain during a deprecation window. A future migration may wrap legacy entries in synthetic internal ownership records, but they must not masquerade as installed marketplace plugins.

Claude agents, LSP declarations, and any other component without a first-release UMCode runtime appear as unsupported components. Installing a plugin with unsupported optional components is allowed after the warning is shown. An unsupported component declared as required prevents activation.

## Verification

Adapter contract fixtures for portable, Codex, and Claude formats normalize equivalent supported components and retain format-specific warnings. Tests cover path traversal, symlink escape, identity mismatch, duplicate names, malformed declarations, unsupported schemas, literal secrets, and source mutation after inspection.

Store tests cover managed copying, source deletion after install, linked reload, atomic rollback, concurrent installation, project enablement persistence, immutable turn snapshots, safe uninstall, and mutable data retention.

Hook tests cover every normalized event, deterministic ordering, concurrent observational hooks, blocking behavior, context injection, required and optional failures, timeouts, output limits, malformed output, secret redaction, approval invariants, audit attribution, and Codex/Claude event mapping.

The server E2E harness will generate a portable plugin with an executable skill, fake stdio MCP server, and hooks implemented by the test binary. Through normal RPC calls, the test will inspect, install, enable, configure, use the skill, invoke the MCP tool, observe hook behavior, verify tool and audit records, disable, and uninstall the plugin. It will mutate or delete the source after installation to prove the cached copy is used. Separate E2E cases cover hook blocking, activation rollback, a Codex package, and a Claude package.

Frontend tests verify inspect-before-install, plugin-only RPC usage, nested component presentation, compatibility and hook diagnostics, managed and linked modes, and the absence of standalone Skills, MCP, built-in plugin, and generic tool sections.

The release verification commands are:

```bash
go test -race ./...
go vet ./...
cd app/frontend && npm run check
cd app/frontend && npm test
cd app/frontend && npm run build
```

## Delivery Boundaries

Implementation should land as reviewable vertical slices, but the feature is complete only when the plugin-only UI and the backend E2E scenario both work. Intermediate compatibility shims must not become parallel permanent plugin systems.
