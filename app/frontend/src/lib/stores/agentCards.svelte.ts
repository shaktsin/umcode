import { app } from './app.svelte';
import { errMsg } from '$lib/format';
import type { AgentPlan, PlanState, AgentPlanGetResult, AgentPlanRespondParams, AgentPlanRespondResult, AgentSuggestion, AgentSuggestionRespondResult } from '$lib/types';

const STATE_RANK: Record<PlanState, number> = { proposed: 0, approved: 1, superseded: 2 };

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
  /** The single gate for offering Enable/Decline: only a confirmed pending suggestion. */
  suggestionActionable(id: string): boolean { return this.suggestions[id]?.state === 'pending'; }
  /** The single gate for offering Approve/Decline/Revise: a proposed plan whose approval is pending. */
  planActionable(planId: string): boolean {
    const p = this.plans[planId];
    return p?.plan.state === 'proposed' && p.approvalStatus === 'pending';
  }
  async refreshSuggestion(id: string) {
    try {
      const r = await app.call<{ suggestion: AgentSuggestion }>('agent/suggestion/get', { suggestionId: id });
      this.setSuggestion(id, r.suggestion); this.error = '';
    } catch (e) { this.error = errMsg(e); }
  }
  async refreshPlan(id: string) {
    try {
      const r = await app.call<AgentPlanGetResult>('agent/plan/get', { planId: id });
      this.setPlan(id, r); this.error = '';
    } catch (e) { this.error = errMsg(e); }
  }
  async respondSuggestion(id: string, decision: 'enable' | 'decline') {
    const r = await app.call<AgentSuggestionRespondResult>('agent/suggestion/respond', { suggestionId: id, decision });
    this.setSuggestion(id, r.suggestion);
    if (r.activationToken && r.expiresAt) this.activation = { activationToken: r.activationToken, expiresAt: r.expiresAt };
    return r;
  }
  async respondPlan(params: AgentPlanRespondParams) {
    const r = await app.call<AgentPlanRespondResult>('agent/plan/respond', params);
    this.applyPlanUpdate(r.plan.id, r.plan, r.approvalStatus);
    return r;
  }
  /** agent/plan/updated: a payload without approvalStatus cannot be acted on, so read the plan back. */
  onPlanUpdated(p: { plan: AgentPlan; approvalStatus?: AgentPlanGetResult['approvalStatus'] }) {
    this.applyPlanUpdate(p.plan.id, p.plan, p.approvalStatus);
    if (!p.approvalStatus) void this.refreshPlan(p.plan.id);
  }
  /** agent/plan/updated and respond results: keep the card, take the new plan and status; fetch the card if never loaded. */
  applyPlanUpdate(planId: string, plan: AgentPlan, approvalStatus?: AgentPlanGetResult['approvalStatus']) {
    const had = this.plans[planId]?.card;
    this.setPlan(planId, { plan, approvalStatus });
    if (!had) void this.loadCard(planId);
  }
  /** Fetch only the card (and its approval id) so a late reply cannot overwrite a newer plan or status. */
  private async loadCard(planId: string) {
    try {
      const r = await app.call<AgentPlanGetResult>('agent/plan/get', { planId });
      const cur = this.plans[planId];
      if (cur && r.card) this.plans[planId] = { ...cur, card: r.card, approvalId: r.approvalId ?? cur.approvalId };
    } catch { /* the next refreshPlan reports the failure */ }
  }
  // Plans carry no version: order by lifecycle, which only moves forward.
  private setSuggestion(id: string, next: AgentSuggestion) {
    const old = this.suggestions[id];
    if (old && old.state !== 'pending' && next.state === 'pending') return;
    this.suggestions[id] = next;
  }
  private setPlan(planId: string, next: Partial<AgentPlanGetResult> & { plan: AgentPlan }) {
    const old = this.plans[planId];
    if (old && STATE_RANK[next.plan.state] < STATE_RANK[old.plan.state]) return;
    const sameState = old?.plan.state === next.plan.state;
    let status = next.approvalStatus || (sameState ? old?.approvalStatus : undefined) || undefined;
    if (sameState && old?.approvalStatus && old.approvalStatus !== 'pending' && status === 'pending') status = old.approvalStatus;
    // A plan that is no longer proposed can never have a pending approval.
    if (next.plan.state === 'approved' && (!status || status === 'pending')) status = 'approved';
    if (next.plan.state === 'superseded' && (!status || status === 'pending')) status = 'expired';
    this.plans[planId] = { ...old, ...next, approvalStatus: status ?? '' };
  }
}
