import Foundation

/// A rendered segment of assistant text: either inline markdown prose or a
/// fenced code block.
public enum MarkdownSegment: Equatable, Sendable, Identifiable {
    case text(String)
    case code(language: String?, code: String)

    public var id: String {
        switch self {
        case .text(let t): return "t:\(t.hashValue)"
        case .code(let lang, let c): return "c:\(lang ?? ""):\(c.hashValue)"
        }
    }
}

/// Split assistant text into prose and fenced (```) code blocks. Pure and total:
/// an unterminated fence (mid-stream) still yields a code segment with whatever
/// has arrived so far, so streaming renders progressively.
public func splitMarkdown(_ input: String) -> [MarkdownSegment] {
    var segments: [MarkdownSegment] = []
    let lines = input.components(separatedBy: "\n")
    var textBuffer: [String] = []

    func flushText() {
        guard !textBuffer.isEmpty else { return }
        let text = textBuffer.joined(separator: "\n")
        textBuffer.removeAll()
        if !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            segments.append(.text(text))
        }
    }

    var i = 0
    while i < lines.count {
        let line = lines[i]
        if line.hasPrefix("```") {
            flushText()
            let lang = String(line.dropFirst(3)).trimmingCharacters(in: .whitespaces)
            var codeLines: [String] = []
            i += 1
            while i < lines.count, !lines[i].hasPrefix("```") {
                codeLines.append(lines[i])
                i += 1
            }
            if i < lines.count { i += 1 } // consume the closing fence
            segments.append(.code(language: lang.isEmpty ? nil : lang, code: codeLines.joined(separator: "\n")))
        } else {
            textBuffer.append(line)
            i += 1
        }
    }
    flushText()
    return segments
}
