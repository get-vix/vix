import Foundation
import Observation
import VixClient
import VixProtocol

/// Observable thread controller for the SwiftUI app: owns the connection, the
/// event-consuming task, and the reduced transcript state. All UI-facing state
/// mutates on the main actor.
@MainActor
@Observable
public final class ThreadModel {
    public enum Connection: Equatable, Sendable {
        case disconnected
        case connecting
        case connected(version: String)
        case failed(String)
    }

    public private(set) var state = TranscriptState()
    public private(set) var connection: Connection = .disconnected
    public private(set) var banner: String?
    public var inputText = ""

    public let cwd: String
    private let client: VixThreadClient
    private var streamTask: Task<Void, Never>?
    private var lastMakeStream: (@Sendable (String) throws -> AsyncThrowingStream<ThreadEvent, Error>)?

    public init(client: VixThreadClient = VixThreadClient(),
                cwd: String = FileManager.default.currentDirectoryPath) {
        self.client = client
        self.cwd = cwd
    }

    /// Ping the daemon (dev-friendly version discovery), open a new thread, and
    /// begin consuming events into `state`.
    public func connect() {
        let client = self.client
        let cwd = self.cwd
        begin { try client.start(cwd: cwd, clientVersion: $0) }
    }

    /// Resume a persisted thread by id; the daemon replays it via event.replay.
    public func attach(threadID: String) {
        let client = self.client
        let cwd = self.cwd
        begin { try client.attach(threadID: threadID, cwd: cwd, clientVersion: $0) }
    }

    /// Retry the last connect/attach after a failure.
    public func retry() {
        guard let make = lastMakeStream else { return }
        connection = .disconnected
        begin(make)
    }

    private func begin(_ makeStream: @escaping @Sendable (String) throws -> AsyncThrowingStream<ThreadEvent, Error>) {
        guard connection == .disconnected || isFailed else { return }
        lastMakeStream = makeStream
        banner = nil
        connection = .connecting

        let client = self.client
        Task { [weak self] in
            // Run the blocking handshake (ping + connect + thread.start) off the
            // main actor so the UI never stalls and we never mutate observed
            // state during a SwiftUI update.
            let outcome: Result<(String, AsyncThrowingStream<ThreadEvent, Error>), ConnectError> =
                await Task.detached {
                    do {
                        let ping = try client.ping()
                        guard ping.ok else {
                            return .failure(.unresponsive(client.socketPath))
                        }
                        return .success((ping.version, try makeStream(ping.version)))
                    } catch {
                        return .failure(.failed("\(error)"))
                    }
                }.value

            guard let self else { return }
            switch outcome {
            case .failure(let error):
                self.connection = .failed(error.message)
            case .success(let (version, events)):
                self.connection = .connected(version: version)
                self.streamTask = Task { @MainActor [weak self] in
                    do {
                        for try await event in events {
                            self?.apply(event)
                        }
                    } catch {
                        self?.connection = .failed("\(error)")
                    }
                }
            }
        }
    }

    /// Send the current input as a user turn.
    public func send() {
        let text = inputText.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty, case .connected = connection else { return }
        inputText = ""
        state.items.append(.user(id: UUID(), text: text))
        do {
            try client.sendInput(text)
        } catch {
            state.items.append(.notice(id: UUID(), text: "send failed: \(error)"))
        }
    }

    public func cancel() {
        try? client.cancel()
    }

    // MARK: Interactive round-trips

    /// Answer a pending permission prompt (event.confirm_request).
    public func answerConfirm(approved: Bool, persistDirs: Bool = false) {
        guard case .confirm = state.pending else { return }
        state.pending = nil
        try? client.sendConfirm(approved: approved, persistDirs: persistDirs)
    }

    /// Answer a pending single question (event.user_question).
    public func answerQuestion(_ answer: String, text: String = "") {
        guard case .question = state.pending else { return }
        state.pending = nil
        try? client.sendUserAnswer(answer, text: text)
    }

    /// Answer a pending batch question (id → answer).
    public func answerQuestionBatch(_ answers: [String: String]) {
        guard case .question = state.pending else { return }
        state.pending = nil
        try? client.sendUserAnswerBatch(answers)
    }

    /// Answer a pending plan review (approve | reject | modify).
    public func answerPlan(_ action: String, text: String = "") {
        guard case .plan = state.pending else { return }
        state.pending = nil
        try? client.sendPlanAction(action, text: text)
    }

    public func disconnect() {
        streamTask?.cancel()
        streamTask = nil
        client.closeThread()
        connection = .disconnected
    }

    private func apply(_ event: ThreadEvent) {
        if let banner = connectionBanner(for: event) { self.banner = banner }
        reduce(&state, event)
    }

    private var isFailed: Bool {
        if case .failed = connection { return true }
        return false
    }
}

/// A Sendable connect failure, so the outcome can cross the actor boundary from
/// the off-main handshake task.
private enum ConnectError: Error, Sendable {
    case unresponsive(String)
    case failed(String)

    var message: String {
        switch self {
        case .unresponsive(let path): return "vixd is not responding on \(path)"
        case .failed(let m): return "cannot reach vixd: \(m)"
        }
    }
}
