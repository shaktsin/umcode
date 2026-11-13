# ML Workbench mockups

Visual reference for the ML Workbench (data, build, train, evaluate and try
models). Scope and phases are in
[`../../specs/2026-10-21-ml-workbench-scope.md`](../../specs/2026-10-21-ml-workbench-scope.md).

These are design mockups, not code to ship. They show layout, hierarchy and
intent. Numbers (times, costs, rates, losses) are illustrative.

## What is here

```
png/       preview images, 1440×900 (screen 03 is 1100×760)
canvas/    editable sources, one .dc.html per screen, plus canvas.json (layout of the canvas)
DEVIATIONS.md   where the build differs from a mockup, and whether that is accepted
```

## Viewing

- **Quickest:** open `png/NN-*.png`.
- **In a browser:** open any `canvas/*.dc.html`. They are plain HTML with inline
  styles, so they render without a build step. The `<x-dc>` wrapper and the
  `support.js` script are for the design canvas and are ignored by a browser.
- **On the design canvas** (private link, only the owner can open it):
  https://claude.ai/artifact/9nj8v1MaCnF8rspW7exGBr

## Changing a mockup

1. Edit the screen on the canvas, or edit the `.dc.html` file directly.
2. Ask Claude to re-export the PNGs and update this folder, or re-render them
   yourself with Playwright at the sizes above.
3. Keep both the `.dc.html` and the PNG in the same commit.

## Screens

| PNG | Source | Phase | What it shows |
|---|---|---|---|
| `00-new-model.png` | `Template.dc.html` | P1 | First screen: choose the task, then name and starting point |
| `01-build.png` | `Main.dc.html` | P1 | Layer palette, canvas with zoom, pan and minimap, properties and generated code |
| `02-train.png` | `Train.dc.html` | P2 | Live run: settings, loss chart, log, GPU panel |
| `03-data-sources-and-compute.png` | `Connect.dc.html` | P2, P4 | Data sources with index status, GPU connectors, add machine |
| `04-inference-playground.png` | `Infer.dc.html` | P3 | Prompt, base versus finetuned output, adapter card, history |
| `05-inference-evaluate.png` | `Evaluate.dc.html` | P3 | Checkpoint comparison, worst examples, loss by category |
| `06-data-prepare.png` | `Data.dc.html` | P4 | Pipeline steps, histograms, before and after row preview |
| `07-data-at-scale.png` | `Scale.dc.html` | P5 | Data in S3, distributed preparation, where each thing lives |
| `08-train-plan.png` | `Plan.dc.html` | P5 | Capacity planner: memory per GPU, run options, cost and time |

## Design values

The mockups use UMCode's existing look, taken from
`app/frontend/src/app.css`. In the app, use the CSS variables, not these hex values.

| Role | Variable | Mockup value |
|---|---|---|
| Page | `--color-paper` | `#f7f8fa` |
| Surface | `--color-surface` | `#eef1f5` |
| Raised | `--color-raised` | `#e3e7ed` |
| Line | `--color-line` | `#d9dee7` |
| Text | `--color-ink` | `#172033` |
| Muted text | `--color-muted` | `#68758a` (mock uses `#5b687c` for small text, for contrast) |
| Accent | `--color-accent` | `#3569d4` (pressed `#2454b5`) |
| Success | `--color-sage` | `#2f7a5c` |
| Error | `--color-rust` | `#bd4b51` |
| Warning | `--color-amber-warm` | `#9a681e` |

Controls are 28 to 32 px high, radii 8 / 12 / 16 px, system font. The mockups
are light theme only; dark theme follows the app's existing dark tokens.

## Checking a build against a mockup

For each screen a phase delivers:

1. Run the app at 1440×900 and take a screenshot of the same state.
2. Put it next to the PNG and go through the checks below.
3. Anything that differs goes into `DEVIATIONS.md` as **accepted** (with a
   reason) or **to fix**.

### Checks per screen

**00 New model**
- Task cards: Text generation, Classification, Custom, with the suggested one selected.
- The "What this sets up" strip names Build, Train and Inference effects.
- Right column: name, starting point (blank, starter, pretrained), optional data and compute.
- Creating the model writes `adapter.py` for the chosen task.

**01 Build**
- Palette groups, dragging a layer shows an insert marker on the nearest connection.
- Every connection shows its tensor shape; shape errors appear inline.
- Selected layer shows handles and its properties; the matching code lines are highlighted.
- Toolbar: undo, redo, zoom out, zoom level, zoom in, fit. Pan arrows and a minimap with the visible area.
- Parameter count shown; task badge next to the model name.

**02 Train**
- Settings column, four stat tiles, loss chart (train and eval), live log, GPU panel.
- Pause and stop; checkpoint lines appear in the log.
- Switching between This Mac and a remote machine.

**03 Data sources and compute**
- Each source shows type, size, index state and progress; "Add from" lists source types.
- Each connector shows state; add-machine form runs a connection test before saving.

**04 Playground**
- Checkpoint, compare-with and compute pickers; load state visible.
- Two outputs side by side with "Add to eval set"; history list; adapter card with schema status.

**05 Evaluate**
- Setup bar, comparison table with the best checkpoint marked, worst examples that open in the playground, loss by category, saved results path.

**06 Data prepare**
- Pipeline steps with their effect on row counts; a suggested step is visibly pending.
- Before and after tiles, length histogram with removed bars shown, category chart, row preview.
- "Apply to all and save" creates a numbered dataset version.

**07 Data at scale**
- Flow from raw data to prepared dataset to training; job progress; locality check.
- Table of what lives where.

**08 Train plan**
- Run options table with fit, time and cost; memory per GPU bar; budget cap; launch summary.
