import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import type { Approval } from '$lib/types';
import { app } from './app.svelte';

const seed: Approval = {
  id: 'a1', threadId: 't1', turnId: 'u1', itemId: 'i1', tool: 'shell.run', args: {}, risk: 'medium', reason: '',
  actionSummary: 'go test ./...', status: 'pending', createdAt: '2026-10-23T10:00:00Z', expiresAt: '2026-10-23T11:00:00Z',
};

beforeEach(() => {
  app.approvals = [seed];
  app.approvalRecords = [];
});
afterEach(() => vi.restoreAllMocks());

it('keeps the approval pending and writes no record when approval/respond fails', async () => {
  vi.spyOn(app, 'try').mockResolvedValue(undefined);
  await app.respondApproval('a1', true);
  expect(app.approvals.map((a) => a.id)).toEqual(['a1']);
  expect(app.approvalRecords).toEqual([]);
});

it('removes the approval and records the decision on success', async () => {
  vi.spyOn(app, 'try').mockResolvedValue({});
  await app.respondApproval('a1', false);
  expect(app.approvals).toEqual([]);
  expect(app.approvalRecords).toEqual([{ id: 'a1', threadId: 't1', turnId: 'u1', summary: 'go test ./...', decision: 'denied' }]);
});
