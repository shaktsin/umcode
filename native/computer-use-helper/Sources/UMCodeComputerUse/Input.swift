// Input.swift
//
// Synthesizes clicks, typing, key presses and scrolling and delivers them
// with CGEventPostToPid rather than CGEventPost(.cghidEventTap, ...): the
// former hands the event straight to the target process's event queue
// without moving the real pointer or requiring the app to be frontmost or
// even key. This is the same technique open-source prior art for this
// problem uses (see computer-harness and blade-computer-use, referenced in
// this package's README) — Apple's own documented, non-private API for
// posting input to a specific process.
//
// A "fill" first tries the Accessibility API's direct value-set
// (AXUIElementSetAttributeValue on the element under the point), which is
// even less disruptive than a synthetic click-and-type when it works
// (common AppKit text fields support it); it falls back to a synthetic
// click + select-all + type when the element doesn't support it (custom
// or web-rendered controls).

import ApplicationServices
import Cocoa
import CoreGraphics

enum InputError: Error, CustomStringConvertible {
    case unsupportedAction(String)
    case missingCoordinates
    case missingText
    case missingKey
    var description: String {
        switch self {
        case .unsupportedAction(let t): return "unsupported computer action \"\(t)\""
        case .missingCoordinates: return "this action requires coordinates"
        case .missingText: return "this action requires text"
        case .missingKey: return "this action requires a key name"
        }
    }
}

enum Input {
    static func perform(_ action: HelperAction, pid: pid_t, window: ResolvedWindow) throws {
        switch action.type {
        case "click":
            guard let x = action.x, let y = action.y else { throw InputError.missingCoordinates }
            postClick(at: absolutePoint(x: x, y: y, window: window), pid: pid, clickCount: 1)
        case "double_click":
            guard let x = action.x, let y = action.y else { throw InputError.missingCoordinates }
            postClick(at: absolutePoint(x: x, y: y, window: window), pid: pid, clickCount: 2)
        case "fill":
            guard let x = action.x, let y = action.y else { throw InputError.missingCoordinates }
            guard let text = action.text, !text.isEmpty else { throw InputError.missingText }
            let point = absolutePoint(x: x, y: y, window: window)
            if AXFill.trySetValue(atScreenPoint: point, text: text) {
                return
            }
            // Fall back: click into the field, select all, then type over it.
            postClick(at: point, pid: pid, clickCount: 1)
            usleep(60_000)
            postKeyCombo(virtualKey: KeyCodes.table["a"]!, command: true, pid: pid)
            usleep(30_000)
            postUnicodeText(text, pid: pid)
        case "type":
            guard let text = action.text, !text.isEmpty else { throw InputError.missingText }
            postUnicodeText(text, pid: pid)
        case "key":
            guard let key = action.key, !key.isEmpty else { throw InputError.missingKey }
            try postNamedKey(key, pid: pid)
        case "scroll":
            guard let delta = action.delta, delta != 0 else { throw InputError.unsupportedAction("scroll") }
            postScroll(delta: delta, at: CGPoint(x: window.bounds.midX, y: window.bounds.midY), pid: pid)
        default:
            throw InputError.unsupportedAction(action.type)
        }
    }

    /// action.x/y arrive already rescaled by the Go side into the window's
    /// own point space (see internal/computeruse/manager.go's Act); this
    /// just adds the window's current on-screen origin.
    private static func absolutePoint(x: Double, y: Double, window: ResolvedWindow) -> CGPoint {
        CGPoint(x: window.bounds.origin.x + x, y: window.bounds.origin.y + y)
    }

    private static func postClick(at point: CGPoint, pid: pid_t, clickCount: Int64) {
        let source = CGEventSource(stateID: .hidSystemState)
        for (type, button) in [(CGEventType.leftMouseDown, CGMouseButton.left), (CGEventType.leftMouseUp, CGMouseButton.left)] {
            guard let event = CGEvent(mouseEventSource: source, mouseType: type, mouseCursorPosition: point, mouseButton: button) else { continue }
            event.setIntegerValueField(.mouseEventClickState, value: clickCount)
            event.postToPid(pid)
        }
    }

    private static func postUnicodeText(_ text: String, pid: pid_t) {
        let source = CGEventSource(stateID: .hidSystemState)
        let utf16 = Array(text.utf16)
        // Large pastes: CGEventKeyboardSetUnicodeString has an internal
        // buffer limit around 20 UTF-16 units per event; chunk to be safe.
        for chunk in stride(from: 0, to: utf16.count, by: 20).map({ Array(utf16[$0..<min($0 + 20, utf16.count)]) }) {
            guard let down = CGEvent(keyboardEventSource: source, virtualKey: 0, keyDown: true) else { continue }
            guard let up = CGEvent(keyboardEventSource: source, virtualKey: 0, keyDown: false) else { continue }
            down.keyboardSetUnicodeString(stringLength: chunk.count, unicodeString: chunk)
            up.keyboardSetUnicodeString(stringLength: chunk.count, unicodeString: chunk)
            down.postToPid(pid)
            up.postToPid(pid)
        }
    }

    private static func postKeyCombo(virtualKey: Int, command: Bool = false, shift: Bool = false, option: Bool = false, control: Bool = false, pid: pid_t) {
        let source = CGEventSource(stateID: .hidSystemState)
        var flags: CGEventFlags = []
        if command { flags.insert(.maskCommand) }
        if shift { flags.insert(.maskShift) }
        if option { flags.insert(.maskAlternate) }
        if control { flags.insert(.maskControl) }
        guard let down = CGEvent(keyboardEventSource: source, virtualKey: CGKeyCode(virtualKey), keyDown: true) else { return }
        guard let up = CGEvent(keyboardEventSource: source, virtualKey: CGKeyCode(virtualKey), keyDown: false) else { return }
        down.flags = flags
        up.flags = flags
        down.postToPid(pid)
        up.postToPid(pid)
    }

    /// key accepts a plain name ("Return", "Tab", "ArrowLeft", "a") or a
    /// "+"-joined combo ("Cmd+A", "Cmd+Shift+Z", case-insensitive).
    private static func postNamedKey(_ raw: String, pid: pid_t) throws {
        let parts = raw.split(separator: "+").map { $0.trimmingCharacters(in: .whitespaces) }
        guard let last = parts.last, let code = KeyCodes.lookup(last) else {
            throw InputError.unsupportedAction("key:\(raw)")
        }
        var command = false, shift = false, option = false, control = false
        for modifier in parts.dropLast() {
            switch modifier.lowercased() {
            case "cmd", "command", "meta": command = true
            case "shift": shift = true
            case "alt", "option": option = true
            case "ctrl", "control": control = true
            default: break
            }
        }
        postKeyCombo(virtualKey: code, command: command, shift: shift, option: option, control: control, pid: pid)
    }

    private static func postScroll(delta: Int, at point: CGPoint, pid: pid_t) {
        let source = CGEventSource(stateID: .hidSystemState)
        guard let event = CGEvent(scrollWheelEvent2Source: source, units: .pixel, wheelCount: 1, wheel1: Int32(delta), wheel2: 0, wheel3: 0) else { return }
        event.location = point
        event.postToPid(pid)
    }
}

/// Best-effort direct value-set through the Accessibility API, tried before
/// falling back to a synthetic click + type. Requires the Accessibility
/// permission the rest of Computer Use already needs.
enum AXFill {
    static func trySetValue(atScreenPoint point: CGPoint, text: String) -> Bool {
        let systemWide = AXUIElementCreateSystemWide()
        var element: AXUIElement?
        let hitResult = AXUIElementCopyElementAtPosition(systemWide, Float(point.x), Float(point.y), &element)
        guard hitResult == .success, let target = element else { return false }
        let result = AXUIElementSetAttributeValue(target, kAXValueAttribute as CFString, text as CFTypeRef)
        return result == .success
    }
}
