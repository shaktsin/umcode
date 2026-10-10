// Package optimization carries immutable per-turn optimization policy.
package optimization

import "context"

type Policy struct {
	ContextCompiler, ContextRetrieval, ProgressiveTools, ToolResultReducers bool
	DesignedWorkflow, AutoPromote, AutomaticWorkflow                        bool
}

func All(enabled bool) Policy {
	return Policy{enabled, enabled, enabled, enabled, enabled, enabled, enabled}
}

type policyKey struct{}

func WithPolicy(ctx context.Context, p Policy) context.Context {
	return context.WithValue(ctx, policyKey{}, p)
}
func FromContext(ctx context.Context) (Policy, bool) {
	p, ok := ctx.Value(policyKey{}).(Policy)
	return p, ok
}
