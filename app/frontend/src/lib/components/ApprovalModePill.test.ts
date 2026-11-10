import { expect, it, vi } from 'vitest';
import { render } from 'svelte/server';
vi.mock('$lib/stores/app.svelte', () => ({ app: { try: vi.fn() } }));
import ApprovalModePill from './ApprovalModePill.svelte';

it('is available before a thread exists and shows the pending mode', () => {
  const { body } = render(ApprovalModePill, { props: { view: { id: null, thread: null, approvalMode: 'auto_all' } as any } });
  expect(body).toContain('aria-haspopup');
  expect(body).toContain('Auto-approve all');
});

it('shows the current approval mode label', () => {
  const { body } = render(ApprovalModePill, { props: { view: { id: 't1', thread: { approvalMode: 'auto_workspace' } } as any } });
  expect(body).toContain('Auto-approve workspace');
});
