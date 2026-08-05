import Foundation
import VixProtocol

/// Result of the daemon `ping` RPC. `version` is the running daemon's build
/// version — the dev-friendly handshake stamps it into thread.start so the
/// client connects to whatever daemon is up without a build-time version pin.
public struct PingResult: Sendable {
    public let ok: Bool
    public let version: String
}

/// A client for a single vix daemon thread over the AF_UNIX NDJSON socket.
///
/// Lifecycle: `ping()` (optional, to learn the version) → `start()` (returns the
/// live `ThreadEvent` stream) → `sendInput` / `sendConfirm` / … while consuming
/// events → `closeThread()`.
public final class VixThreadClient: @unchecked Sendable {
    /// Default socket path: the shared daemon, overridable via `VIXD_SOCK`.
    public static var defaultSocketPath: String {
        ProcessInfo.processInfo.environment["VIXD_SOCK"] ?? "/tmp/vixd.sock"
    }

    public let socketPath: String
    let authToken: String?
    private var socket: VixSocket?
    private let encoder = JSONEncoder()
    private let decoder = JSONDecoder()

    public init(socketPath: String = VixThreadClient.defaultSocketPath, authToken: String? = nil) {
        self.socketPath = socketPath
        self.authToken = authToken
    }

    // MARK: Handshake / RPC

    /// One-shot request/response on a short-lived connection (the daemon's
    /// non-thread RPC path). The auth token is stamped automatically.
    func rpc(_ request: [String: JSONValue]) throws -> [String: JSONValue] {
        let sock = try VixSocket(path: socketPath)
        defer { sock.close() }
        var req = request
        if let t = authToken { req["auth_token"] = .string(t) }
        try sock.writeLine(try encoder.encode(JSONValue.object(req)))
        let resp = try decoder.decode(JSONValue.self, from: try sock.readLine())
        return resp.objectValue ?? [:]
    }

    /// Learn the running daemon's version (and liveness) via the `ping` RPC.
    public func ping() throws -> PingResult {
        let obj = try rpc(["action": .string("ping")])
        return PingResult(
            ok: obj["status"]?.stringValue == "ok",
            version: obj["version"]?.stringValue ?? "")
    }

    /// List the daemon's persisted open threads for a working directory.
    public func listThreads(cwd: String) throws -> [ThreadSummary] {
        let obj = try rpc(["command": .string("thread.list"), "cwd": .string(cwd)])
        guard let threads = obj["threads"] else { return [] }
        let data = try encoder.encode(threads)
        return try decoder.decode([ThreadSummary].self, from: data)
    }

    // MARK: Thread

    /// Open a new thread and return its live event stream. When `clientVersion`
    /// is nil the version is discovered via `ping()` first (dev-friendly). The
    /// first event is normally `event.thread_started`, or `event.error` with
    /// code `version_mismatch`.
    public func start(
        cwd: String,
        model: String = "",
        headless: Bool = true,
        clientVersion: String? = nil
    ) throws -> AsyncThrowingStream<ThreadEvent, Error> {
        let version = try clientVersion ?? ping().version
        return try openStream(ThreadStartData(
            clientVersion: version,
            cwd: cwd,
            enableAutomaticDirectoryAccess: false,
            enableAutomaticWritePermission: false,
            forceInit: false,
            headless: headless,
            model: model))
    }

    /// Resume a persisted thread by id. The daemon replays the conversation via
    /// `event.replay` right after `event.thread_started`.
    public func attach(
        threadID: String,
        cwd: String = "",
        clientVersion: String? = nil
    ) throws -> AsyncThrowingStream<ThreadEvent, Error> {
        let version = try clientVersion ?? ping().version
        return try openStream(ThreadStartData(
            attachThreadId: threadID,
            clientVersion: version,
            cwd: cwd,
            enableAutomaticDirectoryAccess: false,
            enableAutomaticWritePermission: false,
            forceInit: false,
            headless: true,
            model: ""))
    }

    /// Dial a fresh thread socket, send thread.start, and stream decoded
    /// events. Shared by start() and attach().
    private func openStream(_ payload: ThreadStartData) throws -> AsyncThrowingStream<ThreadEvent, Error> {
        let sock = try VixSocket(path: socketPath)
        self.socket = sock
        try send(type: "thread.start", payload: payload)
        let raw = sock.lines()
        let dec = decoder
        return AsyncThrowingStream { continuation in
            Task {
                do {
                    for try await line in raw {
                        continuation.yield(try dec.decode(ThreadEvent.self, from: line))
                    }
                    continuation.finish()
                } catch {
                    continuation.finish(throwing: error)
                }
            }
        }
    }

    // MARK: Commands

    /// Encode a command envelope (`{type, auth_token?, data}`) without sending —
    /// the single place command framing is built, shared by `send` and tests.
    func commandData<T: Encodable>(type: String, payload: T) throws -> Data {
        try encoder.encode(Envelope(type: type, authToken: authToken, data: payload))
    }

    public func send<T: Encodable>(type: String, payload: T) throws {
        try requireSocket().writeLine(try commandData(type: type, payload: payload))
    }

    /// Send a payload-less command (`data` is null on the wire).
    public func sendEmpty(type: String) throws {
        var obj: [String: JSONValue] = ["type": .string(type), "data": .null]
        if let t = authToken { obj["auth_token"] = .string(t) }
        try requireSocket().writeLine(try encoder.encode(JSONValue.object(obj)))
    }

    public func sendInput(_ text: String) throws {
        try send(type: "thread.input", payload: ThreadInputData(text: text))
    }

    public func sendConfirm(approved: Bool, persistDirs: Bool = false) throws {
        try send(type: "thread.confirm", payload: ThreadConfirmData(approved: approved, persistDirs: persistDirs))
    }

    public func sendUserAnswer(_ answer: String, text: String = "") throws {
        try send(type: "thread.user_answer", payload: ThreadUserAnswerData(answer: answer, text: text))
    }

    /// Answer a batch question (one answer per question id).
    public func sendUserAnswerBatch(_ answers: [String: String]) throws {
        try send(type: "thread.user_answer", payload: ThreadUserAnswerData(answer: "", answers: answers))
    }

    public func sendPlanAction(_ action: String, text: String = "") throws {
        try send(type: "thread.plan_action", payload: ThreadPlanActionData(action: action, text: text))
    }

    public func cancel() throws { try sendEmpty(type: "thread.cancel") }
    public func markRead() throws { try sendEmpty(type: "thread.mark_read") }

    /// Close the thread (best-effort thread.close) and tear down the socket.
    public func closeThread() {
        try? sendEmpty(type: "thread.close")
        socket?.close()
        socket = nil
    }

    private func requireSocket() throws -> VixSocket {
        guard let s = socket else { throw VixSocketError.closed }
        return s
    }
}

/// Outbound command envelope. `data` always encodes (a payload object); the
/// optional `auth_token` is omitted when nil.
struct Envelope<T: Encodable>: Encodable {
    let type: String
    let authToken: String?
    let data: T

    enum CodingKeys: String, CodingKey {
        case type
        case authToken = "auth_token"
        case data
    }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(type, forKey: .type)
        try c.encodeIfPresent(authToken, forKey: .authToken)
        try c.encode(data, forKey: .data)
    }
}
