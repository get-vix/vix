import Foundation
import Observation
import VixClient
import VixProtocol

/// Top-level coordinator: owns the persisted thread list and the currently
/// active thread. Views observe this to drive the sidebar and detail panes.
@MainActor
@Observable
public final class AppModel {
    public private(set) var threads: [ThreadSummary] = []
    public private(set) var active: ThreadModel?
    public private(set) var selectedID: String?

    public let cwd: String
    private let socketPath: String
    private let authToken: String?

    public init(socketPath: String = VixThreadClient.defaultSocketPath,
                authToken: String? = nil,
                cwd: String = FileManager.default.currentDirectoryPath) {
        self.socketPath = socketPath
        self.authToken = authToken
        self.cwd = cwd
    }

    private func makeClient() -> VixThreadClient {
        VixThreadClient(socketPath: socketPath, authToken: authToken)
    }

    /// Refresh the persisted thread list (best-effort; off the main thread).
    public func refresh() {
        let client = makeClient()
        let cwd = self.cwd
        Task { [weak self] in
            let list = await Task.detached { (try? client.listThreads(cwd: cwd)) ?? [] }.value
            self?.threads = list
        }
    }

    /// Start a fresh thread and make it active.
    public func newThread() {
        active?.disconnect()
        let model = ThreadModel(client: makeClient(), cwd: cwd)
        active = model
        selectedID = nil
        model.connect()
    }

    /// Attach to an existing persisted thread and make it active.
    public func open(_ summary: ThreadSummary) {
        guard summary.id != selectedID else { return }
        active?.disconnect()
        let model = ThreadModel(client: makeClient(), cwd: cwd)
        active = model
        selectedID = summary.id
        model.attach(threadID: summary.id)
    }
}
