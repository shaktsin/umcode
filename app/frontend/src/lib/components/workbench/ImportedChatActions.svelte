<script lang="ts">
 import type {Promotion} from '$lib/workbench/inferenceTypes';
 let {canPromote,busy,promotion,onPromote,onOpen,onRemove}:{canPromote:boolean;busy:boolean;promotion:Promotion|null;onPromote:()=>void;onOpen:()=>void;onRemove:()=>void}=$props();
</script>
<button class="btn-outline" disabled={!canPromote||busy} onclick={onPromote}>Promote to Chat</button>
{#if promotion}
 <section class="promoted" aria-label="Available in Chat">
  <div><strong>Available in Chat</strong><p>{promotion.name} · pinned weights</p><p>Local text chat. Your original model files stay unchanged.</p></div>
  <div class="actions"><button class="btn-primary btn-sm" disabled={busy} onclick={onOpen}>Open in Chat</button><button class="btn-outline btn-sm" disabled={busy} onclick={onRemove}>Remove from Chat</button></div>
 </section>
{/if}
<style>
 .promoted{flex-basis:100%;border:1px solid var(--color-line);border-radius:8px;padding:16px;margin-top:4px;font-size:13px}.promoted p{font-size:12px;color:var(--color-muted);margin:6px 0;overflow-wrap:anywhere}.actions{display:flex;flex-wrap:wrap;gap:12px;margin-top:12px}
</style>
