import { describe, it, expect } from 'vitest';
import { StorageSettings, type StorageCleanupResult, type StorageResult } from './storageSettings';

const result = (raw = 30): StorageResult => ({
  retention: {raw, stale: 30, blob: 90, redacted: 7}, source: 'default',
  stats: {objects: 0, bytes: 0, eligibleBytes: 0},
});
function deferred<T>() { let resolve!: (value:T)=>void; let reject!: (error:Error)=>void; const promise=new Promise<T>((a,b)=>{resolve=a;reject=b});return {promise,resolve,reject}; }

describe('global vault retention settings', () => {
  it('shows a disconnect on initial load and rejects invalid values before saving', async () => {
    let writes = 0;
    const disconnected = new StorageSettings(async () => { throw new Error('disconnected'); }, () => {});
    await disconnected.load();
    expect(disconnected.view.error).toBe('disconnected');
    expect(disconnected.view.result).toBeUndefined();

    const c = new StorageSettings(async (method) => { if (method.endsWith('/get')) return result(); writes++; return result(); }, () => {});
    await c.load();
    await c.save({raw:1.5, stale:30, blob:90, redacted:7});
    expect(writes).toBe(0);
    expect(c.view.error).toMatch(/whole days/);
  });

  it('ignores stale load responses after a save', async () => {
    const pending = deferred<StorageResult>();
    let reads = 0;
    const c = new StorageSettings(async (method) => method.endsWith('/get') ? (++reads === 1 ? result(30) : pending.promise) : result(4), () => {});
    await c.load();
    const stale = c.load();
    await c.save({raw:4, stale:30, blob:90, redacted:7});
    pending.resolve(result(30)); await stale;
    expect(c.view.result?.retention.raw).toBe(4);
  });

  it('retains saved values on disconnect and save failure', async () => {
    const c = new StorageSettings(async (method) => method.endsWith('/get') ? result() : Promise.reject(new Error('disconnected')), () => {});
    await c.load(); await c.save({raw:1, stale:2, blob:3, redacted:4});
    expect(c.view.result?.retention.raw).toBe(30);
    expect(c.view.error).toBe('disconnected');
    expect(c.view.busy).toBe(false);
  });

  it('prevents duplicate cleanup and reports actual reclaimed bytes, including zero', async () => {
    const pending = deferred<StorageCleanupResult>(); let cleanups = 0;
    const c = new StorageSettings(async (method) => {
      if (method.endsWith('/get')) return result();
      if (method === 'storage/cleanup') { cleanups++; return pending.promise; }
      return result(1);
    }, () => {});
    await c.load(); const first = c.cleanup(); await c.cleanup();
    expect(cleanups).toBe(1);
    pending.resolve({deleted:0, freedBytes:0, stats:{objects:0,bytes:0,eligibleBytes:0}});
    await first;
    expect(c.view.cleanup?.freedBytes).toBe(0);
  });
});
