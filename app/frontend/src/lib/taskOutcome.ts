import type { ActionFailure, Item } from './types';

function asFailure(value: unknown): ActionFailure | null {
  if (!value || typeof value !== 'object') return null;
  const f = value as Partial<ActionFailure>;
  return typeof f.category === 'string' && typeof f.message === 'string' && typeof f.retryable === 'boolean'
    && (f.operationId === undefined || typeof f.operationId === 'string')
    && (f.blockerId === undefined || typeof f.blockerId === 'string')
    && (f.nextStep === undefined || typeof f.nextStep === 'string') ? f as ActionFailure : null;
}
export function parseActionFailure(text: string): ActionFailure | null {
  try { return asFailure(JSON.parse(text)); } catch { return null; }
}
export function itemActionFailure(item: Item): ActionFailure | null {
  const data = item.data as { actionFailure?: unknown } | undefined;
  if (resolvedFailure(item)) return null;
  return asFailure(data?.actionFailure) || (item.kind === 'error' ? parseActionFailure(item.text || '') : null);
}
/** agent.* tools, by registered (agent.spawn) or wire (agent__spawn) name. */
export function isAgentTool(name: string | undefined): boolean {
  return typeof name === 'string' && /^agent(\.|__)/.test(name);
}
/**
 * A failed or denied agent.* tool call: the engine's short refusal (the
 * failure message, else the tool error) and its audit record for a
 * collapsed "Technical details" disclosure. Null for any other item.
 */
export function agentToolFailure(item: Item): { message: string; details: string } | null {
  if (item.kind !== 'toolCall' || !isAgentTool(item.tool?.name) || (item.status !== 'failed' && item.status !== 'denied')) return null;
  const failure = asFailure((item.data as { actionFailure?: unknown } | undefined)?.actionFailure);
  const message = failure?.message || item.tool?.error || '';
  if (!message) return null;
  return { message, details: JSON.stringify(failure ?? { error: item.tool?.error }, null, 2) };
}
function resolvedFailure(item: Item): boolean {
  const data = item.data as { actionFailureResolvedById?: unknown } | undefined;
  return typeof data?.actionFailureResolvedById === 'string' && data.actionFailureResolvedById.length > 0;
}
function resolvedTurnFailure(items: Item[], failure: ActionFailure | null): boolean {
  return Boolean(failure?.operationId && items.some(item => {
    if (!resolvedFailure(item)) return false;
    const data = item.data as { actionFailure?: unknown } | undefined;
    return asFailure(data?.actionFailure)?.operationId === failure.operationId;
  }));
}
function correlated(a: ActionFailure | null, b: ActionFailure | null): boolean {
  if (!a || !b) return false;
  // A shared operation identifies its failure even when recovery adds a blocker.
  // Distinct operations may share one explicit permission blocker.
  return Boolean(a.operationId && a.operationId === b.operationId || a.blockerId && a.blockerId === b.blockerId);
}
// Older tool records omitted the advisory flag that the engine stored on the
// corresponding operation. A completed retryable file search is exploratory.
function legacyAdvisorySearch(item: Item, turnStatus: string): boolean {
  const failure = itemActionFailure(item);
  return turnStatus === 'completed' && item.kind === 'toolCall' && item.tool?.name === 'file.search'
    && failure?.category === 'execution_failed' && failure.retryable && !failure.advisory;
}
export function primaryTaskFailure(items: Item[], turnError: string, turnStatus = ''): ActionFailure | null {
  for (const item of [...items].reverse()) {
    if (item.kind !== 'agentMessage') continue;
    const f = itemActionFailure(item);
    if (f && !f.advisory) return f;
  }
  for (const item of [...items].reverse()) {
    const f = itemActionFailure(item);
    if (f && !f.advisory && !legacyAdvisorySearch(item, turnStatus)) return f;
  }
  const turnFailure = parseActionFailure(turnError);
  return (turnFailure?.advisory && turnStatus === 'completed') || resolvedTurnFailure(items, turnFailure) ? null : turnFailure;
}
export function taskFailurePresentation(items: Item[], turnError: string, turnStatus = '') {
  const failure = primaryTaskFailure(items, turnError, turnStatus);
  // Without an identity we cannot safely hide historical messages.
  const matching = items.filter(item => correlated(itemActionFailure(item), failure));
  const summary = [...matching].reverse().find(item => item.kind === 'agentMessage');
  // An agent tool refusal is shown by its short message; its next step is
  // advice for the model, and its audit record is only the failure.
  const agent = summary ? null : matching.map(agentToolFailure).find(Boolean) ?? null;
  const text = summary?.text || agent?.message || (failure ? [failure.message, failure.nextStep].filter(Boolean).join(' ') : '');
  return {
    failure, text,
    details: agent ? agent.details : [...items.filter(item => matching.includes(item) || isProvisionalExecution(item)).map(item => JSON.stringify(item, null, 2)), turnError].filter(Boolean).join('\n\n'),
    hiddenItemIds: items.filter(item => (item.kind === 'agentMessage' || item.kind === 'error')
      && (matching.includes(item) || resolvedFailure(item) || turnStatus === 'completed' && item.kind === 'error' && itemActionFailure(item)?.advisory)).map(item => item.id),
    showTurnError: Boolean(turnError) && !resolvedTurnFailure(items, parseActionFailure(turnError))
      && !(turnStatus === 'completed' && parseActionFailure(turnError)?.advisory)
      && !correlated(parseActionFailure(turnError), failure),
  };
}

export function isProvisionalExecution(item: Item): boolean {
  return item.kind === 'agentMessage' && Boolean(item.data && typeof item.data === 'object'
    && (item.data as { provisionalExecution?: unknown }).provisionalExecution === true);
}

export function executionOutcomePresentation(items: Item[], turnStatus: string): { text: string; details: string; show: boolean } {
  if (items.some(item => item.data && typeof item.data === 'object'
    && (item.data as { executionPolicy?: string }).executionPolicy === 'model_driven_v1')) {
    return { text: '', details: '', show: false };
  }
  const final = [...items].reverse().find(item => item.kind === 'agentMessage' && !isProvisionalExecution(item)
    && item.data && typeof item.data === 'object' && typeof (item.data as { taskOutcome?: { status?: unknown } }).taskOutcome?.status === 'string');
  const outcome = final?.data && typeof final.data === 'object'
    ? (final.data as { taskOutcome?: { status?: unknown; evidenceIds?: unknown } }).taskOutcome : undefined;
  let text = '';
  switch (outcome?.status) {
    case 'not_executed': text = 'Legacy engine reported that the model did not execute the requested action.'; break;
    case 'unverified': text = 'Legacy engine reported incomplete verification of the requested result.'; break;
    case 'satisfied':
      if (Array.isArray(outcome.evidenceIds) && outcome.evidenceIds.length > 0) text = 'Legacy engine recorded verification evidence.';
      break;
  }
  return {
    text,
    show: Boolean(text) && turnStatus !== 'running',
    details: items.filter(item => isProvisionalExecution(item) || item === final).map(item => JSON.stringify(item, null, 2)).join('\n\n'),
  };
}
