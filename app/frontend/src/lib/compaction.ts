import type {CompactionState} from './types';
export const compactionActive=(state:CompactionState|null):boolean=>!!state&&['preparing','summarizing','restoring'].includes(state.status);
export function applyCompactionUpdate(current:CompactionState|null,incoming:CompactionState):CompactionState {
 if(!current)return incoming;
 if(current.threadId!==incoming.threadId)return current;
 if(current.id===incoming.id) {
  if(incoming.sequence<=current.sequence)return current;
  if(!compactionActive(current)&&compactionActive(incoming))return current;
 } else if(Date.parse(incoming.startedAt)<=Date.parse(current.startedAt))return current;
 return incoming;
}
export function compactionLabel(state:CompactionState):string {
 return ({preparing:'Preparing working context',summarizing:'Summarizing earlier context',restoring:'Restoring working context'} as Record<string,string>)[state.status]??'';
}

export function formatCompactionTime(startedAt:string, nowMs:number):string {
 const start=Date.parse(startedAt);
 const seconds=Number.isFinite(start)&&Number.isFinite(nowMs)?Math.max(0,Math.floor((nowMs-start)/1000)):0;
 const hours=Math.floor(seconds/3600);
 const minutes=Math.floor((seconds%3600)/60);
 const remainder=seconds%60;
 const clock=`${String(minutes).padStart(2,'0')}:${String(remainder).padStart(2,'0')}`;
 return hours?`${String(hours).padStart(2,'0')}:${clock}`:clock;
}
