import { describe, expect, it } from 'vitest';
import type { Approval } from '$lib/types';
import { buildRecord, dockState, recordLine, recordsFor, type ApprovalRecord } from './approvalDock';

function approval(over: Partial<Approval> = {}): Approval {
  return {
    id: 'a1', threadId: 't1', turnId: 'u1', itemId: 'i1', tool: 'shell.run', args: {}, risk: 'medium', reason: '',
    actionSummary: 'go test ./...', status: 'pending', createdAt: '2026-10-23T10:00:00Z', expiresAt: '2026-10-23T11:00:00Z', ...over,
  };
}

it('shows the oldest pending approval first with the total count', () => {
  const newer = approval({ id: 'a2', createdAt: '2026-10-23T10:05:00Z' });
  const older = approval({ id: 'a1', createdAt: '2026-10-23T10:00:00Z' });
  const s = dockState([newer, older], 't1');
  expect(s.current?.id).toBe('a1');
  expect(s.total).toBe(2);
});

it('excludes agent-child approvals', () => {
  expect(dockState([approval({ agentChildId: 'c1' })], 't1')).toEqual({ current: null, total: 0 });
});

it('excludes other threads and a null thread', () => {
  expect(dockState([approval({ threadId: 't2' })], 't1')).toEqual({ current: null, total: 0 });
  expect(dockState([approval()], null)).toEqual({ current: null, total: 0 });
});

it('formats record lines', () => {
  const r = (decision: ApprovalRecord['decision']): ApprovalRecord => ({ id: 'a1', threadId: 't1', turnId: 'u1', summary: 'go test ./...', decision });
  expect(recordLine(r('approved'))).toBe('Approved · go test ./...');
  expect(recordLine(r('approved_always'))).toBe('Approved for this chat · go test ./...');
  expect(recordLine(r('denied'))).toBe('Denied · go test ./...');
});

it('builds records from the decision', () => {
  const a = approval();
  expect(buildRecord(a, true, true).decision).toBe('approved_always');
  expect(buildRecord(a, true, false).decision).toBe('approved');
  expect(buildRecord(a, false, true).decision).toBe('denied');
  expect(buildRecord(a, true, false)).toMatchObject({ id: 'a1', threadId: 't1', turnId: 'u1', summary: 'go test ./...' });
  expect(buildRecord(approval({ actionSummary: '' }), true, false).summary).toBe('Run shell.run');
});

it('filters records by thread and turn keeping order', () => {
  const mk = (id: string, threadId: string, turnId: string): ApprovalRecord => ({ id, threadId, turnId, summary: id, decision: 'approved' });
  const out = recordsFor([mk('1', 't1', 'u1'), mk('2', 't2', 'u1'), mk('3', 't1', 'u2'), mk('4', 't1', 'u1')], 't1', 'u1');
  expect(out.map((r) => r.id)).toEqual(['1', '4']);
});

import { dockFocusTarget } from './approvalDock';
describe('dockFocusTarget', () => {
  const p = { viewId: 't1', dockId: 'a1' };
  it('moves into the dock for the next approval', () => expect(dockFocusTarget(p, { viewId: 't1', dockId: 'a2' }, true)).toBe('dock'));
  it('returns to the input when the dock closes', () => expect(dockFocusTarget(p, { viewId: 't1', dockId: null }, true)).toBe('input'));
  it('never moves focus on a thread switch', () => expect(dockFocusTarget(p, { viewId: 't2', dockId: null }, true)).toBeNull());
  it('does not steal focus from elsewhere', () => expect(dockFocusTarget(p, { viewId: 't1', dockId: null }, false)).toBeNull());
  it('ignores a dock that just opened', () => expect(dockFocusTarget({ viewId: 't1', dockId: null }, p, true)).toBeNull());
});
