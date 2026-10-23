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
  const result = configured.filter((m) => m.enabled && connected.has(m.provider));

  for (const model of catalog) {
    if (!connected.has(model.provider) || model.hidden || !model.supportsTools) continue;
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
