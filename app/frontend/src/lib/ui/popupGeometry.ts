export type Rect={left:number;top:number;width:number;height:number};
export type PopupInput={trigger:Rect;viewport:{width:number;height:number};preferredWidth:number;headerHeight:number;contentHeight?:number;kind:'menu'|'skills';preferredSide:'above'|'below'};
export type PopupGeometry={left:number;top:number;width:number;maxListHeight:number;side:'above'|'below'};
export function placePopup(input:PopupInput):PopupGeometry {
  const {trigger,viewport,headerHeight}=input;
  const width=Math.max(0,Math.min(viewport.width-16,Math.max(trigger.width,input.kind==='skills'?Math.min(768,input.preferredWidth):Math.min(360,Math.max(200,input.preferredWidth)))));
  const above=Math.max(0,trigger.top-16),below=Math.max(0,viewport.height-trigger.top-trigger.height-16);
  const desired=Math.min(320,input.contentHeight??320)+headerHeight+2;
  let side=input.preferredSide;
  const preferred=side==='above'?above:below,other=side==='above'?below:above;
  if(preferred<desired&&other>preferred)side=side==='above'?'below':'above';
  const available=side==='above'?above:below;
  const maxListHeight=Math.max(0,Math.min(320,available-headerHeight-2));
  const height=Math.min(input.contentHeight??320,maxListHeight)+headerHeight+2;
  return {left:Math.max(8,Math.min(trigger.left,viewport.width-width-8)),top:side==='above'?Math.max(8,trigger.top-8-height):trigger.top+trigger.height+8,width,maxListHeight,side};
}
