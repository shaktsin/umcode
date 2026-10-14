# Compact UI verification — 2026-10-13

Scope: local development branch `codex/compact-ui-rules`, based on global-skills
prerequisite `3a14384`. No merge, push, or release publication.

## Implemented

Shared compact geometry, viewport-aware model/skill/sidebar menus, responsive
rail and retained auxiliary panels, simplified composer, and plugin summaries
with Details disclosures. Native default 1180×780; minimum 760×520.

## Verification

- 36 frontend unit tests pass; Svelte reports zero errors/warnings.
- 60 rendered UI cases: five cases × six sizes × two themes, with no browser
  errors or document overflow. Includes modal stacking, clamped divider dragging,
  long skill errors, sidebar keyboard menus, draft retention, and plugin errors.
- Dedicated skill picker browser flow verifies insertion before sending and Retry.
- Full Go tests and Go vet pass. The initial sandboxed Go test could not create
  localhost test servers; the complete run with those enabled passed.
- Screenshots inspected for compact, short, and wide windows in both themes.
- Developer ID signed local Apple Silicon bundle is built and signature verified.
  Local build is not notarized or published.
- Native launch/resizing was not run: this bundle shares app and background
  service identity with the user's running app. Renderer resizing is verified;
  native window behavior remains a manual check in the local build.

Commands and artifact overrides are documented in `app/frontend/e2e/README.md`.
Browser artifacts: `/tmp/umcode-ui-contract`; Go log:
`/tmp/umcode-compact-go-test-full.log`; bundle log:
`/tmp/umcode-local-compact-ui-build.log`.

## Independent review

Fresh whole-branch review found four Important issues. Regression tests first
reproduced failures, then passed after fixes: topmost modal focus/dismissal,
constrained skill error bodies, shared sidebar action popups, and dragging from
the effective panel width. No Critical issues were found.

Review boundaries: backend routing/global skills and real plugin mutations
retain prior behavior and are not proven by RPC fixtures. Mini overlay is outside
this scope. Native behavior is recorded separately above.

## Rulings made

1. Use a dedicated branch in the writable checkout after managed-worktree writes
   were denied. Cost: less isolation from user edits; status inspected before commits.
2. Add measured popup content height to keep short menus adjacent to triggers.
   Cost: one additional measured dimension.
3. Add a temporary navigation drawer below the rail breakpoint so projects/chats
   remain reachable. Cost: an additional modal focus path, now regression tested.

4. Defer native launch/resizing to the local build because the app/service identity
   is shared with the running user app. Cost: native resizing is not verified.
5. Preserve backend routing/global skills and plugin mutations; fixtures establish
   presentation only. Cost: reliance on prerequisite and backend tests.
6. Keep the mini overlay outside this desktop scope. Cost: its density remains
   unchanged.

## Deferred minor review findings

- Plugin Inspect retains a 38px height override beside 32px inputs.
- Truncated skill descriptions lack a full-text tooltip/accessible description.
- Plugin inventory tooltips should preserve component errors and full names.
- Side-chat/inspector tab close controls retain small targets.
