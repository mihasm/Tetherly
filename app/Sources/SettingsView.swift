import SwiftUI
import AppKit

struct SettingsView: View {
    @EnvironmentObject var model: Model
    @State private var mtu = 1380
    @State private var routeAll = true
    @State private var manageDNS = true
    @State private var dnsText = ""
    @State private var autoConnect = false
    @State private var launchAtLogin = HelperInstaller.launchAtLogin
    @State private var cliInstalled = HelperInstaller.cliInstalled
    @State private var cliError: String?
    @State private var loaded = false

    var body: some View {
        Form {
            Section("Connection") {
                Toggle("Connect automatically when the phone offers USB tethering", isOn: $autoConnect)
                Toggle("Send all traffic through the phone", isOn: $routeAll)
                Text("Directly connected networks (for example an OBD adapter or a printer on Wi-Fi) always stay on their own interface.")
                    .font(.caption).foregroundStyle(.secondary)
                HStack {
                    Text("MTU")
                    TextField("", value: $mtu, format: .number.grouping(.never))
                        .frame(width: 70)
                    Stepper("", value: $mtu, in: 576...1500, step: 10).labelsHidden()
                    Menu("Presets") {
                        Button("1500 (Ethernet)") { mtu = 1500 }
                        Button("1400") { mtu = 1400 }
                        Button("1380 (recommended for 4G/5G)") { mtu = 1380 }
                        Button("1280 (most compatible)") { mtu = 1280 }
                    }
                    .fixedSize()
                }
                Text("Lower it if pages hang while ping works (an MTU black hole on the mobile network).")
                    .font(.caption).foregroundStyle(.secondary)
            }
            Section("DNS") {
                Toggle("Make the phone link the primary network (fixes DNS and \"offline\" Safari / App Store)", isOn: $manageDNS)
                    .disabled(!routeAll)
                TextField("DNS servers", text: $dnsText, prompt: Text("the phone's DNS, then 1.1.1.1, 8.8.8.8"))
                    .disabled(!manageDNS || !routeAll)
                Text("Comma-separated. Nothing is written to your network settings: it is removed when you disconnect, even after a crash.")
                    .font(.caption).foregroundStyle(.secondary)
            }
            Section("App") {
                Toggle("Open at login", isOn: $launchAtLogin)
                    .onChange(of: launchAtLogin) { v in
                        HelperInstaller.launchAtLogin = v
                    }
                Toggle("Notifications", isOn: $model.notificationsOn)
                HStack {
                    Text("Command-line tool")
                    Spacer()
                    if cliInstalled {
                        Text("~/.local/bin/tetherly").foregroundStyle(.secondary).textSelection(.enabled)
                    } else {
                        Button("Install ~/.local/bin/tetherly") {
                            do { try HelperInstaller.installCLI(); cliError = nil } catch { cliError = error.localizedDescription }
                            cliInstalled = HelperInstaller.cliInstalled
                        }
                    }
                }
                if let cliError { Text(cliError).font(.caption).foregroundStyle(.red) }
            }
            Section("Helper") {
                HStack {
                    Text("Status")
                    Spacer()
                    Text(helperText).foregroundStyle(.secondary)
                }
                HStack {
                    Button("Reinstall") { model.installHelper() }
                    Button("Uninstall") { model.uninstallHelper() }
                    Button("Login Items…") { HelperInstaller.openLoginItemsSettings() }
                }
            }
            HStack {
                Spacer()
                Button("Apply") { apply() }
                    .keyboardShortcut(.defaultAction)
                    .disabled(model.config == nil)
            }
            if let e = model.actionError { Text(e).font(.caption).foregroundStyle(.red) }
            Text("MTU, routing and DNS changes apply from the next connection.")
                .font(.caption).foregroundStyle(.secondary)
        }
        .formStyle(.grouped)
        .frame(width: 520)
        .onAppear { model.loadConfig(); load() }
        .onReceive(model.$config) { _ in if !loaded { load() } }
    }

    private var helperText: String {
        switch model.helperState {
        case .enabled: return model.helperError == nil ? "installed, running on demand (\(model.status?.version ?? "?"))" : "installed, not answering"
        case .needsApproval: return "waiting for approval in Login Items"
        case .notInstalled: return "not installed"
        case .notFound: return "missing from the app bundle"
        case .unknown: return "unknown"
        }
    }

    private func load() {
        guard let c = model.config else { return }
        mtu = c.mtu
        routeAll = c.route_all
        manageDNS = c.manage_dns
        autoConnect = c.auto_connect
        dnsText = (c.dns_servers ?? []).joined(separator: ", ")
        loaded = true
    }

    private func apply() {
        let servers = dnsText.split(whereSeparator: { $0 == "," || $0 == " " }).map(String.init).filter { !$0.isEmpty }
        model.save(TetherConfig(auto_connect: autoConnect, mtu: min(max(mtu, 576), 1500), route_all: routeAll, manage_dns: manageDNS, dns_servers: servers))
    }
}
