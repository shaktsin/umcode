import {expect,it} from 'vitest';
import {render} from 'svelte/server';
import {readFileSync} from 'node:fs';
import CapacityNotice from './CapacityNotice.svelte';
it('renders nothing before capacity is known to be assumed',()=>{
 const {body}=render(CapacityNotice,{props:{selection:{provider:'openai_compatible',model:'qwen3-coder'}}});
 expect(body).not.toContain('context limit');
});
it('never blocks chat: the notice is informational and dismissible',()=>{
 const source=readFileSync(new URL('./CapacityNotice.svelte',import.meta.url),'utf8');
 expect(source).toContain('role="status"');expect(source).toContain('Dismiss');expect(source).toContain("app.settingsTab = 'models'");
});
it('is mounted above the composer in the chat column',()=>{
 const column=readFileSync(new URL('./ChatColumn.svelte',import.meta.url),'utf8');
 expect(column.indexOf('<CapacityNotice')).toBeGreaterThan(column.indexOf('<CompactionProgress'));
 expect(column.indexOf('<CapacityNotice')).toBeLessThan(column.indexOf('<Composer'));
});
