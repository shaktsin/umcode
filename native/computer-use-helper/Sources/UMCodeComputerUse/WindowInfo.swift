// WindowInfo.swift
//
// Finds the app's "main" on-screen window and reads its current bounds, via
// CGWindowListCopyWindowInfo. This is re-run on every single inspect/act
// call (the helper process is short-lived, invoked fresh each time — see
// driver_darwin.go), so a window the person dragged or resized between
// actions is picked up correctly rather than trusting stale coordinates.

import AppKit
import CoreGraphics

struct ResolvedWindow {
    var windowID: CGWindowID
    var title: String
    var bounds: CGRect // points, global screen coordinates (origin top-left)
}

enum WindowInfo {
    /// The frontmost-looking normal window owned by pid: layer 0 (regular
    /// app windows, not the menu bar or floating panels), largest area if
    /// several qualify (usually the main document/browser window, not a
    /// find bar or a detached inspector).
    static func mainWindow(forPID pid: pid_t) -> ResolvedWindow? {
        let options: CGWindowListOption = [.optionOnScreenOnly, .excludeDesktopElements]
        guard let list = CGWindowListCopyWindowInfo(options, kCGNullWindowID) as? [[String: AnyObject]] else {
            return nil
        }
        var best: ResolvedWindow?
        var bestArea: CGFloat = 0
        for entry in list {
            guard let ownerPID = entry[kCGWindowOwnerPID as String] as? pid_t, ownerPID == pid else { continue }
            guard let layer = entry[kCGWindowLayer as String] as? Int, layer == 0 else { continue }
            // CGRect(dictionaryRepresentation:) is the Apple-documented way
            // to decode kCGWindowBounds's CFDictionary encoding — safer
            // than casting through [String: CGFloat], whose NSNumber
            // bridging is easy to get subtly wrong.
            guard let boundsCF = entry[kCGWindowBounds as String] as? CFDictionary,
                  let bounds = CGRect(dictionaryRepresentation: boundsCF) else { continue }
            let area = bounds.width * bounds.height
            guard area > 1, area > bestArea else { continue }
            guard let windowNumber = entry[kCGWindowNumber as String] as? Int else { continue }
            let title = entry[kCGWindowName as String] as? String ?? ""
            best = ResolvedWindow(windowID: CGWindowID(windowNumber), title: title, bounds: bounds)
            bestArea = area
        }
        return best
    }
}
