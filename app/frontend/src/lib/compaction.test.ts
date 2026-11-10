import { expect,it } from 'vitest';
import {applyCompactionUpdate,compactionLabel} from './compaction';
import type {CompactionState} from './types';
const state:CompactionState={id:'a',threadId:'main',turnId:'t',sequence:2,status:'summarizing',startedAt:'2026-10-20T00:00:00Z',updatedAt:'2026-10-20T00:00:01Z'};
it('ignores stale attempt updates',()=>{
 expect(applyCompactionUpdate(state,{...state,sequence:1})).toBe(state);
 const newer={...state,id:'b',startedAt:'2026-10-20T00:01:00Z',sequence:1};
 expect(applyCompactionUpdate(newer,{...state,sequence:99})).toBe(newer);
 expect(applyCompactionUpdate({...state,status:'completed'}, {...state,sequence:3})).toMatchObject({status:'completed'});
});
it('labels actual phases without percentages',()=>{expect(compactionLabel(state)).toBe('Summarizing earlier context');expect(compactionLabel({...state,status:'restoring'})).toBe('Restoring working context');});
