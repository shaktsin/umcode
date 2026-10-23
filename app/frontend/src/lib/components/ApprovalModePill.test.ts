import { expect, it, vi } from 'vitest';
import { render } from 'svelte/server';
vi.mock('$lib/stores/app.svelte', () => ({ app: { try: vi.fn() } }));
import ApprovalModePill from './ApprovalModePill.svelte';

it('renders nothing before a thread exists', () => {
  const { body } = render(ApprovalModePill, { props: { view: { id: null, thread: null } as any } });
  expect(body).not.toContain('aria-haspopup');
});

it('shows the current approval mode label', () => {
  const { body } = render(ApprovalModePill, { props: { view: { id: 't1', thread: { approvalMode: 'auto_workspace' } } as any } });
  expect(body).toContain('Auto-approve workspace');
});
