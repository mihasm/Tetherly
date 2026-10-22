import SwiftUI
import AppKit

struct LogsView: View {
    @EnvironmentObject var model: Model
    @State private var follow = true
    @State private var filter = ""
    private let timer = Timer.publish(every: 1, on: .main, in: .common).autoconnect()

    private var shown: [LogLine] {
        filter.isEmpty ? model.logs : model.logs.filter { $0.msg.localizedCaseInsensitiveContains(filter) }
    }

    var body: some View {
        VStack(spacing: 0) {
            HStack {
                Image(systemName: "magnifyingglass").foregroundStyle(.secondary)
                TextField("Filter", text: $filter).textFieldStyle(.roundedBorder).frame(maxWidth: 240)
                Toggle("Follow", isOn: $follow).toggleStyle(.checkbox)
                Spacer()
                Button { copyAll() } label: { Label("Copy", systemImage: "doc.on.doc") }
                Button { model.clearLogView() } label: { Label("Clear", systemImage: "trash") }
                Button {
                    NSWorkspace.shared.selectFile("/Library/Logs/Tetherly/tetherly.log", inFileViewerRootedAtPath: "")
                } label: { Label("Show File", systemImage: "folder") }
            }
            .padding(8)
            Divider()
            ScrollViewReader { proxy in
                ScrollView {
                    LazyVStack(alignment: .leading, spacing: 1) {
                        ForEach(shown) { l in
                            HStack(alignment: .top, spacing: 8) {
                                Text(l.t).foregroundStyle(.secondary)
                                Text(l.msg).foregroundStyle(color(l.msg))
                            }
                            .font(.system(.caption, design: .monospaced))
                            .textSelection(.enabled)
                            .id(l.id)
                        }
                    }
                    .padding(8)
                    .frame(maxWidth: .infinity, alignment: .leading)
                }
                .onChange(of: model.logs.last?.id) { id in
                    if follow, let id { proxy.scrollTo(id, anchor: .bottom) }
                }
            }
            if model.helperError != nil {
                Divider()
                Text("The helper is not running, so there is no live log. It starts when Tetherly asks for the status.")
                    .font(.caption).foregroundStyle(.secondary).padding(6)
            }
        }
        .onAppear { model.fetchLogs() }
        .onReceive(timer) { _ in model.fetchLogs() }
    }

    private func color(_ msg: String) -> Color {
        if msg.hasPrefix("ERROR") { return .red }
        if msg.hasPrefix("WARN") { return .orange }
        if msg.hasPrefix("DEBUG") { return .secondary }
        return .primary
    }

    private func copyAll() {
        let text = shown.map { "\($0.t) \($0.msg)" }.joined(separator: "\n")
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(text, forType: .string)
    }
}
