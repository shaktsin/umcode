# Context Compiler: Packets and Interaction Tail (Token Efficiency Phase 4a)

**Status:** Design approved in conversation; pending written-spec review

**Date:** 2026-10-03

**Parent spec:** `2026-09-30-quality-first-token-efficiency-design.md` (Phase 4: context compiler)

**Builds on:** Phase 2 (work model, #43) and Phase 3 (vault and evidence lifecycle, #45)

## Summary

Today every model call replays up to 60 transcript messages, including a one-line note for each earlier tool call and file change. Phase 2 and Phase 3 already record what those notes approximate — the goal, the criteria and their current status, the changed files, and which evidence is still true. Phase 4a sends that record instead of the transcript: a work packet, an evidence packet built from active evidence only, and a short interaction tail.

Phase 4 as a whole also covers retrieval, progressive tool loading and tool-result reducers. Those are Phase 4b and later. This spec is the slice that produces the token saving and consumes Phase 3's staleness, so the model can no longer be shown a "tests passed" that a later edit invalidated.

## Goals

- Replace transcript replay with compiled packets for threads that have an open work.
- Send passing evidence only while it is still valid; show stale criteria as needing a re-run.
- Keep the interaction tail to what is needed to read the user's intent.
- Fall back to the existing history path whenever compilation fails a sanity check, so quality never depends on the compiler being right.
- Measure the result: per-request accounting of packet tokens, and a report of what was included and omitted.
- Ship behind a flag that is off by default, with no user-visible change when off.

## Non-goals

- Retrieval: FTS5, graph traversal, repository symbols and file excerpts (Phase 4b).
- Progressive tool loading and domain-specific tool-result reducers (Phase 4b).
- Prompt-prefix cache work beyond leaving the existing system prompt untouched.
- The paired benchmark and ablations (Phase 6).
- Any user-facing approval, prompt, setting or chat output (see Zero-friction rules).

## Zero-friction rules

Carried forward from Phase 3 as requirements, not preferences.

- **No new approvals or prompts.** The compiler reads the database and the transcript; it never calls a tool or the approval gate.
- **Nothing blocks a turn.** Compilation is in-process and bounded by the data it reads. An error, a failed sanity check or a panic is recovered, logged, counted in the engine's own compiler-failure counter, and the call proceeds on the history path.
- **No settings required.** The flag defaults to false and every other value is a constant.
- **No new chat output.** No items, notifications or prompt text. The compiler's report goes to the developer log only.

## Package (`internal/ctxcompiler`)

```go
type Input struct {
    Detail protocol.WorkDetail // zero value when the thread has no open work
    Items  []protocol.Item     // transcript, as ListItems returns it
    TurnID string              // current turn, excluded from the tail
    Window int                 // model context window, in tokens
}

type Result struct {
    Messages []llm.Message
    Report   Report
}

func Compile(in Input) (Result, bool)
```

The package is pure: no store, no engine, no I/O. The engine reads the work detail and the items and passes them in. `Compile` returns false when a sanity check fails, which means "use the history path".

`Report` records, for the developer log: criteria included, evidence rows included, tail messages included, items dropped with a reason per class, and the estimated tokens of each packet.

## Message layout

1. One synthetic user message, titled `Current work state`, holding the work packet then the evidence packet.
2. The interaction tail.
3. The new user message, unchanged by the compiler (including image parts).

The system prompt is not touched, so the stable prefix and its prompt-cache behavior are unchanged.

## Work packet (P0)

- The goal text.
- Every criterion as `title — status`, with its planned command. Statuses are `pending`, `passed`, `failed`, `stale` and `superseded`; superseded criteria are omitted.
- Unresolved failures: for each criterion whose latest attempt did not pass, that attempt's tail summary.
- The files changed in this work, as paths only.

A `stale` criterion is rendered as needing a re-run. It is never rendered as a pass.

## Evidence packet (P1 and P2)

- Active evidence only, as `work.ActiveEvidence` defines it.
- A failed check: its tail summary plus `[full output: vault <first 8 hex of hash>]` when a hash exists.
- A passing check: one line, included only while active.
- The five most recent failed-tool facts, newest first. Evidence rows carry no turn id, so recency is by `observed_at`.
- Evidence whose vault object is unavailable: summary only, marked truncated.

Text entering a packet is passed through `vault.Redact` again, so a row written before Phase 3's redaction cannot leak through a packet.

## Interaction tail

- The last 10 user and assistant message pairs, in order. `inbound_event` items count as user messages, as they do today.
- Plus the question-and-answer pair the current request depends on: when the last non-empty line of the newest assistant message in the tail ends with `?`, that message and the next user message after it are kept even if they fall outside the 10.
- Earlier tool-call notes and file-change notes are dropped; the work packet carries those facts.
- A compaction marker still wins: the tail starts at the summary and nothing before it is replayed.
- The result starts with a user message and alternates roles, as providers require; consecutive same-role text merges as it does today.

10 pairs is a constant in one place, not a config key.

**Known gap:** a user who refers to something said long ago, with no work record connecting it, loses that context. The full transcript remains stored and visible in the app, and Phase 4b's retrieval is where this is solved.

## Budget and priority

- The packets receive at most 15% of the model's context window; the tail at most 25%.
- Over budget, the compiler drops P2 first, then P1 by age. P0 is never dropped.
- Every drop is recorded in `Report` with its reason.

Priority follows the parent spec, mapped onto what Phase 2 and Phase 3 actually record:

- **P0:** the goal, the criteria and their statuses, unresolved failures. Never dropped.
- **P1:** failed-tool facts and the vault references for failures.
- **P2:** passing evidence lines and the older half of the tail.
- **P3:** not sent at all in this slice.

The parent spec's design decisions and memory candidates arrive in Phase 5, so P1 is thinner here than it will be later. Root `UMCODE.md` guidance already reaches the model through the system prompt from Phase 1, and the compiler does not duplicate it.

## Sanity checks and fallback

`Compile` returns false, and the engine uses `history()` for that call, when:

- the work has no goal;
- a thread has an open work but no work packet could be built;
- P0 alone exceeds the packet budget;
- `HistoryTokens` is non-zero and the compiled messages are estimated larger.

A panic inside the compiler is recovered by the engine, counted and logged, with the same fallback.

## Engine wiring

- One attachment point in `runTurn`, where `msgs` is built. With the flag on and an open work present, the engine compiles; otherwise it calls `e.history(...)` as today.
- The engine estimates the history path's size once per turn and passes it as `HistoryTokens`.
- Packets are rebuilt from the database before every model call inside the turn's tool loop, so an edit made mid-turn reaches the next call as a stale criterion.
- Automatic compaction and `trimToolResults` are unchanged. Compaction still protects the fallback path; `trimToolResults` still bounds the current turn's live tool results, which the compiler does not touch.

## Configuration

`models.context_compiler` (bool, default false) under the existing `ModelsConfig`. No other key, no UI, no protocol surface.

## Accounting

`RequestBreakdown` gains `WorkPacketTokens` and `EvidencePacketTokens`, carved out of `ConversationTokens` so `TotalTokens` is unchanged. The compiler's `Report` is logged beside the existing `context accounting` debug line.

## Testing

All new behavior follows TDD; each test is written and seen failing first.

**Unit (`internal/ctxcompiler`, table-driven)**
- Work packet: goal, every criterion with status, failing attempt summary; a `stale` criterion reads as needing a re-run, never as a pass.
- Evidence packet: active evidence only; stale, superseded and unavailable rows excluded; an unavailable row appears as summary marked truncated.
- A failed check carries its vault reference; a missing hash yields no reference and no error.
- Tail: last 10 pairs; older tool-call and file-change notes dropped; the answered-question pair kept when outside the 10.
- A compaction marker truncates the tail to the summary.
- Messages start with a user role and alternate.
- Budget: over-budget packets drop P2 then P1 by age, never P0, and each drop is reported.
- Sanity checks return false: no goal; P0 over budget; compiled messages larger than `HistoryTokens`.
- A pre-Phase-3 row holding a secret is redacted on its way into a packet.

**Engine**
- Flag off: the request is byte-identical to today's.
- Flag on with an open work: packets and shortened tail present, system prompt unchanged.
- Flag on with no open work: history path used.
- Compiler failure: falls back to history, counts a failure, turn completes.
- Mid-turn: pass, then a file edit, and the next model call in the same turn shows that criterion stale.
- A compiler panic is recovered and falls back.

**End-to-end (`internal/server`)**
- A plain chat turn produces the same items as before, with the flag off and on.
- An edit-and-verify turn with the flag on completes, and the token breakdown shows the packet lines.
- Same thread through both paths: the compiled request is smaller. This is a floor, not a benchmark; the paired benchmark is Phase 6.

## Review focus

- A packet that presents a stale or superseded result as current.
- Tail selection dropping a message the current request depends on (pronouns, "the second one", a mid-turn clarification).
- The fallback path not triggering when it should, or triggering so often that nothing is saved.
- Secrets reaching a packet from rows written before Phase 3.
- Role alternation or a non-user first message breaking a provider.
- Packets rebuilt per call costing noticeable latency in a long tool loop.

## Rollout and risk

The flag is off by default, so merging changes nothing for existing users. With it on, the worst case is the fallback path, which is today's behavior. The compiler reads only data Phase 2 and Phase 3 already write, so there is no migration. Phase 4b adds retrieval and tool reductions on top of this structure; Phase 6 measures the result and decides the default.
