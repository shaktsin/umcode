import { describe, expect, it } from 'vitest';
import { TurnDetailsController, type TurnDetailsResult } from './turnDetails';
const fixture=(costUsd?:number):TurnDetailsResult=>({turn:{id:'t1',status:'completed',selection:{model:'m'},resolved:{model:'m'},startedAt:'2026-01-01',usage:{inputTokens:0,cachedInputTokens:0,outputTokens:0,reasoningTokens:0,costUsd:0,requests:1}},diagnostics:{version:1,complete:false,policy:{},requests:[{ordinal:1,phase:'chat',provider:'p',model:'m',...(costUsd===undefined?{}:{costUsd}),costState:costUsd===undefined?'unavailable':'known',measurementOrigin:'provider'}]},availability:'partial'});
function deferred<T>() {let resolve!:(v:T)=>void;const promise=new Promise<T>(r=>resolve=r);return {promise,resolve};}
describe('TurnDetailsController',()=>{
 it('ignores stale responses and retries failures',async()=>{const first=deferred<TurnDetailsResult>();let calls=0;const states:any[]=[];const c=new TurnDetailsController(async()=>{calls++;if(calls===1)return first.promise;if(calls===2)throw Error('offline');return fixture();},s=>states.push(s));const pending=c.load('a','t1');await c.load('a','t1');expect(c.state.error).toBe('offline');first.resolve(fixture());await pending;expect(c.state.error).toBe('offline');await c.load('a','t1');expect(c.state.result?.turn.id).toBe('t1');expect(calls).toBe(3);});
 it('keeps unknown cost distinct from measured zero and refreshes running data',async()=>{let n=0;const c=new TurnDetailsController(async()=>fixture(++n===1?undefined:0),()=>{});await c.load('a','t1');expect(c.state.result?.diagnostics?.requests[0].costUsd).toBeUndefined();await c.load('a','t1');expect(c.state.result?.diagnostics?.requests[0].costUsd).toBe(0);});
});

import {requestCapacitySummary} from './turnDetails';
it('keeps estimated input and measured usage separate',()=>{
 const r={capacityWindow:32768,capacitySource:'runtime',inputEstimateTokens:1200,inputEstimateOrigin:'heuristic',reservedOutputTokens:8192,capacityMargin:1639,inputTokens:1000};
 expect(requestCapacitySummary(r)).toContain('32,768');expect(requestCapacitySummary(r)).toContain('1,200');expect(requestCapacitySummary({})).toBe('Capacity unavailable');
});
