import { app } from './app.svelte';
import type { AgentPlanGetResult, AgentPlanRespondParams, AgentPlanRespondResult, AgentPlanUpdated, AgentSuggestion, AgentSuggestionRespondResult } from '$lib/types';

/**
 * State of agent.suggest and agent.propose_plan cards. It only reads through the
 * get RPCs and forwards the user's answer; it grants nothing itself.
 */
export class AgentCards {
  private suggestions = $state<Record<string, AgentSuggestion>>({});
  private plans = $state<Record<string, AgentPlanGetResult>>({});
  /** Activation token of the last enable, for the follow-up turn/start. */
  activation = $state<{ activationToken: string; expiresAt: string } | null>(null);
  error = $state('');
  suggestion(id: string): AgentSuggestion | undefined { return this.suggestions[id]; }
  plan(planId: string): AgentPlanGetResult | undefined { return this.plans[planId]; }
  async refreshSuggestion(id: string) {
    const r = await app.call<{ suggestion: AgentSuggestion }>('agent/suggestion/get', { suggestionId: id });
    this.suggestions[id] = r.suggestion;
  }
  async refreshPlan(id: string) {
    this.plans[id] = await app.call<AgentPlanGetResult>('agent/plan/get', { planId: id });
  }
  async respondSuggestion(id: string, decision: 'enable' | 'decline') {
    const r = await app.call<AgentSuggestionRespondResult>('agent/suggestion/respond', { suggestionId: id, decision });
    this.suggestions[id] = r.suggestion;
    if (r.activationToken && r.expiresAt) this.activation = { activationToken: r.activationToken, expiresAt: r.expiresAt };
    return r;
  }
  async respondPlan(params: AgentPlanRespondParams) {
    const r = await app.call<AgentPlanRespondResult>('agent/plan/respond', params);
    this.applyPlanUpdate(r.plan.id, r.plan, r.approvalStatus);
    return r;
  }
  /** Fetch only the card (and its approval id) so a late reply cannot overwrite a newer plan or status. */
  private async loadCard(planId: string) {
    try {
      const r = await app.call<AgentPlanGetResult>('agent/plan/get', { planId });
      const cur = this.plans[planId];
      if (cur && r.card) this.plans[planId] = { ...cur, card: r.card, approvalId: r.approvalId ?? cur.approvalId };
    } catch { /* the next refreshPlan reports the failure */ }
  }
  /** agent/plan/updated: keep the card, take the new plan and status; fetch the card if never loaded. */
  applyPlanUpdate(planId: string, plan: AgentPlanUpdated['plan'], approvalStatus?: AgentPlanGetResult['approvalStatus']) {
    const old = this.plans[planId];
    this.plans[planId] = { ...old, plan, approvalStatus: approvalStatus || old?.approvalStatus };
    if (!old?.card) void this.loadCard(planId);
  }
}
