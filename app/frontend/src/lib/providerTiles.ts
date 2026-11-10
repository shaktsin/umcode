import type { Credential, Provider } from './types';

export interface ProviderTileModel {
  id: string;
  name: string;
  subtitle: string;
  connected: boolean;
  keyCount: number;
  isAdd: boolean;
}

const names: Record<string, string> = {
  claude: 'Anthropic',
  openai: 'OpenAI',
  gemini: 'Gemini',
  openai_compatible: 'OpenAI-compatible',
};

const subtitles: Record<string, string> = {
  claude: 'API key',
  openai: 'API key or ChatGPT',
  gemini: 'API key',
  openai_compatible: 'Ollama, LM Studio, vLLM',
};

const ADD_ID = 'openai_compatible';

export function providerTiles(providers: Provider[], credentials: Credential[]): ProviderTileModel[] {
  const known = providers.length ? providers : Object.keys(names).map((id) => ({ id, displayName: names[id] }));
  const listed = new Set(known.map((p) => p.id));
  const unlisted = [...new Set(credentials.map((c) => c.provider))]
    .filter((id) => !listed.has(id))
    .map((id) => ({ id, displayName: id }));
  const ordered = [...known.filter((p) => p.id !== ADD_ID), ...unlisted, ...known.filter((p) => p.id === ADD_ID)];
  return ordered.map((p) => {
    const keyCount = credentials.filter((c) => c.provider === p.id && c.enabled).length;
    return {
      id: p.id,
      name: names[p.id] ?? p.displayName,
      subtitle: keyCount ? `${keyCount} ${keyCount === 1 ? 'key' : 'keys'}` : (subtitles[p.id] ?? 'API key'),
      connected: keyCount > 0,
      keyCount,
      isAdd: p.id === ADD_ID,
    };
  });
}
