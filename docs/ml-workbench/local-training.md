# Local training and evaluation

Train uses the saved visual architecture and saved Data preparation with PyTorch on CPU.

1. Save the architecture in Build and match its input/output sizes with Data preparation.
2. Import data, choose inputs and targets, and **Save preparation**. Use nonempty validation and test splits for held-out scores.
3. Open Train. Choose epochs, batch size, learning rate and seed. Runs use AdamW and start with fresh weights.
4. **Start training** to view epoch progress and training/validation loss. **Stop** cancels the run without publishing incomplete weights. Training continues when switching tabs.
5. Completed runs show separate training, validation and held-out test metrics. **Try in Inference** selects that run's exact checkpoint, including historical runs.
6. Test a prompt or numeric prediction. A successful compatible causal-generation test can then enable **Promote to Chat**.

## Evaluation and model quality

| Task | Metrics |
| --- | --- |
| Regression | MSE/loss, MAE, RMSE, R² |
| Classification | Cross-entropy, accuracy, macro F1 |
| Text generation | Next-token cross-entropy, perplexity, byte-token accuracy |

R² is omitted when undefined for constant targets. Empty validation/test splits show unavailable instead of training scores. Test rows are evaluated after training, never used for weight updates.

**A completed training run does not establish useful answer quality.** These starters are randomly initialized architectures, not pretrained language models. A small byte-token model trained on a few hundred rows can finish successfully and still produce gibberish. Token accuracy measures next-token prediction, not the percentage of questions answered correctly. More epochs can produce memorization without reliable answers.

Synthetic greeting and linear-regression fixtures verify the workflow. Their learned outputs do not establish general chat capability or DMV question-answering accuracy. The first local DMV experiment had 14,832 parameters, 298 training rows and 20 epochs, with about 27% held-out byte-token accuracy; its answers remained unusable after checkpoint/format bugs were corrected. No user dataset, weights, chat history or project files are included in this PR.

## Persistence and prompt compatibility

Each run snapshots its graph and prepared splits. Later edits do not change its checkpoint. Run metadata and history persist under the model's `runs/` directory. Interrupted runs are marked failed on next access after an engine restart; completed checkpoints remain available. Model deletion cancels the matching job, and a recreated model name does not inherit old run history.

Question/answer checkpoints use the raw question plus the same newline separator as their training rows. Their Chat promotion supports one question per chat, with visible new-chat guidance for further questions. Text-corpus checkpoints continue raw text and are not chat promotions. Imported legacy chat checkpoints retain User/Assistant framing. New checkpoint identities pin prompt format; early local checkpoints recover it from their completed training run. Inference defaults to the newest checkpoint and preserves explicit selection and run handoff.

## Scope and resource limits

Supported: numeric regression/classification and UTF-8 byte-token text generation/classification. Vision training, remote compute, GPU/MPS, optimizer resume, pretrained fine-tuning, runtime installers and early stopping remain future work. Image checkpoint inference is covered by the [Data/Inference guide](data-inference.md).

- One active app-wide training job, with a 45-minute timeout.
- 1–1,000 epochs, batch size 1–256, learning rate 0.000001–1 and seed 0–2,147,483,647.
- Eight million batch activation elements, including attention and repeated blocks; reduce batch size for a long-context transformer.
- A combined 256 MiB prepared-file/tensor budget and 100,000 rows, plus existing graph/checkpoint limits.
- Completed runs enter a short checkpoint validation/publication phase; Stop reports that phase instead of claiming an already completed checkpoint was cancelled.

Install the managed PyTorch runtime using the [Data/Inference setup](data-inference.md#install-the-local-runtime). The app does not bundle or automatically install PyTorch in this PR.

## Validation

Frontend tests, Svelte check and production build; real Python training/metrics tests; Go storage, real-worker and RPC race checks. Browser acceptance covers real final metrics, Start/Stop, historical checkpoint handoff, learned numeric inference and local chat promotion. Repository-wide Go limitations are recorded in the [Data/Inference verification](data-inference.md#verification-and-limits).
