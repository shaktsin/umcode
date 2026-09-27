<script lang="ts">
  import { app } from '$lib/stores/app.svelte';
  import type { Approval, ProjectArtifactContent } from '$lib/types';
  import { prettyJSON } from '$lib/format';

  let { approval, compact = false, chatContext = false, projectName }: {
    approval: Approval; compact?: boolean; chatContext?: boolean; projectName?: string;
  } = $props();
  let showDetails = $state(false);
  let screenshotUrl = $state('');

  // Computer Use actions can't be "remembered": every click, keystroke or
  // scroll gets its own approval signature (see ApprovalSignature on the
  // backend), so a remembered decision here would silently rubber-stamp
  // every future action instead of the one specific one that was reviewed.
  const canRemember = $derived(Boolean(projectName) && !approval.tool.startsWith('computer.'));
  const rememberLabel = $derived(projectName || 'this project');
  const rememberAction = $derived(approval.tool === 'shell.run' ? 'command' : 'action');

  // Approvals for a running Computer Use session carry a path to its latest
  // screenshot, so the person can see what they're approving instead of
  // judging a click or keystroke from raw coordinates.
  $effect(() => {
    screenshotUrl = '';
    if (!approval.screenshot || !approval.projectId) return;
    const path = approval.screenshot;
    void app
      .try<ProjectArtifactContent>('project/readArtifact', { projectId: approval.projectId, threadId: approval.threadId, path })
      .then((r) => {
        if (r && approval.screenshot === path) screenshotUrl = `data:${r.mimeType};base64,${r.dataB64}`;
      })
      .catch(() => {});
  });
</script>

<div class="{chatContext ? '' : `px-3 py-2 ${compact ? 'border-t border-clay/30' : ''} bg-clay-soft/20`} flex flex-wrap items-center gap-2">
  {#if screenshotUrl}
    <img src={screenshotUrl} alt="Current state of the app being controlled" class="w-16 h-16 rounded-md border border-line object-cover shrink-0" />
  {/if}
  <div class="flex-1 min-w-48">
    <div class="text-amber-warm text-sm font-medium selectable">{approval.actionSummary || `Run ${approval.tool}`}</div>
    {#if approval.reason}<div class="text-xs text-muted mt-0.5 selectable">{approval.reason}</div>{/if}
    <div class="text-[11px] text-muted mt-1">{approval.tool} · request expires {new Date(approval.expiresAt).toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' })}</div>
  </div>
  {#if chatContext}
    <button class="btn-ghost btn-sm" aria-expanded={showDetails} onclick={() => (showDetails = !showDetails)}>{showDetails ? 'Hide details' : 'Review details'}</button>
  {/if}
  <div class="w-full flex justify-end gap-2">
    <button class="btn-outline btn-sm" onclick={() => app.respondApproval(approval.id, false)}>Deny</button>
    {#if canRemember}
      <button class="btn-outline btn-sm" title={`Always allow this exact ${rememberAction} in ${rememberLabel}`} onclick={() => app.respondApproval(approval.id, true, true)}>Always allow here</button>
    {/if}
    <button class="btn-primary btn-sm" onclick={() => app.respondApproval(approval.id, true)}>Allow once</button>
  </div>
  {#if chatContext && showDetails}
    <pre class="w-full max-h-56 overflow-auto rounded-lg border border-line bg-paper p-2 font-mono text-[11px] whitespace-pre-wrap break-all selectable">{prettyJSON(approval.args)}</pre>
  {/if}
</div>
