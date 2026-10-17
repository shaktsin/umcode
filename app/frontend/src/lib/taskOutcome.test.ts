import { describe, it, expect } from 'vitest';
import type { Item } from './types';
import { primaryTaskFailure, taskFailurePresentation } from './taskOutcome';
const failure = { category: 'policy_denied', operationId: 'op1', blockerId: 'req1', message: 'The check is blocked.', retryable: false, nextStep: 'Review the current permission.' };
const item = (id: string, kind: Item['kind'], text: string, f: unknown = failure): Item => ({ id, kind, text, threadId: 'th', turnId: 'turn', seq: 1, status: 'failed', createdAt: '', data: { actionFailure: f } });
describe('task blocker presentation', () => {
  it('correlates summary, error and turn by operation and retains audit details', () => {
    const items = [item('summary', 'agentMessage', 'Changes already ran: file.write notes.txt. Final verification is incomplete.'), item('error', 'error', JSON.stringify(failure)), item('tool', 'toolCall', 'Original tool audit')];
    const p = taskFailurePresentation(items, JSON.stringify(failure));
    expect(primaryTaskFailure(items, JSON.stringify(failure))?.operationId).toBe('op1');
    expect(p.hiddenItemIds).toEqual(['summary', 'error']);
    expect(p.text).toContain('Changes already ran');
    expect(p.text).toContain('verification is incomplete');
    expect(p.details).toContain('Original tool audit');
    expect(p.showTurnError).toBe(false);
  });
  it('preserves unrelated errors and never deduplicates by message alone', () => {
    const items = [item('other', 'error', 'Another operation failed', { ...failure, operationId: 'op2', blockerId: 'req2' }), item('summary', 'agentMessage', failure.message)];
    const p = taskFailurePresentation(items, 'Provider connection closed');
    expect(p.hiddenItemIds).toEqual(['summary']);
    expect(p.showTurnError).toBe(true);
  });
  it('produces the same result after stream replacement and reload', () => {
    const streaming = new Map<string, Item>();
    streaming.set('summary', item('summary', 'agentMessage', 'Unsupported claim', null));
    streaming.set('summary', item('summary', 'agentMessage', failure.message));
    const items = [...streaming.values()];
    expect(taskFailurePresentation(items, JSON.stringify(failure))).toEqual(taskFailurePresentation(JSON.parse(JSON.stringify(items)), JSON.stringify(failure)));
  });
  it('keeps legacy text errors and unrelated joined errors visible', () => {
    expect(primaryTaskFailure([], 'HTTP 400')).toBeNull();
    expect(taskFailurePresentation([], 'HTTP 400').showTurnError).toBe(true);
    expect(taskFailurePresentation([item('summary','agentMessage',failure.message)], JSON.stringify(failure)+'\nProvider broke').showTurnError).toBe(true);
  });
});
