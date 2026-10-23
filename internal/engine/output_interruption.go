package engine

import (
	"errors"
	"github.com/shaktsin/umcode/internal/llm"
	"strings"
)

type outputInterruptionKind string

const (
	interruptionContext outputInterruptionKind = "context_exhausted"
	interruptionOutput  outputInterruptionKind = "output_cap"
	interruptionUnknown outputInterruptionKind = "unknown"
)

func classifyOutputInterruption(reason string, usage llm.Usage, r requestBudgetReport, providerContextError bool) outputInterruptionKind {
	if providerContextError {
		return interruptionContext
	}
	if usage.Reported && r.Window > 0 && usage.InputTokens+usage.OutputTokens >= int64(r.Window) {
		return interruptionContext
	}
	switch strings.ToLower(reason) {
	case "max_tokens", "max_output_tokens":
		return interruptionOutput
	}
	if usage.Reported && r.ReservedOutput > 0 && usage.OutputTokens >= int64(r.ReservedOutput) {
		return interruptionOutput
	}
	return interruptionUnknown
}

type outputInterruptionError struct{ Kind outputInterruptionKind }

func (e *outputInterruptionError) Error() string {
	switch e.Kind {
	case interruptionContext:
		return "model response interrupted: context window exhausted; work is incomplete. Partial text was saved and incomplete tool calls were not executed. Original sources remain saved."
	case interruptionOutput:
		return "model response reached its output limit; work is incomplete. Partial text was saved and incomplete tool calls were not executed. Continue with a larger response allowance if endpoint capacity permits."
	default:
		return "model response interrupted at a provider length limit; work is incomplete. The provider did not identify whether context or output capacity was exhausted. Partial text was saved and incomplete tool calls were not executed."
	}
}
func (*outputInterruptionError) Unwrap() error { return errModelOutputLimit }
func confirmedContextOverflow(err error) bool {
	var interrupted *outputInterruptionError
	if errors.As(err, &interrupted) {
		return interrupted.Kind == interruptionContext
	}
	var provider *llm.Error
	if !errors.As(err, &provider) {
		return false
	}
	if provider.Status != 400 && provider.Status != 413 {
		return false
	}
	body := strings.ToLower(provider.Body)
	for _, term := range []string{"context_length_exceeded", "maximum context length", "context window exceeded", "context window is full", "prompt is too long", "input exceeds context"} {
		if strings.Contains(body, term) {
			return true
		}
	}
	return false
}
