import Foundation
import VixClient
import VixProtocol

// vix-mac-probe — a headless proof that the Swift client speaks the vix daemon
// protocol end to end: it opens a real thread against a running vixd, streams a
// turn, and answers permission/question/plan round-trips (auto-approving, like
// the Go headless runner). This is the Phase-2 validation before any UI.
//
// Usage:
//   vix-mac-probe [--socket PATH] [--cwd DIR] [--model SPEC] "your prompt"
//
// Env: VIXD_SOCK overrides the socket path; VIX_AUTH_TOKEN sets the auth token.

struct Options {
    var socket: String = VixThreadClient.defaultSocketPath
    var cwd: String = FileManager.default.currentDirectoryPath
    var model: String = ""
    var prompt: String = "Say hello in one short sentence."
}

func parseArgs() -> Options {
    var opts = Options()
    var args = Array(CommandLine.arguments.dropFirst())
    var positional: [String] = []
    while !args.isEmpty {
        let a = args.removeFirst()
        switch a {
        case "--socket": if !args.isEmpty { opts.socket = args.removeFirst() }
        case "--cwd": if !args.isEmpty { opts.cwd = args.removeFirst() }
        case "--model": if !args.isEmpty { opts.model = args.removeFirst() }
        case "-h", "--help":
            print("usage: vix-mac-probe [--socket PATH] [--cwd DIR] [--model SPEC] \"prompt\"")
            exit(0)
        default: positional.append(a)
        }
    }
    if !positional.isEmpty { opts.prompt = positional.joined(separator: " ") }
    return opts
}

func stderr(_ s: String) { FileHandle.standardError.write(Data((s + "\n").utf8)) }

@main
struct Probe {
    static func main() async {
        let opts = parseArgs()
        let token = ProcessInfo.processInfo.environment["VIX_AUTH_TOKEN"]
        let client = VixThreadClient(socketPath: opts.socket, authToken: token)

        // Handshake: confirm the daemon is up and learn its version.
        let ping: PingResult
        do {
            ping = try client.ping()
        } catch {
            stderr("cannot reach vixd at \(opts.socket): \(error)")
            stderr("start it with `vix daemon start` (or set VIXD_SOCK).")
            exit(1)
        }
        guard ping.ok else {
            stderr("vixd ping failed at \(opts.socket)")
            exit(1)
        }
        stderr("connected to vixd \(ping.version) at \(opts.socket)")

        do {
            let events = try client.start(cwd: opts.cwd, model: opts.model)
            try client.sendInput(opts.prompt)
            stderr("> \(opts.prompt)")

            for try await ev in events {
                switch ev.type {
                case "event.stream_chunk":
                    let c = try ev.data.decode(EventStreamChunk.self)
                    FileHandle.standardOutput.write(Data(c.text.utf8))

                case "event.tool_call":
                    let t = try ev.data.decode(EventToolCall.self)
                    stderr("\n[tool] \(t.name): \(t.summary)")

                case "event.tool_result":
                    let r = try ev.data.decode(EventToolResult.self)
                    if r.isError { stderr("[tool error] \(r.name): \(r.output)") }

                case "event.confirm_request":
                    let r = try ev.data.decode(EventConfirmRequest.self)
                    stderr("[confirm] auto-approving \(r.toolName)")
                    try client.sendConfirm(approved: true)

                case "event.user_question":
                    let q = try ev.data.decode(EventUserQuestion.self)
                    let answer = q.options.first ?? ""
                    stderr("[question] auto-answering: \(answer)")
                    try client.sendUserAnswer(answer)

                case "event.plan_proposed":
                    stderr("[plan] auto-approving")
                    try client.sendPlanAction("approve")

                case "event.error":
                    let e = try ev.data.decode(EventError.self)
                    stderr("\n[error] \(e.message)\(e.code.map { " (\($0))" } ?? "")")

                case "event.agent_done":
                    print("")
                    client.closeThread()
                    return

                case "event.quit":
                    client.closeThread()
                    return

                default:
                    break
                }
            }
        } catch {
            stderr("thread error: \(error)")
            exit(1)
        }
    }
}
