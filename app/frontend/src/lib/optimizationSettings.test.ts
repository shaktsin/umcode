import { describe, it, expect } from 'vitest';
import { OptimizationSettings, type OptimizationResult } from './optimizationSettings';
const result = (enabled: boolean): OptimizationResult => ({enabled, source:'stored', legacyMixed:false});
function deferred<T>() { let resolve!: (value:T)=>void; let reject!: (error:Error)=>void; const promise=new Promise<T>((a,b)=>{resolve=a;reject=b});return {promise,resolve,reject}; }
describe('global optimization settings',()=>{
 it('loads legacy state and replaces it with one saved bundle state',async()=>{
  const requests:unknown[]=[];
  const c=new OptimizationSettings(async(method,params)=>{requests.push([method,params]);return method.endsWith('/get')?{enabled:false,source:'legacy',legacyMixed:true}:result(true)},()=>{});
  await c.load();expect(c.view.result?.legacyMixed).toBe(true);
  await c.save(true);expect(c.view.result).toEqual(result(true));expect(requests[1]).toEqual(['settings/tokenOptimization/set',{enabled:true}]);
 });
 it('retains saved state on failure and prevents overlapping saves',async()=>{
  const pending=deferred<OptimizationResult>();let writes=0;
  const c=new OptimizationSettings(async(method)=>{if(method.endsWith('/get'))return result(false);writes++;return pending.promise},()=>{});
  await c.load();const saving=c.save(true);await c.save(false);expect(writes).toBe(1);expect(c.view.result?.enabled).toBe(false);
  pending.reject(new Error('disk unavailable'));await saving;expect(c.view.result?.enabled).toBe(false);expect(c.view.error).toBe('disk unavailable');expect(c.view.busy).toBe(false);
 });
 it('discards an older load response after a confirmed save',async()=>{
  const pending=deferred<OptimizationResult>();let reads=0;
  const c=new OptimizationSettings(async(method)=>method.endsWith('/set')?result(true):++reads===1?result(false):pending.promise,()=>{});
  await c.load();const loading=c.load();await c.save(true);pending.resolve(result(false));await loading;expect(c.view.result?.enabled).toBe(true);
 });
 it('surfaces initial load failures without inventing a saved state',async()=>{
  const c=new OptimizationSettings(async()=>{throw new Error('database unavailable')},()=>{});await c.load();expect(c.view.result).toBeUndefined();expect(c.view.error).toBe('database unavailable');
 });
});
