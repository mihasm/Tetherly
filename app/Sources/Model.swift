import Foundation
import SwiftUI
import UserNotifications

@MainActor
final class Model: ObservableObject {
    @Published var status: TetherStatus?
    @Published var config: TetherConfig?
    @Published var helperState: HelperInstaller.State = .unknown
    @Published var helperError: String?
    @Published var actionError: String?
    @Published var busy = false
    @Published var logs: [LogLine] = []
    @Published var rxRate: Double = 0
    @Published var txRate: Double = 0
    @Published var panelOpen = false {
        didSet { if panelOpen { refreshNow() } }
    }

    @AppStorage("notifications") var notificationsOn = true

    private var timer: Timer?
    private var lastSample: (Date, UInt64, UInt64)?
    private var lastLog: Int64 = 0
    private let queue = DispatchQueue(label: "dev.miha.tetherly.client")

    init() {
        helperState = HelperInstaller.state
        schedule()
        refreshNow()
        if notificationsOn {
            UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound]) { _, _ in }
        }
    }

    private func schedule() {
        timer?.invalidate()
        timer = Timer.scheduledTimer(withTimeInterval: 1, repeats: true) { [weak self] _ in
            Task { @MainActor in self?.tick() }
        }
    }

    private var ticks = 0
    private func tick() {
        ticks += 1
        // fast while the panel is open or something is happening, otherwise every 3 s
        let active = panelOpen || status?.state == "connecting" || status?.state == "connected"
        if active || ticks % 3 == 0 { refreshNow() }
    }

    func refreshNow() {
        helperState = HelperInstaller.state
        queue.async {
            let result = Result { try HelperClient.call("status", timeout: 2) }
            Task { @MainActor in self.apply(result) }
        }
    }

    private func apply(_ result: Result<Response, Error>) {
        switch result {
        case .success(let r):
            helperError = nil
            if let s = r.status { update(s) }
            if config == nil { loadConfig() }
        case .failure(let e):
            helperError = e.localizedDescription
            if status != nil { update(nil) }
        }
    }

    private func update(_ new: TetherStatus?) {
        let old = status
        status = new
        if let new, new.state == "connected" {
            let now = Date()
            if let (t, rx, tx) = lastSample, now.timeIntervalSince(t) > 0.5, new.rx_bytes >= rx, new.tx_bytes >= tx {
                let dt = now.timeIntervalSince(t)
                rxRate = Double(new.rx_bytes - rx) / dt
                txRate = Double(new.tx_bytes - tx) / dt
            }
            lastSample = (now, new.rx_bytes, new.tx_bytes)
        } else {
            lastSample = nil
            rxRate = 0
            txRate = 0
        }
        notifyTransition(from: old, to: new)
    }

    // MARK: actions

    func connect() { run("connect") }
    func disconnect() { run("disconnect") }

    private func run(_ cmd: String) {
        busy = true
        actionError = nil
        queue.async {
            let result = Result { try HelperClient.call(cmd, timeout: 25) }
            Task { @MainActor in
                self.busy = false
                switch result {
                case .success(let r):
                    if !r.ok { self.actionError = r.error }
                    if let s = r.status { self.update(s) }
                case .failure(let e):
                    self.actionError = e.localizedDescription
                }
            }
        }
    }

    func loadConfig() {
        queue.async {
            let r = try? HelperClient.call("get_config")
            Task { @MainActor in if let c = r?.config { self.config = c } }
        }
    }

    func save(_ c: TetherConfig) {
        config = c
        queue.async {
            let result = Result { try HelperClient.call("set_config", config: c) }
            Task { @MainActor in
                switch result {
                case .success(let r):
                    if let c = r.config { self.config = c }
                    if !r.ok { self.actionError = r.error }
                case .failure(let e):
                    self.actionError = e.localizedDescription
                }
                self.refreshNow()
            }
        }
    }

    func setAutoConnect(_ on: Bool) {
        guard var c = config else { return }
        c.auto_connect = on
        save(c)
    }

    // MARK: helper install

    func installHelper() {
        do {
            try HelperInstaller.install()
            helperError = nil
        } catch {
            helperError = error.localizedDescription
        }
        helperState = HelperInstaller.state
        if helperState == .needsApproval { HelperInstaller.openLoginItemsSettings() }
        DispatchQueue.main.asyncAfter(deadline: .now() + 1) { self.refreshNow() }
    }

    func uninstallHelper() {
        do { try HelperInstaller.uninstall() } catch { actionError = error.localizedDescription }
        helperState = HelperInstaller.state
        refreshNow()
    }

    // MARK: logs

    func fetchLogs() {
        let since = lastLog
        queue.async {
            let r = try? HelperClient.call("logs", since: since)
            Task { @MainActor in
                guard let lines = r?.logs, !lines.isEmpty else { return }
                self.logs.append(contentsOf: lines)
                if self.logs.count > 3000 { self.logs.removeFirst(self.logs.count - 3000) }
                self.lastLog = lines.last!.n
            }
        }
    }

    func clearLogView() { logs.removeAll() }

    // MARK: notifications

    private func notifyTransition(from old: TetherStatus?, to new: TetherStatus?) {
        guard notificationsOn, let old else { return }
        let o = old.state, n = new?.state ?? "helper_gone"
        guard o != n else { return }
        var title: String?, body = ""
        switch (o, n) {
        case (_, "connected"):
            title = "Tethering connected"
            body = [new?.phone?.product, new?.ip.map { "IP \($0)" }].compactMap { $0 }.joined(separator: " - ")
        case ("connected", "error"), ("connecting", "error"):
            title = "Tethering failed"
            body = new?.error ?? ""
        case ("connected", _):
            title = "Tethering disconnected"
            body = n == "no_phone" ? "The phone was unplugged." : (n == "phone_found" ? "USB tethering was switched off on the phone." : "")
        case (_, "tethering_available") where !(new?.auto_connect ?? false):
            title = "Phone ready for USB tethering"
            body = "Click Connect in the menu bar."
        default:
            break
        }
        guard let title else { return }
        let content = UNMutableNotificationContent()
        content.title = title
        content.body = body
        UNUserNotificationCenter.current().add(UNNotificationRequest(identifier: UUID().uuidString, content: content, trigger: nil))
    }
}

// MARK: presentation helpers

extension TetherStatus {
    var title: String {
        switch state {
        case "no_phone": return "No phone connected"
        case "phone_found": return "USB tethering is off"
        case "tethering_available": return "Ready to connect"
        case "connecting": return "Connecting"
        case "connected": return "Connected"
        case "disconnecting": return "Disconnecting"
        case "error": return "Connection problem"
        default: return state
        }
    }

    var phoneName: String? {
        guard let p = phone else { return nil }
        let name = [p.vendor, p.product].filter { !$0.isEmpty }.joined(separator: " ")
        return name.isEmpty ? p.vid_pid : name
    }

    var canConnect: Bool { state == "tethering_available" || (state == "error" && (phone?.rndis ?? false)) }
    var canDisconnect: Bool { state == "connected" || state == "connecting" }

    var sinceDate: Date? {
        guard let since else { return nil }
        return ISO8601DateFormatter().date(from: since)
    }
}

func formatBytes(_ b: Double) -> String {
    ByteCountFormatter.string(fromByteCount: Int64(b), countStyle: .file)
}

func formatRate(_ b: Double) -> String {
    formatBytes(b) + "/s"
}
