<script lang="ts">
  import { ShieldCheck } from '@lucide/svelte';
  import { app } from '$lib/stores/app.svelte';
  import { chat } from '$lib/stores/chat.svelte';
  import { projects } from '$lib/stores/projects.svelte';
  import { prettyJSON, relTime } from '$lib/format';
  import ApprovalCard from '$lib/components/ApprovalCard.svelte';
</script>

<div class="flex items-center mb-4">
  <h1 class="page-title">Approvals</h1>
  <span class="ml-3 text-xs text-muted">Risky actions wait here until you decide. Telegram and other admin clients see the same requests; the first answer wins.</span>
</div>

{#if app.approvals.length === 0}
  <div class="card p-10 text-center">
    <ShieldCheck class="w-8 h-8 mx-auto text-sage mb-2" />
    <p class="text-sm text-muted">Nothing waiting for approval.</p>
  </div>
{/if}

<div class="space-y-3">
  {#each app.approvals as a (a.id)}
    <div class="card p-4">
      <div class="flex items-start gap-3">
        <span class="risk-{a.risk} mt-0.5">{a.risk}</span>
        <div class="flex-1 min-w-0">
          <button class="block text-left text-sm text-ink selectable hover:text-accent-strong" title="Open this chat" onclick={() => chat.open(a.threadId)}>{a.actionSummary || a.tool}</button>
          <div class="text-xs text-muted mt-0.5">
            <code class="font-mono">{a.tool}</code>{a.reason ? ` · ${a.reason}` : ''} · {relTime(a.createdAt)}
          </div>
          <details class="mt-2">
            <summary class="text-xs text-muted cursor-default">Arguments</summary>
            <pre class="mt-1 font-mono text-[11px] text-muted bg-paper rounded p-2 whitespace-pre-wrap break-all max-h-60 overflow-auto">{prettyJSON(a.args)}</pre>
          </details>
        </div>
        <div class="shrink-0 text-xs text-muted">Open chat ↗</div>
      </div>
      <div class="mt-2 -mx-4 -mb-4 rounded-b-xl overflow-hidden"><ApprovalCard approval={a} /></div>
    </div>
  {/each}
</div>
