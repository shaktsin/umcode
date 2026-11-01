import { expect, it, vi } from 'vitest';
import { render } from 'svelte/server';
vi.mock('$lib/stores/app.svelte', () => ({ app: { approvals: [] } }));
import AgentSuggestion from './AgentSuggestion.svelte';

const item = (output = '{"suggestionId":"s1","state":"pending","message":"Stop now"}') => ({ id: 'i', kind: 'toolCall', tool: { name: 'agent.suggest', args: { reason: 'Three independent modules' }, output } }) as any;
const view = (state: string | null, error = '') => ({
  cards: { suggestion: () => (state ? { id: 's1', state } : undefined), suggestionActionable: () => state === 'pending', error },
  answerSuggestion: vi.fn(),
}) as any;

it('offers both buttons and the reason only while the engine says pending', () => {
  const { body } = render(AgentSuggestion, { props: { item: item(), view: view('pending') } });
  expect(body).toContain('Three independent modules');
  expect(body).toContain('Enable agents for this message');
  expect(body).toContain('Continue without agents');
});
it.each(['enabled', 'declined', 'expired'])('is a read-only summary when %s', (state) => {
  const { body } = render(AgentSuggestion, { props: { item: item(), view: view(state) } });
  expect(body).toContain('Three independent modules');
  expect(body).not.toContain('Enable agents for this message');
  expect(body).not.toContain('Continue without agents');
});
it('offers nothing before the get RPC has answered, whatever the result text says', () => {
  const { body } = render(AgentSuggestion, { props: { item: item('{"suggestionId":"s1","state":"pending"}'), view: view(null) } });
  expect(body).not.toContain('<button');
});
it('shows a refresh error', () => {
  expect(render(AgentSuggestion, { props: { item: item(), view: view(null, 'boom') } }).body).toContain('boom');
});
