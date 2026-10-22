import Foundation

/// Status as the helper reports it (see internal/ipc/ipc.go).
struct TetherStatus: Codable, Equatable {
    struct Phone: Codable, Equatable {
        var vendor: String
        var product: String
        var serial: String
        var vid_pid: String
        var rndis: Bool
        var adb: Bool
    }
    var state: String
    var phone: Phone?
    var interface: String?
    var ip: String?
    var gateway: String?
    var dns: [String]?
    var dns_managed: Bool
    var routed: Bool
    var mtu: Int?
    var since: String?
    var rx_bytes: UInt64
    var tx_bytes: UInt64
    var error: String?
    var hint: String?
    var auto_connect: Bool
    var version: String
}

struct TetherConfig: Codable, Equatable {
    var auto_connect: Bool
    var mtu: Int
    var route_all: Bool
    var manage_dns: Bool
    var dns_servers: [String]?
}

struct LogLine: Codable, Identifiable, Equatable {
    var n: Int64
    var t: String
    var msg: String
    var id: Int64 { n }
}

private struct Request: Encodable {
    var cmd: String
    var config: TetherConfig?
    var since: Int64?
}

struct Response: Decodable {
    var ok: Bool
    var error: String?
    var status: TetherStatus?
    var config: TetherConfig?
    var logs: [LogLine]?
}

enum HelperError: LocalizedError {
    case notRunning(String)
    case protocolError(String)
    case refused(String)

    var errorDescription: String? {
        switch self {
        case .notRunning(let s): return "The helper is not running (\(s))."
        case .protocolError(let s): return "Unexpected answer from the helper: \(s)"
        case .refused(let s): return s
        }
    }
}

/// Talks to the root helper over its Unix socket, one JSON line each way.
enum HelperClient {
    static var socketPath: String {
        ProcessInfo.processInfo.environment["TETHERLY_SOCKET"] ?? "/var/run/tetherly.sock"
    }

    static func call(_ cmd: String, config: TetherConfig? = nil, since: Int64? = nil, timeout: TimeInterval = 4) throws -> Response {
        let fd = socket(AF_UNIX, SOCK_STREAM, 0)
        guard fd >= 0 else { throw HelperError.notRunning(String(cString: strerror(errno))) }
        defer { close(fd) }

        var tv = timeval(tv_sec: Int(timeout), tv_usec: Int32((timeout - floor(timeout)) * 1_000_000))
        setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &tv, socklen_t(MemoryLayout<timeval>.size))
        setsockopt(fd, SOL_SOCKET, SO_SNDTIMEO, &tv, socklen_t(MemoryLayout<timeval>.size))
        var one: Int32 = 1
        setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &one, socklen_t(MemoryLayout<Int32>.size))

        var addr = sockaddr_un()
        addr.sun_family = sa_family_t(AF_UNIX)
        let path = socketPath
        withUnsafeMutableBytes(of: &addr.sun_path) { buf in
            buf.copyBytes(from: path.utf8.prefix(buf.count - 1))
        }
        let len = socklen_t(MemoryLayout<sockaddr_un>.size)
        let rc = withUnsafePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { connect(fd, $0, len) }
        }
        guard rc == 0 else { throw HelperError.notRunning(String(cString: strerror(errno))) }

        var payload = try JSONEncoder().encode(Request(cmd: cmd, config: config, since: since))
        payload.append(0x0A)
        let sent = payload.withUnsafeBytes { send(fd, $0.baseAddress, $0.count, 0) }
        guard sent == payload.count else { throw HelperError.notRunning("write failed") }

        var data = Data()
        var buf = [UInt8](repeating: 0, count: 65536)
        while true {
            let n = recv(fd, &buf, buf.count, 0)
            if n <= 0 { break }
            data.append(buf, count: n)
            if buf[n - 1] == 0x0A { break }
        }
        guard !data.isEmpty else { throw HelperError.protocolError("no answer") }
        do {
            return try JSONDecoder().decode(Response.self, from: data)
        } catch {
            throw HelperError.protocolError(error.localizedDescription)
        }
    }
}
