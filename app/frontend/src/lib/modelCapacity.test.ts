import { expect,it } from 'vitest';
import { capacityFields, describeCapacity, capacityIsAssumed, assumedCapacityNotice, DEFAULT_CONTEXT_WINDOW } from './modelCapacity';
it('omits empty capacity overrides rather than persisting zero',()=>{
 expect(capacityFields(undefined,undefined,undefined)).toEqual({});
 expect(capacityFields(32768,8192,4096)).toEqual({contextWindow:32768,maxOutputTokens:8192,responseReserveTokens:4096});
});
it('rejects nonpositive or fractional capacity',()=>{
 for(const value of [0,-1,1.5,NaN,Infinity]) expect(()=>capacityFields(value,undefined,undefined)).toThrow();
});
it('describes each capacity source in plain language',()=>{
 expect(describeCapacity(null)).toBe('Context window: detected automatically on first use');
 expect(describeCapacity({effectiveContextWindow:32768,source:'runtime'})).toBe('Context window: 32,768 tokens (detected from the server)');
 expect(describeCapacity({effectiveContextWindow:32768,source:'observed'})).toContain('learned automatically');
 expect(describeCapacity({effectiveContextWindow:16384,source:'override'})).toContain('set by you');
 expect(describeCapacity({effectiveContextWindow:256000,source:'default'})).toContain('assumed until the server reports a limit');
 expect(describeCapacity({effectiveContextWindow:8192,source:'something-new'})).toBe('Context window: 8,192 tokens');
});
it('flags only an assumed window for the chat warning',()=>{
 expect(capacityIsAssumed({source:'default'})).toBe(true);
 for(const source of ['runtime','observed','override','catalog','runtime+override']) expect(capacityIsAssumed({source})).toBe(false);
 expect(capacityIsAssumed(null)).toBe(false);
 expect(DEFAULT_CONTEXT_WINDOW).toBe(256000);
 expect(assumedCapacityNotice('qwen3-coder')).toContain('256,000 tokens');
});
