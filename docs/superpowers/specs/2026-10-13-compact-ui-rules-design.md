# UMCode compact UI and resize rules

## Intent and approved direction

Make UMCode feel consistent and uncluttered while users resize its desktop
window. The user chose compact density and approved shared style tokens and
reusable controls, responsive panel behavior, compact plugin summaries, and
measurable browser verification.

Explicit requirements:

- Remove the Workspace selector and model-route text such as “Claude Sonnet 5
  +4” from the chat composer.
- Show plugin capability counts by default; reveal the full inventory on demand.
- Make control dimensions, menu rows, dropdown widths, alignment, and spacing
  consistent.
- Verify the UI at different widths and heights, including light and dark themes.

Preserve the current palette, native system typography, and desktop character.
This is a shared UI foundation and targeted cleanup, not a new visual identity.
The existing global-installed-skills fix is a separate pending change and must
remain intact during this work.

## Existing implementation

The native main window already supports resizing, defaults to 1180 × 780, and
has a 760 × 520 minimum in `app/main.go`. The mini overlay deliberately disables
resizing and is outside this work's layout scope.

`app/frontend/src/app.css` defines shared utilities and theme colors, but many
components override dimensions individually. `App.svelte` persists side-chat
and inspector widths and uses a single 1100 px breakpoint. `Rail.svelte` stores
the user's collapse preference. `Composer.svelte` mixes controls, conditional
workspace selection, and route information in one row. Plugin cards always
expand `PluginInventory.svelte`, including empty capability groups.

## Approach and boundaries

Use shared CSS tokens and reusable controls, with a small shell layout policy
and a shared popup primitive. Component-specific CSS patches would allow drift;
a complete visual redesign would add unrelated scope.

Responsibilities:

1. Shared tokens and control utilities own geometry, typography, focus styling,
   and spacing. Feature components choose semantic variants.
2. The shell layout policy takes viewport dimensions, requested panel widths,
   rail preference, and active auxiliary panel. It returns visible inline and
   overlay geometry without mutating conversation state.
3. The popup primitive owns positioning, viewport constraints, scrolling,
   dismissal, and focus restoration. Menu contents own their selection actions.
4. Plugin summaries own their per-card disclosure state. Existing inventories
   render inside the expanded details area.

Apply these contracts to the main shell, composer, model picker, slash picker,
plugin management, settings forms, and custom menus/dropdowns used by these
surfaces. Reuse existing utilities before introducing another component.
Do not replace platform-native select behavior where a styled native control
already meets the interaction and sizing rules.

## Compact geometry contract

All dimensions below are CSS pixels at 100% zoom; borders are included through
border-box sizing.

| Element | Contract |
| --- | --- |
| Standard button, single-line input, select | 32 px height |
| Explicit compact toolbar variant | 28 px height |
| Icon-only action | 32 × 32 px target; 16 × 16 px icon |
| Compact toolbar icon action | 28 × 28 px target; 16 × 16 px icon |
| Standard control text | 13 px, 20 px line height |
| Secondary text | 12 px, 16 px line height |
| Body/chat text | Existing readable 14 px scale, not reduced to fit controls |
| Page title | 18 px, 24 px line height |
| Ordinary menu row | At least 32 px height |
| Menu row with a description | At least 48 px; may grow for wrapping |
| Related controls | 8 px gap and centered cross-axis alignment |
| Standard card padding | 16 px |
| Subgroup spacing | 12 or 16 px |
| Section spacing | 24 px |
| Page inset | 24 px; 16 px below 960 px viewport width |
| Surface radius | Controls 8 px; cards/popups 12 px; composer 16 px |

Use a spacing scale of 4, 8, 12, 16, 24, and 32 px for layout padding and gaps.
One-pixel borders and small focus offsets are deliberate exceptions. Icon size
and target size are separate: smaller visible icons must retain their targets.

Equal-height controls align within the same row. Widths follow purpose: icon
buttons stay square, fields fill their form column, labels retain useful space,
and menus fit content. Textareas, message content, tables, and expanded details
grow naturally; fixed single-line control heights must not clip their content.
Error/help text appears outside the field's fixed-height box.

## Menus, dropdowns, and popups

- Ordinary custom menus use a preferred width between 200 and 360 px. Match at
  least the trigger width when it fits; a wide trigger may exceed the preferred
  maximum. Viewport bounds take precedence over trigger width.
- The slash skill picker is a deliberate rich-list exception: align it with
  the composer and cap it at the composer's width, up to 768 px.
- Keep at least 8 px between a popup and every viewport edge. Prefer the
  trigger's natural direction, flip above/below when needed, and shift sideways
  to fit. Recalculate on window resize and relevant container scrolling.
- List content scrolls inside the popup. Its height is the smaller of 320 px
  and the available viewport space after the edge insets and popup header.
  The surrounding app must not acquire document-level scrolling.
- Rows share consistent label, icon, description, shortcut, and selection-mark
  columns. Single-line labels remain readable; secondary descriptions truncate
  with a full-text tooltip or accessible description. A wrapping row grows.
- Use the correct interaction pattern: listbox for skill selection, menu for
  actions, dialog for forms. Do not interchange roles to silence diagnostics.
- Arrow keys move selection where appropriate; Enter selects; Tab behavior
  follows the control's pattern. Shift+Tab remains usable. Escape dismisses and
  restores focus to the trigger. Clicking outside dismisses.
- Loading, empty, and failure states use the same popup shell. Retry remains
  reachable by keyboard. Repositioning must retain selection and draft text.
- Custom menus must escape clipping by scroll containers using a portal or an
  equivalent overlay layer. No dependency installation is required by design.

## Resizable shell behavior

Support the current 760 × 520 minimum and arbitrary larger window sizes.
Use available space, including divider widths, rather than only breakpoint
names to decide which columns fit.

- Expanded rail width is 280 px; collapsed rail width is 64 px. Below 960 px,
  automatically use the collapsed presentation without overwriting the user's
  saved expanded/collapsed preference. Restore that preference when space returns.
- Reserve at least 440 px for the main chat region. Requested side-chat and
  inspector widths remain persisted preferences, bounded to 280–720 px, but
  their effective widths are clamped to the current window.
- Keep auxiliary panels inline only when the main region, rail, all visible
  panels, and dividers fit. Otherwise retain the most recently activated
  auxiliary panel inline if it fits; show the other on demand as an overlay.
- If neither auxiliary panel fits inline, the active one uses an overlay over
  the content region. At most one auxiliary overlay is visible at a time.
  Keep inactive panels reachable through labeled controls. Closing or hiding
  a panel must not close a side conversation or discard inspector tabs.
- Overlay width is at most the available content width minus 16 px. It has a
  visible close control, Escape dismissal, focus containment/restoration, and
  a backdrop. Height stays within the content region.
- Explicit panel drag gestures update the stored preference. Temporary resize
  clamping does not overwrite it. Viewport changes preserve chat drafts,
  selection, scroll behavior, and active work.
- At short heights, navigation and content areas scroll independently. Keep
  composer actions accessible. A growing composer textarea scrolls internally
  when necessary rather than pushing Send outside the window.

## Composer cleanup

Remove the Workspace label/select and successful model-route label entirely
from the composer. New project chats use the existing project-folder default.
Do not introduce a replacement workspace menu in this change. Existing thread
workspace metadata and backend support remain available for existing worktrees.

Keep attachment, model selection, complexity selection, and Send/Stop controls.
Use a consistent compact toolbar height. At narrow widths, groups wrap onto
additional rows with an 8 px gap; Send/Stop remains visible and reachable.
Remove unused display-only route formatting. Retain route computation and
failover behavior, and retain actionable unavailable-model/error feedback.
The normal composer must not gain or lose layout space when a thread is first
created solely because previously conditional metadata disappears.

## Plugin information hierarchy

An installed card shows name, version, meaningful status, nonzero capability
counts such as “15 skills”, and primary actions. Keep source paths, package
format/mode, per-capability health rows, and unsupported-component inventories
inside a labeled Details disclosure, collapsed initially. Omit empty capability
groups rather than rendering four boxes containing “None”.

Details uses semantic disclosure controls with `aria-expanded` and a clear
accessible relationship. Its state is independent per plugin and remains stable
during resize or refresh of the same installed plugin. Full skill names remain
available in Details and through the slash picker.

Errors requiring action remain visible in the summary, including missing
configuration or failed integrations. Detailed diagnostics and hook failure
history belong in Details. Project integration enablement must be clearly
distinguished from globally available installed skills.

## Verification and acceptance criteria

Extend the existing Vite/Playwright browser harnesses with deterministic fixtures:
empty and active chats, a Git project, a projectless chat, main plus side chats,
inspector tabs, long names/descriptions, a plugin with 15 skills, and
loading/error/empty menu states. Tests must exercise rendered behavior rather
than assert source-file strings.

Run in both light and dark themes at these viewport sizes:

- 760 × 520: supported minimum.
- 960 × 640: compact transition boundary.
- 1180 × 780: native default.
- 1440 × 900: wide desktop.
- 1920 × 1080: sufficient space for multiple columns.
- 1180 × 520: short-height stress case.

Also drag auxiliary dividers, continuously resize while menus are open, and
cross the 960 px rail boundary in both directions. Geometry assertions tolerate
at most 1 px rounding difference.

Acceptance:

1. Standard and compact controls meet their height contracts; icon actions meet
   their target contracts. Related controls align within 1 px.
2. There is no unintended document horizontal or vertical overflow. Intentional
   code/table and list scrolling stays within its owning region.
3. Popups remain within the 8 px viewport inset; selected rows can be scrolled
   into view, and triggers and essential actions are not clipped.
4. Focus, Escape, outside-click dismissal, Retry, and selection work by keyboard
   and pointer. Selecting a skill continues to insert and wait for Send.
5. All chat/panel combinations keep the main region's width contract, with
   automatic overlay behavior when inline geometry cannot fit.
6. Drafts, conversation identity, inspector tabs, and saved layout preferences
   survive resizing and panel presentation changes.
7. New and existing composer layouts omit Workspace and successful route text.
8. A 15-skill plugin starts as a compact count summary; Details reveals all
   skills; errors needing action remain visible while collapsed.
9. Browser runs have no unexpected console errors or failed application requests.
   Capture and inspect screenshots for every matrix size/theme and key expanded
   menu/plugin/panel states. A screenshot alone is not behavioral verification.
10. Frontend tests, Svelte checking, production build, and applicable Go checks
    pass. Verify native macOS resizing and the minimum size in a local build;
    report that check as not run if only browser geometry was exercised.

## Delivery boundary

This document is the design artifact. Product code changes start only after the
user approves this written spec, reviews the subsequent implementation plan,
and selects its execution method. Keep UI work on a development branch, preserve
the pending global-skills fix, and deliver a local build for review before any
separately authorized release promotion or publication.
