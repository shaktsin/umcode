export interface RetentionDays { raw: number; stale: number; blob: number; redacted: number }
export interface VaultStats { objects: number; bytes: number; eligibleBytes: number }
export interface StorageResult { retention: RetentionDays; source: 'stored' | 'config' | 'default'; stats: VaultStats }
export interface StorageCleanupResult { deleted: number; freedBytes: number; stats: VaultStats }
export interface StorageView {
  result?: StorageResult;
  cleanup?: StorageCleanupResult;
  busy: boolean;
  error: string;
}
type Response = StorageResult | StorageCleanupResult;
type Request = (method: string, params: unknown) => Promise<Response>;

export class StorageSettings {
  view: StorageView = {busy: false, error: ''};
  private generation = 0;
  private mutation = false;
  constructor(private request: Request, private publish: (view: StorageView) => void) {}
  private update(patch: Partial<StorageView>) { this.view = {...this.view, ...patch}; this.publish(this.view); }
  async load() {
    if (this.mutation) return;
    const generation = ++this.generation;
    this.update({busy: true, error: ''});
    try {
      const result = await this.request('settings/storage/get', {}) as StorageResult;
      if (generation === this.generation) this.update({result});
    } catch (error) {
      if (generation === this.generation) this.update({error: error instanceof Error ? error.message : String(error)});
    } finally { if (generation === this.generation) this.update({busy: false}); }
  }
  async save(retention: RetentionDays) {
    if (this.mutation || !this.view.result) return;
    if (Object.values(retention).some((days) => !Number.isInteger(days) || days < 1 || days > 3650)) {
      this.update({error: 'Enter whole days from 1 to 3650 for every retention period.'}); return;
    }
    const generation = ++this.generation;
    this.mutation = true;
    this.update({busy: true, error: ''});
    try {
      const result = await this.request('settings/storage/setRetention', retention) as StorageResult;
      if (generation === this.generation) this.update({result, cleanup: undefined});
    } catch (error) {
      if (generation === this.generation) this.update({error: error instanceof Error ? error.message : String(error)});
    } finally { this.mutation = false; if (generation === this.generation) this.update({busy: false}); }
  }
  async cleanup() {
    if (this.mutation || !this.view.result) return;
    const generation = ++this.generation;
    this.mutation = true;
    this.update({busy: true, error: '', cleanup: undefined});
    try {
      const cleanup = await this.request('storage/cleanup', {}) as StorageCleanupResult;
      if (generation === this.generation) this.update({cleanup, result: {...this.view.result!, stats: cleanup.stats}});
    } catch (error) {
      if (generation === this.generation) this.update({error: error instanceof Error ? error.message : String(error)});
    } finally { this.mutation = false; if (generation === this.generation) this.update({busy: false}); }
  }
}
