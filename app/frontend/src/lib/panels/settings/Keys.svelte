<script lang="ts">
  import { Plus, Star, Trash2, FlaskConical, RefreshCw, LogIn, Copy } from '@lucide/svelte';
  import { app } from '$lib/stores/app.svelte';
  import { errMsg, fmtUsd, relTime } from '$lib/format';
  import { dialog } from '$lib/stores/dialog.svelte';
  import type { ChatGPTSignInResult, ChatGPTSignInStart, Credential, CredentialTestResult } from '$lib/types';

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
  // OpenAI can be connected with an API key or by signing in with ChatGPT.
  let authMode = $state<'key' | 'chatgpt'>('key');
  let signin = $state<ChatGPTSignInStart | null>(null);
  const viaChatGPT = $derived(provider === 'openai' && authMode === 'chatgpt');

  $effect(() => app.rpc.on('chatgpt/signin/completed', async (r: ChatGPTSignInResult) => {
    if (!signin || r.sessionId !== signin.sessionId) return;
    signin = null;
    if (r.ok) {
      adding = false;
      app.toast('info', 'Signed in with ChatGPT.');
      await refresh();
      if (r.credential) await test(r.credential);
    } else if (r.error && r.error !== 'sign-in cancelled') {
      app.toast('error', r.error);
    }
  }));
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

  async function openLink(url: string) {
    try {
      const r = await fetch('/__umcode/shell/openURL', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ url }) });
      if (r.ok) return;
    } catch { /* not running inside the desktop app */ }
    window.open(url, '_blank', 'noopener');
  }

  async function startSignIn() {
    busy = true;
    try {
      signin = await app.call<ChatGPTSignInStart>('chatgpt/signin/start', {});
      await openLink(signin.verificationUrl);
    } catch (e) {
      app.toast('error', errMsg(e));
    } finally {
      busy = false;
    }
  }

  async function cancelSignIn() {
    const s = signin;
    signin = null;
    if (s) await app.try('chatgpt/signin/cancel', { sessionId: s.sessionId });
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
    const chatgpt = c.kind === 'chatgpt';
    const ask = chatgpt ? `Sign out “${c.label}”? Usage history is kept.` : `Delete the key “${c.label}”? Usage history is kept.`;
    if (!(await dialog.confirm(ask, { okLabel: chatgpt ? 'Sign out' : 'Delete', danger: true }))) return;
    await app.try('credential/delete', { credentialId: c.id }, chatgpt ? 'Signed out.' : 'Key deleted.');
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
    <h2 class="text-sm font-semibold text-ink">Providers</h2>
    <p class="text-xs text-muted mt-1 max-w-2xl">Connect Claude, OpenAI, Gemini or a local model server to power chat. Use an API key for any provider, or, for OpenAI, sign in with your ChatGPT account instead. Keys and sign-ins are stored in the macOS Keychain and never sent to the app.</p>
  </div>
  <button class="btn-primary ml-auto" onclick={() => (adding = !adding)}><Plus class="w-4 h-4" />Connect</button>
</div>

{#if adding}
  <form class="card p-4 mb-4 space-y-3" onsubmit={(e) => { e.preventDefault(); if (!viaChatGPT) add(); }}>
    <div class="grid grid-cols-2 gap-3">
      <div>
        <label class="label" for="k-prov">Provider</label>
        <select id="k-prov" class="input" bind:value={provider} onchange={() => { if (provider !== 'openai') authMode = 'key'; }}>
          {#each providerIds as id}<option value={id}>{providerNames[id] ?? id}</option>{/each}
        </select>
      </div>
      {#if !viaChatGPT}
        <div><label class="label" for="k-label">Label</label><input id="k-label" class="input" bind:value={label} placeholder="Personal, Work…" /></div>
      {/if}
    </div>
    {#if provider === 'openai'}
      <div class="inline-flex rounded-md border border-line p-0.5 text-sm" role="group" aria-label="How to connect OpenAI">
        <button type="button" class="px-3 py-1 rounded {authMode === 'key' ? 'bg-clay text-white' : 'text-ink-soft'}" onclick={() => { authMode = 'key'; cancelSignIn(); }}>Add a key</button>
        <button type="button" class="px-3 py-1 rounded {authMode === 'chatgpt' ? 'bg-clay text-white' : 'text-ink-soft'}" onclick={() => (authMode = 'chatgpt')}>Sign in with ChatGPT</button>
      </div>
    {/if}
    {#if viaChatGPT}
      {#if signin}
        <div class="rounded-md border border-line p-3 space-y-2">
          <p class="text-sm text-ink-soft">A browser window opened. Sign in to ChatGPT and enter this code:</p>
          <div class="flex items-center gap-2">
            <code class="text-lg font-mono tracking-widest text-ink selectable">{signin.userCode}</code>
            <button type="button" class="btn-ghost btn-sm" aria-label="Copy code" onclick={() => navigator.clipboard?.writeText(signin!.userCode)}><Copy class="w-3.5 h-3.5" /></button>
            <button type="button" class="btn-ghost btn-sm" onclick={() => openLink(signin!.verificationUrl)}>Open the page again</button>
          </div>
          <p class="text-xs text-muted">Waiting for approval… this closes by itself when you finish. Only continue if you started this here.</p>
        </div>
      {:else}
        <p class="text-xs text-muted max-w-xl">Uses your ChatGPT plan instead of an API key, so calls count against your plan’s limits and are not billed per token. This is OpenAI’s Codex sign-in, which OpenAI has not published for third-party apps, so it may change or stop working. Anthropic accounts can’t be used this way.</p>
      {/if}
    {:else}
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
    {/if}
    <div class="flex justify-end gap-2">
      <button type="button" class="btn-ghost" onclick={() => { cancelSignIn(); adding = false; }}>Cancel</button>
      {#if viaChatGPT}
        <button type="button" class="btn-primary" disabled={busy || !!signin} onclick={startSignIn}><LogIn class="w-4 h-4" />{signin ? 'Waiting…' : 'Sign in with ChatGPT'}</button>
      {:else}
      <button type="submit" class="btn-primary" disabled={busy || (!secret.trim() && provider !== 'openai_compatible') || (provider === 'openai_compatible' && !baseUrl.trim())}>
        {busy ? 'Saving…' : 'Save and test'}
      </button>
      {/if}
    </div>
  </form>
{/if}

{#if app.credentials.length === 0 && !adding}
  <div class="card p-8 text-center text-sm text-muted">Nothing connected yet. Add an API key (or sign in with ChatGPT for OpenAI) to start chatting.</div>
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
            <div class="text-sm text-ink">{c.label}
              {#if c.kind === 'chatgpt'}<span class="ml-1 rounded bg-raised px-1.5 py-0.5 text-[10px] uppercase tracking-wide text-muted">ChatGPT sign-in</span>
              {:else}<span class="font-mono text-xs text-muted">••••{c.last4 || '-'}</span>{/if}</div>
            <div class="text-[11px] text-muted">
              {#if c.baseUrl}<span class="font-mono">{c.baseUrl}</span> · {/if}
              {#if c.kind === 'chatgpt'}Uses your ChatGPT plan{:else}This month {fmtUsd(c.monthUsage?.costUsd)}{c.monthlyBudgetUsd ? ` of ${fmtUsd(c.monthlyBudgetUsd)}` : ''}{/if}
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
          {#if c.kind !== 'chatgpt'}
            <button class="btn-ghost btn-sm" onclick={() => editBudget(c)}>Budget</button>
            <button class="btn-ghost btn-sm" title="Replace key" aria-label="Replace key" onclick={() => rotate(c)}><RefreshCw class="w-3.5 h-3.5" /></button>
          {/if}
          <button class="btn-danger btn-sm" aria-label={c.kind === 'chatgpt' ? 'Sign out' : 'Delete key'} title={c.kind === 'chatgpt' ? 'Sign out' : 'Delete key'} onclick={() => remove(c)}><Trash2 class="w-3.5 h-3.5" /></button>
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
