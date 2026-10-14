import { expect, it } from 'vitest';
import { placePopup } from './popupGeometry';
it('keeps popups inside edges and flips near the bottom', () => {
  const p=placePopup({trigger:{left:700,top:480,width:50,height:28},viewport:{width:760,height:520},preferredWidth:320,headerHeight:32,kind:'menu',preferredSide:'below'});
  expect(p.left).toBeGreaterThanOrEqual(8);expect(p.left+p.width).toBeLessThanOrEqual(752);
  expect(p.top).toBeGreaterThanOrEqual(8);expect(p.side).toBe('above');expect(p.maxListHeight).toBeLessThanOrEqual(320);
});
it('bounds wide triggers and preserves short content proximity',()=>{
  const p=placePopup({trigger:{left:4,top:100,width:800,height:32},viewport:{width:760,height:520},preferredWidth:360,headerHeight:0,contentHeight:32,kind:'menu',preferredSide:'above'});
  expect(p.width).toBe(744);expect(p.left).toBe(8);expect(p.top).toBe(58);
});
