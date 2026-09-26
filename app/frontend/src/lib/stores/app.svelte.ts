import { RpcClient, PROTOCOL_VERSION, type ConnState } from '$lib/rpc';
import { fetchConnection } from '$lib/connection';
import { errMsg } from '$lib/format';
import type {
  Approval, BudgetWarning, ComplexityDefaults, Credential, EngineStatus, Model, Provider, ProviderIdentity, RoutingConfig,
} from '$lib/types';

export type View = 'chat' | 'approvals' | 'tasks' | 'extensions' | 'usage' | 'project' | 'settings';

export interface Toast {
  id: number;
  kind: 'info' | 'error' | 'warn';
  text: string;
}

class AppState {
  conn = $state<ConnState>('closed');
  connError = $state('');
  shell = $state('browser');
  status = $state<EngineStatus | null>(null);
  view = $state<View>('chat');
  approvals = $state<Approval[]>([]);
  providers = $state<Provider[]>([]);
  identities = $state<ProviderIdentity[]>([]);
  models = $state<Model[]>([]);
  credentials = $state<Credential[]>([]);
  complexity = $state<ComplexityDefaults | null>(null);
  /** The models the user has approved, and how they are pooled. */
  routing = $state<RoutingConfig>({ models: [], pools: [] });
  toasts = $state<Toast[]>([]);
  private toastId = 0;
  private readyHooks: Array<() => void | Promise<void>> = [];

  rpc: RpcClient;

  constructor() {
    this.rpc = new RpcClient(
      async () => {
        try {
          const info = await fetchConnection();
          this.shell = info.shell || 'browser';
          return info;
        } catch (e) {
          const shell = (e as { shell?: string }).shell;
          if (shell) this.shell = shell;
          throw e;
        }
      },
      async (c) => {
        await c.call('initialize', {
          clientName: this.shell === 'mac' ? 'umcode-mac' : 'umcode-web',
          clientVersion: '0.4.0',
          protocolVersion: PROTOCOL_VERSION,
          admin: true,
        });
        await c.call('events/subscribe', { all: true });
      },
    );
    this.rpc.onState((s, err) => {
      this.conn = s;
      this.connError = err || '';
      if (s === 'open') void this.onReady();
    });
    this.rpc.on('approval/request', (p: { approval: Approval }) => {
      if (!this.approvals.some((a) => a.id === p.approval.id)) this.approvals = [...this.approvals, p.approval];
    });
    this.rpc.on('approval/resolved', (p: { approval: Approval }) => {
      this.approvals = this.approvals.filter((a) => a.id !== p.approval.id);
    });
    this.rpc.on('usage/budgetWarning', (p: BudgetWarning) => {
      this.toast('warn', `${p.label} has used ${Math.round(p.percent)}% of its $${p.budgetUsd} monthly budget.`);
    });
  }

  /** Register work to run each time the engine connection (re)opens. */
  onConnected(fn: () => void | Promise<void>) {
    this.readyHooks.push(fn);
    if (this.conn === 'open') void fn();
  }

  private async onReady() {
    await Promise.allSettled([this.refreshStatus(), this.refreshApprovals(), this.refreshCatalog()]);
    for (const fn of this.readyHooks) {
      try {
        await fn();
      } catch (e) {
        console.error(e);
      }
    }
  }

  start() {
    this.rpc.start();
    setInterval(() => {
      if (this.conn === 'open') void this.refreshStatus();
    }, 15_000);
  }

  call<T = any>(method: string, params?: unknown, timeoutMs?: number): Promise<T> {
    return this.rpc.call<T>(method, params, timeoutMs);
  }

  /** Call and surface failures as a toast; returns undefined on error. */
  async try<T = any>(method: string, params?: unknown, okText?: string): Promise<T | undefined> {
    try {
      const r = await this.call<T>(method, params);
      if (okText) this.toast('info', okText);
      return r;
    } catch (e) {
      this.toast('error', errMsg(e));
      return undefined;
    }
  }

  toast(kind: Toast['kind'], text: string) {
    const id = ++this.toastId;
    this.toasts = [...this.toasts, { id, kind, text }];
    setTimeout(() => this.dismiss(id), kind === 'error' ? 8000 : 4500);
  }

  dismiss(id: number) {
    this.toasts = this.toasts.filter((t) => t.id !== id);
  }

  async refreshStatus() {
    try {
      this.status = await this.call<EngineStatus>('engine/status');
    } catch {
      /* shown via conn state */
    }
  }

  async refreshApprovals() {
    const r = await this.call<{ approvals: Approval[] }>('approval/list');
    this.approvals = r.approvals ?? [];
  }

  async refreshCatalog() {
    const [p, i, m, c, x, r] = await Promise.all([
      this.call<{ providers: Provider[] }>('provider/list'),
      // Older/external engines may not expose managed identities yet. Keep the
      // rest of the catalog usable while the app prompts for an engine update.
      this.call<{ identities: ProviderIdentity[] }>('identity/list').catch(() => ({ identities: [] })),
      this.call<{ models: Model[] }>('model/list', {}),
      this.call<{ credentials: Credential[] }>('credential/list'),
      this.call<ComplexityDefaults>('complexity/getDefaults'),
      this.call<RoutingConfig>('routing/get'),
    ]);
    this.providers = p.providers ?? [];
    this.identities = i.identities ?? [];
    this.models = m.models ?? [];
    this.credentials = c.credentials ?? [];
    this.complexity = x;
    this.routing = r;
  }

  async respondApproval(id: string, approve: boolean, remember = false) {
    const r = await this.try('approval/respond', { approvalId: id, approve, remember });
    if (r !== undefined) this.approvals = this.approvals.filter((a) => a.id !== id);
  }

  modelsFor(provider: string): Model[] {
    return this.models.filter((m) => m.provider === provider && !m.hidden);
  }

  credentialsFor(provider: string): Credential[] {
    return this.credentials.filter((c) => c.provider === provider && c.enabled);
  }

  /** Providers that have at least one enabled key. */
  get usableProviders(): Provider[] {
    return this.providers.filter((p) => this.credentials.some((c) => c.provider === p.id && c.enabled));
  }
}

export const app = new AppState();
