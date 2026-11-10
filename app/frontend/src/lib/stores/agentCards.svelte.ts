import { app } from './app.svelte';
import { errMsg } from '$lib/format';
import type { AgentPlan, PlanState, AgentPlanGetResult, AgentPlanRespondParams, AgentPlanRespondResult } from '$lib/types';

const STATE_RANK: Record<PlanState, number> = { proposed: 0, approved: 1, superseded: 2 };

/**
 * State of agent.propose_plan cards. The backend grants authority on approval.
 */
export class AgentCards {
  private plans = $state<Record<string, AgentPlanGetResult>>({});
  error = $state('');
  plan(planId: string): AgentPlanGetResult | undefined { return this.plans[planId]; }
  approvedPlanNeedingContinuation(taskId: string): AgentPlanGetResult | undefined {
    return Object.values(this.plans).find(p => p.plan.taskId === taskId && p.plan.state === 'approved' && p.continuationNeeded === true);
  }
  /** The single gate for offering Approve/Decline/Revise: a proposed plan whose approval is pending. */
  planActionable(planId: string): boolean {
    const p = this.plans[planId];
    return p?.plan.state === 'proposed' && p.approvalStatus === 'pending';
  }
  async refreshPlan(id: string) {
    try {
      const r = await app.call<AgentPlanGetResult>('agent/plan/get', { planId: id });
      this.setPlan(id, r); this.error = '';
    } catch (e) { this.error = errMsg(e); }
  }
  async respondPlan(params: AgentPlanRespondParams) {
    const r = await app.call<AgentPlanRespondResult>('agent/plan/respond', params);
    this.applyPlanUpdate(r.plan.id, r.plan, r.approvalStatus, r.continuationNeeded);
    return r;
  }
  /** agent/plan/updated: a payload without approvalStatus cannot be acted on, so read the plan back. */
  onPlanUpdated(p: { plan: AgentPlan; approvalStatus?: AgentPlanGetResult['approvalStatus'] }) {
    this.applyPlanUpdate(p.plan.id, p.plan, p.approvalStatus);
    if (!p.approvalStatus) void this.refreshPlan(p.plan.id);
  }
  /** agent/plan/updated and respond results: keep the card, take the new plan and status; fetch the card if never loaded. */
  applyPlanUpdate(planId: string, plan: AgentPlan, approvalStatus?: AgentPlanGetResult['approvalStatus'], continuationNeeded?: boolean) {
    const had = this.plans[planId]?.card;
    this.setPlan(planId, { plan, approvalStatus, continuationNeeded });
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
  private setPlan(planId: string, next: Partial<AgentPlanGetResult> & { plan: AgentPlan }) {
    const old = this.plans[planId];
    if (old && STATE_RANK[next.plan.state] < STATE_RANK[old.plan.state]) return;
    const sameState = old?.plan.state === next.plan.state;
    let status = next.approvalStatus || (sameState ? old?.approvalStatus : undefined) || undefined;
    if (sameState && old?.approvalStatus && old.approvalStatus !== 'pending' && status === 'pending') status = old.approvalStatus;
    // A plan that is no longer proposed can never have a pending approval.
    if (next.plan.state === 'approved' && (!status || status === 'pending')) status = 'approved';
    if (next.plan.state === 'superseded' && (!status || status === 'pending')) status = 'expired';
    this.plans[planId] = { ...old, ...next, continuationNeeded: next.continuationNeeded ?? (sameState ? old?.continuationNeeded : undefined), approvalStatus: status ?? '' };
  }
}
