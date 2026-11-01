import type { Approval, Item, PlanCard, PlanCardAgent, PlanCardModel, PlanLimits, AgentPlanRespondParams } from './types';

/** Result JSON of agent.suggest (internal/engine/agentrt_activation.go). */
export function parseSuggestionResult(output?: string): { suggestionId: string; state: string } | null {
  const r = parseObject(output);
  return r && typeof r.suggestionId === 'string' && r.suggestionId ? { suggestionId: r.suggestionId, state: String(r.state ?? '') } : null;
}

/** Result JSON of agent.propose_plan: approved carries planId, revise carries feedback, declined a message. */
export function parsePlanResult(output?: string): { planId?: string; state?: string; feedback?: string; message?: string } | null {
  const r = parseObject(output);
  if (!r) return null;
  const s = (v: unknown) => (typeof v === 'string' && v ? v : undefined);
  return { planId: s(r.planId), state: s(r.state), feedback: s(r.feedback), message: s(r.message) };
}

function parseObject(output?: string): Record<string, unknown> | null {
  if (!output) return null;
  try { const v = JSON.parse(output); return v && typeof v === 'object' && !Array.isArray(v) ? v as Record<string, unknown> : null; } catch { return null; }
}

/** The plan an agent.propose_plan item belongs to: the pending agent_plan approval's card, else the approved result. */
export function planIdForItem(item: Item, approvals: Approval[]): string | undefined {
  const a = approvals.find(x => x.itemId === item.id && (x.kind === 'agent_plan' || x.tool === 'agent.propose_plan'));
  const fromCard = (a?.args as { planId?: unknown } | undefined)?.planId;
  if (typeof fromCard === 'string' && fromCard) return fromCard;
  return parsePlanResult(item.tool?.output)?.planId;
}

export const modelUsable = (m: PlanCardModel) => m.toolsOk && m.available;

/**
 * Plan limit ceilings: tools.MaxPlanConcurrent / MaxPlanDepth / MaxPlanTotal / MaxPlanSliceShare
 * (internal/tools/agentrt.go) as applied by clampPlanLimits (internal/engine/agentrt_plan.go).
 * The card's own limits are the lead's proposal and only the defaults.
 */
export const PLAN_CEILINGS = { maxConcurrent: 8, maxDepth: 3, maxTotal: 40, sliceShare: 0.5 } as const;

export interface PlanEdits { models: Record<string, string>; dropped: string[]; limits: Partial<PlanLimits>; }
export interface PlanDraft { models: Record<string, string>; dropped: string[]; limits: PlanLimits; }
export const noEdits = (): PlanEdits => ({ models: {}, dropped: [], limits: {} });

/**
 * What the select shows and Approve keeps: the entry's own model (even when unusable, so the user sees it),
 * else the previous plan's assignment of the same name when usable, else '' (the chat model).
 */
export function defaultModel(card: PlanCard, a: PlanCardAgent): string {
  if (a.model) return a.model;
  const usable = new Set((card.models ?? []).filter(modelUsable).map(m => m.name));
  const prev = (card.previous ?? []).find(p => p.name.toLowerCase() === a.name.toLowerCase());
  return prev && usable.has(prev.model) ? prev.model : '';
}

/** The entry's own model when it is not an exact pool name: shown as a disabled option so the select never lies. */
export const modelOutsidePool = (card: PlanCard, a: PlanCardAgent) => !!a.model && !(card.models ?? []).some(m => m.name === a.model);

/**
 * Bounds each limit to the protocol ceilings. The card does not say whether the project allows network,
 * so the network box can only lower a plan that already has it, never turn it on.
 */
export function clampLimits(l: PlanLimits, proposedNetwork: boolean): PlanLimits {
  const int = (v: number, max: number) => Math.max(1, Math.min(Math.floor(Number.isFinite(v) ? v : 1), max));
  const share = Number.isFinite(l.sliceShare) ? Math.min(Math.max(l.sliceShare, 0.01), PLAN_CEILINGS.sliceShare) : PLAN_CEILINGS.sliceShare;
  return { maxConcurrent: int(l.maxConcurrent, PLAN_CEILINGS.maxConcurrent), maxDepth: int(l.maxDepth, PLAN_CEILINGS.maxDepth), maxTotal: int(l.maxTotal, PLAN_CEILINGS.maxTotal), sliceShare: share, network: proposedNetwork && l.network };
}

/** An empty or invalid field is "unchanged": it keeps the last valid value instead of becoming 1. */
export function applyLimit(edits: PlanEdits, key: 'maxConcurrent' | 'maxDepth' | 'maxTotal' | 'sliceShare', v: number): PlanEdits {
  const limits = { ...edits.limits };
  if (Number.isFinite(v)) limits[key] = v;
  return { ...edits, limits };
}

export function draftOf(card: PlanCard, edits: PlanEdits): PlanDraft {
  const models: Record<string, string> = {};
  for (const a of card.agents) models[a.name] = a.name in edits.models ? edits.models[a.name] : defaultModel(card, a);
  return { models, dropped: edits.dropped.filter(n => card.agents.some(a => a.name === n)), limits: clampLimits({ ...card.limits, ...edits.limits }, card.limits.network) };
}

export const kept = (card: PlanCard, d: PlanDraft) => card.agents.filter(a => !d.dropped.includes(a.name));
/** maxTotal below the kept agents cannot hold the plan: flagged, and Approve stays disabled (maxConcurrent may legitimately be lower). */
export function limitsError(card: PlanCard, d: PlanDraft): string {
  const n = kept(card, d).length;
  return n > 0 && d.limits.maxTotal < n ? `Max agents (${d.limits.maxTotal}) is below the ${n} kept agents. Raise it or drop agents.` : '';
}
/** Approve needs at least one agent left and limits that hold them. */
export const canApprove = (card: PlanCard, d: PlanDraft) => kept(card, d).length > 0 && !limitsError(card, d);

const sameLimits = (a: PlanLimits, b: PlanLimits) => a.maxConcurrent === b.maxConcurrent && a.maxDepth === b.maxDepth && a.maxTotal === b.maxTotal && a.sliceShare === b.sliceShare && a.network === b.network;

/** Only what the user changed travels: models that differ from the entry's own, dropped names, limits that differ from the card. */
export function approveParams(card: PlanCard, d: PlanDraft): AgentPlanRespondParams {
  const p: AgentPlanRespondParams = { planId: card.planId, decision: 'approve' };
  const models: Record<string, string> = {};
  for (const a of kept(card, d)) if (d.models[a.name] && d.models[a.name] !== (a.model ?? '')) models[a.name] = d.models[a.name];
  if (Object.keys(models).length) p.models = models;
  if (d.dropped.length) p.drop = [...d.dropped];
  if (!sameLimits(d.limits, card.limits)) p.limits = d.limits;
  return p;
}
export const declineParams = (planId: string): AgentPlanRespondParams => ({ planId, decision: 'decline' });
/** Revise needs feedback. */
export function reviseParams(planId: string, feedback: string): AgentPlanRespondParams | null {
  const f = feedback.trim();
  return f ? { planId, decision: 'revise', feedback: f } : null;
}
