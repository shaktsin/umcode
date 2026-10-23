<script lang="ts">
  import DelegationActivation from './DelegationActivation.svelte';
  import { tick } from 'svelte';
  import { ArrowUp, Square, Paperclip, X } from '@lucide/svelte';
  import { chat, type ThreadView } from '$lib/stores/chat.svelte';
  import { app } from '$lib/stores/app.svelte';
  import { projects } from '$lib/stores/projects.svelte';
  import PopupSurface from './PopupSurface.svelte';
  import ModelPicker from './ModelPicker.svelte';
  import ApprovalModePill from './ApprovalModePill.svelte';
  import ApprovalDock from './ApprovalDock.svelte';
  import { dockState } from '$lib/approvalDock';
  import type { Attachment, SkillInfo } from '$lib/types';
  import { errMsg } from '$lib/format';
  import { composerPlaceholder } from '$lib/firstRun';
  import { filterSkills, selectSkill, slashQuery } from '$lib/skillPicker';

  let { view, compact = false }: { view: ThreadView; compact?: boolean } = $props();

  let text = $state('');
  let files = $state<Attachment[]>([]);
  let input: HTMLTextAreaElement | undefined = $state();
  let fileInput: HTMLInputElement | undefined = $state();
  const pickerId = $props.id();
  let caret = $state(0);
  let focused = $state(false);
  let dismissed = $state(false);
  let skills = $state<SkillInfo[]>([]);
  let skillsLoading = $state(false);
  let skillsError = $state('');
  let activeSkill = $state(0);
  let retry = $state(0);

  const MAX_FILE = 15 * 1024 * 1024;
  const docked = $derived(Boolean(dockState(app.approvals, view.id).current));
  const busy = $derived(view.sending || !!view.running);
  const offline = $derived(app.conn !== 'open');
  const isMain = $derived(view === chat.main);
  const projectId = $derived(view.id ? view.thread?.projectId ?? null : isMain ? projects.activeId : null);
  const query = $derived(slashQuery(text, caret));
  const pickerOpen = $derived(focused && query !== null && !dismissed && !offline && !busy);
  const matches = $derived(filterSkills(skills, query ?? ''));

  $effect(() => {
    void query;
    void skills;
    activeSkill = 0;
  });

  $effect(() => {
    const project = projectId;
    void retry;
    if (!pickerOpen) return;
    let cancelled = false;
    skillsLoading = true;
    skillsError = '';
    skills = [];
    app.call<{ skills: SkillInfo[] }>('skill/list', { projectId: project ?? undefined }).then((result) => {
      if (!cancelled) skills = result.skills ?? [];
    }).catch((error) => {
      if (!cancelled) skillsError = errMsg(error);
    }).finally(() => {
      if (!cancelled) skillsLoading = false;
    });
    return () => { cancelled = true; };
  });

  function updateCaret(e: Event) {
    caret = (e.currentTarget as HTMLTextAreaElement).selectionStart;
  }

  function onInput(e: Event) {
    updateCaret(e);
    dismissed = false;
  }

  function retrySkills() {
    input?.focus();
    retry++;
  }

  async function chooseSkill(skill: SkillInfo) {
    const selected = selectSkill(text, caret, skill.name);
    text = selected.text;
    caret = selected.caret;
    dismissed = true;
    await tick();
    input?.focus();
    input?.setSelectionRange(caret, caret);
  }

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
    if (busy || offline || app.needsProvider || view.delegation.pending) return;
    const t = text;
    const a = files;
    if (!t.trim() && a.length === 0) return;
    text = '';
    files = [];
    if (isMain) void chat.send(t, a);
    else void view.send(t, a);
  }

  function onKeydown(e: KeyboardEvent) {
    if (e.isComposing) return;
    if (pickerOpen) {
      if (e.key === 'Escape') {
        e.preventDefault();
        e.stopPropagation();
        dismissed = true;
        return;
      }
      if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
        e.preventDefault();
        if (matches.length) {
          activeSkill = (activeSkill + (e.key === 'ArrowDown' ? 1 : -1) + matches.length) % matches.length;
          void tick().then(() => document.getElementById(`${pickerId}-${activeSkill}`)?.scrollIntoView({ block: 'nearest' }));
        }
        return;
      }
      const canSelect = !skillsLoading && !skillsError && !!matches[activeSkill];
      if (e.key === 'Tab' && e.shiftKey && skillsError) {
        const retryButton = document.querySelector<HTMLButtonElement>(`[data-skill-popup="${pickerId}"] button`);
        if (retryButton) { e.preventDefault(); retryButton.focus(); return; }
      }
      if ((e.key === 'Tab' && !e.shiftKey && canSelect) || (e.key === 'Enter' && !e.shiftKey)) {
        e.preventDefault();
        if (canSelect) void chooseSkill(matches[activeSkill]);
        return;
      }
    }
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

  const blockedReason = $derived(view.route && !view.route.chosen ? view.route.reason || 'No model is available' : '');

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
  <div class="{compact ? '' : 'max-w-3xl'} mx-auto">
    <ApprovalDock approvals={app.approvals} threadId={view.id} />
  </div>
  <div
    class="relative {compact ? '' : 'max-w-3xl'} mx-auto {docked ? 'composer-docked' : ''} bg-surface border border-line rounded-2xl focus-within:border-line-strong transition-colors"
    role="region"
    aria-label="Message composer"
    ondragover={(e) => e.preventDefault()}
    ondrop={onDrop}
    onfocusout={(e) => { const target = e.relatedTarget as HTMLElement | null; if (!e.currentTarget.contains(target) && target?.closest('[data-skill-popup]')?.getAttribute('data-skill-popup') !== pickerId) focused = false; }}
    data-composer
  >
    <PopupSurface open={pickerOpen} anchor={input} kind="skills" preferredSide="above" onclose={() => dismissed = true}>
      <div data-skill-popup={pickerId}>
        <div class="popup-header flex items-center justify-between border-b border-line px-3 py-2 text-xs text-muted">
          <span>Skills</span><span>↑↓ navigate · Enter select · Esc close</span>
        </div>
        <div class="popup-list">
        {#if skillsLoading}
          <div class="px-3 py-4 text-sm text-muted" role="status">Loading skills…</div>
        {:else if skillsError}
          <div class="px-3 py-3 text-sm text-muted" role="alert">
            Couldn’t load skills: {skillsError}
            <button class="btn-ghost btn-sm mt-2" onmousedown={(e) => e.preventDefault()} onclick={retrySkills}>Retry</button>
          </div>
        {:else if matches.length === 0}
          <div class="px-3 py-4 text-sm text-muted" role="status">{skills.some((skill) => !skill.error) ? 'No matching skills.' : 'No skills available. Install a plugin in Plugins.'}</div>
        {:else}
          <div id={pickerId} role="listbox" aria-label="Available skills" class="p-1">
            {#each matches as skill, index (skill.name)}
              <button
                id={`${pickerId}-${index}`}
                role="option"
                aria-selected={index === activeSkill}
                aria-describedby={`${pickerId}-${index}-description`}
                tabindex="-1"
                class="block w-full rounded-lg px-3 py-2 text-left {index === activeSkill ? 'bg-raised' : 'hover:bg-raised'}"
                onmousedown={(e) => e.preventDefault()}
                onclick={() => chooseSkill(skill)}
              >
                <span class="block text-sm font-medium text-ink">/{skill.name}</span>
                <span id={`${pickerId}-${index}-description`} class="block truncate text-xs text-muted" title={skill.description}>{skill.description}</span>
              </button>
            {/each}
          </div>
        {/if}
        </div>
      </div>
    </PopupSurface>
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
      {...{ autocorrect: 'off' }}
      autocapitalize="off"
      spellcheck={false}
      bind:this={input}
      bind:value={text}
      onkeydown={onKeydown}
      oninput={onInput}
      onkeyup={updateCaret}
      onclick={updateCaret}
      onselect={updateCaret}
      onfocus={() => focused = true}
      aria-label="Message"
      aria-autocomplete="list"
      aria-controls={pickerOpen && matches.length && !skillsLoading && !skillsError ? pickerId : undefined}
      aria-activedescendant={pickerOpen && matches[activeSkill] && !skillsLoading && !skillsError ? `${pickerId}-${activeSkill}` : undefined}
      onpaste={onPaste}
      rows="1"
      placeholder={app.needsProvider ? composerPlaceholder(placeholder, true) : `${placeholder} · / for skills`}
      class="composer-input w-full bg-transparent px-4 pt-3 pb-1 text-sm resize-none focus:outline-none placeholder:text-faint"
      style="field-sizing: content; min-height: 2.75rem"
    ></textarea>
    <DelegationActivation state={view.delegation} {busy} onenable={() => chat.enableDelegation(view)} />
    <div class="composer-toolbar">
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
      <ApprovalModePill {view} />
      {#if blockedReason}
        <span class="composer-error" role="status" title={blockedReason}>No model available</span>
      {/if}
      <div class="ml-auto">
        {#if busy}
          <button
            class="icon-button compact rounded-full bg-ink text-paper hover:brightness-110"
            title="Stop"
            aria-label="Stop"
            onclick={() => view.interrupt()}
          >
            <Square class="w-3.5 h-3.5 fill-current" />
          </button>
        {:else}
          <button
            class="icon-button compact rounded-full bg-clay text-white hover:brightness-110 disabled:opacity-30"
            title="Send (Enter)"
            aria-label="Send"
            disabled={offline || app.needsProvider || view.delegation.pending || (!text.trim() && files.length === 0)}
            onclick={submit}
          >
            <ArrowUp class="w-4 h-4" />
          </button>
        {/if}
      </div>
    </div>
  </div>
</div>
