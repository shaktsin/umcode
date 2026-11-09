# Imported Transformer and CNN models

This feature extends the Workbench from PR #93. It does not replace the native,
from-scratch builder.

## Import rules

- Select both `config.json` and safetensors weights (including every shard and
  its index). Either alone is rejected.
- A metadata-only pair opens an editable architecture in Build. Data, Train and
  Inference explain why they need the complete model folder.
- A supported complete folder is copied into the project locally and validated.
  Text models need their tokenizer files; vision models need their image
  processor configuration. Original files remain unchanged.
- Attach and validate model files only in Build. Data, Train and Inference use
  that selected model's attached files, and link back to Build for missing setup.
  A different configuration or tensor manifest is rejected without replacing
  the model. Matching metadata does not prove identical learned weight values.
- Layer settings and connections must still match the original architecture to
  execute its pretrained weights. Display names and positions may change.

Build supports GPT-2, Llama/Mistral, Qwen2, BERT, ViT, ResNet and ConvNeXT family
inspection. Expand a Transformer layer to inspect its internal attention,
normalization and feed-forward stages; edit the layer as a unit. The same
configured assistant supports native and imported architecture snapshots and
reviewable proposals. It receives architecture metadata, not weight payloads.

## Local execution

Supported tasks: GPT-2/Llama/Mistral/Qwen2 causal language modeling, BERT sequence
classification, and ViT/ResNet/ConvNeXT image classification.

Inference uses the imported tokenizer or image processor. Enter a prompt or
choose an image; no new training is necessary for a valid pretrained model.

Data preparation retains raw text/images and deterministic train/validation/test
splits. Classification targets must be numeric pretrained label indices or exact
pretrained labels; labels are not silently remapped.

Train performs **output-head-only CPU fine-tuning**, not full-model training.
The backbone and source weights remain unchanged. Tied LM embeddings are
separated before training the output head. Held-out loss (and classification
accuracy) are reported; the new checkpoint can be selected in Inference.
Checkpoint output is staged outside the model directory and published only
after model identity checks. Deleting a model cancels its imported training job.

Current limits: one imported runtime operation at a time, 8 GiB of folder
assets, one billion model parameters, 1,000 prepared rows, 1–10 epochs,
2,048 input tokens (subject to model context and output-memory limits), and
256 generated tokens. Training requires nonempty train/validation/test splits.
Input images are PNG/JPEG; preprocessing dimensions are bounded.
Quantized/custom-code models and unsupported task heads remain gated.
This iteration does not add imported-model chat promotion or remote execution.

## Dependencies and testing

Use the existing local PyTorch environment, then install the additional pinned
packages from `internal/workbench/pytorch/requirements-imported.txt`.
The engine uses `UMCODE_WORKBENCH_PYTHON`, or
`<UMCODE_HOME>/runtimes/pytorch/bin/python`.
Runtime loading is offline with `trust_remote_code=False` and safetensors only.
No Python supplied by a model folder is executed.

```sh
cd app/frontend
npm test
npm run check
npm run build
cd ../..
go test ./internal/workbench ./internal/engine ./internal/server -run 'Workbench|Imported'
UMCODE_IMPORTED_TEST_PYTHON=/path/to/venv/bin/python go test ./internal/workbench -run TestImportedRuntimeRealWorkflows
/path/to/venv/bin/python -m unittest discover -s internal/workbench/pytorch -p test_imported.py
```

Verified locally: 325 frontend tests, Svelte checks/build, focused Go tests,
real GPT-2 and ResNet import → inference → training → checkpoint reload, BERT
classification, and generation with Qwen2.5-0.5B-Instruct. The browser confirmed
the three gated-tab explanations. Automated browser folder upload was blocked
by the Chrome extension's file-access setting; that click-through remains a
manual acceptance check.

Build-only folder setup has rendered-component regression coverage for all four
tabs, plus a backend test proving that a different model folder is rejected
without changing the saved Build architecture. The browser connection was
unavailable for this follow-up, so its live visual check remains pending.

The full `go test ./... -timeout=6m` run was **not green** in the restricted
environment. It reported sandbox_apply/operation-not-permitted failures,
evaluation/workflow failures, a six-minute engine timeout and unavailable
Chromium automation. Affected tests include:

- `TestCLIRoundTrip`, `TestDelegationWorkspaceSandboxCommands`
- `TestDelegationWorkflowCancelDuringCommand`, `TestDelegationWorkflowEndToEnd`,
  `TestDelegationWorkflowRequiredChecks`, `TestEvaluationBundleMatrix`
- `TestVerifierTrustBoundary`, `TestVerifierFailureModes`,
  `TestEvaluationAllowsOwnedRuntime`
- `TestDelegationRPCEndToEnd`, `TestChatWithApprovedShellTool`, `TestAgentFixLoop`,
  `TestWebSocketTransport`, `TestModelDrivenScaffoldEditsAndGrep`,
  `TestOptimizationBundleAcrossChats`, `TestShellEditAfterPassKeepsWorkOpen`,
  `TestWorkGetActiveOnly`, `TestGuidedWorkflowRecordsSolutionTaskAndVerification`,
  `TestRejectedDecisionKeepsTaskBlockedAndAllowsReplacement`
- `TestEvaluationToolsConfined`, `TestShellRunAndRisk`,
  `TestShellStreamsOutputBeforeCommandExits`, `TestVisualQABlackBoxSession`

These results do not establish a clean repository-wide regression run. Review
CI/manual acceptance and the unrestricted full-suite results before merging.
