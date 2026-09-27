// AppList.swift
//
// Lists candidate apps and resolves a HelperTarget to a running
// NSRunningApplication. Deliberately never calls .activate(options:) on any
// app: that's exactly the frontmost-stealing behavior this rewrite exists
// to avoid. Open (launching the app / navigating a URL) is still done by
// the Go side via `/usr/bin/open`, same as before — this helper only ever
// looks up apps that already exist.

import AppKit

enum AppList {
    /// Regular, user-facing apps only (skips background agents, menu-bar-only
    /// apps, and this helper's own host process where relevant).
    static func running() -> [NSRunningApplication] {
        NSWorkspace.shared.runningApplications.filter { $0.activationPolicy == .regular }
    }

    static func toHelperApp(_ app: NSRunningApplication) -> HelperApp {
        HelperApp(
            name: app.localizedName ?? "",
            bundle_id: app.bundleIdentifier,
            pid: Int(app.processIdentifier),
            path: app.bundleURL?.path
        )
    }

    /// Resolve a target the same way the Go side does when it lists apps to
    /// find one after `open`: prefer an exact PID (fastest, least ambiguous,
    /// and what every act/inspect call after the first uses), then
    /// bundle ID, then path, then case-insensitive name.
    static func resolve(_ target: HelperTarget) -> NSRunningApplication? {
        let apps = running()
        if let pid = target.pid, pid > 0 {
            if let match = apps.first(where: { Int($0.processIdentifier) == pid }) {
                return match
            }
        }
        if let bundleID = target.bundle_id, !bundleID.isEmpty {
            if let match = apps.first(where: { $0.bundleIdentifier == bundleID }) {
                return match
            }
        }
        if let path = target.path, !path.isEmpty {
            if let match = apps.first(where: { $0.bundleURL?.path == path }) {
                return match
            }
        }
        if let name = target.name, !name.isEmpty {
            if let match = apps.first(where: { ($0.localizedName ?? "").caseInsensitiveCompare(name) == .orderedSame }) {
                return match
            }
        }
        return nil
    }
}
