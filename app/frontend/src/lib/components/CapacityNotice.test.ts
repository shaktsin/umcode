import {expect,it} from 'vitest';
import {render} from 'svelte/server';
import {readFileSync} from 'node:fs';
import CapacityNotice from './CapacityNotice.svelte';
it('renders nothing before capacity is known to be assumed',()=>{
 const {body}=render(CapacityNotice,{props:{selection:{provider:'openai_compatible',model:'qwen3-coder'}}});
 expect(body).not.toContain('context limit');
});
it('never blocks chat: the notice is informational and dismissible',()=>{
 const line=readFileSync(new URL('./CapacityLine.svelte',import.meta.url),'utf8');
 const source=readFileSync(new URL('./CapacityNotice.svelte',import.meta.url),'utf8');
 expect(line).toContain('role="status"');expect(line).toContain('Dismiss');expect(source).toContain("app.settingsTab = 'models'");
});
it('shows the slim line, not the old full-width bar',()=>{
 const source=readFileSync(new URL('./CapacityNotice.svelte',import.meta.url),'utf8');
 expect(source).toContain('<CapacityLine');
 expect(source).not.toContain('border-top');
});
it('sits in the same column as the composer, not edge to edge',()=>{
 const column=readFileSync(new URL('./ChatColumn.svelte',import.meta.url),'utf8');
 const rail=column.slice(column.lastIndexOf('<div',column.indexOf('<Agents')),column.indexOf('<Composer'));
 expect(rail).toContain("variant === 'main' ? 'max-w-3xl'");
 expect(rail).toContain('mx-auto');
 expect(rail).toContain('<CapacityNotice');
 expect(rail).toContain('<Agents');
});
it('is mounted above the composer in the chat column',()=>{
 const column=readFileSync(new URL('./ChatColumn.svelte',import.meta.url),'utf8');
 expect(column.indexOf('<CapacityNotice')).toBeGreaterThan(column.indexOf('<CompactionProgress'));
 expect(column.indexOf('<CapacityNotice')).toBeLessThan(column.indexOf('<Composer'));
});
