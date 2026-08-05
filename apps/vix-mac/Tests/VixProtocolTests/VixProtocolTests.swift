import Foundation
import Testing
@testable import VixClient
import VixProtocol

#if canImport(Darwin)
import Darwin
#elseif canImport(Glibc)
import Glibc
#endif

// MARK: NDJSON framing (over a socketpair, no daemon)

@Test func framingRoundTripsMultipleMessages() throws {
    var fds: [Int32] = [0, 0]
    #if canImport(Glibc)
    let streamType = Int32(SOCK_STREAM.rawValue)
    #else
    let streamType = SOCK_STREAM
    #endif
    let rc = fds.withUnsafeMutableBufferPointer { socketpair(AF_UNIX, streamType, 0, $0.baseAddress) }
    #expect(rc == 0)

    let writer = VixSocket(fd: fds[0])
    let reader = VixSocket(fd: fds[1])
    defer { writer.close(); reader.close() }

    // Two messages, including one containing an embedded newline in a string.
    let a = try JSONEncoder().encode(JSONValue.object(["n": .int(1), "s": .string("hello")]))
    let b = try JSONEncoder().encode(JSONValue.object(["n": .int(2), "s": .string("line\nbreak")]))
    try writer.writeLine(a)
    try writer.writeLine(b)

    let got1 = try JSONDecoder().decode(JSONValue.self, from: reader.readLine())
    let got2 = try JSONDecoder().decode(JSONValue.self, from: reader.readLine())
    #expect(got1.objectValue?["n"]?.intValue == 1)
    #expect(got1.objectValue?["s"]?.stringValue == "hello")
    #expect(got2.objectValue?["n"]?.intValue == 2)
    #expect(got2.objectValue?["s"]?.stringValue == "line\nbreak")
}

// MARK: Handshake / command envelope

@Test func threadStartEnvelopeHasVersionAndCwd() throws {
    let client = VixThreadClient(socketPath: "/tmp/unused.sock", authToken: nil)
    let payload = ThreadStartData(
        clientVersion: "v9.9.9",
        cwd: "/work",
        enableAutomaticDirectoryAccess: false,
        enableAutomaticWritePermission: false,
        forceInit: false,
        headless: true,
        model: "")
    let data = try client.commandData(type: "thread.start", payload: payload)
    let env = try JSONDecoder().decode(JSONValue.self, from: data).objectValue

    #expect(env?["type"]?.stringValue == "thread.start")
    #expect(env?["auth_token"] == nil) // omitted when nil
    let d = env?["data"]?.objectValue
    #expect(d?["client_version"]?.stringValue == "v9.9.9")
    #expect(d?["cwd"]?.stringValue == "/work")
    #expect(d?["headless"]?.boolValue == true)
}

@Test func authTokenIsStampedWhenPresent() throws {
    let client = VixThreadClient(socketPath: "/tmp/unused.sock", authToken: "secret")
    let data = try client.commandData(type: "thread.input", payload: ThreadInputData(text: "hi"))
    let env = try JSONDecoder().decode(JSONValue.self, from: data).objectValue
    #expect(env?["auth_token"]?.stringValue == "secret")
    #expect(env?["data"]?.objectValue?["text"]?.stringValue == "hi")
}

// MARK: Event payload decoding (generated models)

@Test func decodeStreamChunkFromEnvelope() throws {
    let json = #"{"type":"event.stream_chunk","data":{"text":"hello world"}}"#
    let ev = try JSONDecoder().decode(ThreadEvent.self, from: Data(json.utf8))
    #expect(ev.type == "event.stream_chunk")
    #expect(try ev.data.decode(EventStreamChunk.self).text == "hello world")
}

@Test func decodeConfirmRequestWithParamsAndDirs() throws {
    let json = """
    {"type":"event.confirm_request","data":{
      "tool_name":"write_file",
      "params":{"path":"/x/y.txt","content":"hi"},
      "requested_dirs":["/x"],
      "detail":"```diff\\n+hi\\n```"
    }}
    """
    let ev = try JSONDecoder().decode(ThreadEvent.self, from: Data(json.utf8))
    let cr = try ev.data.decode(EventConfirmRequest.self)
    #expect(cr.toolName == "write_file")
    #expect(cr.requestedDirs == ["/x"])
    #expect(cr.params["path"]?.stringValue == "/x/y.txt")
}

@Test func decodeUserQuestionBatchForm() throws {
    let json = """
    {"type":"event.user_question","data":{
      "question":"", "options":[],
      "questions":[
        {"id":"q1","category":"Lang","question":"Which language?","options":["Go","Swift"]}
      ]
    }}
    """
    let ev = try JSONDecoder().decode(ThreadEvent.self, from: Data(json.utf8))
    let q = try ev.data.decode(EventUserQuestion.self)
    #expect(q.questions?.count == 1)
    #expect(q.questions?.first?.options == ["Go", "Swift"])
}

@Test func payloadlessEventDataDecodesAsNull() throws {
    let json = #"{"type":"event.agent_done","data":null}"#
    let ev = try JSONDecoder().decode(ThreadEvent.self, from: Data(json.utf8))
    #expect(ev.type == "event.agent_done")
    #expect(ev.data.isNull)
}

// MARK: RPC projection decoding (generated ThreadSummary)

@Test func decodeThreadSummaryList() throws {
    let json = """
    [
      {"id":"s1","cwd":"/work","model":"anthropic/x","title":"Hello","first_message":"hi","unread":true,"origin":""},
      {"id":"s2","cwd":"/w","model":"","origin":"vix","job_status":"ok","trigger":{"type":"cron","ref":"job-1"}}
    ]
    """
    let list = try JSONDecoder().decode([ThreadSummary].self, from: Data(json.utf8))
    #expect(list.count == 2)
    #expect(list[0].displayTitle == "Hello")
    #expect(list[0].unread == true)
    #expect(list[1].isVixInitiated == true)
    #expect(list[1].displayTitle == "s2") // no title/first_message → falls back to id
    #expect(list[1].trigger?.ref == "job-1")
}
