<script lang="ts">
  import { Boxes, Braces, PlugZap, TriangleAlert } from '@lucide/svelte';
  import { groupPluginComponents } from '$lib/plugins';
  import type { PluginInfo } from '$lib/types';

  let { plugin }: { plugin: PluginInfo } = $props();
  const groups = $derived(groupPluginComponents(plugin));
</script>

<div class="grid gap-2 sm:grid-cols-2 xl:grid-cols-4">
  <div class="rounded-lg bg-paper/70 p-2.5">
    <div class="mb-1.5 flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wide text-muted"><Braces class="h-3.5 w-3.5" /> Skills <span class="text-faint">{groups.skills.length}</span></div>
    {#each groups.skills as item}<div class="truncate text-xs text-ink" title={item.path}>{item.name} <span class="text-faint">· {item.health}</span></div>{:else}<div class="text-xs text-faint">None</div>{/each}
  </div>
  <div class="rounded-lg bg-paper/70 p-2.5">
    <div class="mb-1.5 flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wide text-muted"><PlugZap class="h-3.5 w-3.5" /> MCP <span class="text-faint">{groups.mcpServers.length}</span></div>
    {#each groups.mcpServers as item}<div class="truncate text-xs text-ink" title={item.error || item.path}>{item.name} <span class={item.health === 'failed' ? 'text-rust' : 'text-faint'}>· {item.health}</span></div>{:else}<div class="text-xs text-faint">None</div>{/each}
  </div>
  <div class="rounded-lg bg-paper/70 p-2.5">
    <div class="mb-1.5 flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wide text-muted"><Boxes class="h-3.5 w-3.5" /> Hooks <span class="text-faint">{groups.hooks.length}</span></div>
    {#each groups.hooks as item}<div class="truncate text-xs text-ink">{item.name} <span class="text-faint">· {item.health}</span></div>{:else}<div class="text-xs text-faint">None</div>{/each}
  </div>
  <div class="rounded-lg bg-paper/70 p-2.5">
    <div class="mb-1.5 flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wide text-muted"><TriangleAlert class="h-3.5 w-3.5" /> Unsupported <span class="text-faint">{groups.unsupported.length}</span></div>
    {#each groups.unsupported as item}<div class="truncate text-xs text-amber-warm">{item.kind}: {item.name}</div>{:else}<div class="text-xs text-faint">None</div>{/each}
  </div>
</div>
