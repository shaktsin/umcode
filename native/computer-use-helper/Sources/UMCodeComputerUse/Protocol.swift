// Protocol.swift
//
// Every type here mirrors internal/computeruse's Go JSON contract exactly
// (field names, snake_case, optionality) — this helper is a drop-in
// replacement for the previous "UMCode Computer Use.app", invoked the same
// way: `<helper> <command>` with a JSON object on stdin and a JSON object
// (or nothing, for "act") on stdout. See internal/computeruse/driver_darwin.go
// on the Go side for the exact call sites. Do not rename these fields
// without updating that file too.

import Foundation

struct HelperApp: Codable {
    var name: String
    var bundle_id: String?
    var pid: Int?
    var path: String?
}

struct HelperWindow: Codable {
    var id: Int
    var title: String?
    var x: Double
    var y: Double
    var width: Double
    var height: Double
    var pixel_width: Int?
    var pixel_height: Int?
}

struct HelperTarget: Codable {
    var name: String?
    var bundle_id: String?
    var path: String?
    var url: String?
    var pid: Int?
}

struct HelperState: Codable {
    var app: HelperApp
    var window: HelperWindow
    var permission: String?
    var controls: [String]?
}

struct HelperAction: Codable {
    var type: String
    var x: Double?
    var y: Double?
    var text: String?
    var key: String?
    var delta: Int?
}

// ---- command payloads ----

struct ListOutput: Codable {
    var apps: [HelperApp]
}

struct InspectInput: Codable {
    var target: HelperTarget
    var screenshot: String
}

struct ActInput: Codable {
    var target: HelperTarget
    var action: HelperAction
}

enum HelperError: Error, CustomStringConvertible {
    case message(String)
    var description: String {
        switch self {
        case .message(let m): return m
        }
    }
}

func fail(_ message: String) -> Never {
    FileHandle.standardError.write((message + "\n").data(using: .utf8)!)
    exit(1)
}

func readStdin() -> Data {
    FileHandle.standardInput.readDataToEndOfFile()
}

func writeJSON<T: Encodable>(_ value: T) {
    let encoder = JSONEncoder()
    guard let data = try? encoder.encode(value) else {
        fail("failed to encode output")
    }
    FileHandle.standardOutput.write(data)
}
