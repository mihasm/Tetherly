import SwiftUI
import AppKit

struct MenuView: View {
    @EnvironmentObject var model: Model
    @Environment(\.openWindow) private var openWindow
    @State private var launchAtLogin = HelperInstaller.launchAtLogin

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            header
            if model.helperState != .enabled || model.helperError != nil {
                helperCard
            } else if let s = model.status {
                if s.state == "connected" { details(s) }
                if let e = s.error, !e.isEmpty { message(e, symbol: "exclamationmark.triangle", tint: .orange) }
                if let h = s.hint, !h.isEmpty { message(h, symbol: "info.circle", tint: .secondary) }
                actionButton(s)
            }
            if let e = model.actionError {
                message(e, symbol: "xmark.circle", tint: .red)
            }
            Divider()
            toggles
            Divider()
            footer
        }
        .padding(14)
        .frame(width: 320)
        .onAppear {
            model.panelOpen = true
            launchAtLogin = HelperInstaller.launchAtLogin
        }
        .onDisappear { model.panelOpen = false }
    }

    // MARK: header

    private var header: some View {
        HStack(spacing: 10) {
            ZStack {
                Circle().fill(stateColor.opacity(0.15))
                Image(systemName: stateSymbol)
                    .font(.system(size: 17, weight: .regular))
                    .foregroundStyle(stateColor)
            }
            .frame(width: 36, height: 36)
            VStack(alignment: .leading, spacing: 2) {
                Text(headerTitle).font(.headline)
                if let p = model.status?.phoneName {
                    Text(p).font(.subheadline).foregroundStyle(.secondary).lineLimit(1)
                }
            }
            Spacer()
            if model.busy || model.status?.state == "connecting" {
                ProgressView().controlSize(.small)
            }
        }
    }

    private var headerTitle: String {
        if model.helperState != .enabled { return "Helper not set up" }
        if model.helperError != nil { return "Helper not reachable" }
        return model.status?.title ?? "Starting"
    }

    private var stateSymbol: String {
        guard model.helperError == nil, let s = model.status else { return "personalhotspot.slash" }
        switch s.state {
        case "connected": return "personalhotspot"
        case "connecting", "disconnecting": return "arrow.triangle.2.circlepath"
        case "tethering_available": return "cable.connector"
        case "phone_found": return "smartphone"
        case "error": return "exclamationmark.triangle"
        default: return "personalhotspot.slash"
        }
    }

    private var stateColor: Color {
        guard model.helperError == nil, let s = model.status else { return .secondary }
        switch s.state {
        case "connected": return .green
        case "connecting", "disconnecting", "tethering_available": return .blue
        case "error": return .orange
        default: return .secondary
        }
    }

    // MARK: connected details

    private func details(_ s: TetherStatus) -> some View {
        Grid(alignment: .leading, horizontalSpacing: 12, verticalSpacing: 5) {
            row("Interface", s.interface ?? "-")
            row("Address", [s.ip, s.gateway].compactMap { $0 }.joined(separator: "  via  "))
            row("DNS", (s.dns ?? []).isEmpty ? "system" : (s.dns ?? []).joined(separator: ", "))
            row("MTU", s.mtu.map(String.init) ?? "-")
            if let d = s.sinceDate {
                GridRow {
                    Text("Up for").foregroundStyle(.secondary)
                    Text(d, style: .relative)
                }
            }
            GridRow {
                Text("Traffic").foregroundStyle(.secondary)
                HStack(spacing: 10) {
                    Label(formatRate(model.rxRate), systemImage: "arrow.down")
                    Label(formatRate(model.txRate), systemImage: "arrow.up")
                }
                .labelStyle(.titleAndIcon)
            }
            row("Total", "\(formatBytes(Double(s.rx_bytes))) down, \(formatBytes(Double(s.tx_bytes))) up")
        }
        .font(.callout)
        .textSelection(.enabled)
    }

    private func row(_ k: String, _ v: String) -> some View {
        GridRow {
            Text(k).foregroundStyle(.secondary)
            Text(v).lineLimit(2)
        }
    }

    private func message(_ text: String, symbol: String, tint: Color) -> some View {
        HStack(alignment: .top, spacing: 8) {
            Image(systemName: symbol).foregroundStyle(tint)
            Text(text).font(.callout).fixedSize(horizontal: false, vertical: true)
        }
    }

    @ViewBuilder
    private func actionButton(_ s: TetherStatus) -> some View {
        if s.canDisconnect {
            Button(role: .destructive) { model.disconnect() } label: {
                Label("Disconnect", systemImage: "power").frame(maxWidth: .infinity)
            }
            .controlSize(.large)
            .disabled(model.busy)
        } else {
            Button { model.connect() } label: {
                Label("Connect", systemImage: "link").frame(maxWidth: .infinity)
            }
            .controlSize(.large)
            .buttonStyle(.borderedProminent)
            .disabled(!s.canConnect || model.busy)
            if s.state == "no_phone" {
                Text("Plug in an Android phone with a USB cable, then switch on USB tethering on the phone.")
                    .font(.caption).foregroundStyle(.secondary)
            }
        }
    }

    // MARK: helper setup

    private var helperCard: some View {
        VStack(alignment: .leading, spacing: 8) {
            switch model.helperState {
            case .needsApproval:
                Text("Allow Tetherly in System Settings > General > Login Items & Extensions, then come back here.")
                    .font(.callout)
                Button { HelperInstaller.openLoginItemsSettings() } label: {
                    Label("Open Login Items", systemImage: "gearshape")
                }
            case .enabled:
                Text(model.helperError ?? "The helper is not answering.").font(.callout)
                Button { model.refreshNow() } label: { Label("Retry", systemImage: "arrow.clockwise") }
            default:
                Text("Tetherly needs a small helper that runs as root to create the network interface and routes. It only starts when needed and never touches the phone until you connect.")
                    .font(.callout).fixedSize(horizontal: false, vertical: true)
                Button { model.installHelper() } label: {
                    Label("Install Helper", systemImage: "arrow.down.circle")
                }
                .buttonStyle(.borderedProminent)
            }
            if let e = model.helperError, model.helperState != .enabled {
                Text(e).font(.caption).foregroundStyle(.secondary)
            }
        }
    }

    // MARK: toggles and footer

    private var toggles: some View {
        VStack(alignment: .leading, spacing: 6) {
            Toggle("Connect automatically when tethering is on", isOn: Binding(
                get: { model.config?.auto_connect ?? false },
                set: { model.setAutoConnect($0) }
            ))
            .disabled(model.config == nil)
            Toggle("Open at login", isOn: Binding(
                get: { launchAtLogin },
                set: { HelperInstaller.launchAtLogin = $0; launchAtLogin = HelperInstaller.launchAtLogin }
            ))
        }
        .toggleStyle(.switch)
        .controlSize(.small)
    }

    private var footer: some View {
        HStack {
            Button { openSettings() } label: { Label("Settings", systemImage: "gearshape") }
            Button {
                openWindow(id: "logs")
                NSApp.activate(ignoringOtherApps: true)
            } label: { Label("Log", systemImage: "doc.text.magnifyingglass") }
            Spacer()
            Button { NSApp.terminate(nil) } label: { Label("Quit", systemImage: "xmark.circle") }
        }
        .buttonStyle(.borderless)
        .labelStyle(.titleAndIcon)
        .font(.callout)
    }

    private func openSettings() {
        NSApp.activate(ignoringOtherApps: true)
        if #available(macOS 14, *) {
            NSApp.sendAction(Selector(("showSettingsWindow:")), to: nil, from: nil)
        } else {
            NSApp.sendAction(Selector(("showPreferencesWindow:")), to: nil, from: nil)
        }
    }
}
