import Foundation
import ServiceManagement
import AppKit

/// Registers the root helper (a launchd daemon inside the app bundle) and the
/// login item through SMAppService. Nothing is copied outside the app: the
/// daemon's plist lives in Contents/Library/LaunchDaemons and its program is
/// Contents/MacOS/tetherly-helper. launchd starts it on demand when a client
/// opens /var/run/tetherly.sock, and it exits again when idle.
enum HelperInstaller {
    static let plistName = "dev.miha.tetherly.helper.plist"

    static var daemon: SMAppService { SMAppService.daemon(plistName: plistName) }

    enum State: Equatable {
        case notInstalled, needsApproval, enabled, notFound, unknown
    }

    static var state: State {
        switch daemon.status {
        case .notRegistered: return .notInstalled
        case .requiresApproval: return .needsApproval
        case .enabled: return .enabled
        case .notFound: return .notFound
        @unknown default: return .unknown
        }
    }

    static func install() throws {
        try daemon.register()
    }

    static func uninstall() throws {
        try daemon.unregister()
    }

    static func openLoginItemsSettings() {
        SMAppService.openSystemSettingsLoginItems()
    }

    // MARK: launch at login

    static var launchAtLogin: Bool {
        get { SMAppService.mainApp.status == .enabled }
        set {
            do {
                if newValue { try SMAppService.mainApp.register() } else { try SMAppService.mainApp.unregister() }
            } catch {
                NSLog("Tetherly: launch at login: \(error.localizedDescription)")
            }
        }
    }

    // MARK: command-line tool

    static var helperBinary: URL {
        Bundle.main.bundleURL.appendingPathComponent("Contents/MacOS/tetherly-helper")
    }

    static var cliLink: URL {
        FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent(".local/bin/tetherly")
    }

    static var cliInstalled: Bool {
        (try? FileManager.default.destinationOfSymbolicLink(atPath: cliLink.path)) == helperBinary.path
    }

    /// Links ~/.local/bin/tetherly to the helper binary (a client when called as `tetherly`).
    static func installCLI() throws {
        let fm = FileManager.default
        try fm.createDirectory(at: cliLink.deletingLastPathComponent(), withIntermediateDirectories: true)
        if (try? fm.destinationOfSymbolicLink(atPath: cliLink.path)) != nil || fm.fileExists(atPath: cliLink.path) {
            try fm.removeItem(at: cliLink)
        }
        try fm.createSymbolicLink(at: cliLink, withDestinationURL: helperBinary)
    }
}
