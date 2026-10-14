export type Auxiliary='side'|'inspector';
export type ShellInput={width:number;height:number;railCollapsedPreference:boolean;sideRequested:boolean;inspectorRequested:boolean;sidePreferredWidth:number;inspectorPreferredWidth:number;activeAuxiliary:Auxiliary|null};
export type ShellLayout={railWidth:64|280;mainWidth:number;inline:{side:number|null;inspector:number|null};overlay:{panel:Auxiliary;width:number}|null};
export function resolveShellLayout(input:ShellInput):ShellLayout{
  const railWidth=input.width<960||input.railCollapsedPreference?64:280;
  const content=input.width-railWidth;
  const preferred={side:Math.min(720,Math.max(280,input.sidePreferredWidth)),inspector:Math.min(720,Math.max(280,input.inspectorPreferredWidth))};
  const requested={side:input.sideRequested,inspector:input.inspectorRequested};
  const active=input.activeAuxiliary&&requested[input.activeAuxiliary]?input.activeAuxiliary:null;
  const order:Auxiliary[]=active?[active,active==='side'?'inspector':'side']:['inspector','side'];
  const inline:ShellLayout['inline']={side:null,inspector:null};
  let remaining=content;
  // At wide sizes keep both requested widths; otherwise give the active panel priority.
  if(requested.side&&requested.inspector&&preferred.side+preferred.inspector+2+440<=content){inline.side=preferred.side;inline.inspector=preferred.inspector;remaining-=preferred.side+preferred.inspector+2;}
  else for(const panel of order){if(requested[panel]&&remaining-441>=280){inline[panel]=Math.min(preferred[panel],remaining-441);remaining-=inline[panel]!+1;}}
  return {railWidth,mainWidth:remaining,inline,overlay:active&&inline[active]===null?{panel:active,width:Math.min(preferred[active],content-16)}:null};
}
