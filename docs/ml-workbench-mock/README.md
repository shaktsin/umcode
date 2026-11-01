# ML Workbench workflow concept

An interactive design preview for the proposed Data, Train, and Inference tabs. This is a separate design-review deliverable from the visual builder and assistant in PR #91. It does not implement the ML runtime or change desktop app behavior.

The [live review preview](https://umcode-ml-workbench-concept.nishanth-prakash.chatgpt.site) is available before merge and requires no sign-in. It serves the same exported page committed in this PR.

The browser-ready page is [site/previews/ml-workbench/index.html](../../site/previews/ml-workbench/index.html). After this PR merges, the existing Pages workflow will also publish it at **https://umcode.dev/previews/ml-workbench/**.

## Try the workflows

1. **Data:** switch among Examples, Local files, Hugging Face, Kaggle, OpenML, UCI, Roboflow, and URL. Choose an example and inspect its inputs, target, preparation, and split. Selecting all columns demonstrates an input-mapping error. Local files shows its empty upload state instead of a previous example's table.
2. **Build:** inspect the starter architecture associated with a tabular, image, or text example. The interactive drag-and-drop builder remains in the desktop app; this page shows an architecture summary only.
3. **Train:** use a dataset, change the run settings, start or stop a simulated run, and open its example checkpoint.
4. **Inference:** try a checkpoint prediction, or choose a local GGUF model / existing inference server to explore llama.cpp, vLLM, and SGLang connection options. Test a simulated connection, generate an illustrative response, and preview the Use in chat action.
5. **Assistant:** open the contextual assistant and try its illustrative suggestions.

The page follows the browser's light/dark appearance and adapts to small screens. No Codex or ChatGPT account is required to use the exported page.

## What is simulated

- Dataset cards are a curated, static catalog. The `nprak26` names and dataset counts came from public Hugging Face metadata supplied during the design discussion; row previews are illustrative. The Kaggle and Roboflow starter cards are explicitly illustrative samples.
- No dataset is downloaded, no uploaded file content is read, and no credentials are requested or transmitted. File selection only displays a filename/count.
- Runs, losses, validation metrics, checkpoints, predictions, connection checks, assistant answers, and chat registration are all simulated. No inference server or GPU is contacted.
- The browser stores limited preview selections locally when storage is available. Returning to the page can restore the selected tab, dataset, source, and model/runtime selection. Compute and connection status are not persisted.
- Public access / Connect account controls, real account authorization, complete file parsing, model download/loading, and container lifecycle management remain implementation work. The proposed packaging direction is a bundled native llama.cpp runner, with other runtimes and model weights downloaded on first use on compatible compute.

## Preview locally

From the repository root:

```sh
python3 -m http.server 8000 --directory site
```

Open http://localhost:8000/previews/ml-workbench/. The checked-in page requires no frontend build. Decorative icons and shared presentation resources load from pinned CDN URLs; the concept does not require a backend.

## Editing

`workbench.fragment.html` is the editable source. `site/previews/ml-workbench/index.html` is its exported standalone page. To regenerate with the visualization skill available:

```sh
python3 "$VISUALIZE_SKILL_DIR/scripts/render.py" \
  docs/ml-workbench-mock/workbench.fragment.html \
  site/previews/ml-workbench/index.html \
  --title 'ML Workbench — interactive workflow concept' --force
```

Commit both files after making changes. Contributors without that helper can serve the checked-in export directly.

## Validation

The standalone page was checked in headless Chrome for source switching, the local-file empty state, mapping validation, simulated training/checkpoint inference, all three server runtimes, chat registration preview, gated-model messaging, and the assistant. Data, Train, and Inference were checked for horizontal overflow at desktop, tablet, and mobile widths. Preview screenshots are linked in the PR description.
