import {expect,it} from 'vitest';
import {render} from 'svelte/server';
import CapacityLine from './CapacityLine.svelte';
const props={model:'gpt-5.6-sol',onsetlimit(){},ondismiss(){}};
it('is one short status line with the details in a tooltip',()=>{
 const {body}=render(CapacityLine,{props});
 expect(body).toContain('role="status"');
 expect(body).toContain('Context window assumed at 256k tokens');
 expect(body).toContain('role="tooltip"');
 expect(body).toContain("doesn't know the context limit for gpt-5.6-sol");
 expect(body).not.toContain('UMCode adapts automatically');
});
it('offers Set limit and a labelled dismiss button, both real buttons',()=>{
 const {body}=render(CapacityLine,{props});
 expect(body).toMatch(/<button[^>]*>\s*Set limit\s*<\/button>/);
 expect(body).toMatch(/<button[^>]*aria-label="Dismiss"/);
 expect(body).toMatch(/<button[^>]*aria-label="Why is the context window assumed\?"/);
});
it('ties the info button to its tooltip for screen readers',()=>{
 const {body}=render(CapacityLine,{props});
 const id=/aria-describedby="([^"]+)"/.exec(body)?.[1];
 expect(id).toBeTruthy();
 expect(body).toContain(`id="${id}"`);
});
