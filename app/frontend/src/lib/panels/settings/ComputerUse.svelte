<script lang="ts">
  import { app } from '$lib/stores/app.svelte';

  let enabled = $state(true);
  let saving = $state(false);

  async function load() {
    const result = await app.try<{ enabled: boolean }>('settings/computerUse/getDefault', {});
    if (result) enabled = result.enabled;
  }

  async function setEnabled(value: boolean) {
    saving = true;
    try {
      const result = await app.try<{ enabled: boolean }>('settings/computerUse/setDefault', { enabled: value });
      if (result) enabled = result.enabled;
    } finally {
      saving = false;
    }
  }

  $effect(() => {
    void app.conn;
    if (app.conn === 'open') void load();
  });
</script>

<section class="card p-4 max-w-2xl">
  <h2 class="text-sm font-semibold">Computer Use</h2>
  <p class="text-xs text-muted mt-1 mb-4">Choose the default for projects. A project can explicitly turn Computer Use on or off, overriding this default. Chat approval settings still govern agent actions.</p>
  <label class="flex items-start gap-3 text-sm">
    <input class="mt-1" type="checkbox" checked={enabled} disabled={saving} onchange={(e) => setEnabled(e.currentTarget.checked)} />
      <span>Enable Computer Use by default for projects
      <span class="block text-xs text-muted">Lets the agent interact with selected desktop apps. UMCode captures and controls targets in its own process and streams the live view in chat.</span>
    </span>
  </label>
</section>
