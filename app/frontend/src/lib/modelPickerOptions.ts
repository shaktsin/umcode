import type { ConfiguredModel, Credential, Model } from './types';

/** Models that can be selected in chat, based on enabled provider credentials.
 * Catalog entries are choices, not approvals: only persist them when selected
 * into a pool. Respect models explicitly disabled in Settings -> Models.
 */
export function modelPickerOptions(
  catalog: Model[],
  configured: ConfiguredModel[],
  credentials: Credential[],
): ConfiguredModel[] {
  const connected = new Set(credentials.filter((c) => c.enabled).map((c) => c.provider));
  const allConfigured = new Set(configured.map((m) => `${m.provider}/${m.model}`));
  const localIds = new Set(catalog.filter(m => m.provider === 'workbench_local' && !m.hidden).map(m => m.id));
  const result = configured.filter((m) => m.enabled && (m.provider === 'workbench_local' ? localIds.has(m.model) : connected.has(m.provider)));

  for (const model of catalog) {
    if (model.hidden || (model.provider !== 'workbench_local' && (!connected.has(model.provider) || !model.supportsTools))) continue;
    if (allConfigured.has(`${model.provider}/${model.id}`)) continue;
    result.push({
      id: `catalog:${model.provider}/${model.id}`,
      provider: model.provider,
      model: model.id,
      name: model.displayName || model.id,
      enabled: true,
    });
  }
  return result;
}

/** Persist newly selected catalog choices only when a multi-model pool is made. */
export function poolModelIds(
  draftModels: ConfiguredModel[],
  selected: ConfiguredModel[],
  newId: () => string,
): string[] {
  return selected.map((candidate) => {
    let stored = draftModels.find((m) => m.provider === candidate.provider && m.model === candidate.model);
    if (!stored) {
      stored = { ...candidate, id: newId() };
      draftModels.push(stored);
    }
    return stored.id;
  });
}

/** A local checkpoint is an exclusive plain-chat selection, never a cloud fallback pool. */
export function toggleModelSelection(selected: string[], model: ConfiguredModel, options: ConfiguredModel[]): string[] {
  if (selected.includes(model.id)) return selected.filter(id => id !== model.id);
  if (model.provider === 'workbench_local') return [model.id];
  return [...selected.filter(id => options.some(option => option.id === id && option.provider !== 'workbench_local')), model.id];
}
