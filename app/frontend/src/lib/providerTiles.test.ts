import { expect, it } from 'vitest';
import { providerTiles } from './providerTiles';
import type { Credential, Provider } from './types';

const prov = (id: string, displayName = id): Provider => ({ id, displayName, enabled: true, defaultModel: '', credentials: 0 });
const cred = (provider: string, enabled = true): Credential => ({ id: Math.random().toString(), provider, kind: 'api_key', label: 'k', last4: '1234', enabled, isDefault: false, fallback: false, createdAt: '' });

it('falls back to the four known providers with openai_compatible last', () => {
  const t = providerTiles([], []);
  expect(t.map((x) => x.id)).toEqual(['claude', 'openai', 'gemini', 'openai_compatible']);
  expect(t.map((x) => x.name)).toEqual(['Anthropic', 'OpenAI', 'Gemini', 'OpenAI-compatible']);
});

it('puts openai_compatible last even when listed first, and only it is the add tile', () => {
  const t = providerTiles([prov('openai_compatible'), prov('claude')], []);
  expect(t.map((x) => x.id)).toEqual(['claude', 'openai_compatible']);
  expect(t.filter((x) => x.isAdd).map((x) => x.id)).toEqual(['openai_compatible']);
});

it('counts enabled credentials for connected state and subtitle', () => {
  const t = providerTiles([], [cred('claude'), cred('claude'), cred('claude', false)]);
  const c = t.find((x) => x.id === 'claude')!;
  expect(c).toMatchObject({ keyCount: 2, connected: true, subtitle: '2 keys' });
  expect(providerTiles([], [cred('claude')])[0].subtitle).toBe('1 key');
});

it('uses the unconnected subtitles', () => {
  const s = Object.fromEntries(providerTiles([], []).map((x) => [x.id, x.subtitle]));
  expect(s).toEqual({ claude: 'API key', openai: 'API key or ChatGPT', gemini: 'API key', openai_compatible: 'Ollama, LM Studio, vLLM' });
});

it('uses displayName and a generic subtitle for unknown providers', () => {
  const t = providerTiles([prov('foo', 'Foo AI')], []);
  expect(t[0]).toMatchObject({ id: 'foo', name: 'Foo AI', subtitle: 'API key', connected: false, isAdd: false });
});

it('adds a tile for credentials whose provider is not listed, before the add tile', () => {
  const t = providerTiles([prov('claude'), prov('openai_compatible')], [cred('legacy'), cred('legacy'), cred('legacy', false)]);
  expect(t.map((x) => x.id)).toEqual(['claude', 'legacy', 'openai_compatible']);
  expect(t[1]).toMatchObject({ name: 'legacy', subtitle: '2 keys', connected: true, keyCount: 2, isAdd: false });
  const off = providerTiles([], [cred('legacy', false)]).find((x) => x.id === 'legacy')!;
  expect(off).toMatchObject({ connected: false, subtitle: 'API key' });
});
