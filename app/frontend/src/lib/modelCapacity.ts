import { fmtTokens } from './format';
import type { ModelCapacityProfile } from './types';
export function capacityFields(contextWindow: number | undefined, maxOutputTokens: number | undefined, responseReserveTokens: number | undefined): Partial<ModelCapacityProfile> {
 const fields = { contextWindow, maxOutputTokens, responseReserveTokens };
 const result: Partial<ModelCapacityProfile> = {};
 for (const key of ['contextWindow','maxOutputTokens','responseReserveTokens'] as const) {
  const value=fields[key];
  if(value===undefined) continue;
  if(!Number.isSafeInteger(value)||value<=0||value>2147483647) throw new Error('Capacity must be a positive integer.');
  result[key]=value;
 }
 return result;
}

/** Window assumed by the engine until a server reports or states its real limit. */
export const DEFAULT_CONTEXT_WINDOW = 256000;

const sourceLabels: Record<string, string> = {
 runtime: 'detected from the server',
 'runtime+override': 'detected, limited by your setting',
 catalog: 'from the model catalog',
 override: 'set by you',
 observed: 'learned automatically',
 default: 'assumed until the server reports a limit',
};

/** One plain-language line describing the effective context window. */
export function describeCapacity(result: { effectiveContextWindow?: number; source: string } | null): string {
 if (!result || !result.effectiveContextWindow) return 'Context window: detected automatically on first use';
 const tokens = result.effectiveContextWindow.toLocaleString('en-US');
 const how = sourceLabels[result.source];
 return how ? `Context window: ${tokens} tokens (${how})` : `Context window: ${tokens} tokens`;
}

/** True while the window is only an assumption, so the chat should say so. */
export function capacityIsAssumed(result: { source: string } | null): boolean {
 return result?.source === 'default';
}

/** The one-line status shown while the window is assumed. */
export function assumedCapacityNotice(window = DEFAULT_CONTEXT_WINDOW): string {
 return `Context window assumed at ${fmtTokens(window)} tokens`;
}

/** The full explanation, shown on hover or focus rather than in the line itself. */
export function assumedCapacityDetail(model: string, window = DEFAULT_CONTEXT_WINDOW): string {
 return `UMCode doesn't know the context limit for ${model} yet, so it assumes ${window.toLocaleString('en-US')} tokens. Chat works normally, and UMCode adapts if the server rejects a request as too large.`;
}
