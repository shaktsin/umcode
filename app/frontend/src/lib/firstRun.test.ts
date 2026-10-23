import { describe, expect, it } from 'vitest';
import { composerPlaceholder, shouldRedirectToProviders } from './firstRun';

const ready = { conn: 'open', catalogReady: true, hasEnabledCredential: false };

describe('shouldRedirectToProviders', () => {
  it('redirects when connected, loaded, and no credential is enabled', () => {
    expect(shouldRedirectToProviders(ready)).toBe(true);
  });
  it('does not redirect while the engine is closed or connecting', () => {
    expect(shouldRedirectToProviders({ ...ready, conn: 'closed' })).toBe(false);
    expect(shouldRedirectToProviders({ ...ready, conn: 'connecting' })).toBe(false);
  });
  it('does not redirect before the catalog has loaded', () => {
    expect(shouldRedirectToProviders({ ...ready, catalogReady: false })).toBe(false);
  });
  it('does not redirect when a credential is enabled', () => {
    expect(shouldRedirectToProviders({ ...ready, hasEnabledCredential: true })).toBe(false);
  });
});

describe('composerPlaceholder', () => {
  it('asks for a provider when one is needed', () => {
    expect(composerPlaceholder('Ask about x', true)).toBe('Connect a provider to chat');
  });
  it('keeps the base placeholder otherwise', () => {
    expect(composerPlaceholder('Ask about x', false)).toBe('Ask about x');
  });
});
