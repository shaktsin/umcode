# Model assistant workflows

Captured from the actual app and engine using temporary projects and a local,
deterministic HTTP/SSE provider. These are UI acceptance captures; they do not
claim trained-model behavior or validate the quality of a live LLM's advice.

## Ask about the selected layer

![Selected layer context and question composer](01-layer-question.png)

## Inspect a proposed change

![Suggested settings and parameter changes in chat](02-proposal.png)

## Preview before applying

![Read-only candidate architecture with affected layer highlighted](03-preview.png)

## Apply to the unsaved draft

![Applied suggestion with normal Undo, Redo and Save controls](04-applied-draft.png)

## Preserve newer manual edits

![Outdated suggestion disabled after changing the draft](05-stale-suggestion.png)

## Preview in a narrow dark layout

![Narrow preview closes the chat overlay and keeps Apply and Cancel reachable](06-narrow-dark-preview.png)
