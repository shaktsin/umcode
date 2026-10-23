# First-run, providers, composer and approvals UX

Date: 2026-10-23
Status: spec approved by the user; implementation plan next
Base: master 4a4d9fe (includes #79). Branch: ux/first-run-composer-redesign
Mockups: https://claude.ai/artifact/CpAaMyHCtzJ8fP7nH7RDUB (version 2, approved)

## Intent

New UMCode users should reach a working chat without hunting for settings, and the
chat composer should be calm: few controls, each clearly named, with permission
requests where the user is already looking.

Success looks like:

- A new install with no provider connected opens on a Providers screen, and the user
  can connect one provider in a few clicks.
- Providers read as a set of recognizable tiles, not a form with a dropdown.
- The composer toolbar holds model, complexity, approval mode and a "⋯" menu, in one
  consistent row of 28px pills.
- A pending approval is visible next to the text box without scrolling, and the
  thread keeps a one-line record of the decision.

## Decisions made with the user

| Topic | Decision |
|---|---|
| First launch | Redirect to Settings on the Providers tab when no provider has an enabled credential. |
| Providers | One rectangular tile per provider with a small icon. Only the OpenAI-compatible tile uses "+". |
| Complexity | Auto / Quick / Standard / Deep collapse into one dropdown pill. |
| Approval mode | Moves from the chat header into the composer toolbar as a dropdown pill. |
| Approval requests | Dock directly above the text box. The thread shows a one-line record. |
| "Enable agents for next message" | Moves into a "⋯" menu. A blue "Agents" chip shows while it is on. |

## Out of scope

- Backend or RPC changes. Every action below already exists
  (`approval/respond`, `thread/setApprovalMode`, `credential/*`, `chat.enableDelegation`).
- Vendor logos. Provider icons are simple shapes and monograms in the accent color.
- Approvals that belong to a delegated agent child (`agentChildId` set). They stay in
  the Agents panel (`Agents.svelte`).
- The Approvals panel (`panels/Approvals.svelte`).
- Persisting the thread approval record across app restarts (see section 3).

## 1. First launch

**Behavior.** When the engine connection is open and the catalog has loaded, and no
credential is enabled, switch `app.view` to `settings` with the Providers tab selected.
This happens at most once per app launch. If the user navigates away, UMCode does not
redirect again until the next launch. Once any credential is enabled the redirect never
fires.

**Welcome strip.** The Settings page shows a blue strip above the tabs while no
credential is enabled: "Welcome to UMCode. Connect one model provider to start
chatting. You can add more later." It disappears as soon as a credential is enabled.

**Chat before setup.** With no enabled credential, the empty chat keeps its existing
"Connect a provider" button, and the composer placeholder becomes "Connect a provider to
chat". Send is disabled. This reuses `hasKeys` from `ChatColumn.svelte`.

**State.** `AppState` gains `catalogReady` (set true after the first successful
`refreshCatalog`) so the redirect never fires on the initial empty arrays.
The `firstRunHandled` flag lives in `App.svelte` and is not persisted.

**Files.** `app/frontend/src/App.svelte`, `lib/stores/app.svelte.ts`,
`lib/panels/Settings.svelte`, `lib/components/Composer.svelte`.

## 2. Provider tiles

**Layout.** The "API keys" tab is renamed "Providers" (tab id stays `keys`). The
Connect button and provider dropdown are removed. The tab shows a grid of tiles,
`repeat(auto-fit, minmax(min(100%, 180px), 1fr))`, one per entry in `app.providers`
(falling back to the four known ids when the list is empty). Each tile shows an icon,
the provider name, and a one-line subtitle ("API key", "API key or ChatGPT", "1 key ·
default").

**States.**

- Not connected: neutral border.
- Connected (at least one enabled credential): green "Connected" badge and the key count.
- Expanded: accent border, panel opens directly below the grid.

**OpenAI-compatible.** Rendered last, with a dashed border and a "+" in its icon box.
It is the only tile with "+" because it is the only provider that takes a custom base
URL and can be added more than once (Ollama, LM Studio, vLLM).

**Expanded panel.** Clicking a tile toggles its panel (only one open at a time).

- The add form is the existing form with the provider preset and the provider
  dropdown removed. Fields: API key (optional for OpenAI-compatible), base URL
  (OpenAI and OpenAI-compatible; required for OpenAI-compatible), label, "use as
  default". OpenAI keeps the "Add a key / Sign in with ChatGPT" switch.
- If the provider has credentials, their rows appear in the same panel using the
  existing row markup and actions unchanged (default star, fallback, enabled, test,
  budget, replace, delete).
- The `add()`, `test()`, `rotate()`, `remove()`, budget and ChatGPT sign-in functions
  are not modified.

**Icons.** A small map from provider id to an inline SVG. Unknown ids render a monogram
of the first letter.

**Files.** `lib/panels/settings/Keys.svelte` only. Extract
`lib/panels/settings/ProviderTile.svelte` for the tile so `Keys.svelte` stays readable.

## 3. Composer toolbar and approvals

### Toolbar

Order, left to right: attach, model, complexity pill, approval-mode pill, "⋯", then the
send/stop button on the right. All pills are 28px tall with 6px gaps. The toolbar
wraps rather than clips at narrow widths.

**Complexity pill.** Replaces the four-button strip in `ModelPicker.svelte`. It shows a
bolt icon and the current level, or the inherited default when unset. The menu lists
Auto, Quick, Standard, Deep, each with today's hint text. Choosing the selected level
returns to the inherited default, as `setComplexity` does today. The `compact` prop
(side chats) keeps one-letter labels in the pill.

**Approval-mode pill.** A shield icon and the mode name: "Ask every time",
"Auto-approve workspace", "Auto-approve all". The menu lists the three modes with a
one-line description each. "Auto-approve all" is separated by a rule and its
description reads "Nothing asks. Use with care." Selecting calls
`thread/setApprovalMode` exactly as `setApprovalMode` does in `ChatColumn.svelte`.
The pill appears once `view.thread` exists, matching the header select it replaces.
The header select is removed.

**Model list.** The model menu keeps the contents it has on master (`modelPickerOptions`
from #79). Only the complexity control and its position change.

**Menus.** Anchored to their button, opening upward, left-aligned to the button. The
"⋯" menu is right-aligned to its button so it never leaves the window. Escape and
outside click close all menus.

### Docked approval

**Source.** Pending approvals for the active thread: `app.approvals` filtered to
`threadId === view.id` and no `agentChildId`. If several are pending, show the oldest
and a "1 of N" count in the dock header.

**Component.** New `lib/components/ApprovalDock.svelte`, rendered by `Composer.svelte`
directly above the text box inside the composer wrapper. It has a top-rounded amber
border and the composer drops its top radius while a dock is shown, so the two read as
one object. The dock reuses `ApprovalCard.svelte` with `chatContext` for its body, so
labels, "Review details" and expiry text stay exactly as the card renders them today
("Allow once", "Always allow in this chat" when remembering is allowed, "Deny").
The button labels in the mockup are illustrative; the real labels are the existing ones.

**Thread.** `ChatColumn.svelte` no longer renders the full approval card in the thread.
While an approval is pending it renders one line: "Waiting for your approval".
After a response it renders "Approved · <action summary>", "Approved for this chat ·
<summary>" or "Denied · <summary>".

**Record persistence.** The record is kept in frontend state keyed by thread id and
turn id, appended by `app.respondApproval`. It is not restored after an app restart.
Persisting it would need backend support and is out of scope.

**Files.** `lib/components/Composer.svelte`, `lib/components/ModelPicker.svelte`,
`lib/components/ChatColumn.svelte`, new `lib/components/ApprovalDock.svelte`,
`lib/stores/app.svelte.ts` (decision record). `ApprovalCard.svelte` is reused as is.

## 4. Agents in the "⋯" menu

**Menu.** The "⋯" pill opens a menu with one item, "Agents for next message", with the
description "Lets the model hand work to sub-agents. Turns off after sending."
A check mark shows when it is on.

**Chip.** While agents are enabled for the next message, a blue "Agents" chip with an
✕ appears in the toolbar after "⋯". Its tooltip reads "Agents are on for the next
message". The ✕ clears the token (`state.token = null`, as the Remove button does now).
While activation is pending the chip reads "Enabling…".

**Behavior.** Unchanged: the item calls `chat.enableDelegation(view)`; it is disabled
while `state.pending`, `busy`, or a send is in flight. When `state.active` is true the
control is hidden, as today. The `state.error` message still renders under the composer.

**Files.** `lib/components/DelegationActivation.svelte` is rewritten as the menu item
plus the chip, rendered inside the toolbar. `Composer.svelte` places it.

## Error handling

- `thread/setApprovalMode` failure: toast via `app.try` (existing), pill keeps the
  previous value.
- `approval/respond` failure: the approval stays in `app.approvals`, so the dock stays
  visible and the user can retry. No record line is written.
- An approval that expires: it disappears from `app.approvals` on the next refresh and
  the dock closes. The "Waiting for your approval" line is removed and no record is
  written, because no decision was made.
- Provider add/test failures: unchanged (existing toasts and test result line).
- Redirect when the engine is offline: not attempted (`app.conn !== 'open'`).

## Accessibility

- All pills are buttons with `aria-haspopup="menu"` and `aria-expanded`.
- The dock has `role="region"` with an `aria-label` and `aria-live="assertive"`, as the
  in-thread card has today.
- Tiles are buttons with `aria-expanded`. Focus returns to the pill or tile that
  opened a menu or panel.
- The first-run Settings page does not steal focus from the first tile.

## Testing

Unit (vitest, existing `*.test.ts` pattern):

- First-run decision: redirect only when conn open, catalog ready, no enabled
  credential, and not already handled; never after a credential exists.
- Approval-dock selection: filters by thread, excludes agent-child approvals, shows
  oldest first with the correct count.
- Decision record: appended on respond, not appended on failure, no record on expiry.
- Complexity pill label: explicit level, inherited default, compact one-letter form.

End to end (existing `app/frontend/e2e/*.mjs` harness): update the scripts that
reference the removed UI, in particular `delegation.mjs` ("Enable agents for next
message") and any script that selects the header approval `<select>` or the
`Complexity` button group (`ui-contract.mjs`, `ui-polish.mjs`, `chat.mjs`). Add a
first-run script: fresh profile opens on Providers with the welcome strip, connecting
a provider removes the strip and enables send.

Manual: check light and dark themes, a 480px-wide window, and a side chat in `compact`
mode.

## Risks

- `ModelPicker.svelte` is also used for side chats. The new pill must keep the compact
  behavior. Covered by the compact label test and the manual check.
- E2E scripts depend on the old selectors. They are listed above and updated in the
  same change.
