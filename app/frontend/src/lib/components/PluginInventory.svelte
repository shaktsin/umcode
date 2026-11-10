<script lang="ts">
  import { Boxes, Braces, PlugZap, TriangleAlert } from '@lucide/svelte';
  import { groupPluginComponents } from '$lib/plugins';
  import type { PluginInfo } from '$lib/types';

  let { plugin }: { plugin: PluginInfo } = $props();
  const groups = $derived(groupPluginComponents(plugin));
</script>

<div class="grid gap-4 sm:grid-cols-2">
  {#each [{label:'Skills',items:groups.skills,icon:Braces},{label:'MCP integrations',items:groups.mcpServers,icon:PlugZap},{label:'Hooks',items:groups.hooks,icon:Boxes},{label:'Unsupported',items:groups.unsupported,icon:TriangleAlert}] as group}
    {#if group.items.length}
      <div class="min-w-0 rounded-lg bg-paper/70 p-3">
        <div class="mb-2 flex items-center gap-2 text-xs font-semibold text-muted"><group.icon class="h-4 w-4" />{group.label}<span class="text-faint">{group.items.length}</span></div>
        {#each group.items as item}<div class="flex min-w-0 items-center gap-2 py-1 text-xs"><span class="min-w-0 flex-1 truncate text-ink" title={[item.name, item.path].filter(Boolean).join("\n")}>{item.name}</span><span class={item.health === 'failed' ? 'text-rust' : 'text-faint'} title={item.error} aria-label={item.error ? `${item.health}: ${item.error}` : item.health}>{item.health}</span></div>{/each}
      </div>
    {/if}
  {/each}
</div>
