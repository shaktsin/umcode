import { PanOnScrollMode, XYPanZoom } from '@xyflow/system';
export default function zoom(domNode, params) {
    const { minZoom, maxZoom, initialViewport, onPanZoomStart, onPanZoom, onPanZoomEnd, translateExtent, setPanZoomInstance, onDraggingChange, onTransformChange } = params;
    const panZoomInstance = XYPanZoom({
        domNode,
        minZoom,
        maxZoom,
        translateExtent,
        viewport: initialViewport,
        onPanZoom,
        onPanZoomStart,
        onPanZoomEnd,
        onDraggingChange
    });
    const viewport = panZoomInstance.getViewport();
    if (initialViewport.x !== viewport.x ||
        initialViewport.y !== viewport.y ||
        initialViewport.zoom !== viewport.zoom) {
        onTransformChange([viewport.x, viewport.y, viewport.zoom]);
    }
    setPanZoomInstance(panZoomInstance);
    panZoomInstance.update(params);
    return {
        update(params) {
            panZoomInstance.update(params);
        }
    };
}
