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
  return asFailure(data?.actionFailure) || (item.kind === 'error' ? parseActionFailure(item.text || '') : null);
}
function correlated(a: ActionFailure | null, b: ActionFailure | null): boolean {
  if (!a || !b) return false;
  // A shared operation identifies its failure even when recovery adds a blocker.
  // Distinct operations may share one explicit permission blocker.
  return Boolean(a.operationId && a.operationId === b.operationId || a.blockerId && a.blockerId === b.blockerId);
}
export function primaryTaskFailure(items: Item[], turnError: string): ActionFailure | null {
  for (const item of [...items].reverse()) {
    if (item.kind !== 'agentMessage') continue;
    const f = itemActionFailure(item);
    if (f && !f.advisory) return f;
  }
  for (const item of [...items].reverse()) {
    const f = itemActionFailure(item);
    if (f && !f.advisory) return f;
  }
  return parseActionFailure(turnError);
}
export function taskFailurePresentation(items: Item[], turnError: string) {
  const failure = primaryTaskFailure(items, turnError);
  // Without an identity we cannot safely hide historical messages.
  const matching = items.filter(item => correlated(itemActionFailure(item), failure));
  const summary = [...matching].reverse().find(item => item.kind === 'agentMessage');
  const text = summary?.text || (failure ? [failure.message, failure.nextStep].filter(Boolean).join(' ') : '');
  return {
    failure, text,
    details: [...items.filter(item => matching.includes(item) || isProvisionalExecution(item)).map(item => JSON.stringify(item, null, 2)), turnError].filter(Boolean).join('\n\n'),
    hiddenItemIds: matching.filter(item => item.kind === 'agentMessage' || item.kind === 'error').map(item => item.id),
    showTurnError: Boolean(turnError) && !correlated(parseActionFailure(turnError), failure),
  };
}

export function isProvisionalExecution(item: Item): boolean {
  return item.kind === 'agentMessage' && Boolean(item.data && typeof item.data === 'object'
    && (item.data as { provisionalExecution?: unknown }).provisionalExecution === true);
}

export function executionOutcomePresentation(items: Item[], turnStatus: string): { text: string; details: string; show: boolean } {
  const final = [...items].reverse().find(item => item.kind === 'agentMessage' && !isProvisionalExecution(item)
    && item.data && typeof item.data === 'object' && typeof (item.data as { taskOutcome?: { status?: unknown } }).taskOutcome?.status === 'string');
  const outcome = final?.data && typeof final.data === 'object'
    ? (final.data as { taskOutcome?: { status?: unknown; evidenceIds?: unknown } }).taskOutcome : undefined;
  let text = '';
  switch (outcome?.status) {
    case 'not_executed': text = 'The model did not execute the requested action.'; break;
    case 'unverified': text = 'Changes ran; verification of the requested result is incomplete.'; break;
    case 'satisfied':
      if (Array.isArray(outcome.evidenceIds) && outcome.evidenceIds.length > 0) text = 'Verified the requested result.';
      break;
  }
  return {
    text,
    show: Boolean(text) && turnStatus !== 'running',
    details: items.filter(item => isProvisionalExecution(item) || item === final).map(item => JSON.stringify(item, null, 2)).join('\n\n'),
  };
}
