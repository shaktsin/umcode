import {expect,it} from 'vitest';
import {render} from 'svelte/server';
import {readFileSync} from 'node:fs';
import CompactionProgress from './CompactionProgress.svelte';
const state={id:'a',threadId:'main',turnId:'t',sequence:1,status:'preparing',startedAt:'2026-10-20T00:00:00Z',updatedAt:'2026-10-20T00:00:01Z'};
it('renders automatic progress in main and side chat',()=>{
 for(const threadId of ['main','side']) {
  const {body}=render(CompactionProgress,{props:{state:{...state,threadId}}});
  expect(body).toContain('Automatically compacting context…');expect(body).toContain('role="progressbar"');expect(body).toContain('aria-live="polite"');expect(body).not.toContain('aria-valuenow');
 }
});
it('clears bar on every terminal state',()=>{for(const status of ['completed','failed','cancelled','interrupted']){expect(render(CompactionProgress,{props:{state:{...state,status}}}).body).not.toContain('role="progressbar"');}});
it('exposes no manual compaction action',()=>{const column=readFileSync(new URL('./ChatColumn.svelte',import.meta.url),'utf8');expect(column).not.toContain('thread/compact');expect(column).not.toContain('compactChat');expect(column).toContain('<CompactionProgress');});
