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
  it('keeps a recovered inspection error in tool history without a red chat alert', () => {
    const advisory = { category: 'execution_failed', operationId: 'search1', message: 'editor.tsx is not a folder', retryable: true, advisory: true };
    const items = [item('search', 'toolCall', '', advisory), item('error', 'error', JSON.stringify(advisory), advisory), item('answer', 'agentMessage', 'I inspected the editor.', null)];
    const presentation = taskFailurePresentation(items, '', 'completed');
    expect(presentation.failure).toBeNull();
    expect(presentation.hiddenItemIds).toContain('error');
    expect(presentation.hiddenItemIds).not.toContain('search');
    expect(taskFailurePresentation(JSON.parse(JSON.stringify(items)), '', 'completed')).toEqual(presentation);
  });
  it('recognizes older retryable file.search records whose advisory flag was not saved', () => {
    const oldFailure = { category: 'execution_failed', operationId: 'search1', message: 'editor.tsx is not a folder', retryable: true };
    const search = { ...item('search', 'toolCall', '', oldFailure), tool: { name: 'file.search' } } as Item;
    const presentation = taskFailurePresentation([search, item('answer', 'agentMessage', 'Status found', null)], '', 'completed');
    expect(presentation.failure).toBeNull();
    expect(presentation.hiddenItemIds).not.toContain('search');
  });
  it('keeps a failed read-back warning actionable', () => {
    const readback = { category: 'verification_failed', operationId: 'read1', message: 'Changed file could not be verified', retryable: true, advisory: false };
    const presentation = taskFailurePresentation([item('error', 'error', JSON.stringify(readback), readback)], '');
    expect(presentation.failure?.category).toBe('verification_failed');
    expect(presentation.hiddenItemIds).toContain('error');
  });
  it('does not promote an advisory turn error into a blocker', () => {
    const advisory = { category: 'execution_failed', operationId: 'search1', message: 'Bad search path', retryable: true, advisory: true };
    const presentation = taskFailurePresentation([], JSON.stringify(advisory), 'completed');
    expect(presentation.failure).toBeNull();
    expect(presentation.showTurnError).toBe(false);
    expect(taskFailurePresentation([], JSON.stringify(advisory), 'failed').failure?.category).toBe('execution_failed');
  });
  it('clears a verified failure and its summary while retaining the failed tool record', () => {
    const retryable = { category: 'execution_failed', operationId: 'op1', message: 'First attempt failed', retryable: true };
    const resolved = { actionFailure: retryable, actionFailureResolvedById: 'retry-op' };
    const tool = { ...item('tool', 'toolCall', '', retryable), data: resolved };
    const summary = { ...item('summary', 'agentMessage', 'The original action failed', retryable), data: resolved };
    const presentation = taskFailurePresentation([tool, summary], '', 'completed');
    expect(presentation.failure).toBeNull();
    expect(presentation.hiddenItemIds).toContain('summary');
    expect(presentation.hiddenItemIds).not.toContain('tool');
    expect(taskFailurePresentation(JSON.parse(JSON.stringify([tool, summary])), '', 'completed')).toEqual(presentation);
  });
  it('clears a verified failure recorded on the original failed turn', () => {
    const retryable = { category: 'execution_failed', operationId: 'op1', message: 'First attempt failed', retryable: true };
    const tool = { ...item('tool', 'toolCall', '', retryable), data: { actionFailure: retryable, actionFailureResolvedById: 'retry-op' } };
    const presentation = taskFailurePresentation([tool], JSON.stringify(retryable), 'failed');
    expect(presentation.failure).toBeNull();
    expect(presentation.showTurnError).toBe(false);
  });
});
