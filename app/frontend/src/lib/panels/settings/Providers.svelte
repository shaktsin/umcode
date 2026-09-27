<script lang="ts">
  import { Check, LogIn, LogOut, RefreshCw, Terminal, TriangleAlert, X } from '@lucide/svelte';
  import { app } from '$lib/stores/app.svelte';
  import { onDestroy, tick } from 'svelte';

  let refreshing = $state(false);
  let terminalProvider = $state('');
  let terminalSession = $state('');
  let terminalOpening = $state(false);
  let terminalOutput = $state('');
  let terminalError = $state('');
  let terminalDonePending = $state('');
  let terminalCancelled = $state(false);
  let terminalSurface = $state<HTMLDivElement>();
  let terminalScroll = $state<HTMLDivElement>();

  const terminalHtml = $derived(renderTerminal(terminalOutput));

  function renderTerminal(raw: string): string {
    const plain = raw
      .replace(/^\x04/, '')
      .replace(/\x1b\][^\x07]*(?:\x07|\x1b\\)/g, '')
      .replace(/\x1b\[[0-?]*[ -/]*[@-~]/g, '')
      .replace(/\x1b[@-Z\\-_]/g, '')
      .replace(/\r\n/g, '\n').replace(/\r/g, '\n').replace(/\x08/g, '');
    const escaped = plain.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
    return escaped.replace(/https?:\/\/[^\s<>"']+/g, (match) => {
      const trimmed = match.replace(/[),.;!?]+$/, '');
      const tail = match.slice(trimmed.length);
      return `<a href="${trimmed}" data-terminal-link>${trimmed}</a>${tail}`;
    });
  }

  $effect(() => {
    terminalOutput;
    if (terminalScroll) terminalScroll.scrollTop = terminalScroll.scrollHeight;
  });

  function finishTerminal(status: string) {
    appendTerminalOutput(`\n\n[${status}]\n`);
    terminalSession = '';
    terminalOpening = false;
    void refresh();
  }

  function appendTerminalOutput(text: string) {
    terminalOutput = (terminalOutput + text).slice(-80_000);
  }

  const offOutput = app.rpc.on('identity/console/output', (p: { sessionId: string; text: string }) => {
    if (p.sessionId === terminalSession || (terminalOpening && !terminalSession)) appendTerminalOutput(p.text);
  });
  const offDone = app.rpc.on('identity/console/done', (p: { sessionId: string; status: string }) => {
    if (terminalOpening && !terminalSession) { terminalDonePending = p.status; return; }
    if (p.sessionId !== terminalSession) return;
    finishTerminal(p.status);
  });
  onDestroy(() => { offOutput(); offDone(); });

  async function refresh() {
    refreshing = true;
    try {
      await app.refreshCatalog();
    } finally {
      refreshing = false;
    }
  }

  async function openTerminal(identityId: string, signIn: boolean) {
    terminalProvider = identityId;
    terminalSession = '';
    terminalOpening = true;
    terminalOutput = '';
    terminalError = '';
    terminalDonePending = '';
    terminalCancelled = false;
    try {
      const result = await app.call<{ sessionId: string }>('identity/console/start', { provider: identityId, signIn });
      if (terminalCancelled) {
        await app.try('identity/console/stop', { sessionId: result.sessionId });
        return;
      }
      terminalSession = result.sessionId;
      await tick();
      terminalSurface?.focus();
      if (terminalDonePending) {
        const status = terminalDonePending;
        terminalDonePending = '';
        finishTerminal(status);
      }
    } catch (e) {
      terminalError = e instanceof Error ? e.message : String(e);
      terminalOpening = false;
    }
  }

  async function sendTerminalData(data: string) {
    const session = terminalSession;
    if (!session) return;
    try { await app.call('identity/console/input', { sessionId: session, data }); }
    catch (e) { if (session === terminalSession) terminalError = e instanceof Error ? e.message : String(e); }
  }

  function terminalKeydown(event: KeyboardEvent) {
    if (!terminalSession || event.isComposing) return;
    let data = '';
    if (event.metaKey || event.altKey) return;
    if (event.ctrlKey) {
      const key = event.key.toLowerCase();
      if (key === 'c') data = '\x03';
      else if (key === 'd') data = '\x04';
      else if (key === 'l') data = '\x0c';
      else if (key === 'a') data = '\x01';
      else if (key === 'e') data = '\x05';
      else if (key === 'u') data = '\x15';
      else if (key === 'w') data = '\x17';
      else return;
    } else {
      const keys: Record<string, string> = {
        Enter: '\r', Backspace: '\x7f', Tab: '\t', Escape: '\x1b',
        ArrowUp: '\x1b[A', ArrowDown: '\x1b[B', ArrowRight: '\x1b[C', ArrowLeft: '\x1b[D',
        Home: '\x1b[H', End: '\x1b[F', Delete: '\x1b[3~', PageUp: '\x1b[5~', PageDown: '\x1b[6~',
      };
      data = keys[event.key] ?? (event.key.length === 1 ? event.key : '');
      if (!data) return;
    }
    event.preventDefault();
    void sendTerminalData(data);
  }

  function terminalPaste(event: ClipboardEvent) {
    if (!terminalSession) return;
    const text = event.clipboardData?.getData('text/plain');
    if (!text) return;
    event.preventDefault();
    void sendTerminalData(text);
  }

  async function openTerminalLink(event: MouseEvent) {
    const target = event.target as HTMLElement | null;
    const anchor = target?.closest<HTMLAnchorElement>('a[data-terminal-link]');
    if (!anchor) return;
    event.preventDefault();
    try {
      const response = await fetch('/__umcode/shell/openURL', {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ url: anchor.href }),
      });
      if (!response.ok) throw new Error('Could not open link in the browser');
    } catch (e) {
      terminalError = e instanceof Error ? e.message : String(e);
    }
  }

  async function closeTerminal() {
    const session = terminalSession;
    terminalCancelled = terminalOpening && !session;
    terminalSession = '';
    terminalOpening = false;
    if (session) await app.try('identity/console/stop', { sessionId: session });
  }
</script>

<div class="flex items-start gap-4 mb-3">
  <div>
    <h2 class="text-sm font-semibold text-ink">Sign in with a subscription (no API key needed)</h2>
    <p class="text-xs text-muted mt-1 max-w-2xl">Uses the official Codex (ChatGPT) or Claude Code console sign-in. UMCode only checks that a session exists and never reads, copies, or stores its token. Once signed in, the account's models appear in the model picker. Chats run through the Codex or Claude Code command-line tool, so they use your plan's limits rather than a metered key, and UMCode does not track their cost.</p>
  </div>
  <button class="btn-ghost btn-sm ml-auto" disabled={refreshing} onclick={refresh}><RefreshCw class="w-3.5 h-3.5 {refreshing ? 'animate-spin' : ''}" />Refresh</button>
</div>

<div class="space-y-2">
  {#each app.identities as identity (identity.id)}
    <section class="card px-3 py-2.5 flex items-center gap-3">
      <div class="w-8 h-8 shrink-0 rounded-lg bg-raised border border-line flex items-center justify-center text-muted"><Terminal class="w-4 h-4" /></div>
      <div class="min-w-0 flex-1">
        <h3 class="text-sm font-medium text-ink">{identity.displayName} <span class="text-xs font-normal text-muted">via {identity.runtimeName}</span></h3>
        <p class="text-[11px] text-muted truncate">{identity.accountType || identity.status}</p>
      </div>
      <span class="inline-flex items-center gap-1.5 text-xs {identity.signedIn ? 'text-sage' : 'text-muted'}">
        {#if identity.signedIn}<Check class="w-3.5 h-3.5" />{:else}<TriangleAlert class="w-3.5 h-3.5" />{/if}
        {identity.signedIn ? 'Signed in' : identity.installed ? 'Not signed in' : `${identity.runtimeName} not installed`}
      </span>
      <button class="btn-outline btn-sm" onclick={() => openTerminal(identity.id, !identity.signedIn)} disabled={!identity.installed || terminalOpening || !!terminalSession}>
        {#if identity.signedIn}<LogOut class="w-3.5 h-3.5" />Sign out{:else}<LogIn class="w-3.5 h-3.5" />Sign in{/if}
      </button>
    </section>
  {/each}
</div>

{#if terminalOpening || terminalSession || terminalError}
  <section class="card mt-5 overflow-hidden">
    <header class="flex items-center gap-2 px-3 py-2 border-b border-line text-xs text-muted">
      <Terminal class="w-3.5 h-3.5" />Provider sign-in terminal
      <span class="text-faint">{app.identities.find((identity) => identity.id === terminalProvider)?.displayName ?? ''}</span>
      <button class="ml-auto hover:text-ink" aria-label="Close terminal" onclick={closeTerminal}><X class="w-3.5 h-3.5" /></button>
    </header>
    <div class="provider-terminal" bind:this={terminalSurface} role="textbox" aria-label="Interactive provider sign-in terminal" aria-multiline="true" tabindex="0" onkeydown={terminalKeydown} onpaste={terminalPaste} onclick={openTerminalLink}>
      <div class="provider-terminal-scroll" bind:this={terminalScroll}>
        <pre>{@html terminalHtml || (terminalOpening ? 'Starting provider…\n' : '')}<span class="terminal-cursor" aria-hidden="true"></span></pre>
      </div>
    </div>
    {#if terminalError}<div class="px-3 py-2 text-xs text-rust">{terminalError}</div>{/if}
  </section>
{/if}

<style>
  .provider-terminal {
    height: min(42vh, 360px);
    min-height: 210px;
    padding: 14px 16px;
    overflow: hidden;
    outline: none;
    background: #10151e;
    color: #d8dee9;
    font: 13px/1.55 ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
    cursor: text;
  }
  .provider-terminal:focus-visible { box-shadow: inset 0 0 0 1px var(--color-accent); }
  .provider-terminal-scroll { width: 100%; height: 100%; overflow: auto; }
  .provider-terminal pre { margin: 0; min-height: 100%; white-space: pre-wrap; overflow-wrap: anywhere; }
  .provider-terminal :global(a[data-terminal-link]) { color: #80c7ff; text-decoration: underline; text-underline-offset: 2px; cursor: pointer; }
  .terminal-cursor { display: inline-block; width: 0.6em; height: 1.05em; margin-left: 1px; vertical-align: text-bottom; background: #a3e6b2; animation: terminal-blink 1.1s steps(2, start) infinite; }
  @keyframes terminal-blink { to { visibility: hidden; } }
</style>
