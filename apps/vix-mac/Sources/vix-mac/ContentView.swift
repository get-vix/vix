import SwiftUI
import VixMacCore

struct ContentView: View {
    @Bindable var model: ThreadModel

    var body: some View {
        HStack(spacing: 0) {
            VStack(spacing: 0) {
                header
                Divider()
                if let banner = model.banner {
                    HStack(spacing: 6) {
                        Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.orange)
                        Text(banner).font(.callout)
                        Spacer()
                    }
                    .padding(.horizontal, 12)
                    .padding(.vertical, 6)
                    .background(.orange.opacity(0.12))
                    Divider()
                }
                transcript
                Divider()
                inputBar
            }
            if !model.state.todos.isEmpty {
                Divider()
                TodoPanelView(todos: model.state.todos)
                    .frame(width: 240)
            }
        }
        .sheet(isPresented: pendingBinding) {
            InteractionSheet(model: model).interactiveDismissDisabled()
        }
    }

    // Presented whenever a blocking round-trip is awaiting the user. The setter
    // is a no-op: the sheet dismisses by answering, which clears model.pending.
    private var pendingBinding: Binding<Bool> {
        Binding(get: { model.state.pending != nil }, set: { _ in })
    }

    // MARK: Header

    private var header: some View {
        HStack(spacing: 8) {
            Circle()
                .fill(statusColor)
                .frame(width: 8, height: 8)
            Text(model.state.title.isEmpty ? "vix" : model.state.title)
                .font(.headline)
            Spacer()
            if case .failed = model.connection {
                Button("Reconnect") { model.retry() }
                    .buttonStyle(.borderless)
                    .font(.caption)
            }
            if let tokens = model.state.lastTokens {
                Text("↑\(tokens.input) ↓\(tokens.output)")
                    .font(.caption2.monospaced())
                    .foregroundStyle(.secondary)
                    .help("Tokens: input / output (last turn)")
            }
            Text(statusText)
                .font(.caption)
                .foregroundStyle(.secondary)
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
    }

    private var statusColor: Color {
        switch model.connection {
        case .connected: return .green
        case .connecting: return .yellow
        case .failed: return .red
        case .disconnected: return .gray
        }
    }

    private var statusText: String {
        switch model.connection {
        case .disconnected: return "disconnected"
        case .connecting: return "connecting…"
        case .connected(let v): return "vixd \(v)"
        case .failed(let m): return m
        }
    }

    // MARK: Transcript

    private var transcript: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 10) {
                    if model.state.items.isEmpty && !model.state.isStreaming {
                        Text("Send a message to start.")
                            .font(.callout)
                            .foregroundStyle(.secondary)
                            .frame(maxWidth: .infinity, alignment: .center)
                            .padding(.top, 40)
                    }
                    ForEach(model.state.items) { item in
                        row(for: item).id(item.id)
                    }
                    if model.state.isStreaming {
                        ProgressView().controlSize(.small).id("streaming")
                    }
                }
                .padding(12)
            }
            .onChange(of: model.state.items.count) {
                if let last = model.state.items.last {
                    withAnimation { proxy.scrollTo(last.id, anchor: .bottom) }
                }
            }
        }
    }

    @ViewBuilder
    private func row(for item: TranscriptItem) -> some View {
        switch item {
        case .user(_, let text):
            messageBubble(text, role: "You", color: .accentColor.opacity(0.15), align: .trailing)
        case .assistant(_, let text):
            assistantBubble(text)
        case .thinking(_, let text):
            DisclosureGroup {
                Text(text)
                    .font(.callout)
                    .foregroundStyle(.secondary)
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
            } label: {
                Label("Thinking", systemImage: "brain")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        case .tool(_, let tool):
            toolRow(tool)
        case .notice(_, let text):
            Text(text)
                .font(.callout)
                .foregroundStyle(.red)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
    }

    private func messageBubble(_ content: some View, role: String, color: Color, align: HorizontalAlignment) -> some View {
        VStack(alignment: align, spacing: 2) {
            Text(role).font(.caption2).foregroundStyle(.secondary)
            content
                .padding(8)
                .background(color, in: RoundedRectangle(cornerRadius: 8))
                .textSelection(.enabled)
        }
        .frame(maxWidth: .infinity, alignment: align == .trailing ? .trailing : .leading)
    }

    private func messageBubble(_ text: String, role: String, color: Color, align: HorizontalAlignment) -> some View {
        messageBubble(Text(text), role: role, color: color, align: align)
    }

    // Assistant bubble: prose rendered as inline markdown, fenced code blocks
    // rendered as monospaced, copyable blocks.
    private func assistantBubble(_ text: String) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            Text("vix").font(.caption2).foregroundStyle(.secondary)
            VStack(alignment: .leading, spacing: 8) {
                ForEach(Array(splitMarkdown(text).enumerated()), id: \.offset) { _, segment in
                    switch segment {
                    case .text(let t):
                        markdown(t)
                            .font(.callout)
                            .textSelection(.enabled)
                            .frame(maxWidth: .infinity, alignment: .leading)
                    case .code(let language, let code):
                        CodeBlockView(language: language, code: code)
                    }
                }
            }
            .padding(8)
            .background(.gray.opacity(0.12), in: RoundedRectangle(cornerRadius: 8))
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private func toolRow(_ tool: ToolRow) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            if tool.done, !tool.output.isEmpty {
                DisclosureGroup {
                    ScrollView(.horizontal, showsIndicators: false) {
                        Text(tool.output.prefix(4000))
                            .font(.caption.monospaced())
                            .foregroundStyle(.secondary)
                            .textSelection(.enabled)
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                } label: {
                    toolHeader(tool)
                }
            } else {
                toolHeader(tool)
            }
        }
        .padding(8)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(.quaternary.opacity(0.4), in: RoundedRectangle(cornerRadius: 8))
    }

    private func toolHeader(_ tool: ToolRow) -> some View {
        HStack(spacing: 6) {
            Image(systemName: tool.done ? (tool.isError ? "xmark.circle" : "checkmark.circle") : "gearshape")
                .foregroundStyle(tool.isError ? .red : .secondary)
            Text(tool.name).font(.callout.monospaced()).bold()
            Text(tool.summary).font(.callout).foregroundStyle(.secondary).lineLimit(1)
        }
    }

    private func markdown(_ text: String) -> Text {
        if let attr = try? AttributedString(markdown: text, options: .init(interpretedSyntax: .inlineOnlyPreservingWhitespace)) {
            return Text(attr)
        }
        return Text(text)
    }

    // MARK: Input

    private var inputBar: some View {
        HStack(spacing: 8) {
            TextField("Message vix…", text: $model.inputText, axis: .vertical)
                .textFieldStyle(.plain)
                .lineLimit(1...6)
                .onSubmit { model.send() }
                .disabled(!isConnected)
            if model.state.isStreaming {
                Button(role: .cancel) { model.cancel() } label: {
                    Image(systemName: "stop.circle.fill")
                }
                .buttonStyle(.borderless)
            } else {
                Button { model.send() } label: {
                    Image(systemName: "arrow.up.circle.fill")
                }
                .buttonStyle(.borderless)
                .disabled(!isConnected || model.inputText.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
            }
        }
        .padding(10)
    }

    private var isConnected: Bool {
        if case .connected = model.connection { return true }
        return false
    }
}

/// A fenced code block: language label + copy button + horizontally-scrollable
/// monospaced body.
struct CodeBlockView: View {
    let language: String?
    let code: String

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack {
                Text(language ?? "code")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
                Spacer()
                Button {
                    NSPasteboard.general.clearContents()
                    NSPasteboard.general.setString(code, forType: .string)
                } label: {
                    Image(systemName: "doc.on.doc").font(.caption2)
                }
                .buttonStyle(.borderless)
                .help("Copy")
            }
            .padding(.horizontal, 8)
            .padding(.vertical, 4)
            .background(.black.opacity(0.06))

            ScrollView(.horizontal, showsIndicators: false) {
                Text(code)
                    .font(.caption.monospaced())
                    .textSelection(.enabled)
                    .padding(8)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
        .background(.black.opacity(0.04))
        .clipShape(RoundedRectangle(cornerRadius: 6))
        .overlay(RoundedRectangle(cornerRadius: 6).stroke(.quaternary))
    }
}
