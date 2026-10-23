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
