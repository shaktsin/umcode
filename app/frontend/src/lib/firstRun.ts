export interface FirstRunInput {
  conn: string;
  catalogReady: boolean;
  hasEnabledCredential: boolean;
}

/** True only once the engine is connected and the catalog has loaded with no usable credential. */
export function shouldRedirectToProviders(input: FirstRunInput): boolean {
  return input.conn === 'open' && input.catalogReady && !input.hasEnabledCredential;
}

export function composerPlaceholder(base: string, needsProvider: boolean): string {
  return needsProvider ? 'Connect a provider to chat' : base;
}
