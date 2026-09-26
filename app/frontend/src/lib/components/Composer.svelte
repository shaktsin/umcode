<script lang="ts">
  import { ArrowUp, Square, Paperclip, X } from '@lucide/svelte';
  import { chat, type ThreadView } from '$lib/stores/chat.svelte';
  import { app } from '$lib/stores/app.svelte';
  import { projects } from '$lib/stores/projects.svelte';
  import ModelPicker from './ModelPicker.svelte';
  import type { Attachment } from '$lib/types';

  let { view, compact = false }: { view: ThreadView; compact?: boolean } = $props();

  let text = $state('');
  let files = $state<Attachment[]>([]);
  let input: HTMLTextAreaElement | undefined = $state();
  let fileInput: HTMLInputElement | undefined = $state();

  const MAX_FILE = 15 * 1024 * 1024;
  const busy = $derived(view.sending || !!view.running);
  const offline = $derived(app.conn !== 'open');
  const isMain = $derived(view === chat.main);

  // A side chat can hand its answer to the main composer.
  $effect(() => {
    if (!isMain) return;
    const onCompose = (e: Event) => {
      const detail = (e as CustomEvent<string>).detail ?? '';
      text = text ? `${text}\n\n${detail}` : detail;
      input?.focus();
    };
    window.addEventListener('umcode:compose', onCompose);
    return () => window.removeEventListener('umcode:compose', onCompose);
  });

  function submit() {
    if (busy || offline) return;
    const t = text;
    const a = files;
    if (!t.trim() && a.length === 0) return;
    text = '';
    files = [];
    if (isMain) void chat.send(t, a);
    else void view.send(t, a);
  }

  function onKeydown(e: KeyboardEvent) {
    if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
      e.preventDefault();
      submit();
    }
  }

  function readFile(f: File): Promise<Attachment> {
    return new Promise((resolve, reject) => {
      const r = new FileReader();
      r.onload = () => {
        const s = String(r.result);
        resolve({ name: f.name, mimeType: f.type || 'application/octet-stream', dataB64: s.slice(s.indexOf(',') + 1) });
      };
      r.onerror = () => reject(r.error);
      r.readAsDataURL(f);
    });
  }

  async function addFiles(list: FileList | File[] | null) {
    if (!list) return;
    for (const f of Array.from(list)) {
      if (f.size > MAX_FILE) {
        app.toast('error', `${f.name} is larger than 15 MB.`);
        continue;
      }
      files = [...files, await readFile(f)];
    }
  }

  function onPaste(e: ClipboardEvent) {
    const fl = e.clipboardData?.files;
    if (fl && fl.length) {
      e.preventDefault();
      void addFiles(fl);
    }
  }

  function onDrop(e: DragEvent) {
    e.preventDefault();
    void addFiles(e.dataTransfer?.files ?? null);
  }

  const route = $derived(view.route?.chosen ?? null);
  const fallbacks = $derived((view.route?.alternatives ?? []).filter((a) => !a.unavailable));
  const routeChip = $derived(
    route ? `${route.displayName || route.model}${fallbacks.length ? ` +${fallbacks.length}` : ''}` : '',
  );
  // With nothing to run on, the chip says so rather than vanishing: this is
  // exactly the moment the reason matters.
  const blockedReason = $derived(view.route && !view.route.chosen ? view.route.reason || 'No model is available' : '');
  const routeTitle = $derived(
    !route
      ? [
          blockedReason,
          ...(view.route?.alternatives ?? [])
            .slice(0, 4)
            .map((a) => `${a.displayName || a.model} (${a.credentialLabel || 'no key'}): ${a.unavailable}`),
        ]
          .filter(Boolean)
          .join('\n')
      : [
          `Runs on ${route.displayName || route.model} (${route.credentialLabel || 'default key'})`,
          fallbacks.length
            ? `Falls back to ${fallbacks
                .slice(0, 3)
                .map((f) => `${f.displayName || f.model} (${f.credentialLabel || 'default key'})`)
                .join(', ')}${fallbacks.length > 3 ? '…' : ''}`
            : 'No fallback is available',
          ...(view.route?.alternatives ?? [])
            .filter((a) => a.unavailable)
            .slice(0, 3)
            .map((a) => `${a.displayName || a.model}: ${a.unavailable}`),
        ].join('\n'),
  );

  const placeholder = $derived(
    offline
      ? 'Engine offline…'
      : compact
        ? 'Ask about this…'
        : projects.active
          ? `Ask about ${projects.active.name}…`
          : 'Ask anything. Open a project to make changes',
  );
</script>

<div class="px-4 pb-4 pt-2 shrink-0">
  <div
    class="{compact ? '' : 'max-w-3xl'} mx-auto bg-surface border border-line rounded-2xl focus-within:border-line-strong transition-colors"
    role="region"
    aria-label="Message composer"
    ondragover={(e) => e.preventDefault()}
    ondrop={onDrop}
  >
    {#if files.length}
      <div class="flex flex-wrap gap-1.5 px-3 pt-2.5">
        {#each files as f, i}
          <span class="inline-flex items-center gap-1 text-xs bg-raised rounded-md pl-2 pr-1 py-0.5">
            {f.name}
            <button aria-label={`Remove ${f.name}`} class="text-faint hover:text-ink" onclick={() => (files = files.filter((_, j) => j !== i))}>
              <X class="w-3 h-3" />
            </button>
          </span>
        {/each}
      </div>
    {/if}
    <textarea
      bind:this={input}
      bind:value={text}
      onkeydown={onKeydown}
      onpaste={onPaste}
      rows="1"
      {placeholder}
      class="w-full bg-transparent px-4 pt-3 pb-1 text-sm resize-none focus:outline-none placeholder:text-faint max-h-60"
      style="field-sizing: content; min-height: 2.75rem"
    ></textarea>
    <div class="flex items-center gap-2 px-2.5 pb-2.5">
      <button class="btn-ghost btn-sm" title="Attach files" aria-label="Attach files" onclick={() => fileInput?.click()}>
        <Paperclip class="w-4 h-4" />
      </button>
      <input
        bind:this={fileInput}
        type="file"
        multiple
        class="hidden"
        onchange={(e) => {
          addFiles(e.currentTarget.files);
          e.currentTarget.value = '';
        }}
      />
      <ModelPicker value={view.selection} onchange={(v) => view.setSelection(v)} compact={compact} />
      {#if !compact && projects.active && view.id === null}
        <label class="inline-flex items-center gap-1.5 text-[11px] text-muted" title="Choose whether this chat edits the project folder or works in an isolated Git copy">
          <span>Workspace</span>
          <select class="select h-7 max-w-36 px-2 text-[11px]" bind:value={view.workspaceMode}>
            <option value="local">Project folder</option>
            {#if projects.active.vcs?.kind === 'git' && projects.active.vcs.hasCommit}<option value="worktree">Isolated worktree</option>{/if}
          </select>
        </label>
      {/if}
      {#if route}
        <span class="text-[11px] text-faint truncate {compact ? 'hidden sm:inline' : ''}" title={routeTitle}>
          {routeChip}
        </span>
      {:else if blockedReason}
        <span class="text-[11px] text-rust truncate {compact ? 'hidden sm:inline' : ''}" title={routeTitle}>
          Nothing available
        </span>
      {/if}
      <div class="ml-auto">
        {#if busy}
          <button
            class="w-8 h-8 rounded-full bg-ink text-paper flex items-center justify-center hover:brightness-110"
            title="Stop"
            aria-label="Stop"
            onclick={() => view.interrupt()}
          >
            <Square class="w-3.5 h-3.5 fill-current" />
          </button>
        {:else}
          <button
            class="w-8 h-8 rounded-full bg-clay text-white flex items-center justify-center hover:brightness-110 disabled:opacity-30"
            title="Send (Enter)"
            aria-label="Send"
            disabled={offline || (!text.trim() && files.length === 0)}
            onclick={submit}
          >
            <ArrowUp class="w-4 h-4" />
          </button>
        {/if}
      </div>
    </div>
  </div>
</div>
