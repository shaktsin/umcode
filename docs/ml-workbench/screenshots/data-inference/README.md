# Data, Train and Inference workflows

Captured from the real Svelte app and Go engine using an isolated project and an
empty in-memory secret store. Numeric/text examples are synthetic trained fixtures;
Hugging Face inspection/download uses the real public native-file endpoint.

## Local data import and preparation

![Local upload and imported dataset](01-local-data.png)

![Actual rows, mappings, split settings and saved preparation](06-saved-preparation.png)

## Hugging Face inspection

![Public repository, pinned revision and file selection](02-hugging-face.png)

## Test an architecture without trained weights

![Actual random-weight execution, shapes and untrained label](03-architecture-test.png)

## Test a trained prompt and promote its checkpoint

![Real PyTorch response and persisted chat promotion](04-prompt-and-promote.png)

## Use the promoted checkpoint in regular chat

![Local checkpoint replying in the regular chat window](05-local-chat.png)

The final capture needs no cloud API key, exposes plain text chat controls, and uses
real generated text from the pinned checkpoint. These small trained examples establish execution, not general answer quality.


## Train locally and create a checkpoint

![Completed CPU training, epoch progress, loss chart and checkpoint handoff](07-training-checkpoint.jpg)

This capture uses an isolated synthetic numeric-regression project. The run writes its own checkpoint and exposes separate training/validation/test metrics. Training progress and Stop were exercised through the real browser. See the [Train guide](../../local-training.md) for metrics and model-quality limitations.

![Separate training, validation and held-out regression metrics](08-training-evaluation.jpg)

![Cancelled run retained in history without an incomplete checkpoint](09-training-cancelled.jpg)
