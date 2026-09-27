// main.swift
//
// Entry point. Invoked as `<binary> <command>` with a JSON object on
// stdin (see driver_darwin.go's helperDriver.invoke) — this file only
// dispatches; the real work is in AppList/WindowInfo/Screenshot/Input.
//
// Commands: list, inspect, act. (Go's Open/"computer.start" goes through
// /usr/bin/open directly and never reaches this binary — see
// driver_darwin.go's helperDriver.Open.)

import AppKit
import ApplicationServices
import Foundation

func permissionSummary() -> String {
    var missing: [String] = []
    if !AXIsProcessTrusted() {
        missing.append("Accessibility")
    }
    if #available(macOS 11.0, *) {
        if !CGPreflightScreenCaptureAccess() {
            missing.append("Screen Recording")
        }
    }
    return missing.isEmpty ? "ready" : "missing: " + missing.joined(separator: ", ")
}

func resolveWindowed(_ target: HelperTarget) -> (app: NSRunningApplication, window: ResolvedWindow)? {
    guard let app = AppList.resolve(target) else { return nil }
    guard let window = WindowInfo.mainWindow(forPID: app.processIdentifier) else { return nil }
    return (app, window)
}

func runList() {
    let apps = AppList.running().map(AppList.toHelperApp)
    writeJSON(ListOutput(apps: apps))
}

func runInspect() {
    let input: InspectInput
    do {
        input = try JSONDecoder().decode(InspectInput.self, from: readStdin())
    } catch {
        fail("invalid inspect input: \(error)")
    }
    guard let resolved = resolveWindowed(input.target) else {
        fail("could not find a window for the requested app; it may have quit or have no visible window")
    }
    let (app, window) = resolved
    let pixelSize: (width: Int, height: Int)
    do {
        pixelSize = try Screenshot.capture(window: window, to: input.screenshot)
    } catch {
        fail("\(error)")
    }
    let state = HelperState(
        app: AppList.toHelperApp(app),
        window: HelperWindow(
            id: Int(window.windowID), title: window.title.isEmpty ? nil : window.title,
            x: window.bounds.origin.x, y: window.bounds.origin.y,
            width: window.bounds.width, height: window.bounds.height,
            pixel_width: pixelSize.width, pixel_height: pixelSize.height
        ),
        permission: permissionSummary(),
        controls: nil
    )
    writeJSON(state)
}

func runAct() {
    let input: ActInput
    do {
        input = try JSONDecoder().decode(ActInput.self, from: readStdin())
    } catch {
        fail("invalid act input: \(error)")
    }
    guard let resolved = resolveWindowed(input.target) else {
        fail("could not find a window for the requested app; it may have quit or have no visible window")
    }
    do {
        try Input.perform(input.action, pid: resolved.app.processIdentifier, window: resolved.window)
    } catch {
        fail("\(error)")
    }
    // No output on success, matching the Go side's Act, which discards it.
}

let arguments = CommandLine.arguments
guard arguments.count >= 2 else {
    fail("usage: \(arguments.first ?? "UMCodeComputerUse") <list|inspect|act>")
}

switch arguments[1] {
case "list":
    runList()
case "inspect":
    runInspect()
case "act":
    runAct()
default:
    fail("unknown command \"\(arguments[1])\"")
}
