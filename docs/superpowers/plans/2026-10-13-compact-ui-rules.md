# Compact UI Rules Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give UMCode consistent compact controls, uncluttered chat/plugin surfaces, and verified behavior throughout desktop window resizing.

**Architecture:** Centralize geometry in CSS tokens and reusable controls. Separate pure shell/popup layout calculations from Svelte focus and presentation behavior. Keep feature state in existing stores while changing only its presentation.

**Tech Stack:** Svelte 5, TypeScript, Tailwind CSS 4, Vitest, Vite, Playwright/Chrome, Go/Wails desktop shell.

**Spec:** `docs/superpowers/specs/2026-10-13-compact-ui-rules-design.md` (approved; read it alongside this plan).

## Global Constraints

- Standard single-line controls: 32 px; explicit compact variants: 28 px. Border-box sizing.
- Icon targets: 32 × 32 px, or 28 × 28 px compact; icons: 16 × 16 px.
- Control typography: 13/20 px; secondary: 12/16 px; body: existing 14 px scale; titles: 18/24 px.
- Layout spacing: 4, 8, 12, 16, 24, 32 px. Related controls: 8 px gap; cards: 16 px padding; sections: 24 px separation.
- Ordinary menu rows: minimum 32 px; description rows: minimum 48 px. Preserve readable wrapping.
- Control/card/composer radii: 8/12/16 px. Preserve existing colors and system fonts.
- Main-window minimum: 760 × 520; default: 1180 × 780. Mini overlay resizing is out of scope.
- Expanded/collapsed rail: 280/64 px; auto-collapse below 960 px without changing the saved preference.
- Main chat region: minimum 440 px. Auxiliary preferred widths: 280–720 px; include dividers in fit calculations.
- Popups: 8 px viewport inset; ordinary preferred widths 200–360 px, subject to trigger and viewport rules; skill popup up to composer width/768 px; list height at most 320 px and available space.
- Remove composer Workspace selection and successful route text; retain actionable failure feedback and backend route/worktree behavior.
- Plugin inventories are collapsed Details disclosures by default. Keep actionable errors visible.
- No new product dependencies are required. Keep global-installed-skills changes intact and separate from UI commits.
- Deliver a local build for review. Do not push, merge, promote release branches, or publish as part of this plan.

## Review Focus

- A popup opened near a window edge during resizing must remain reachable without losing keyboard selection (Task 2).
- Persisted panel widths larger than the current window must not squeeze chat or overwrite user preferences (Task 3).
- Moving focus into Retry or an auxiliary overlay must not dismiss it before the action completes (Tasks 2 and 3).
- Long plugin names, diagnostics, and large inventories must not hide actions or force document overflow (Task 5).
- Growing textarea content at minimum height must preserve visible Send/Stop controls and the draft (Task 4).

## Files and responsibilities

- `app/frontend/src/app.css`: shared geometry tokens/utilities, rail/panel and typography rules.
- `app/frontend/src/lib/ui/popupGeometry.ts` and `.test.ts`: pure popup positioning and available-space calculation.
- `app/frontend/src/lib/ui/portal.ts`: mount popup content outside clipping ancestors and restore/remove it on teardown.
- `app/frontend/src/lib/components/PopupSurface.svelte`: popup presentation, positioning observers, focus/dismiss events; accepts feature content as a Svelte snippet.
- `app/frontend/src/lib/ui/shellLayout.ts` and `.test.ts`: pure inline/overlay and rail policy.
- `app/frontend/src/App.svelte`, `Rail.svelte`, `SideChats.svelte`, `Inspector.svelte`: integrate shell policy and accessible panel controls.
- `app/frontend/src/lib/components/Composer.svelte` and `ModelPicker.svelte`: compact groups and shared popups, remove unwanted composer metadata.
- `app/frontend/src/lib/panels/Extensions.svelte` and `components/PluginInventory.svelte`: compact summary and explicit inventory disclosure.
- `app/frontend/src/lib/components/Dialog.svelte` and `panels/Settings.svelte`: apply shared form/popup geometry in primary settings/dialog surfaces; preserve existing actions.
- `app/frontend/e2e/ui-fixtures.mjs`: deterministic browser fixture server mounting real Svelte surfaces with fake RPC boundaries.
- `app/frontend/e2e/ui-contract.mjs`: interactive geometry, focus, resize, disclosure, and overflow assertions.
- `app/frontend/e2e/README.md`: test commands, fixture assumptions, screenshot outputs.

## Task 1: Shared compact controls and browser fixture contract

**Files:** Modify `app/frontend/src/app.css`; create `app/frontend/e2e/ui-fixtures.mjs` and `ui-contract.mjs`.

**Interfaces:** Export `startUiFixtureServer(): Promise<{url: string; close(): Promise<void>}>` from `ui-fixtures.mjs`. Serve a test-only page with controls for standard/compact buttons, icon actions, input, select, textarea, and validation feedback. Later tasks add their scenarios to the same server. Use real feature components and mock only engine RPC, following `e2e/skills.mjs`.

- [ ] Record the pending global-skills diff and preserve it as a separate local prerequisite commit using its exact current file set. Create a `codex/compact-ui-rules` development branch from that state, following using-git-worktrees at execution time; preserve unrelated work and the approved spec/plan.
- [ ] Write `standard_and_compact_controls_have_shared_geometry` in the browser contract. Assert standard heights equal 32, compact heights equal 28, icon rectangles equal their target sizes, icons equal 16, related centers differ by at most 1 px, and gap equals 8. Assert help/error text does not change the field rectangle.
- [ ] Run `node e2e/ui-contract.mjs --case controls` from `app/frontend`; observe the current inconsistent dimensions failing.
- [ ] Add named CSS variables for the spec's sizes, spacing, line heights, and radii. Update shared `.btn`, `.btn-sm`, `.input`, `.select`, `.icon-button`, `.card`, label and title utilities. Use flex centering and explicit single-line control heights; leave textareas/content naturally sized.
- [ ] Rerun the controls browser case, then `npm test` and `npm run check`; expect passing assertions and zero Svelte errors/warnings.
- [ ] Commit only this task: `style: establish compact UI geometry tokens`.

## Task 2: Shared viewport-safe custom popups

**Files:** Create `src/lib/ui/popupGeometry.ts`, `popupGeometry.test.ts`, `portal.ts`, and `components/PopupSurface.svelte`; modify `components/ModelPicker.svelte`, `Composer.svelte`, and browser fixtures/contracts. Paths are relative to `app/frontend` for this and subsequent frontend tasks.

**Interfaces:**

```ts
type Rect = { left: number; top: number; width: number; height: number };
type PopupInput = {
  trigger: Rect; viewport: { width: number; height: number };
  preferredWidth: number; headerHeight: number;
  kind: 'menu' | 'skills'; preferredSide: 'above' | 'below';
};
type PopupGeometry = { left: number; top: number; width: number;
  maxListHeight: number; side: 'above' | 'below' };
function placePopup(input: PopupInput): PopupGeometry;
function portal(node: HTMLElement): { destroy(): void };
```

`PopupSurface` props: `open: boolean`, `anchor: HTMLElement | undefined`, `kind: 'menu' | 'skills'`, `preferredSide: 'above' | 'below'`, `onclose: (reason: 'escape' | 'outside') => void`, and `children: Snippet`. It positions a presentation wrapper; feature content supplies listbox/menu semantics. It exposes a scrollable list slot through a shared CSS class using calculated available height. Focus stays with the feature's appropriate trigger/listbox pattern; do not impose dialog-style focus containment on ordinary menus.

- [ ] Write `placePopup_stays_inside_edges`, `placePopup_flips_for_short_height`, and `wide_trigger_is_bounded_by_viewport`. For a 760 × 520 viewport, assert left/right/top bounds respect 8 px, width does not exceed 744, and list height is between 0 and 320. Include a 500 px trigger and an anchor near each edge.
- [ ] Write browser cases `open_menu_survives_resize`, `skill_selection_survives_reposition`, and `keyboard_retry_remains_reachable`. Assert menu remains visible inside bounds, active row and draft survive, Escape returns focus, outside click closes, and Shift+Tab can reach Retry.
- [ ] Run `npm test -- src/lib/ui/popupGeometry.test.ts` and `node e2e/ui-contract.mjs --case popups`; establish missing behavior with failing assertions.
- [ ] Implement the exact interfaces. Observe anchor/header bounds and viewport changes, listen to relevant scroll events, and clean up listeners/observers on dismissal. Mount custom popups outside clipping ancestors. Apply the spec's width priorities, row sizing, and bounded internal scrolling. Migrate ModelPicker and slash picker while preserving selection actions and insert-and-wait behavior.
- [ ] Run both targeted commands, `node e2e/skills.mjs`, and `npm run check`; expect all cases passing and no leaked duplicate popups after repeated opening.
- [ ] Commit: `feat: keep compact popups within window bounds`.

## Task 3: Responsive shell and accessible auxiliary overlays

**Files:** Create `src/lib/ui/shellLayout.ts` and `.test.ts`; modify `src/App.svelte`, `components/Rail.svelte`, `SideChats.svelte`, `Inspector.svelte`, `app.css`, and browser fixtures/contracts.

**Interfaces:**

```ts
type Auxiliary = 'side' | 'inspector';
type ShellInput = {
  width: number; height: number; railCollapsedPreference: boolean;
  sideRequested: boolean; inspectorRequested: boolean;
  sidePreferredWidth: number; inspectorPreferredWidth: number;
  activeAuxiliary: Auxiliary | null;
};
type ShellLayout = {
  railWidth: 64 | 280; mainWidth: number;
  inline: { side: number | null; inspector: number | null };
  overlay: { panel: Auxiliary; width: number } | null;
};
function resolveShellLayout(input: ShellInput): ShellLayout;
```

The shell owns most-recent auxiliary activation and overlay presentation. Stores retain actual conversations and tabs. Include 1 px dividers for inline panels. Rail persistence stores only manual preference, never automatic collapse. Existing column storage retains requested widths; effective geometry is derived.

- [ ] Write tests `minimum_window_preserves_main_width`, `both_panels_fit_on_wide_desktop`, `oversized_saved_widths_do_not_squeeze_chat`, and `rail_preference_restored_above_boundary`. Assert main width is at least 440, rail auto-collapses at 959 but restores preference at 960, at most one overlay exists, and widths plus dividers do not exceed viewport width.
- [ ] Add browser cases for both panels open at 760 × 520 and 1920 × 1080. Assert labeled controls can reach hidden panels, closing overlays preserves side IDs/inspector tabs, focus stays inside an open overlay and returns on close, and ResizeObserver/window resize does not rewrite stored widths.
- [ ] Run `npm test -- src/lib/ui/shellLayout.test.ts` and `node e2e/ui-contract.mjs --case shell`; observe failures before implementation.
- [ ] Implement the policy and integrate it into App/Rail, replacing the single `narrow` heuristic. Add overlay backdrop, labeled close controls, Escape handling, focus containment/restoration, and manual divider width persistence. Keep scroll regions bounded vertically.
- [ ] Rerun the tests across threshold crossings and a full shrink/grow cycle; verify draft, conversation IDs, tabs, and storage contents remain stable.
- [ ] Commit: `feat: adapt desktop panels to available window space`.

## Task 4: Stable, uncluttered composer and consistent primary forms

**Files:** Modify `components/Composer.svelte`, `ModelPicker.svelte`, `Dialog.svelte`, `panels/Settings.svelte`, `app.css`, and browser contracts.

**Interfaces:** Consume Task 1 geometry and Task 2 PopupSurface. Preserve existing `Composer` props `{view: ThreadView; compact?: boolean}` and model-selection callbacks. Keep workspaceMode and model routing in stores/backend; remove only obsolete composer presentation.

- [ ] Write `composer_omits_workspace_and_successful_route_text` for new Git-project, existing project, and projectless chats. Assert no Workspace selector or successful route label; assert attachment/model/complexity/Send controls remain and unavailable-model feedback is visible when applicable.
- [ ] Write `minimum_height_long_draft_keeps_send_visible`. Fill a long multiline draft at 760 × 520, resize while `/` is open, and assert textarea scrolls internally, Send/Stop stays within viewport, and the draft is unchanged. Assert actual send payload still defaults new project chats to `workspaceMode: 'local'` and preserves existing thread mode.
- [ ] Run `node e2e/ui-contract.mjs --case composer`; observe failing cleanup/geometry assertions.
- [ ] Remove Workspace markup and successful route chip plus unused display formatting. Preserve actionable errors and routing computation. Group toolbar controls with the compact variant, 8 px gaps, and wrapping. Bound textarea growth to available short-window space. Apply shared standard geometry to primary settings forms and dialog actions; avoid one-off height overrides.
- [ ] Rerun composer and controls cases, existing skills browser tests, `npm test`, `npm run check`, and `npm run build`; expect no selection/send regressions or clipped dialog actions.
- [ ] Commit: `style: simplify composer and align primary controls`.

## Task 5: Compact plugin summaries with explicit Details

**Files:** Modify `panels/Extensions.svelte`, `components/PluginInventory.svelte`, browser fixtures/contracts, and `src/lib/panels/Extensions.test.ts` only if existing assertions conflict with approved behavior.

**Interfaces:** Preserve `PluginInventory` props `{plugin: PluginInfo}`. The Extensions card maintains disclosure state keyed by `plugin.id`; refreshes and resizing do not reset it. Counts derive from existing `groupPluginComponents(plugin)` and omit zero categories.

- [ ] Write `plugin_inventory_is_explicit_disclosure` with a 15-skill plugin: initially names and source path are hidden, “15 skills” and Details are visible; activation reveals all 15 names and correct `aria-expanded`; closing restores the summary.
- [ ] Write `plugin_actions_survive_long_content`. At 760 × 520 use long name/source, nonzero MCP/hooks, and configuration/failure diagnostics. Assert actions remain reachable, actionable errors remain in the collapsed summary, details scroll within the page region, and the document does not overflow horizontally. Refresh the same ID and resize; assert disclosure state persists.
- [ ] Run `node e2e/ui-contract.mjs --case plugins`; verify the always-expanded inventory fails the first test.
- [ ] Implement the compact card and semantic Details disclosure. Place format/mode/source, full inventories, unsupported entries, and diagnostic history in Details. Omit empty groups. Clarify project integration enablement versus global skills. Use shared geometry for card actions and fields.
- [ ] Rerun plugin cases, `npm test`, and `npm run check`; verify removal/configuration/reload/update actions still call their existing RPC methods.
- [ ] Commit: `style: disclose plugin inventories on demand`.

## Task 6: Full resize matrix, visual review, and local build

**Files:** Complete `e2e/ui-contract.mjs`, `e2e/ui-fixtures.mjs`, and `e2e/README.md`; update any affected component from Tasks 1–5 only when failures justify it.

**Interfaces:** `ui-contract.mjs` accepts `--case controls|popups|shell|composer|plugins|all`, defaults to `all`, and supports `UI_ARTIFACT_DIR` for screenshots and a JSON check report. Use existing `PLAYWRIGHT_MODULE`/`CHROME_PATH` overrides from skills.mjs. Return nonzero on assertion, console, or unexpected request failures. Always close browser and fixture server.

- [ ] Complete the matrix at 760×520, 960×640, 1180×780, 1440×900, 1920×1080, and 1180×520 in light/dark themes. Assert all spec acceptance criteria in rendered DOM; allow at most 1 px rounding. Save screenshots for baseline and expanded popup/plugin/panel states, and inspect every saved state for clipping and alignment.
- [ ] Run `node e2e/ui-contract.mjs --case all`. Fix each observed failure, then rerun its case and affected matrix states. Record screenshot review findings alongside behavioral results; do not claim screenshot-only verification.
- [ ] Run `npm test`, `npm run check`, `npm run build`, and root `make go-vet`. Run root `make go-test` and app-module `go test ./...` if implementation changes Go/native code; otherwise carry forward the already verified global-skills baseline and report the exact scope of new checks.
- [ ] Request one independent review of the complete UI diff and spec coverage. Resolve actionable findings and rerun their affected checks.
- [ ] Build a separately named local macOS app with the existing bundle script and signing identity. Verify its signature, launch it without interrupting an active user session, and test native resizing/minimum dimensions. If native interaction cannot safely be exercised, report it as not run and provide the local build for user review.
- [ ] Update the test README with commands, dependency locations via overrides, expected report artifacts, and native checks. Commit: `test: verify compact UI across desktop sizes`.
- [ ] Deliver the local app, check report, screenshots, and any remaining limitations. Leave release publication for separate authorization.

## Plan self-review

Spec coverage: shared geometry (1), popup bounds/focus/scrolling (2), rail and panel resize policy/preferences (3), composer removal/forms/short-height behavior (4), plugin information hierarchy (5), complete theme/viewport matrix and native verification (6).

Review Focus conditions each have named behavioral tests in their owning task.
Interfaces above use consistent types and filenames. No product dependency or
release workflow change is required. Existing global-skills work is preserved
as an explicit prerequisite rather than mixed into UI task commits.
