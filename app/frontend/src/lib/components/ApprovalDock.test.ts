import { expect, it, vi } from 'vitest';
import { render } from 'svelte/server';
vi.mock('$lib/stores/app.svelte', () => ({ app: { approvals: [], respondApproval() {}, try: async () => undefined } }));
import ApprovalDock from './ApprovalDock.svelte';

const mk = (id: string, createdAt: string) => ({
  id, threadId: 't1', turnId: 'u1', itemId: 'i', tool: 'shell.run', args: {}, risk: 'high', reason: '',
  actionSummary: `cmd ${id}`, status: 'pending', createdAt, expiresAt: '2026-10-23T11:00:00Z',
}) as any;

it('renders one pending approval as a region without a count', () => {
  const { body } = render(ApprovalDock, { props: { approvals: [mk('a1', '2026-10-23T10:00:00Z')], threadId: 't1' } });
  expect(body).toContain('role="region"');
  expect(body).toContain('id="approval-dock"');
  expect(body).toContain('id="approval-a1"');
  expect(body).toContain('high risk');
  expect(body).toContain('cmd a1');
  expect(body).toContain('Deny');
  expect(body).toContain('Allow once');
  expect(body).not.toContain('of 2');
});

it('shows the position when several are pending', () => {
  const { body } = render(ApprovalDock, { props: { approvals: [mk('a2', '2026-10-23T10:05:00Z'), mk('a1', '2026-10-23T10:00:00Z')], threadId: 't1' } });
  expect(body).toContain('1 of 2');
  expect(body).toContain('cmd a1');
  expect(body).not.toContain('cmd a2');
});

it('renders nothing when no approval is pending', () => {
  const { body } = render(ApprovalDock, { props: { approvals: [], threadId: 't1' } });
  expect(body).not.toContain('Allow once');
  expect(body).not.toContain('Approval needed');
});
