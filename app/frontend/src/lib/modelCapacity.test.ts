import { expect,it } from 'vitest';
import { capacityFields } from './modelCapacity';
it('omits empty capacity overrides rather than persisting zero',()=>{
 expect(capacityFields(undefined,undefined,undefined)).toEqual({});
 expect(capacityFields(32768,8192,4096)).toEqual({contextWindow:32768,maxOutputTokens:8192,responseReserveTokens:4096});
});
it('rejects nonpositive or fractional capacity',()=>{
 for(const value of [0,-1,1.5,NaN,Infinity]) expect(()=>capacityFields(value,undefined,undefined)).toThrow();
});
