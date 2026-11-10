# First-run, providers, composer and approvals UX Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** New users land on a Providers screen, providers become icon tiles, and the composer toolbar gets complexity, approval-mode and "⋯" menus with approval requests docked above the text box.

**Architecture:** Frontend only (Svelte 5 in `app/frontend/src`). Decisions that can be tested without a DOM live in small pure modules under `lib/` (`firstRun.ts`, `providerTiles.ts`, `complexityPill.ts`, `approvalModes.ts`, `approvalDock.ts`). Svelte components are thin and reuse `PopupSurface.svelte` for menu placement and `ApprovalCard.svelte` for the approval body. No RPC or Go changes.

**Tech Stack:** Svelte 5 runes, TypeScript, Tailwind 4, vitest (`svelte/server` render for component tests), Playwright scripts in `app/frontend/e2e`.

**Spec:** `docs/superpowers/specs/2026-10-23-first-run-and-composer-ux-design.md`. Approved mockups: https://claude.ai/artifact/CpAaMyHCtzJ8fP7nH7RDUB

**Working directory:** worktree `.worktrees/first-run-composer-ux`, branch `ux/first-run-composer-redesign`, cut from master `4a4d9fe`. All paths below are relative to `app/frontend/` unless they start with `docs/` or `e2e/`. Commands run from `app/frontend/`. Run `npm ci` once before Task 1 (the worktree has no `node_modules`). `docs/` is git-ignored, so commit plan and spec files with `git add -f`.

## Global Constraints

- No backend, RPC or Go changes. Reuse `approval/respond`, `thread/setApprovalMode`, `credential/*`, `delegation/enable`.
- Every toolbar pill is 28px tall (`var(--ui-compact)`) with 6px gaps. Toolbar order: attach, model, complexity pill, approval-mode pill, "⋯", Agents chip (when on), send/stop on the right.
- Copy, verbatim: welcome strip "Welcome to UMCode" / "Connect one model provider to start chatting. You can add more later."; composer placeholder "Connect a provider to chat"; tab label "Providers" (tab id stays `keys`); menu item "Agents for next message" with hint "Lets the model hand work to sub-agents. Turns off after sending."; chip text "Agents" with title "Agents are on for the next message" and ✕ aria-label "Turn agents off"; pending line "Waiting for your approval"; records "Approved · <summary>", "Approved for this chat · <summary>", "Denied · <summary>".
- Approval modes and hints: "Ask every time" / "Approve each command and edit"; "Auto-approve workspace" / "Edits inside the project folder run freely"; "Auto-approve all" / "Nothing asks. Use with care." (separated by a rule).
- Complexity levels keep today's hints: Auto "Pick per message", Quick "Fast, little or no thinking", Standard "Balanced", Deep "More thinking for complex work". Choosing the selected level returns to the inherited default. `compact` (side chats) shows one-letter labels.
- Only "OpenAI-compatible" gets the "+" tile. Provider icons are inline SVG shapes or monograms, never vendor logos.
- Dock shows only approvals with the thread's id and no `agentChildId` (agent-child approvals stay in `Agents.svelte`). The thread record lives in frontend state and is not restored after restart.
- `ModelPicker` keeps `modelPickerOptions` (from #79) for its model list.
- Accessibility: pills are buttons with `aria-haspopup="menu"` and `aria-expanded`; the dock is `role="region"` with `aria-live="assertive"`; menus close on Escape and outside click (provided by `PopupSurface`).

## Review Focus

1. Startup before the catalog loads (empty credential arrays) must not redirect to Settings. Owned by Task 1.
2. An approval that belongs to an agent child must not appear in the composer dock. Owned by Task 5.
3. Two pending approvals: the dock shows the oldest with a "1 of 2" count, and answering it reveals the next. Owned by Task 5.
4. `approval/respond` failing leaves the approval pending and writes no record. Owned by Task 5.
5. Side-chat (`compact`) complexity pill shows one letter and a default-inherited level still shows its name. Owned by Task 3.

---

### Task 1: First-run flow

**Files:**
- Create: `src/lib/firstRun.ts`, `src/lib/firstRun.test.ts`
- Modify: `src/lib/stores/app.svelte.ts`, `src/App.svelte`, `src/lib/panels/Settings.svelte`, `src/lib/components/Composer.svelte`

**Interfaces:**
- Produces (`firstRun.ts`):
  - `interface FirstRunInput { conn: string; catalogReady: boolean; hasEnabledCredential: boolean }`
  - `shouldRedirectToProviders(input: FirstRunInput): boolean`
  - `composerPlaceholder(base: string, needsProvider: boolean): string`
- Produces (`AppState`): `catalogReady = $state(false)`, `get hasEnabledCredential(): boolean`, `get needsProvider(): boolean` (conn `'open'`, `catalogReady`, no enabled credential).

- [ ] **Step 1: Commit the spec and plan.** `git add -f docs/superpowers/specs/2026-10-23-first-run-and-composer-ux-design.md docs/superpowers/plans/2026-10-23-first-run-and-composer-ux.md && git commit -m "docs: spec and plan for first-run and composer UX"`.
- [ ] **Step 2: Write `src/lib/firstRun.test.ts`** with these cases, each asserting `shouldRedirectToProviders`: open + ready + no credential → `true`; `conn: 'closed'` and `conn: 'connecting'` → `false`; `catalogReady: false` → `false` (Review Focus 1); `hasEnabledCredential: true` → `false`. Plus `composerPlaceholder('Ask about x', true) === 'Connect a provider to chat'` and `composerPlaceholder('Ask about x', false) === 'Ask about x'`.
- [ ] **Step 3: Run** `npx vitest run src/lib/firstRun.test.ts`. Expected: FAIL, module not found.
- [ ] **Step 4: Implement `firstRun.ts`** with the signatures above.
- [ ] **Step 5: Run the same command.** Expected: PASS.
- [ ] **Step 6: Extend `AppState`** in `app.svelte.ts`: add `catalogReady`, set it `true` at the end of `refreshCatalog()` after the assignments (a failed `Promise.all` leaves it false), and add the two getters.
- [ ] **Step 7: Redirect in `App.svelte`.** Add an `$effect` that returns early unless `app.catalogReady` and a local `firstRunHandled` is false; then sets `firstRunHandled = true` and, if `shouldRedirectToProviders({ conn: app.conn, catalogReady: app.catalogReady, hasEnabledCredential: app.hasEnabledCredential })`, sets `app.view = 'settings'`. The flag is set whether or not it redirected, so it runs at most once per launch and never traps a user who navigates away.
- [ ] **Step 8: `Settings.svelte`.** Change the `keys` tab label to "Providers" and, above the tab row, render the welcome strip (copy in Global Constraints, accent-soft background, `role="status"`) while `app.needsProvider`.
- [ ] **Step 9: `Composer.svelte`.** Wrap the textarea placeholder with `composerPlaceholder(placeholder, app.needsProvider)` (keep the ` · / for skills` suffix only when not `needsProvider`), add `|| app.needsProvider` to the send button's `disabled`, and return early from `submit()` when `app.needsProvider`.
- [ ] **Step 10: Verify.** `npx vitest run` and `npm run check`. Expected: all pass, no svelte-check errors.
- [ ] **Step 11: Commit** `git add -A src && git commit -m "feat: open Providers on first launch when none is connected"`.

### Task 2: Provider tiles

**Files:**
- Create: `src/lib/providerTiles.ts`, `src/lib/providerTiles.test.ts`, `src/lib/panels/settings/ProviderTile.svelte`, `src/lib/panels/settings/ProviderTile.test.ts`
- Modify: `src/lib/panels/settings/Keys.svelte`, `e2e/pools.mjs`

**Interfaces:**
- Produces (`providerTiles.ts`):
  - `interface ProviderTileModel { id: string; name: string; subtitle: string; connected: boolean; keyCount: number; isAdd: boolean }`
  - `providerTiles(providers: Provider[], credentials: Credential[]): ProviderTileModel[]`
- Produces (`ProviderTile.svelte`): props `{ tile: ProviderTileModel; expanded: boolean; onclick: () => void }`; a `<button>` with `aria-expanded`.

- [ ] **Step 1: Write `providerTiles.test.ts`.** Cases: empty `providers` falls back to ids `['claude','openai','gemini','openai_compatible']` with names "Anthropic", "OpenAI", "Gemini", "OpenAI-compatible"; `openai_compatible` is last even when listed first, and only it has `isAdd: true`; two enabled credentials and one disabled for `claude` → `keyCount: 2`, `connected: true`, `subtitle: '2 keys'`; one credential → `'1 key'`; unconnected subtitles: claude "API key", openai "API key or ChatGPT", gemini "API key", openai_compatible "Ollama, LM Studio, vLLM"; an unknown provider id `foo` with `displayName: 'Foo AI'` → name "Foo AI", subtitle "API key".
- [ ] **Step 2: Run** `npx vitest run src/lib/providerTiles.test.ts`. Expected: FAIL.
- [ ] **Step 3: Implement `providerTiles.ts`** (name map, subtitle map, enabled-credential count, add tile last).
- [ ] **Step 4: Run again.** Expected: PASS.
- [ ] **Step 5: Write `ProviderTile.test.ts`** using `svelte/server` `render`: a connected tile renders "Connected" and `aria-expanded="false"`; the add tile renders "+" and "OpenAI-compatible"; an unknown id renders its first letter as the monogram.
- [ ] **Step 6: Implement `ProviderTile.svelte`.** 28px icon box, name, subtitle, green "Connected" badge when `tile.connected`, dashed border and "+" icon when `tile.isAdd`, accent border when `expanded`. Inline SVG per id (`claude`, `openai`, `gemini`, `openai_compatible`), monogram otherwise. Run the test; expected PASS.
- [ ] **Step 7: Rework `Keys.svelte`.** Replace the header Connect button, the provider `<select>` and the "Nothing connected yet" card with a grid (`grid-template-columns: repeat(auto-fit, minmax(min(100%, 180px), 1fr))`, gap 10px) of `ProviderTile`. Add `let openProvider = $state<string | null>(null)`; a tile click toggles it and sets the existing `provider` state to that id (and resets `authMode` to `'key'`). Below the grid, when `openProvider` is set, show one panel: the existing add form (without the provider dropdown; `adding` is replaced by `openProvider !== null`) followed by that provider's existing credential rows from `grouped`. Do not edit `add`, `test`, `update`, `rotate`, `remove`, `editBudget`, `saveBudget`, `startSignIn`, `cancelSignIn`; the row markup moves unchanged.
- [ ] **Step 8: Update `e2e/pools.mjs`** where it clicks "Connect" and selects `openai_compatible` in the add form's combobox (around line 47): click the "OpenAI-compatible" tile instead and drop the `selectOption` call.
- [ ] **Step 9: Verify.** `npx vitest run` and `npm run check`. Expected: pass.
- [ ] **Step 10: Commit** `git add -A src e2e && git commit -m "feat: provider tiles replace the connect form"`.

### Task 3: Pill menu and complexity pill

**Files:**
- Create: `src/lib/pillMenu.ts`, `src/lib/complexityPill.ts`, `src/lib/complexityPill.test.ts`, `src/lib/components/PillMenu.svelte`, `src/lib/components/PillMenu.test.ts`
- Modify: `src/lib/components/ModelPicker.svelte`, `src/app.css`

**Interfaces:**
- Produces (`pillMenu.ts`): `interface PillMenuItem { id: string; label: string; hint?: string; checked?: boolean; disabled?: boolean; separatorBefore?: boolean }`
- Produces (`PillMenu.svelte`): props `{ ariaLabel: string; label?: string; icon?: Snippet; items: PillMenuItem[]; onselect: (id: string) => void; title?: string; disabled?: boolean }`. Without `label` it renders an icon-only pill. Trigger is a `<button class="pill-button">` with `aria-haspopup="menu"` and `aria-expanded`; items are `role="menuitem"` buttons inside `PopupSurface` (`kind="menu"`, `preferredSide="above"`); selecting closes the menu and calls `onselect(id)`.
- Produces (`complexityPill.ts`): `COMPLEXITY_LEVELS: { id: 'auto'|'quick'|'standard'|'deep'; label: string; hint: string }[]`; `complexityPillLabel(selected: Complexity | undefined, inherited: Complexity | undefined, compact: boolean): string`; `nextComplexity(current: Complexity | undefined, picked: Complexity): Complexity | undefined`.

- [ ] **Step 1: Write `complexityPill.test.ts`:** `complexityPillLabel('deep','auto',false) === 'Deep'`; `(undefined,'standard',false) === 'Standard'`; `(undefined,undefined,false) === 'Auto'`; `('quick','auto',true) === 'Q'`; `(undefined,'standard',true) === 'S'` (Review Focus 5); `nextComplexity('deep','deep') === undefined`; `nextComplexity('auto','deep') === 'deep'`; `nextComplexity(undefined,'quick') === 'quick'`.
- [ ] **Step 2: Run** `npx vitest run src/lib/complexityPill.test.ts`. Expected: FAIL.
- [ ] **Step 3: Implement `complexityPill.ts`** (levels and hints from Global Constraints; unset and unknown inherited fall back to Auto).
- [ ] **Step 4: Run again.** Expected: PASS.
- [ ] **Step 5: Write `PillMenu.test.ts`** with `render`: closed state renders the label, `aria-haspopup="menu"`, `aria-expanded="false"`, and none of the item labels; icon-only (no `label`) renders the `ariaLabel` as `aria-label`.
- [ ] **Step 6: Implement `pillMenu.ts` and `PillMenu.svelte`.** Menu item markup: check mark column, bold label, muted hint; `separatorBefore` draws a rule above. Run the test; expected PASS.
- [ ] **Step 7: `app.css`.** Add `.pill-button` (height `var(--ui-compact)`, padding-inline 9px, gap 6px, 1px `--color-line-strong` border, `--ui-control-radius`, paper background, 12px text, accent border when `[aria-expanded="true"]`, `.icon-only` variant 32px wide). Change `.composer-toolbar` gap to 6px and `.composer-toolbar > .model-picker-controls` to `flex: 0 1 auto`. Remove the `.complexity-control` rules.
- [ ] **Step 8: `ModelPicker.svelte`.** Replace the `.complexity-control` button group with `<PillMenu>` (bolt icon, `complexityPillLabel(value.complexity, app.complexity?.default, compact)`, items from `COMPLEXITY_LEVELS` with `checked` on the active one). `onselect` calls `onchange({ ...value, complexity: nextComplexity(value.complexity, id as Complexity) })`. Leave the model popup and `modelPickerOptions` use untouched.
- [ ] **Step 9: Verify.** `npx vitest run`, `npm run check`, then `npm run dev` and open a chat: the pill opens upward, lists four levels with hints, and selecting changes the label. Check light and dark.
- [ ] **Step 10: Commit** `git add -A src && git commit -m "feat: complexity dropdown pill in the composer toolbar"`.

### Task 4: Approval-mode pill

**Files:**
- Create: `src/lib/approvalModes.ts`, `src/lib/approvalModes.test.ts`, `src/lib/components/ApprovalModePill.svelte`, `src/lib/components/ApprovalModePill.test.ts`
- Modify: `src/lib/components/Composer.svelte`, `src/lib/components/ChatColumn.svelte`

**Interfaces:**
- Consumes: `PillMenu`, `PillMenuItem` (Task 3).
- Produces (`approvalModes.ts`): `APPROVAL_MODES: { id: 'normal'|'auto_workspace'|'auto_all'; label: string; hint: string; separatorBefore?: boolean }[]`; `approvalModeLabel(mode: ApprovalMode | undefined): string` (`''`, `undefined` and `'normal'` → "Ask every time").
- Produces (`ApprovalModePill.svelte`): props `{ view: ThreadView }`. Renders nothing when `view.thread` is null; otherwise a `PillMenu` (shield icon, current label) whose `onselect` calls `app.try('thread/setApprovalMode', { threadId: view.id, mode })`.

- [ ] **Step 1: Write `approvalModes.test.ts`:** labels for `''`, `undefined`, `'normal'` → "Ask every time"; `'auto_workspace'` → "Auto-approve workspace"; `'auto_all'` → "Auto-approve all"; `APPROVAL_MODES` has three entries and only `auto_all` has `separatorBefore`.
- [ ] **Step 2: Run** `npx vitest run src/lib/approvalModes.test.ts`. Expected: FAIL. Implement `approvalModes.ts`; run again; expected PASS.
- [ ] **Step 3: Write `ApprovalModePill.test.ts`** with `render`, mocking `$lib/stores/app.svelte` as `Agents.test.ts` does: `view = { id: null, thread: null }` → body does not contain `aria-haspopup`; `view = { id: 't1', thread: { approvalMode: 'auto_workspace' } }` → body contains "Auto-approve workspace".
- [ ] **Step 4: Implement `ApprovalModePill.svelte`.** Run the test; expected PASS.
- [ ] **Step 5: `Composer.svelte`.** Render `<ApprovalModePill {view} />` directly after `<ModelPicker>` in the toolbar.
- [ ] **Step 6: `ChatColumn.svelte`.** Delete the header `<select>`, `approvalLabel`, and `setApprovalMode`.
- [ ] **Step 7: Verify.** `npx vitest run`, `npm run check`; in `npm run dev`, switching modes in a chat with a thread persists (reload shows the new label).
- [ ] **Step 8: Commit** `git add -A src && git commit -m "feat: approval mode pill in the composer toolbar"`.

### Task 5: Docked approvals and thread records

**Files:**
- Create: `src/lib/approvalDock.ts`, `src/lib/approvalDock.test.ts`, `src/lib/stores/app.approvals.test.ts`, `src/lib/components/ApprovalDock.svelte`, `src/lib/components/ApprovalDock.test.ts`
- Modify: `src/lib/stores/app.svelte.ts`, `src/lib/components/Composer.svelte`, `src/lib/components/ChatColumn.svelte`, `src/app.css`

**Interfaces:**
- Produces (`approvalDock.ts`):
  - `type ApprovalDecision = 'approved' | 'approved_always' | 'denied'`
  - `interface ApprovalRecord { id: string; threadId: string; turnId: string; summary: string; decision: ApprovalDecision }`
  - `dockState(approvals: Approval[], threadId: string | null): { current: Approval | null; total: number }` (same thread, no `agentChildId`, oldest `createdAt` first)
  - `buildRecord(approval: Approval, approve: boolean, remember: boolean): ApprovalRecord` (`summary` is `approval.actionSummary || \`Run ${approval.tool}\``)
  - `recordLine(record: ApprovalRecord): string`
  - `recordsFor(records: ApprovalRecord[], threadId: string, turnId: string): ApprovalRecord[]`
- Produces (`AppState`): `approvalRecords = $state<ApprovalRecord[]>([])`; `respondApproval` unchanged in signature.
- Produces (`ApprovalDock.svelte`): props `{ approvals: Approval[]; threadId: string | null }`.

- [ ] **Step 1: Write `approvalDock.test.ts`** with approval fixtures (`createdAt` ISO strings): `dockState` for two pending approvals on thread `t1` returns the older as `current` and `total: 2` (Review Focus 3); an approval with `agentChildId: 'c1'` is excluded and `{ current: null, total: 0 }` when it is the only one (Review Focus 2); approvals of another thread are excluded; `threadId: null` → `{ current: null, total: 0 }`. `recordLine` returns `'Approved · go test ./...'`, `'Approved for this chat · go test ./...'`, `'Denied · go test ./...'`. `buildRecord(a, true, true).decision === 'approved_always'`, `(a,true,false)` → `'approved'`, `(a,false,true)` → `'denied'`. `recordsFor` filters by thread and turn and keeps order.
- [ ] **Step 2: Run** `npx vitest run src/lib/approvalDock.test.ts`. Expected: FAIL. Implement `approvalDock.ts`; run again; expected PASS.
- [ ] **Step 3: Write `app.approvals.test.ts`.** Import `app`; seed `app.approvals` with one approval for thread `t1`. With `vi.spyOn(app, 'try').mockResolvedValue(undefined)`, `await app.respondApproval('a1', true)` leaves the approval in `app.approvals` and `app.approvalRecords` empty (Review Focus 4). With `mockResolvedValue({})`, a denied response removes it and appends one record with `decision: 'denied'` and the right `threadId` and `turnId`.
- [ ] **Step 4: Run it; expected FAIL. Update `respondApproval`** in `app.svelte.ts`: find the approval before the call, and on a defined result remove it and append `buildRecord(approval, approve, remember)` to `approvalRecords`. Run again; expected PASS.
- [ ] **Step 5: Write `ApprovalDock.test.ts`** with `render` (mock `$lib/stores/app.svelte` as in `Agents.test.ts`): one pending approval renders `role="region"`, the risk text, the action summary, "Deny" and "Allow once", and no "of 2" text; two approvals render "1 of 2"; none renders an empty body.
- [ ] **Step 6: Implement `ApprovalDock.svelte`:** amber top-rounded container (`id="approval-dock"`, `aria-live="assertive"`, `aria-label="Approval needed"`), header with shield icon, "Approval needed", risk (`{risk} risk`) and "1 of N" when `total > 1`, body is `<ApprovalCard approval={current} chatContext />` inside an element with `id={\`approval-${current.id}\`}`. Add `.composer-docked` rules in `app.css` (composer top radius 0 and amber top border). Run the test; expected PASS.
- [ ] **Step 7: `Composer.svelte`.** Render `<ApprovalDock approvals={app.approvals} threadId={view.id} />` as the sibling directly above the element with `data-composer`, and add class `composer-docked` to that element while `dockState(app.approvals, view.id).current` is set.
- [ ] **Step 8: `ChatColumn.svelte`.** Remove the in-thread `<section id="approval-…">` card and the effect near lines 73-76 that scrolls to it; remove the `ApprovalCard` import if now unused. For each turn group, render one muted line "Waiting for your approval" (with the shield icon) while `app.approvals` has an approval for `view.id` and `g.turnId`, then one line per `recordsFor(app.approvalRecords, view.id, g.turnId)` using `recordLine` (✓ sage for approved, ✕ rust for denied).
- [ ] **Step 9: Verify.** `npx vitest run`, `npm run check`; in `npm run dev` with the fake provider (see `e2e/README.md`), trigger a command approval: the dock appears above the text box, Approve resolves it, the thread shows "Approved · …".
- [ ] **Step 10: Commit** `git add -A src && git commit -m "feat: dock approval requests above the composer"`.

### Task 6: Agents in the "⋯" menu

**Files:**
- Create: `src/lib/components/AgentsMenu.svelte`, `src/lib/components/AgentsMenu.test.ts`
- Modify: `src/lib/components/DelegationActivation.svelte`, `src/lib/components/Composer.svelte`

**Interfaces:**
- Consumes: `PillMenu` (Task 3), `DelegationState` (`state.active`, `state.token`, `state.pending`).
- Produces (`AgentsMenu.svelte`): props `{ state: DelegationState; busy?: boolean; onenable: () => void }`. Renders nothing while `state.active`. Otherwise an icon-only `PillMenu` (`ariaLabel="More options"`) with one item `{ id: 'agents', label: 'Agents for next message', hint, checked: !!state.token, disabled: busy || state.pending || !!state.token }` whose selection calls `onenable()`, followed by the chip when `state.pending` ("Enabling…") or `state.token` ("Agents" + ✕ button that sets `state.token = null`).
- `DelegationActivation.svelte` keeps its props but renders only the `state.error` alert (existing behavior and test).

- [ ] **Step 1: Write `AgentsMenu.test.ts`** with `render` and a fake state: default → contains `aria-label="More options"` and not "Turn agents off"; `token` set → contains title "Agents are on for the next message" and `aria-label="Turn agents off"`; `pending: true` → contains "Enabling…"; `active` set → body contains neither "More options" nor "Agents".
- [ ] **Step 2: Run** `npx vitest run src/lib/components/AgentsMenu.test.ts`. Expected: FAIL. Implement `AgentsMenu.svelte`; run again; expected PASS.
- [ ] **Step 3: Reduce `DelegationActivation.svelte`** to the error paragraph and keep the existing test in `Agents.test.ts` green.
- [ ] **Step 4: `Composer.svelte`.** Render `<AgentsMenu state={view.delegation} {busy} onenable={() => chat.enableDelegation(view)} />` after `<ApprovalModePill>` in the toolbar. Keep `<DelegationActivation>` under the text box for the error line only. Keep the send button's `view.delegation.pending` disable.
- [ ] **Step 5: Verify.** `npx vitest run`, `npm run check`; in `npm run dev` open ⋯, choose the item: chip "Agents" appears, ✕ removes it, and the next message is sent with the activation token as before.
- [ ] **Step 6: Commit** `git add -A src && git commit -m "feat: move Enable agents into the more-options menu"`.

### Task 7: End-to-end updates and final verification

**Files:**
- Create: `e2e/first-run.mjs`
- Modify: `e2e/delegation.mjs`, `e2e/chat.mjs`, `e2e/pools.mjs`, `e2e/ui-contract.mjs`, `e2e/ui-polish.mjs`, `e2e/README.md`

- [ ] **Step 1: Find stale selectors.** Run `grep -nE "Enable agents|Waiting for your approval|Allow once|approval-|complexity|Complexity|option\\[value|composer.*select|Connect\\b" e2e/*.mjs` and list every hit that targets removed UI (header approval `<select>`, the Auto/Quick/Standard/Deep button group, the in-thread approval card, the "Enable agents for next message" button, the Connect button).
- [ ] **Step 2: Update them.** `delegation.mjs` (around line 77): click `getByRole('button', { name: 'More options' })`, then `getByRole('menuitem', { name: /Agents for next message/ })`. Approval assertions in `chat.mjs`: look for the card inside `#approval-dock` instead of the thread. Complexity assertions: open the complexity pill (`aria-haspopup="menu"`, name starts with the level label) and pick from `getByRole('menuitem')`. `pools.mjs` line ~74 (`div[aria-label="Message composer"] select`): confirm it still targets the model selector and adjust only if the model picker changed. `ui-contract.mjs` composer case: assert pill height is 28px.
- [ ] **Step 3: Write `e2e/first-run.mjs`** following the structure of `e2e/chat.mjs`: start with an engine home that has no credentials, load the UI, assert the Settings view is shown with the "Welcome to UMCode" strip and tiles for Anthropic, OpenAI, Gemini and OpenAI-compatible; navigate to Chat and assert the composer placeholder is "Connect a provider to chat" and send is disabled and that the page does not return to Settings by itself; go back to Settings, connect an `openai_compatible` key at `http://127.0.0.1:18997/v1` through its tile, and assert the strip disappears, the tile shows "Connected", and the composer accepts text.
- [ ] **Step 4: Document** the new script in the list in `e2e/README.md`.
- [ ] **Step 5: Run the suite** per `e2e/README.md`: `node e2e/first-run.mjs`, `node e2e/chat.mjs`, `node e2e/delegation.mjs`, `node e2e/pools.mjs`, `node e2e/ui-contract.mjs`, then `SCHEME=light node e2e/chat.mjs`. Expected: every script exits 0.
- [ ] **Step 6: Whole-branch checks.** `npx vitest run`, `npm run check`, `npm run build`. Expected: all pass. Manually check a 480px-wide window (toolbar wraps, send stays at the right), a side chat (one-letter complexity pill, dock works), light and dark themes.
- [ ] **Step 7: Commit** `git add -A e2e && git commit -m "test: update e2e for first-run flow and composer controls"`.
