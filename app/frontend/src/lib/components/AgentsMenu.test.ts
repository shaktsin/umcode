import { expect, it } from 'vitest';
import { render } from 'svelte/server';
import AgentsMenu from './AgentsMenu.svelte';

const fake = (over: Record<string, unknown> = {}) => ({ active: undefined, token: null, pending: false, error: '', ...over }) as any;
const html = (state: any) => render(AgentsMenu, { props: { state, onenable() {} } }).body;

it('shows only the more-options button by default', () => {
  const body = html(fake());
  expect(body).toContain('aria-label="More options"');
  expect(body).not.toContain('Turn agents off');
});
it('shows the Agents chip with a remove button when a token is set', () => {
  const body = html(fake({ token: { activationToken: 't', expiresAt: 'x' } }));
  expect(body).toContain('Agents are on for the next message');
  expect(body).toContain('aria-label="Turn agents off"');
});
it('shows the pending label while enabling', () => {
  expect(html(fake({ pending: true }))).toContain('Enabling…');
});
it('renders nothing while agents are active', () => {
  const body = html(fake({ active: { task: { state: 'running' } } }));
  expect(body).not.toContain('More options');
  expect(body).not.toContain('Agents');
});
