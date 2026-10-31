# Browser tests

These drive the real UI against a real engine and a fake provider. They are
plain Playwright scripts rather than a test framework: each one prints a line
per step and exits non-zero if any step failed, and each takes screenshots so a
failure can be looked at.

```sh
export UMCODE_HOME=/tmp/uf-e2e/home        # a throwaway engine home
python3 fake-provider.py 18997 &             # a fake OpenAI-compatible server
umcode engine &                            # against that home
#   add two openai_compatible keys pointing at http://127.0.0.1:18997/v1,
#   label them "primary" and "backup", and add a project
UMCODE_ENGINE_WS_PORT=<port> npm run dev & # the UI, pointed at that engine

node e2e/first-run.mjs   # needs an engine home with NO credentials: welcome strip, Providers tiles, connect prompt in the composer
node e2e/chat.mjs        # project edits, diffs, side chats, approvals, undo
node e2e/routing.mjs     # the route chip, failover across keys and models
node e2e/pools.mjs       # configuring models and pools, then routing through one
SCHEME=light node e2e/chat.mjs   # the same again in the light theme
```

`first-run.mjs` connects a key, so run it against a throwaway home before the
keys are added for the other scripts (it adds an `openai_compatible` key
pointing at `http://127.0.0.1:18997/v1`, which is what the others expect too).

The fake provider rate-limits the key labelled `primary`, and rate-limits every
key for `local-llama` when the message contains "drop down", which is how the
routing tests force a switch.

Each script leaves the engine's state behind (cooled-down keys, configured
models, chat history), and each starts by putting back what it needs, so they
can be run repeatedly in any order.

The skill picker test runs independently of the engine. It serves the real
composer with a fake RPC boundary and checks keyboard and mouse selection,
insert-before-send behavior, project scoping, and loading/error/empty states:

```sh
node e2e/skills.mjs
```

It requires `playwright-core` and defaults to Chrome's macOS application path.
Use `PLAYWRIGHT_MODULE` to point to an installed module, `CHROME_PATH` for a
different browser executable, and `SCREENSHOT_PATH` to save the final picker.

The compact UI contract runs the real Svelte application against a deterministic
RPC fixture, without changing your engine state:

```sh
node e2e/ui-contract.mjs                 # all 60 size/theme/case combinations
node e2e/ui-contract.mjs --case shell    # controls, popups, shell, composer, plugins
```

It checks six window sizes in both themes: 760×520, 960×640, 1180×780,
1440×900, 1920×1080, and 1180×520. Assertions cover control geometry,
popup bounds and keyboard behavior, panel resizing and draft retention,
composer routing/feedback, and plugin disclosures/errors. Screenshots and
`report.json` are saved to `/tmp/umcode-ui-contract`; set `UI_ARTIFACT_DIR`
to preserve them elsewhere. `PLAYWRIGHT_MODULE` and `CHROME_PATH` work as
above. Browser fixtures verify renderer behavior, not native macOS window
constraints or signing.

The delegated-development acceptance matrix uses the same real App fixture
server and the installed Playwright/Chrome runtime:

```sh
node e2e/delegation.mjs
```

It runs widths 320, 720 and 1200 in both themes, checking keyboard activation
before the first message, one-use token submission, collapsed/expanded Agents,
results and check blockers, attributed approvals, cancellation, notification
updates, reconnect hydration and failure recovery. Repeated exact-snapshot links
reuse inspector tabs. It captures page/console errors, failed requests and
horizontal bounds. Screenshots and `report.json` go to `/tmp/umcode-delegation`
(or `UI_ARTIFACT_DIR`). `PLAYWRIGHT_MODULE` and `CHROME_PATH` work as above.
This deterministic RPC fixture validates browser rendering and interaction;
`internal/server/delegation_e2e_test.go` separately exercises real RPC, engine,
Git and fake-provider execution. Neither check claims native signing/window
acceptance or supplies a trusted interactive visual-QA completion receipt.

## Visual ML workbench

This smoke starts its own real engine, temporary home/projects, and Vite server.
No provider credentials are needed. Run from `app/frontend`, with an engine
built from the same checkout:

```sh
go -C ../.. build -o /tmp/umcode-workbench-test ./cmd/umcode
npm install --prefix /tmp/umcode-browser-tools --no-package-lock playwright-core
PLAYWRIGHT_MODULE=/tmp/umcode-browser-tools/node_modules/playwright-core/index.mjs \
  UMCODE_TEST_ENGINE=/tmp/umcode-workbench-test node e2e/visual-workbench.mjs
```

It requires an installed `playwright-core` module. `PLAYWRIGHT_MODULE` can point
to its absolute `index.mjs`, `CHROME_PATH` selects a browser executable, and
`UI_ARTIFACT_DIR` selects the screenshot/report folder (default
`/tmp/umcode-workbench-smoke`). `e2e/workbench.mjs` is a compatibility entry point.

Coverage includes text, image CNN/ViT, feature-vector and Custom starters;
actual palette drag/drop and port wiring; property/shape errors; pointer and
keyboard movement; undo/redo/delete; code selection/download; disk save and
fresh-page reopen; project switching; close/reopen with unsaved drafts; legacy
graph protection; and chat project isolation. It checks 760/1180/1440px in both
themes, producing 18 layout captures and additional workflow screenshots.
Data, training and inference are placeholders, not tested runtime features.

Generated architectures can also be checked with real PyTorch:

```sh
MODEL_ARTIFACT_DIR=/tmp/umcode-exported-models node e2e/export-models.mjs
python e2e/verify-models.py /tmp/umcode-exported-models
```

Use a Python environment with `torch` and `numpy` installed. The verifier imports
seven generated modules, checks inferred versus actual parameter counts/output
shapes, runs training-mode backward passes with finite gradients, and checks
causal-prefix behavior for the text starter. These checks establish architecture
execution, not trained-model quality or native macOS download behavior.
