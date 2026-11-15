export type FilterParams = {
    panActivationKeyPressed: boolean;
    zoomActivationKeyPressed: boolean;
    zoomOnScroll: boolean;
    zoomOnPinch: boolean;
    panOnDrag: boolean | number[];
    panOnScroll: boolean;
    zoomOnDoubleClick: boolean;
    userSelectionActive: boolean;
    noWheelClassName: string;
    noPanClassName: string;
    lib: string;
    connectionInProgress: boolean;
};
export declare function createFilter({ panActivationKeyPressed, zoomActivationKeyPressed, zoomOnScroll, zoomOnPinch, panOnDrag, panOnScroll, zoomOnDoubleClick, userSelectionActive, noWheelClassName, noPanClassName, lib, connectionInProgress, }: FilterParams): (event: any) => boolean;
//# sourceMappingURL=filter.d.ts.map