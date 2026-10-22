import { describe, expect, it } from 'vitest';
import { modelPickerOptions, poolModelIds } from './modelPickerOptions';
import type { ConfiguredModel, Credential, Model } from './types';

const claude: ConfiguredModel = { id: 'claude-default', name: 'Claude', provider: 'claude', model: 'claude-sonnet-5', enabled: true };
const openai: Model = { provider: 'openai', id: 'gpt-5.6-terra', displayName: 'GPT-5.6 Terra', supportsTools: true, supportsImages: true, supportsReasoning: true, inputPerMTok: 0, cachedInputPerMTok: 0, outputPerMTok: 0, hidden: false, source: 'bundled' };
const key = (provider: string, enabled = true): Credential => ({ id: 'credential', provider, kind: 'api_key', label: 'key', last4: '1234', enabled, isDefault: true, fallback: false, createdAt: '' });

describe('connected-provider model picker', () => {
  it('shows connected ChatGPT catalog models without requiring Settings → Models', () => {
    const result = modelPickerOptions([openai], [claude], [key('openai')]);
    expect(result).toEqual([expect.objectContaining({ provider: 'openai', model: 'gpt-5.6-terra', id: 'catalog:openai/gpt-5.6-terra' })]);
  });

  it('hides disconnected providers and responds to disabled credentials', () => {
    expect(modelPickerOptions([openai], [claude], [key('openai', false)])).toEqual([]);
    expect(modelPickerOptions([openai], [claude], [key('claude')])).toEqual([claude]);
  });

  it('does not duplicate configured models or resurrect explicitly disabled models', () => {
    const configured = [{ ...claude, provider: 'openai', model: openai.id, id: 'saved' }];
    expect(modelPickerOptions([openai], configured, [key('openai')])).toEqual(configured);
    expect(modelPickerOptions([openai], [{ ...configured[0], enabled: false }], [key('openai')])).toEqual([]);
  });

  it('excludes hidden and non-chat/tool catalog choices', () => {
    expect(modelPickerOptions([{ ...openai, hidden: true }, { ...openai, id: 'embedding', supportsTools: false }], [], [key('openai')])).toEqual([]);
  });

  it('creates stable pool memberships from choices and preserves existing model IDs', () => {
    const draft = [claude];
    const choices = modelPickerOptions([openai], [claude], [key('claude'), key('openai')]);
    const result = poolModelIds(draft, choices, () => 'new-model');
    expect(result).toEqual(['claude-default', 'new-model']);
    expect(draft).toHaveLength(2);
    expect(draft[1]).toEqual(expect.objectContaining({ provider: 'openai', model: openai.id, id: 'new-model' }));
    expect(poolModelIds(draft, choices, () => 'unused')).toEqual(result);
    expect(draft).toHaveLength(2);
  });
});
