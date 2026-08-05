import Foundation
import VixProtocol

/// One rendered row in the chat transcript. Ids are stable so SwiftUI can diff
/// the list and so streaming rows can be updated in place.
public enum TranscriptItem: Identifiable, Equatable, Sendable {
    case user(id: UUID, text: String)
    case assistant(id: UUID, text: String)
    case thinking(id: UUID, text: String)
    case tool(id: UUID, ToolRow)
    case notice(id: UUID, text: String)

    public var id: UUID {
        switch self {
        case .user(let id, _), .assistant(let id, _), .thinking(let id, _),
             .tool(let id, _), .notice(let id, _):
            return id
        }
    }
}

/// A tool call and (eventually) its result, correlated by the daemon's tool_id.
public struct ToolRow: Equatable, Sendable {
    public var toolID: String
    public var name: String
    public var summary: String
    public var output: String
    public var isError: Bool
    public var done: Bool

    public init(toolID: String, name: String, summary: String, output: String = "", isError: Bool = false, done: Bool = false) {
        self.toolID = toolID
        self.name = name
        self.summary = summary
        self.output = output
        self.isError = isError
        self.done = done
    }
}

/// Token accounting from the last completed model turn.
public struct TokenStats: Equatable, Sendable {
    public var input: Int64
    public var output: Int64
    public var cacheCreation: Int64
    public var cacheRead: Int64
}

/// An event that blocks the turn until the client answers. This is what makes
/// the client interactive rather than a one-shot runner.
public enum PendingInteraction: Equatable, Sendable {
    case confirm(EventConfirmRequest)
    case question(EventUserQuestion)
    case plan(Plan?)
}

/// The full, UI-independent transcript state. Mutated only by `reduce`.
public struct TranscriptState: Equatable, Sendable {
    public var items: [TranscriptItem] = []
    public var isStreaming = false
    public var threadID = ""
    public var title = ""
    public var lastTokens: TokenStats?

    /// A blocking round-trip awaiting the user's answer (permission, question,
    /// or plan review), or nil when the turn is free-running.
    public var pending: PendingInteraction?

    /// The current todo list (from event.todo_list_updated / replay).
    public var todos: [TodoItem] = []

    // Cursors into `items` for the row currently being streamed into.
    var streamingAssistantID: UUID?
    var streamingThinkingID: UUID?

    public init() {}

    /// Concatenated assistant text — convenience for tests/assertions.
    public var assistantText: String {
        items.reduce(into: "") { acc, item in
            if case .assistant(_, let t) = item { acc += t }
        }
    }

    public var toolRows: [ToolRow] {
        items.compactMap { if case .tool(_, let r) = $0 { return r } else { return nil } }
    }
}

/// Apply a single daemon event to the transcript state. Pure and total: unknown
/// events and malformed payloads are ignored. This is the headless-testable core
/// of the app.
public func reduce(_ s: inout TranscriptState, _ event: ThreadEvent) {
    switch event.type {
    case "event.thread_started":
        if let d = try? event.data.decode(EventThreadStarted.self) {
            s.threadID = d.threadId
        }

    case "event.stream_chunk":
        let text = (try? event.data.decode(EventStreamChunk.self))?.text ?? ""
        appendAssistant(&s, text)
        s.isStreaming = true

    case "event.thinking_chunk":
        let text = (try? event.data.decode(EventThinkingChunk.self))?.text ?? ""
        appendThinking(&s, text)
        s.isStreaming = true

    case "event.stream_done":
        if let d = try? event.data.decode(EventStreamDone.self) {
            s.lastTokens = TokenStats(
                input: d.inputTokens, output: d.outputTokens,
                cacheCreation: d.cacheCreationTokens, cacheRead: d.cacheReadTokens)
        }
        endStreaming(&s)

    case "event.tool_call":
        if let d = try? event.data.decode(EventToolCall.self) {
            s.items.append(.tool(id: UUID(), ToolRow(toolID: d.toolId, name: d.name, summary: d.summary)))
            endStreaming(&s)
        }

    case "event.tool_result":
        if let d = try? event.data.decode(EventToolResult.self) {
            applyToolResult(&s, d)
        }

    case "event.confirm_request":
        if let d = try? event.data.decode(EventConfirmRequest.self) {
            s.pending = .confirm(d)
        }

    case "event.user_question":
        if let d = try? event.data.decode(EventUserQuestion.self) {
            s.pending = .question(d)
        }

    case "event.plan_proposed":
        if let d = try? event.data.decode(EventPlanProposed.self) {
            s.pending = .plan(d.plan)
        }

    case "event.todo_list_updated":
        if let d = try? event.data.decode(EventTodoListUpdated.self) {
            s.todos = d.todos
        }

    case "event.replay":
        if let d = try? event.data.decode(EventReplay.self) {
            applyReplay(&s, d)
        }

    case "event.error":
        let msg = (try? event.data.decode(EventError.self))?.message ?? "unknown error"
        s.items.append(.notice(id: UUID(), text: "Error: \(msg)"))
        s.pending = nil
        endStreaming(&s)

    case "event.title_updated":
        if let d = try? event.data.decode(EventTitleUpdated.self) {
            s.title = d.title
        }

    case "event.agent_done":
        endStreaming(&s)

    case "event.clear":
        let keptThread = s.threadID
        s = TranscriptState()
        s.threadID = keptThread

    default:
        break
    }
}

// MARK: - Private helpers

private func appendAssistant(_ s: inout TranscriptState, _ text: String) {
    guard !text.isEmpty else { return }
    if let id = s.streamingAssistantID,
       let idx = s.items.firstIndex(where: { $0.id == id }),
       case .assistant(_, let current) = s.items[idx] {
        s.items[idx] = .assistant(id: id, text: current + text)
    } else {
        let id = UUID()
        s.streamingAssistantID = id
        s.items.append(.assistant(id: id, text: text))
    }
}

private func appendThinking(_ s: inout TranscriptState, _ text: String) {
    guard !text.isEmpty else { return }
    if let id = s.streamingThinkingID,
       let idx = s.items.firstIndex(where: { $0.id == id }),
       case .thinking(_, let current) = s.items[idx] {
        s.items[idx] = .thinking(id: id, text: current + text)
    } else {
        let id = UUID()
        s.streamingThinkingID = id
        s.items.append(.thinking(id: id, text: text))
    }
}

private func applyToolResult(_ s: inout TranscriptState, _ d: EventToolResult) {
    if let idx = s.items.lastIndex(where: {
        if case .tool(_, let r) = $0 { return r.toolID == d.toolId && !r.done }
        return false
    }), case .tool(let id, var row) = s.items[idx] {
        row.output = d.output
        row.isError = d.isError
        row.done = true
        s.items[idx] = .tool(id: id, row)
    } else {
        // Result with no matching call — surface it rather than drop it.
        s.items.append(.tool(id: UUID(), ToolRow(
            toolID: d.toolId, name: d.name, summary: "", output: d.output, isError: d.isError, done: true)))
    }
}

private func endStreaming(_ s: inout TranscriptState) {
    s.isStreaming = false
    s.streamingAssistantID = nil
    s.streamingThinkingID = nil
}

/// A user-facing banner when an event indicates the thread can't proceed
/// (already open elsewhere, or gone), else nil. Pure — unit-tested headlessly.
public func connectionBanner(for event: ThreadEvent) -> String? {
    guard event.type == "event.error",
          let error = try? event.data.decode(EventError.self) else { return nil }
    switch error.code {
    case "thread_busy": return "This thread is already open in another window."
    case "thread_not_found": return "This thread no longer exists."
    default: return nil
    }
}

// applyReplay rebuilds the transcript from a persisted thread (event.replay,
// emitted once right after thread_started when attaching).
private func applyReplay(_ s: inout TranscriptState, _ d: EventReplay) {
    s.items.removeAll()
    s.streamingAssistantID = nil
    s.streamingThinkingID = nil

    for message in d.messages {
        for block in message.blocks {
            switch block.kind {
            case "text":
                let text = block.text ?? ""
                if message.role == "user" {
                    s.items.append(.user(id: UUID(), text: text))
                } else {
                    s.items.append(.assistant(id: UUID(), text: text))
                }
            case "thinking":
                s.items.append(.thinking(id: UUID(), text: block.text ?? ""))
            case "tool_use":
                s.items.append(.tool(id: UUID(), ToolRow(
                    toolID: block.toolId ?? "", name: block.toolName ?? "", summary: "")))
            case "tool_result":
                applyToolResult(&s, EventToolResult(
                    isError: block.isError ?? false,
                    name: block.toolName ?? "",
                    output: block.output ?? "",
                    toolId: block.toolId ?? ""))
            case "retry":
                s.items.append(.notice(id: UUID(), text: block.text ?? "retrying…"))
            default:
                break
            }
        }
    }

    if let todos = d.todos { s.todos = todos }
    if let title = d.title { s.title = title }
    s.isStreaming = false
}
