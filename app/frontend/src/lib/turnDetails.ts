export interface TurnDetailsResult {
  turn: { id: string; status: string; selection: { provider?: string; model?: string }; resolved: { provider?: string; model?: string }; startedAt: string; finishedAt?: string; usage: { inputTokens: number; cachedInputTokens: number; outputTokens: number; reasoningTokens: number; costUsd: number; requests: number; estimated?: boolean }; routeTrail?: Array<{ provider: string; model: string; status?: string }> };
  diagnostics: null | { version: number; complete: boolean; policy: Record<string, boolean | string>; requests: Array<{ ordinal: number; phase: string; provider: string; model: string; inputTokens?: number; cachedInputTokens?: number; outputTokens?: number; reasoningTokens?: number; costUsd?: number; costState: string; measurementOrigin: string; selectedSources?: Array<{ id: string; kind: string }>; submittedSources?: Array<{ id: string; kind: string }>; toolNames?: string[]; toolCalls?: Record<string, number>; estimates?: Record<string, number>; capacityWindow?:number;capacitySource?:string;inputEstimateTokens?:number;inputEstimateOrigin?:string;reservedOutputTokens?:number;capacityMargin?:number;calibrationFactor?:number;contextReductions?:number;retryCause?:string;interruptionKind?:string;attempted?:boolean;stopReason?: string; fallback?: string }> };
  availability: 'recorded' | 'partial' | 'not_recorded';
}
export interface TurnDetailsState { busy: boolean; result?: TurnDetailsResult; error: string }
export type TurnDetailsRequest = (threadId: string, turnId: string) => Promise<TurnDetailsResult>;
export class TurnDetailsController {
  state: TurnDetailsState = {busy:false,error:''};
  private seq=0;
  constructor(private request:TurnDetailsRequest,private publish:(state:TurnDetailsState)=>void) {}
  async load(threadId:string,turnId:string) {
    const seq=++this.seq;this.state={busy:true,error:''};this.publish(this.state);
    try {const result=await this.request(threadId,turnId);if(seq!==this.seq)return;this.state={busy:false,result,error:''};this.publish(this.state);}
    catch(e) {if(seq!==this.seq)return;this.state={busy:false,error:e instanceof Error?e.message:String(e)};this.publish(this.state);}
  }
  invalidate(){this.seq++;}
}

export function requestCapacitySummary(r:{capacityWindow?:number;capacitySource?:string;inputEstimateTokens?:number;inputEstimateOrigin?:string;reservedOutputTokens?:number;capacityMargin?:number}):string {
 if(typeof r.capacityWindow!=='number'||r.capacityWindow<=0)return 'Capacity unavailable';
 const count=(n?:number)=>typeof n==='number'?n.toLocaleString('en-US'):'unavailable';
 return `Window ${count(r.capacityWindow)} (${r.capacitySource||'unknown source'}) · Estimated input ${count(r.inputEstimateTokens)} (${r.inputEstimateOrigin||'unknown origin'}) · Response reserved ${count(r.reservedOutputTokens)} · Headroom ${count(r.capacityMargin)}`;
}
