// Screenshot.swift
//
// Captures a single window's current pixels without needing it frontmost,
// active, or even unoccluded, via CGWindowListCreateImage keyed to that
// window's CGWindowID. This is the piece that makes the whole "doesn't
// need to front or focus automatically" property possible: the previous
// helper's screenshot step is the most likely reason it needed the window
// on top of everything else.
//
// CGWindowListCreateImage is deprecated (in favor of ScreenCaptureKit) as
// of macOS 14, but still functional as of macOS 15/16 at the time this was
// written, and its synchronous, no-entitlement-beyond-Screen-Recording
// shape is a much smaller surface to get right without being able to test
// on real hardware. A future revision could move to
// SCScreenshotManager.captureImage(contentFilter:configuration:), which is
// the modern non-deprecated equivalent and behaves the same way
// (background-window capture, no focus needed) but requires ScreenCaptureKit's
// async APIs and macOS 14+.

import AppKit
import CoreGraphics
import UniformTypeIdentifiers

enum ScreenshotError: Error, CustomStringConvertible {
    case captureFailed
    case writeFailed(String)
    var description: String {
        switch self {
        case .captureFailed: return "could not capture the window (Screen Recording permission may be missing, or the window is minimized)"
        case .writeFailed(let m): return "could not write the screenshot: \(m)"
        }
    }
}

enum Screenshot {
    /// Captures `window` and writes it as a PNG to `path`. Returns the
    /// image's pixel dimensions (which can be a Retina multiple of
    /// window.bounds' point dimensions) for the pixel_width/pixel_height
    /// fields the Go side uses to rescale model-given click coordinates.
    @discardableResult
    static func capture(window: ResolvedWindow, to path: String) throws -> (width: Int, height: Int) {
        let imageOptions: CGWindowImageOption = [.boundsIgnoreFraming, .bestResolution]
        guard let image = CGWindowListCreateImage(.null, .optionIncludingWindow, window.windowID, imageOptions) else {
            throw ScreenshotError.captureFailed
        }
        guard let destination = CGImageDestinationCreateWithURL(
            URL(fileURLWithPath: path) as CFURL, UTType.png.identifier as CFString, 1, nil
        ) else {
            throw ScreenshotError.writeFailed("could not open destination")
        }
        CGImageDestinationAddImage(destination, image, nil)
        guard CGImageDestinationFinalize(destination) else {
            throw ScreenshotError.writeFailed("CGImageDestinationFinalize failed")
        }
        return (image.width, image.height)
    }
}
