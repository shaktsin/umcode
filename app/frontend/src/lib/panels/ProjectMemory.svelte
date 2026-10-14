<script lang="ts">
 import { onDestroy } from 'svelte';
 import { app } from '$lib/stores/app.svelte';
 import { inspector } from '$lib/stores/inspector.svelte';
 import { MemoryReviewController, type MemoryReviewState, type MemoryReviewEntry } from '$lib/memoryReview';
 let { projectId }:{projectId:string}=$props();
 let view: MemoryReviewState = $state<MemoryReviewState>({entries:[],includeHistory:false,busy:false,error:'',conflict:false,editText:'',warning:''});
 const controller=new MemoryReviewController((method,params)=>app.call(method,params),(next)=>view=next);
 let editing=$state<string|null>(null);
 $effect(()=>{if(projectId&&app.conn==='open')void controller.load(projectId)});
 async function correct(row:MemoryReviewEntry){await controller.correct(row,view.editText);if(!view.error)editing=null}
 async function retire(row:MemoryReviewEntry){await controller.retire(row)}
 function openTarget(row:MemoryReviewEntry){void inspector.openFile(row.targetPath,undefined,projectId)}
 onDestroy(()=>controller.invalidate());
</script>
<section class="card p-4 mt-4 space-y-3">
 <div class="flex items-center gap-2"><h2 class="text-sm font-semibold">Project memory</h2><label class="ml-auto text-xs text-muted"><input type="checkbox" checked={view.includeHistory} onchange={(e)=>{view.includeHistory=e.currentTarget.checked;void controller.load(projectId)}} /> Include history</label></div>
 <p class="text-xs text-muted">Review knowledge that may be included in future turns. User-authored instructions remain normal project guidance.</p>
 {#if view.error}<p class="text-xs text-rust" role="alert">{view.error}</p>{#if view.conflict}<button class="text-xs underline" onclick={()=>controller.load(projectId)}>Refresh after conflict</button>{/if}{/if}
 {#if view.warning}<p class="text-xs text-clay" role="status">{view.warning} <button class="underline" onclick={()=>{void inspector.openFile('UMCODE.md',undefined,projectId)}}>Open project instructions</button></p>{/if}
 {#if view.busy}<p class="text-xs text-muted">Loading…</p>{/if}
 <div class="space-y-2">
 {#each view.entries as row (row.id)}
  <article class="rounded-md border border-line p-3 space-y-2">
   <div class="flex items-start gap-2"><div class="min-w-0 flex-1"><p class="text-xs font-medium">{row.category} · {row.status}{row.userOwned ? ' · user-authored' : ' · verified source'}</p><p class="text-sm whitespace-pre-wrap break-words">{row.text}</p><p class="text-[11px] text-muted">{row.targetPath} · source {row.sourceRevision || 'unknown'} · {row.evidenceIds.length} evidence references</p></div><button class="text-xs underline" onclick={()=>openTarget(row)}>Open file</button></div>
   {#if editing===row.id}<div class="space-y-2"><textarea class="input w-full min-h-16 text-xs" bind:value={view.editText} aria-label="Corrected memory text"></textarea><div class="flex gap-2"><button class="btn-sm" disabled={view.busy} onclick={()=>correct(row)}>Save correction</button><button class="btn-ghost btn-sm" onclick={()=>editing=null}>Cancel</button></div><p class="text-[11px] text-muted">A correction is recorded as user-authored guidance; it is not automatically marked verified.</p></div>
   {:else if row.status==='active'}<div class="flex gap-3"><button class="text-xs underline" onclick={()=>{editing=row.id;view.editText=row.text.replace(/^- /,'')}}>Correct</button><button class="text-xs underline text-rust" disabled={view.busy} onclick={()=>retire(row)}>Retire</button></div>{/if}
  </article>
 {:else}<p class="text-xs text-muted">No project memory entries.</p>{/each}
 </div>
 {#if view.cursor}<button class="text-xs underline" disabled={view.busy} onclick={()=>controller.loadMore()}>Load more</button>{/if}
</section>
