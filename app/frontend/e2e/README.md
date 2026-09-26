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

node e2e/chat.mjs        # project edits, diffs, side chats, approvals, undo
node e2e/routing.mjs     # the route chip, failover across keys and models
node e2e/pools.mjs       # configuring models and pools, then routing through one
SCHEME=light node e2e/chat.mjs   # the same again in the light theme
```

The fake provider rate-limits the key labelled `primary`, and rate-limits every
key for `local-llama` when the message contains "drop down", which is how the
routing tests force a switch.

Each script leaves the engine's state behind (cooled-down keys, configured
models, chat history), and each starts by putting back what it needs, so they
can be run repeatedly in any order.
