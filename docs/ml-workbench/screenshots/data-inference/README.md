# Data and Inference workflows

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
real generated text from the pinned checkpoint. Train remains a placeholder.
