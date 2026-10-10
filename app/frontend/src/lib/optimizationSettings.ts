export interface OptimizationResult { enabled: boolean; source: 'stored' | 'legacy' | 'default'; legacyMixed: boolean }
export interface OptimizationView { result?: OptimizationResult; busy: boolean; error: string }
type Request = (method: string, params: unknown) => Promise<OptimizationResult>;

// Owns saved state and request ordering; the view never treats an optimistic
// checkbox position as a successfully persisted global policy.
export class OptimizationSettings {
  view: OptimizationView = { busy: false, error: '' };
  private generation = 0;
  private saving = false;
  constructor(private request: Request, private publish: (view: OptimizationView) => void) {}
  private update(patch: Partial<OptimizationView>) { this.view = {...this.view, ...patch}; this.publish(this.view); }
  async load() {
    if (this.saving) return;
    const generation = ++this.generation;
    this.update({busy: true, error: ''});
    try {
      const result = await this.request('settings/tokenOptimization/get', {});
      if (generation === this.generation) this.update({result});
    } catch (error) {
      if (generation === this.generation) this.update({error: error instanceof Error ? error.message : String(error)});
    } finally { if (generation === this.generation) this.update({busy: false}); }
  }
  async save(enabled: boolean) {
    if (this.saving || !this.view.result) return;
    this.saving = true;
    const generation = ++this.generation;
    this.update({busy: true, error: ''});
    try {
      const result = await this.request('settings/tokenOptimization/set', {enabled});
      if (generation === this.generation) this.update({result});
    } catch (error) {
      if (generation === this.generation) this.update({error: error instanceof Error ? error.message : String(error)});
    } finally {
      this.saving = false;
      if (generation === this.generation) this.update({busy: false});
    }
  }
}
