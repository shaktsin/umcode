# Imported chat and MoE workflows

## Promote an imported model to Chat

Import a complete supported model folder, open **Inference**, select original weights or a local fine-tuned checkpoint, and run a prompt. A successful nonempty response from a tokenizer with a native chat template enables **Promote to Chat**. **Open in Chat** selects it in the normal chat window; **Remove from Chat** removes the promotion without deleting model files.

Promotions pin the tested model version and weights. Later training does not silently replace a promoted checkpoint. Conversation roles use the model's own template; no cloud fallback or coding-agent tools are used. Models without chat templates remain usable for completion in Inference, but cannot be promoted. Promotion does not improve response quality.

## Mixture-of-experts models

Build inspection/editing and local Data, Train and Inference recognize:

- Mixtral (`mixtral`)
- Granite MoE (`granitemoe`)
- Qwen2 MoE (`qwen2_moe`)
- Qwen3 MoE (`qwen3_moe`)

Build shows decoder layers, attention, routers, experts and tensor shapes. Expand details to inspect internals; edit decoder layers as units. Model files are managed in Build only. Complete folders are validated once and shared across the tabs; config + safetensors alone remain inspection-only without tokenizer assets.

The managed Python environment must include PyTorch and the packages in `internal/workbench/pytorch/requirements-imported.txt` (Transformers 4.57.3 and PEFT 0.17.1 for LoRA). No remote model code is executed. Unsupported or quantized formats, changed architectures and invalid/missing files remain gated with a reason.

### Limits

- Local CPU runtime; bundle copying supports at most 8 GiB.
- Execution supports at most **one billion total parameters**, counting every expert, not only active experts. Standard large Mixtral/Qwen MoE checkpoints and Granite 3.1 3B remain inspectable in Build but exceed this execution limit.
- Causal text training uses rank-8 attention LoRA (query/value projections, GPT-2 fused attention). The base weights, output head and MoE experts/router stay frozen during training. Classification uses a frozen-backbone head trainer, not LoRA.
- Chat uses a bounded context and reports overflow rather than silently truncating it.

### Reproducible small-model test

Use an environment containing the dependencies above:

```sh
python internal/workbench/pytorch/moe_fixture.py /tmp/umcode-tiny-mixtral mixtral
```

The same command accepts `granitemoe`, `qwen2_moe`, and `qwen3_moe`. These are **synthetic, random-weight models** for testing import, preparation, training and inference—not meaningful conversation. Import the generated folder once, save a small text dataset preparation, train one epoch, and select the local checkpoint in Inference.

Real CPU workflow coverage is opt-in:

```sh
UMCODE_IMPORTED_TEST_PYTHON=/path/to/python go test ./internal/workbench -run TestImportedRuntimeRealWorkflows -count=1 -timeout=180s
UMCODE_IMPORTED_TEST_PYTHON=/path/to/python go test ./internal/engine -run TestImportedPromotionRealLocalChat -count=1 -timeout=90s
```

Native model references: [Mixtral](https://huggingface.co/docs/transformers/model_doc/mixtral), [Granite MoE](https://huggingface.co/docs/transformers/model_doc/granitemoe), [Qwen2 MoE](https://huggingface.co/docs/transformers/model_doc/qwen2_moe), [Qwen3 MoE](https://huggingface.co/docs/transformers/model_doc/qwen3_moe).

## Train an imported chat model with LoRA

1. Import a supported, genuinely pretrained chat/instruct model with its full tokenizer assets. Random-weight fixtures will not become useful assistants from a short run.
2. In Data, select the question/prompt column and the response/answer column. Click **Save preparation**, including for previously saved datasets. Old combined-text preparations must be re-saved.
3. Choose a sequence length that fits the entire formatted example. Overlong examples are rejected with the required token count instead of silently dropping answers. Question/answer training requires a native chat template; corpus training is continuation-only.
4. In Train, start with **1 epoch**, learning rate **0.0001**. The runtime trains attention LoRA with AdamW, gradient clipping, accumulated gradients and a warmup/decay schedule. The validation split selects the best epoch, including the original baseline; test rows are evaluated only after selection.
5. In Inference, explicitly choose **Latest local fine-tuned checkpoint**, compare a few familiar and held-out questions against **Original imported weights**, then promote the tested checkpoint to Chat if desired.

The selected adapter is merged into a separate complete checkpoint. Original files are not modified. This uses full-model disk space per checkpoint; standalone adapter export and resuming a previous adapter are not included. Each run starts from the imported original. LoRA and lower validation loss do not guarantee conversational quality or eliminate forgetting; data quality and representative held-out examples still matter.

Implementation reference: [Hugging Face PEFT LoRA](https://huggingface.co/docs/peft/package_reference/lora).
