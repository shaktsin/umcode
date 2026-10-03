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

// lastPackets is the packet accounting of the most recent successful
// compilation for a turn, for the request breakdown.
type compileOutcome struct {
	msgs    []llm.Message
	packets RequestPackets
}

// compileMessages builds the request prefix from the thread's recorded work
// instead of the transcript. The second result is false whenever the history
// path should be used: the flag is off, no work is open, a read failed, the
// compiler declined, or it panicked. Nothing here blocks or prompts.
func (e *Engine) compileMessages(ctx context.Context, th protocol.Thread, turnID string, window, historyTokens int) ([]llm.Message, bool) {
	out, ok := e.compile(ctx, th, turnID, window, historyTokens)
	if !ok {
		return nil, false
	}
	return out.msgs, true
}

func (e *Engine) compile(ctx context.Context, th protocol.Thread, turnID string, window, historyTokens int) (out compileOutcome, ok bool) {
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
	items, err := e.Store.ListItems(ctx, th.ID, 0)
	if err != nil {
		e.compilerFailures.Add(1)
		e.Log.Debug("context compiler: items failed", "err", err)
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
