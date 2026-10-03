package engine

import (
	"context"

	"github.com/shaktsin/umcode/internal/ctxcompiler"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/work"
)

// compileHook is a test seam: it runs before compilation so a test can inject a
// panic and prove the fallback.
var compileHook func()

// compileOutcome is one successful compilation: the request prefix and what it
// cost, for the request breakdown.
type compileOutcome struct {
	msgs    []llm.Message
	packets RequestPackets
}

// requestMessages puts the compiled prefix in front of this turn's live
// messages, copying rather than aliasing either slice.
func requestMessages(prefix, live []llm.Message) []llm.Message {
	out := make([]llm.Message, 0, len(prefix)+len(live))
	out = append(out, prefix...)
	return append(out, live...)
}

// compile builds the request prefix from the thread's recorded work instead of
// the transcript. ok is false whenever the history path should be used: the flag
// is off, no work is open, a read failed, the compiler declined, or it panicked.
// items is the transcript the caller already read, so a long tool loop does not
// re-read it for every model call. Nothing here blocks or prompts.
func (e *Engine) compile(ctx context.Context, th protocol.Thread, turnID string, window, historyTokens int, items []protocol.Item) (out compileOutcome, ok bool) {
	if e.Cfg == nil || !e.Cfg.Models.ContextCompiler || e.Work == nil {
		return compileOutcome{}, false
	}
	defer func() {
		if r := recover(); r != nil {
			e.compilerFailures.Add(1)
			e.Log.Warn("context compiler panicked", "thread", th.ID, "panic", r)
			out, ok = compileOutcome{}, false
		}
	}()
	if compileHook != nil {
		compileHook()
	}
	w, open, err := e.Store.OpenWorkForThread(ctx, th.ID)
	if err != nil {
		e.compilerFailures.Add(1)
		e.Log.Debug("context compiler: open work lookup failed", "err", err)
		return compileOutcome{}, false
	}
	if !open {
		return compileOutcome{}, false // a plain chat has nothing to compile
	}
	d, err := e.Store.GetWorkDetail(ctx, w.ID)
	if err != nil {
		e.compilerFailures.Add(1)
		e.Log.Debug("context compiler: work detail failed", "err", err)
		return compileOutcome{}, false
	}
	// Staleness is computed here rather than read from the node status, which is
	// only written back at turn end. The environment rule is left to that
	// write-back: running git for every model call would cost a turn latency.
	res, good := ctxcompiler.Compile(ctxcompiler.Input{Detail: d, Stale: work.Staleness(d, ""),
		Active: work.ActiveEvidence(d), Items: items, TurnID: turnID, Window: window, HistoryTokens: historyTokens})
	if !good {
		e.compilerFailures.Add(1)
		e.Log.Debug("context compiler declined", "thread", th.ID, "reason", res.Report.Declined)
		return compileOutcome{}, false
	}
	e.Log.Debug("context compiled", "thread", th.ID, "report", res.Report)
	return compileOutcome{msgs: res.Messages,
		packets: RequestPackets{Work: res.Report.WorkPacketTokens, Evidence: res.Report.EvidencePacketTokens}}, true
}
