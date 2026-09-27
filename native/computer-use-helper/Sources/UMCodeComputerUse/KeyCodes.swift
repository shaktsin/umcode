// KeyCodes.swift
//
// Apple's virtual keycodes (the ADB-derived codes CGEvent expects) for a
// US ANSI keyboard layout, hardcoded rather than imported from Carbon.
// These numbers are stable across Intel and Apple Silicon and have not
// changed in over a decade; see Apple's old
// <HIToolbox/Events.h> (kVK_* constants) for the canonical source if this
// table ever needs extending.

enum KeyCodes {
    static let table: [String: Int] = [
        "a": 0x00, "s": 0x01, "d": 0x02, "f": 0x03, "h": 0x04, "g": 0x05,
        "z": 0x06, "x": 0x07, "c": 0x08, "v": 0x09, "b": 0x0B, "q": 0x0C,
        "w": 0x0D, "e": 0x0E, "r": 0x0F, "y": 0x10, "t": 0x11,
        "1": 0x12, "2": 0x13, "3": 0x14, "4": 0x15, "6": 0x16, "5": 0x17,
        "equal": 0x18, "9": 0x19, "7": 0x1A, "minus": 0x1B, "8": 0x1C,
        "0": 0x1D, "rightbracket": 0x1E, "o": 0x1F, "u": 0x20,
        "leftbracket": 0x21, "i": 0x22, "p": 0x23, "l": 0x25, "j": 0x26,
        "quote": 0x27, "k": 0x28, "semicolon": 0x29, "backslash": 0x2A,
        "comma": 0x2B, "slash": 0x2C, "n": 0x2D, "m": 0x2E, "period": 0x2F,
        "tab": 0x30, "space": 0x31, "backtick": 0x32, "backspace": 0x33,
        "delete": 0x33, "escape": 0x35, "esc": 0x35,
        "return": 0x24, "enter": 0x24,
        "leftarrow": 0x7B, "arrowleft": 0x7B, "left": 0x7B,
        "rightarrow": 0x7C, "arrowright": 0x7C, "right": 0x7C,
        "downarrow": 0x7D, "arrowdown": 0x7D, "down": 0x7D,
        "uparrow": 0x7E, "arrowup": 0x7E, "up": 0x7E,
        "forwarddelete": 0x75, "pagedown": 0x79, "pageup": 0x74,
        "home": 0x73, "end": 0x77,
        "f1": 0x7A, "f2": 0x78, "f3": 0x63, "f4": 0x76, "f5": 0x60,
        "f6": 0x61, "f7": 0x62, "f8": 0x64, "f9": 0x65, "f10": 0x6D,
        "f11": 0x67, "f12": 0x6F,
    ]

    static func lookup(_ name: String) -> Int? {
        table[name.lowercased()]
    }
}
