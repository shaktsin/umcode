import {expect,it} from 'vitest';
import {render} from 'svelte/server';
import {readFileSync} from 'node:fs';
import CompactionProgress from './CompactionProgress.svelte';

const state={id:'a',threadId:'main',turnId:'t',sequence:1,status:'summarizing',startedAt:'2026-10-20T00:00:00Z',updatedAt:'2026-10-20T00:00:01Z'};
const now=Date.parse('2026-10-20T00:00:18Z');

it('shows the active phase, circular progress, timer, and collapsed activity list',()=>{
 const {body}=render(CompactionProgress,{props:{state,now}});
 expect(body).toContain('<details');
 expect(body).not.toContain('<details open');
 expect(body).toContain('Summarizing earlier context');
 expect(body).toContain('role="progressbar"');
 expect(body).toContain('00:18');
 expect(body).toContain('aria-live="polite"');
 expect(body).not.toContain('aria-valuenow');
 expect(body).toContain('Prepared working context');
 expect(body).toContain('Restoring working context');
 expect(body).toContain('aria-current="step"');
});

it('uses the phase reported by the backend in each chat',()=>{
 for(const threadId of ['main','side']) {
  for(const [status,label] of [['preparing','Preparing working context'],['summarizing','Summarizing earlier context'],['restoring','Restoring working context']]) {
   const {body}=render(CompactionProgress,{props:{state:{...state,threadId,status},now}});
   expect(body).toContain(label);
  }
 }
});

it('removes the progress row on every terminal state',()=>{
 for(const status of ['completed','failed','cancelled','interrupted']) {
  const {body}=render(CompactionProgress,{props:{state:{...state,status},now}});
  expect(body).not.toContain('<details');
  expect(body).not.toContain('role="progressbar"');
 }
});

it('places compaction in the active chat turn, not above the composer',()=>{
 const column=readFileSync(new URL('./ChatColumn.svelte',import.meta.url),'utf8');
 const groupStart=column.indexOf('{#each groups as g');
 const component=column.indexOf('<CompactionProgress');
 const groupEnd=column.indexOf('</section>',groupStart);
 expect(component).toBeGreaterThan(groupStart);
 expect(component).toBeLessThan(groupEnd);
 expect(column).not.toContain('thread/compact');
});
