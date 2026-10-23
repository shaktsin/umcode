import { expect, it } from 'vitest';
import { render } from 'svelte/server';
import ProviderTile from './ProviderTile.svelte';
import type { ProviderTileModel } from '$lib/providerTiles';

const tile = (o: Partial<ProviderTileModel>): ProviderTileModel => ({ id: 'claude', name: 'Anthropic', subtitle: 'API key', connected: false, keyCount: 0, isAdd: false, ...o });

it('shows the Connected badge and aria-expanded', () => {
  const { body } = render(ProviderTile, { props: { tile: tile({ connected: true, keyCount: 1, subtitle: '1 key' }), expanded: false, onclick() {} } });
  expect(body).toContain('Connected');
  expect(body).toContain('aria-expanded="false"');
});

it('renders the add tile with a plus', () => {
  const { body } = render(ProviderTile, { props: { tile: tile({ id: 'openai_compatible', name: 'OpenAI-compatible', isAdd: true }), expanded: true, onclick() {} } });
  expect(body).toContain('+');
  expect(body).toContain('OpenAI-compatible');
  expect(body).toContain('aria-expanded="true"');
});

it('uses the first letter as a monogram for unknown providers', () => {
  const { body } = render(ProviderTile, { props: { tile: tile({ id: 'foo', name: 'Foo AI' }), expanded: false, onclick() {} } });
  expect(body).toContain('>F<');
});
