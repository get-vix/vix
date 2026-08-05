import SwiftUI
import VixMacCore
import VixProtocol

/// Presents the active blocking round-trip (permission / question / plan) as a
/// modal sheet. Driven by `model.state.pending`; each sheet answers via the
/// model, which clears `pending` and replies to the daemon.
struct InteractionSheet: View {
    let model: ThreadModel

    var body: some View {
        switch model.state.pending {
        case .confirm(let req):
            ConfirmSheet(model: model, request: req)
        case .question(let q):
            QuestionSheet(model: model, question: q)
        case .plan(let plan):
            PlanSheet(model: model, plan: plan)
        case .none:
            EmptyView()
        }
    }
}

// MARK: Permission

private struct ConfirmSheet: View {
    let model: ThreadModel
    let request: EventConfirmRequest
    @State private var rememberDirs = false

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Permission required").font(.headline)
            Text(request.toolName).font(.callout.monospaced()).bold()

            if !request.params.isEmpty {
                VStack(alignment: .leading, spacing: 2) {
                    ForEach(request.params.sorted(by: { $0.key < $1.key }), id: \.key) { key, value in
                        Text("\(key): \(jsonString(value))")
                            .font(.caption.monospaced())
                            .foregroundStyle(.secondary)
                            .lineLimit(3)
                    }
                }
            }

            if let dirs = request.requestedDirs, !dirs.isEmpty {
                Text("Requests access to: \(dirs.joined(separator: ", "))")
                    .font(.caption).foregroundStyle(.secondary)
                Toggle("Remember this directory", isOn: $rememberDirs)
                    .font(.caption)
            }

            if let detail = request.detail, !detail.isEmpty {
                ScrollView {
                    Text(detail).font(.caption.monospaced()).textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
                .frame(maxHeight: 200)
                .background(.quaternary.opacity(0.3), in: RoundedRectangle(cornerRadius: 6))
            }

            HStack {
                Button("Deny", role: .cancel) { model.answerConfirm(approved: false) }
                Spacer()
                Button("Approve") { model.answerConfirm(approved: true, persistDirs: rememberDirs) }
                    .keyboardShortcut(.defaultAction)
            }
        }
        .padding(20)
        .frame(width: 460)
    }
}

// MARK: Question

private struct QuestionSheet: View {
    let model: ThreadModel
    let question: EventUserQuestion
    @State private var text = ""
    @State private var batch: [String: String] = [:]

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Question").font(.headline)

            if let questions = question.questions, !questions.isEmpty {
                ForEach(questions, id: \.id) { q in
                    VStack(alignment: .leading, spacing: 4) {
                        Text(q.question).font(.callout)
                        Picker("", selection: binding(for: q)) {
                            ForEach(q.options ?? [], id: \.self) { Text($0).tag($0) }
                        }
                        .labelsHidden()
                    }
                }
                Button("Submit") { submitBatch(questions) }
                    .keyboardShortcut(.defaultAction)
            } else {
                Text(question.question).font(.callout)
                ForEach(question.options, id: \.self) { option in
                    Button(option) { model.answerQuestion(option) }
                        .frame(maxWidth: .infinity)
                }
                if question.options.isEmpty || question.placeholder != nil {
                    TextField(question.placeholder ?? "Your answer", text: $text)
                    Button("Send") {
                        model.answerQuestion(text.isEmpty ? (question.options.first ?? "") : text, text: text)
                    }
                    .keyboardShortcut(.defaultAction)
                }
            }
        }
        .padding(20)
        .frame(width: 460)
    }

    private func binding(for q: QuestionDef) -> Binding<String> {
        Binding(
            get: { batch[q.id] ?? q.options?.first ?? "" },
            set: { batch[q.id] = $0 })
    }

    private func submitBatch(_ questions: [QuestionDef]) {
        var answers = batch
        for q in questions where answers[q.id] == nil {
            answers[q.id] = q.options?.first ?? ""
        }
        model.answerQuestionBatch(answers)
    }
}

// MARK: Plan

private struct PlanSheet: View {
    let model: ThreadModel
    let plan: Plan?
    @State private var feedback = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Plan proposed").font(.headline)

            if let plan {
                Text(plan.name).font(.title3).bold()
                ScrollView {
                    VStack(alignment: .leading, spacing: 8) {
                        Text(plan.context).font(.callout).foregroundStyle(.secondary)
                        ForEach(plan.tasks, id: \.id) { task in
                            Text("• \(task.title)").font(.callout)
                        }
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                }
                .frame(maxHeight: 260)
            }

            TextField("Request changes (optional)", text: $feedback)

            HStack {
                Button("Reject", role: .cancel) { model.answerPlan("reject") }
                Spacer()
                if !feedback.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                    Button("Request changes") { model.answerPlan("modify", text: feedback) }
                }
                Button("Approve") { model.answerPlan("approve") }
                    .keyboardShortcut(.defaultAction)
            }
        }
        .padding(20)
        .frame(width: 480)
    }
}

// MARK: Helpers

/// Render a JSONValue compactly for display in the permission sheet.
private func jsonString(_ value: JSONValue) -> String {
    switch value {
    case .null: return "null"
    case .bool(let b): return String(b)
    case .int(let i): return String(i)
    case .double(let d): return String(d)
    case .string(let s): return s
    case .array, .object:
        if let data = try? JSONEncoder().encode(value), let s = String(data: data, encoding: .utf8) {
            return s
        }
        return ""
    }
}
