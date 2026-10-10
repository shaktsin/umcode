<script lang="ts">
  import Keys from './settings/Keys.svelte';
  import Models from './settings/Models.svelte';
  import ComplexityTab from './settings/Complexity.svelte';
  import Engine from './settings/Engine.svelte';
  import ComputerUse from './settings/ComputerUse.svelte';
 import TokenOptimization from './settings/TokenOptimization.svelte';

  const tabs = [
    { id: 'keys', label: 'API keys' },
    { id: 'models', label: 'Models' },
    { id: 'complexity', label: 'Complexity' },
    { id: 'computer-use', label: 'Computer Use' },
 { id: 'optimization', label: 'Token optimization' },
    { id: 'engine', label: 'Engine & app' },
  ] as const;
  let tab = $state<(typeof tabs)[number]['id']>('keys');
</script>

<h1 class="page-title mb-3">Settings</h1>
<div class="flex flex-wrap gap-1 border-b border-line mb-4">
  {#each tabs as t}
    <button
      class="px-3 py-2 text-sm -mb-px border-b-2 {tab === t.id ? 'border-clay text-ink' : 'border-transparent text-muted hover:text-ink'}"
      onclick={() => (tab = t.id)}
    >{t.label}</button>
  {/each}
</div>

{#if tab === 'keys'}<Keys />
{:else if tab === 'models'}<Models />
{:else if tab === 'complexity'}<ComplexityTab />
{:else if tab === 'computer-use'}<ComputerUse />
{:else if tab === 'optimization'}<TokenOptimization />
{:else}<Engine />
{/if}
