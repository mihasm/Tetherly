import SwiftUI
import AppKit

@main
enum Entry {
    static func main() {
        // `Tetherly status --json` etc.: hand over to the command-line client in the helper binary.
        let args = CommandLine.arguments.dropFirst()
        // Helper registration without the menu: `Tetherly --install-helper | --uninstall-helper | --helper-state`
        if let first = args.first, ["--install-helper", "--uninstall-helper", "--helper-state"].contains(first) {
            do {
                if first == "--install-helper" { try HelperInstaller.install() }
                if first == "--uninstall-helper" { try HelperInstaller.uninstall() }
            } catch {
                print("error: \(error.localizedDescription)")
            }
            print("helper: \(HelperInstaller.state)")
            exit(HelperInstaller.state == .enabled ? 0 : 1)
        }
        let cli: Set<String> = ["status", "connect", "disconnect", "logs", "config", "version", "devices", "help", "--help", "-h"]
        if let first = args.first, cli.contains(first) {
            let path = HelperInstaller.helperBinary.path
            var argv: [UnsafeMutablePointer<CChar>?] = ([path] + args).map { strdup($0) }
            argv.append(nil)
            execv(path, &argv)
            perror("tetherly: exec \(path)")
            exit(127)
        }
        TetherlyApp.main()
    }
}

struct TetherlyApp: App {
    @StateObject private var model = Model()

    var body: some Scene {
        MenuBarExtra {
            MenuView()
                .environmentObject(model)
        } label: {
            Image(systemName: menuBarSymbol)
                .accessibilityLabel("Tetherly: \(model.status?.title ?? "helper not running")")
        }
        .menuBarExtraStyle(.window)

        Window("Tetherly Log", id: "logs") {
            LogsView()
                .environmentObject(model)
        }
        .defaultSize(width: 760, height: 460)

        Settings {
            SettingsView()
                .environmentObject(model)
        }
    }

    private var menuBarSymbol: String {
        guard model.helperError == nil, let s = model.status else { return "personalhotspot.slash" }
        switch s.state {
        case "connected": return "personalhotspot"
        case "connecting", "disconnecting": return "arrow.triangle.2.circlepath"
        case "tethering_available", "phone_found": return "cable.connector"
        case "error": return "exclamationmark.triangle"
        default: return "personalhotspot.slash"
        }
    }
}
