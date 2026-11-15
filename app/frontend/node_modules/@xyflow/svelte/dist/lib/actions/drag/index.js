import { XYDrag } from '@xyflow/system';
export default function drag(domNode, params) {
    const { store, onDrag, onDragStart, onDragStop, onNodeMouseDown } = params;
    const dragInstance = XYDrag({
        onDrag,
        onDragStart,
        onDragStop,
        onNodeMouseDown,
        getStoreItems: () => {
            const { snapGrid, viewport } = store;
            return {
                nodes: store.nodes,
                nodeLookup: store.nodeLookup,
                edges: store.edges,
                nodeExtent: store.nodeExtent,
                snapGrid: snapGrid ? snapGrid : [0, 0],
                snapToGrid: !!snapGrid,
                nodeOrigin: store.nodeOrigin,
                multiSelectionActive: store.multiselectionKeyPressed,
                domNode: store.domNode,
                transform: [viewport.x, viewport.y, viewport.zoom],
                autoPanOnNodeDrag: store.autoPanOnNodeDrag,
                nodesDraggable: store.nodesDraggable,
                selectNodesOnDrag: store.selectNodesOnDrag,
                nodeDragThreshold: store.nodeDragThreshold,
                unselectNodesAndEdges: store.unselectNodesAndEdges,
                updateNodePositions: store.updateNodePositions,
                onSelectionDrag: store.onselectiondrag,
                onSelectionDragStart: store.onselectiondragstart,
                onSelectionDragStop: store.onselectiondragstop,
                panBy: store.panBy
            };
        }
    });
    function updateDrag(domNode, params) {
        if (params.disabled) {
            dragInstance.destroy();
            return;
        }
        dragInstance.update({
            domNode,
            noDragClassName: params.noDragClass,
            handleSelector: params.handleSelector,
            nodeId: params.nodeId,
            isSelectable: params.isSelectable,
            nodeClickDistance: params.nodeClickDistance
        });
    }
    updateDrag(domNode, params);
    return {
        update(params) {
            updateDrag(domNode, params);
        },
        destroy() {
            dragInstance.destroy();
        }
    };
}
