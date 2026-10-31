# Workflow snapshots

Captured from the real Svelte app and Go engine by
`app/frontend/e2e/visual-workbench.mjs`, using temporary test projects. The
validation messages, model names, generated code and parameter counts come from
the running application. No training results are simulated.

## Create a model

![Text task, input type and starting point](01-create-text.png)

## Return to models with an unsaved draft

![Model chooser retaining unsaved changes](02-models-and-drafts.png)

## Build a text model

![Text architecture, palette and selected-layer properties](03-text-builder.png)

## Inspect and fix validation issues

![Actionable validation messages and affected layer settings](04-validation.png)

## Inspect generated code and export Python

![Selected architecture layer highlighted in generated Python](05-code-export.png)

## Classify text

![Text classifier architecture and output dimensions](06-text-classifier.png)

## Choose vision input and a CNN starter

![Classification task with Vision input and architecture starters](07-create-vision.png)

## Edit a CNN

![CNN with image dimensions and Vision palette](08-cnn-builder.png)

## Edit a vision Transformer

![Vision Transformer with bidirectional attention settings](09-vision-transformer.png)

## Classify feature vectors

![Feature-vector input and classifier architecture](10-feature-classifier.png)

## Build a custom image architecture

This intentionally unfinished example shows a newly dragged Conv2d layer wired
to Image Input; the missing Output is reported explicitly.

![Custom image architecture under construction](11-custom-builder.png)

## Use a narrow dark layout

![760px dark desktop layout with layer properties](12-narrow-dark.png)

## See the current training boundary

Training is not implemented by this PR. Data and Inference have corresponding
unavailable states.

![Train tab explicitly marks training as unavailable](13-training-placeholder.png)
