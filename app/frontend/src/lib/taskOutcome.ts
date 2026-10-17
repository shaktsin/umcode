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
    details: [...matching.map(item => JSON.stringify(item, null, 2)), turnError].filter(Boolean).join('\n\n'),
    hiddenItemIds: matching.filter(item => item.kind === 'agentMessage' || item.kind === 'error').map(item => item.id),
    showTurnError: Boolean(turnError) && !correlated(parseActionFailure(turnError), failure),
  };
}
