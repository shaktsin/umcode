<script lang="ts">
  import { Plus, Star, Trash2, FlaskConical, RefreshCw } from '@lucide/svelte';
  import { app } from '$lib/stores/app.svelte';
  import { errMsg, fmtUsd, relTime } from '$lib/format';
  import { dialog } from '$lib/stores/dialog.svelte';
  import type { Credential, CredentialTestResult } from '$lib/types';

  const providerNames: Record<string, string> = {
    claude: 'Anthropic Claude', openai: 'OpenAI', gemini: 'Google Gemini', openai_compatible: 'OpenAI-compatible (Ollama, LM Studio, vLLM…)',
  };
  const providerIds = $derived(app.providers.length ? app.providers.map((p) => p.id) : Object.keys(providerNames));

  let adding = $state(false);
  let provider = $state('claude');
  let label = $state('');
  let secret = $state('');
  let baseUrl = $state('');
  let isDefault = $state(false);
  let busy = $state(false);
  let tests = $state<Record<string, CredentialTestResult | 'running'>>({});
  let budgetEdit = $state<Record<string, { amount: string; hardStop: boolean }>>({});

  const grouped = $derived.by(() => {
    const g: Record<string, Credential[]> = {};
    for (const c of app.credentials) (g[c.provider] ??= []).push(c);
    return Object.entries(g);
  });

  async function refresh() {
    await app.refreshCatalog().catch((e) => app.toast('error', errMsg(e)));
  }

  async function add() {
    busy = true;
    try {
      const c = await app.call<Credential>('credential/add', {
        provider, label: label.trim() || providerNames[provider] || provider, secret: secret.trim(),
        baseUrl: baseUrl.trim() || undefined, isDefault,
      });
      secret = '';
      label = '';
      baseUrl = '';
      adding = false;
      await refresh();
      await test(c);
    } catch (e) {
      app.toast('error', errMsg(e));
    } finally {
      busy = false;
    }
  }

  async function test(c: Credential) {
    tests[c.id] = 'running';
    try {
      tests[c.id] = await app.call<CredentialTestResult>('credential/test', { credentialId: c.id }, 60_000);
    } catch (e) {
      tests[c.id] = { ok: false, latencyMs: 0, error: errMsg(e) };
    }
    await refresh();
  }

  async function update(c: Credential, patch: Record<string, unknown>) {
    await app.try('credential/update', { credentialId: c.id, ...patch });
    await refresh();
  }

  async function rotate(c: Credential) {
    const s = await dialog.prompt(`Paste the new key for “${c.label}”. The old one is replaced.`, { okLabel: 'Replace', secret: true });
    if (!s?.trim()) return;
    await app.try('credential/rotate', { credentialId: c.id, secret: s.trim() }, 'Key replaced.');
    await test(c);
  }

  async function remove(c: Credential) {
    if (!(await dialog.confirm(`Delete the key “${c.label}”? Usage history is kept.`, { okLabel: 'Delete', danger: true }))) return;
    await app.try('credential/delete', { credentialId: c.id }, 'Key deleted.');
    await refresh();
  }

  function editBudget(c: Credential) {
    budgetEdit[c.id] = { amount: c.monthlyBudgetUsd ? String(c.monthlyBudgetUsd) : '', hardStop: !!c.hardStop };
  }

  async function saveBudget(c: Credential) {
    const b = budgetEdit[c.id];
    const amount = parseFloat(b.amount || '0');
    if (isNaN(amount) || amount < 0) {
      app.toast('error', 'Enter a dollar amount, or 0 for no budget.');
      return;
    }
    await app.try('usage/setBudget', { credentialId: c.id, monthlyBudgetUsd: amount, hardStop: b.hardStop }, 'Budget saved.');
    delete budgetEdit[c.id];
    await refresh();
  }
</script>

<div class="flex items-start gap-4 mb-3">
  <div>
    <h2 class="text-sm font-semibold text-ink">API keys</h2>
    <p class="text-xs text-muted mt-1 max-w-2xl">Keys for Claude, OpenAI, Gemini or a local model server power chat. They are stored in the macOS Keychain and never sent to the app.</p>
  </div>
  <button class="btn-primary ml-auto" onclick={() => (adding = !adding)}><Plus class="w-4 h-4" />Add key</button>
</div>

{#if adding}
  <form class="card p-4 mb-4 space-y-3" onsubmit={(e) => { e.preventDefault(); add(); }}>
    <div class="grid grid-cols-2 gap-3">
      <div>
        <label class="label" for="k-prov">Provider</label>
        <select id="k-prov" class="input" bind:value={provider}>
          {#each providerIds as id}<option value={id}>{providerNames[id] ?? id}</option>{/each}
        </select>
      </div>
      <div><label class="label" for="k-label">Label</label><input id="k-label" class="input" bind:value={label} placeholder="Personal, Work…" /></div>
    </div>
    <div>
      <label class="label" for="k-secret">API key{provider === 'openai_compatible' ? ' (optional)' : ''}</label>
      <input id="k-secret" class="input font-mono" type="password" autocomplete="off" bind:value={secret} placeholder={provider === 'claude' ? 'sk-ant-…' : provider === 'openai' ? 'sk-…' : ''} />
    </div>
    {#if provider === 'openai_compatible' || provider === 'openai'}
      <div>
        <label class="label" for="k-url">Base URL{provider === 'openai' ? ' (optional)' : ''}</label>
        <input id="k-url" class="input font-mono" bind:value={baseUrl} placeholder="http://localhost:11434/v1" />
      </div>
    {/if}
    <label class="flex items-center gap-2 text-sm text-ink-soft"><input type="checkbox" bind:checked={isDefault} /> Use as the default key for this provider</label>
    <div class="flex justify-end gap-2">
      <button type="button" class="btn-ghost" onclick={() => (adding = false)}>Cancel</button>
      <button type="submit" class="btn-primary" disabled={busy || (!secret.trim() && provider !== 'openai_compatible') || (provider === 'openai_compatible' && !baseUrl.trim())}>
        {busy ? 'Saving…' : 'Save and test'}
      </button>
    </div>
  </form>
{/if}

{#if app.credentials.length === 0 && !adding}
  <div class="card p-8 text-center text-sm text-muted">No API keys yet. Add one to start chatting.</div>
{/if}

{#each grouped as [prov, list]}
  <h2 class="text-xs font-semibold uppercase tracking-wider text-muted mt-5 mb-2">{providerNames[prov] ?? prov}</h2>
  <div class="space-y-2">
    {#each list as c (c.id)}
      {@const t = tests[c.id]}
      <div class="card p-3">
        <div class="flex items-center gap-3">
          <button
            class={c.isDefault ? 'text-amber-warm' : 'text-faint hover:text-muted'}
            title={c.isDefault ? 'Default key' : 'Make default'}
            aria-label={c.isDefault ? 'Default key' : 'Make default'}
            onclick={() => !c.isDefault && update(c, { isDefault: true })}
          ><Star class="w-4 h-4 {c.isDefault ? 'fill-current' : ''}" /></button>
          <div class="flex-1 min-w-0">
            <div class="text-sm text-ink">{c.label} <span class="font-mono text-xs text-muted">••••{c.last4 || '-'}</span></div>
            <div class="text-[11px] text-muted">
              {#if c.baseUrl}<span class="font-mono">{c.baseUrl}</span> · {/if}
              This month {fmtUsd(c.monthUsage?.costUsd)}{c.monthlyBudgetUsd ? ` of ${fmtUsd(c.monthlyBudgetUsd)}` : ''}
              {#if c.lastTestedAt} · tested {relTime(c.lastTestedAt)} {c.lastTestOk ? '✓' : '✗'}{/if}
            </div>
          </div>
          <label class="flex items-center gap-1.5 text-xs text-muted" title="Try this key when another key for the provider is rate limited">
            <input type="checkbox" checked={c.fallback} onchange={(e) => update(c, { fallback: e.currentTarget.checked })} /> Fallback
          </label>
          <label class="flex items-center gap-1.5 text-xs text-muted">
            <input type="checkbox" checked={c.enabled} onchange={(e) => update(c, { enabled: e.currentTarget.checked })} /> Enabled
          </label>
          <button class="btn-ghost btn-sm" onclick={() => test(c)} disabled={t === 'running'}><FlaskConical class="w-3.5 h-3.5" />{t === 'running' ? 'Testing…' : 'Test'}</button>
          <button class="btn-ghost btn-sm" onclick={() => editBudget(c)}>Budget</button>
          <button class="btn-ghost btn-sm" title="Replace key" aria-label="Replace key" onclick={() => rotate(c)}><RefreshCw class="w-3.5 h-3.5" /></button>
          <button class="btn-danger btn-sm" aria-label="Delete key" onclick={() => remove(c)}><Trash2 class="w-3.5 h-3.5" /></button>
        </div>
        {#if t && t !== 'running'}
          <div class="mt-2 text-xs {t.ok ? 'text-sage' : 'text-rust'} selectable">
            {t.ok ? `Works (${t.latencyMs} ms${t.models?.length ? `, ${t.models.length} models` : ''})` : `Failed: ${t.error}`}
          </div>
        {/if}
        {#if budgetEdit[c.id]}
          <div class="mt-3 flex items-end gap-3">
            <div class="w-40"><label class="label" for={`b-${c.id}`}>Monthly budget (USD)</label><input id={`b-${c.id}`} class="input" inputmode="decimal" bind:value={budgetEdit[c.id].amount} placeholder="0 = none" /></div>
            <label class="flex items-center gap-2 text-sm text-ink-soft pb-2"><input type="checkbox" bind:checked={budgetEdit[c.id].hardStop} /> Stop using this key at 100%</label>
            <div class="ml-auto flex gap-2 pb-0.5">
              <button class="btn-ghost btn-sm" onclick={() => delete budgetEdit[c.id]}>Cancel</button>
              <button class="btn-primary btn-sm" onclick={() => saveBudget(c)}>Save</button>
            </div>
          </div>
        {/if}
      </div>
    {/each}
  </div>
{/each}
