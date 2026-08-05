import Foundation
import Testing
@testable import VixMacCore
import VixProtocol

private func event(_ type: String, _ data: JSONValue) -> ThreadEvent {
    ThreadEvent(data: data, type: type)
}

@Test func streamChunksAccumulateIntoOneAssistantMessage() {
    var s = TranscriptState()
    reduce(&s, event("event.stream_chunk", .object(["text": .string("Hello, ")])))
    reduce(&s, event("event.stream_chunk", .object(["text": .string("world")])))
    #expect(s.assistantText == "Hello, world")
    #expect(s.items.count == 1)
    #expect(s.isStreaming == true)
}

@Test func streamDoneRecordsTokensAndEndsStreaming() {
    var s = TranscriptState()
    reduce(&s, event("event.stream_chunk", .object(["text": .string("hi")])))
    reduce(&s, event("event.stream_done", .object([
        "input_tokens": .int(10), "output_tokens": .int(3),
        "cache_creation_tokens": .int(0), "cache_read_tokens": .int(0),
        "elapsed_ms": .int(42),
    ])))
    #expect(s.isStreaming == false)
    #expect(s.lastTokens?.input == 10)
    #expect(s.lastTokens?.output == 3)
}

@Test func toolResultMatchesCallByToolID() {
    var s = TranscriptState()
    reduce(&s, event("event.tool_call", .object([
        "tool_id": .string("t1"), "name": .string("bash"), "summary": .string("ls"),
    ])))
    reduce(&s, event("event.tool_result", .object([
        "tool_id": .string("t1"), "name": .string("bash"),
        "output": .string("file.txt"), "is_error": .bool(false),
    ])))
    #expect(s.toolRows.count == 1)
    let row = s.toolRows[0]
    #expect(row.toolID == "t1")
    #expect(row.done == true)
    #expect(row.output == "file.txt")
    #expect(row.isError == false)
}

@Test func toolErrorFlagsRow() {
    var s = TranscriptState()
    reduce(&s, event("event.tool_call", .object([
        "tool_id": .string("t9"), "name": .string("bash"), "summary": .string("boom"),
    ])))
    reduce(&s, event("event.tool_result", .object([
        "tool_id": .string("t9"), "name": .string("bash"),
        "output": .string("exit 1"), "is_error": .bool(true),
    ])))
    #expect(s.toolRows[0].isError == true)
}

@Test func interleavedToolCallSplitsAssistantText() {
    var s = TranscriptState()
    reduce(&s, event("event.stream_chunk", .object(["text": .string("before")])))
    reduce(&s, event("event.tool_call", .object([
        "tool_id": .string("t1"), "name": .string("bash"), "summary": .string("x"),
    ])))
    reduce(&s, event("event.stream_chunk", .object(["text": .string("after")])))
    // Two separate assistant rows around the tool row.
    let assistantCount = s.items.filter { if case .assistant = $0 { return true } else { return false } }.count
    #expect(assistantCount == 2)
    #expect(s.assistantText == "beforeafter")
}

@Test func errorEventAppendsNoticeAndEndsStreaming() {
    var s = TranscriptState()
    reduce(&s, event("event.stream_chunk", .object(["text": .string("partial")])))
    reduce(&s, event("event.error", .object(["message": .string("boom")])))
    #expect(s.isStreaming == false)
    if case .notice(_, let text) = s.items.last {
        #expect(text.contains("boom"))
    } else {
        Issue.record("expected a notice item")
    }
}

@Test func threadStartedAndTitleUpdate() {
    var s = TranscriptState()
    reduce(&s, event("event.thread_started", .object([
        "thread_id": .string("sess-1"), "started_at": .string("2026-01-01T00:00:00Z"),
    ])))
    reduce(&s, event("event.title_updated", .object(["title": .string("My thread")])))
    #expect(s.threadID == "sess-1")
    #expect(s.title == "My thread")
}

@Test func clearResetsTranscriptButKeepsThread() {
    var s = TranscriptState()
    reduce(&s, event("event.thread_started", .object([
        "thread_id": .string("sess-1"), "started_at": .string("x"),
    ])))
    reduce(&s, event("event.stream_chunk", .object(["text": .string("stuff")])))
    reduce(&s, event("event.clear", .null))
    #expect(s.items.isEmpty)
    #expect(s.threadID == "sess-1")
}

@Test func agentDoneEndsStreaming() {
    var s = TranscriptState()
    reduce(&s, event("event.stream_chunk", .object(["text": .string("hi")])))
    reduce(&s, event("event.agent_done", .null))
    #expect(s.isStreaming == false)
}

// MARK: Interactive round-trips (pending state)

@Test func confirmRequestSetsPending() {
    var s = TranscriptState()
    reduce(&s, event("event.confirm_request", .object([
        "tool_name": .string("write_file"),
        "params": .object(["path": .string("/x/y.txt")]),
    ])))
    if case .confirm(let req)? = s.pending {
        #expect(req.toolName == "write_file")
        #expect(req.params["path"]?.stringValue == "/x/y.txt")
    } else {
        Issue.record("expected pending .confirm")
    }
}

@Test func userQuestionSetsPending() {
    var s = TranscriptState()
    reduce(&s, event("event.user_question", .object([
        "question": .string("Pick one"),
        "options": .array([.string("a"), .string("b")]),
    ])))
    if case .question(let q)? = s.pending {
        #expect(q.options == ["a", "b"])
    } else {
        Issue.record("expected pending .question")
    }
}

@Test func planProposedSetsPending() {
    var s = TranscriptState()
    reduce(&s, event("event.plan_proposed", .object([
        "plan": .object([
            "name": .string("My plan"),
            "context": .string("ctx"),
            "tasks": .array([]),
            "current_idx": .int(0),
        ]),
    ])))
    if case .plan(let p)? = s.pending {
        #expect(p?.name == "My plan")
    } else {
        Issue.record("expected pending .plan")
    }
}

@Test func errorClearsPending() {
    var s = TranscriptState()
    reduce(&s, event("event.confirm_request", .object([
        "tool_name": .string("bash"), "params": .object([:]),
    ])))
    #expect(s.pending != nil)
    reduce(&s, event("event.error", .object(["message": .string("boom")])))
    #expect(s.pending == nil)
}

// MARK: Connection banner mapping

@Test func threadBusyErrorMapsToBanner() {
    let ev = event("event.error", .object([
        "message": .string("thread is already open in another window"),
        "code": .string("thread_busy"),
    ]))
    #expect(connectionBanner(for: ev)?.contains("another window") == true)
}

@Test func plainErrorHasNoBanner() {
    let ev = event("event.error", .object(["message": .string("boom")]))
    #expect(connectionBanner(for: ev) == nil)
}

@Test func nonErrorEventHasNoBanner() {
    let ev = event("event.stream_chunk", .object(["text": .string("hi")]))
    #expect(connectionBanner(for: ev) == nil)
}

// MARK: Todos & replay

@Test func todoListUpdateReplacesTodos() {
    var s = TranscriptState()
    reduce(&s, event("event.todo_list_updated", .object([
        "todos": .array([
            .object(["id": .string("t1"), "content": .string("do a"), "status": .string("in_progress")]),
            .object(["id": .string("t2"), "content": .string("do b"), "status": .string("pending")]),
        ]),
    ])))
    #expect(s.todos.count == 2)
    #expect(s.todos.first?.content == "do a")
    #expect(s.todos.first?.status == "in_progress")
}

@Test func replayRebuildsTranscriptTodosAndTitle() {
    var s = TranscriptState()
    // Pre-existing junk that replay must clear.
    reduce(&s, event("event.stream_chunk", .object(["text": .string("stale")])))

    reduce(&s, event("event.replay", .object([
        "messages": .array([
            .object([
                "role": .string("user"),
                "blocks": .array([.object(["kind": .string("text"), "text": .string("hello")])]),
            ]),
            .object([
                "role": .string("assistant"),
                "blocks": .array([
                    .object(["kind": .string("text"), "text": .string("hi there")]),
                    .object(["kind": .string("tool_use"), "tool_id": .string("t1"), "tool_name": .string("bash")]),
                    .object(["kind": .string("tool_result"), "tool_id": .string("t1"), "output": .string("done"), "is_error": .bool(false)]),
                ]),
            ]),
        ]),
        "todos": .array([
            .object(["id": .string("x"), "content": .string("task"), "status": .string("completed")]),
        ]),
        "title": .string("Resumed thread"),
    ])))

    #expect(s.title == "Resumed thread")
    #expect(s.todos.count == 1)
    #expect(s.assistantText == "hi there")
    // user + assistant text + one tool row (call+result merged)
    #expect(s.toolRows.count == 1)
    #expect(s.toolRows.first?.done == true)
    #expect(s.toolRows.first?.output == "done")
    // no leftover "stale" chunk
    #expect(s.assistantText.contains("stale") == false)
}
