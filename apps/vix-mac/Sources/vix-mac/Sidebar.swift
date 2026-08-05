import SwiftUI
import VixMacCore
import VixClient
import VixProtocol

/// The thread sidebar, grouped into user threads and vix-initiated runs.
struct ThreadListView: View {
    @Bindable var app: AppModel

    var body: some View {
        List(selection: selection) {
            let userThreads = app.threads.filter { !$0.isVixInitiated }
            let vixThreads = app.threads.filter { $0.isVixInitiated }

            if !userThreads.isEmpty {
                Section("Threads") {
                    ForEach(userThreads) { row($0) }
                }
            }
            if !vixThreads.isEmpty {
                Section("Vix-initiated") {
                    ForEach(vixThreads) { row($0) }
                }
            }
        }
        .navigationTitle("vix")
        .toolbar {
            ToolbarItem {
                Button { app.newThread() } label: { Image(systemName: "square.and.pencil") }
                    .help("New thread")
            }
            ToolbarItem {
                Button { app.refresh() } label: { Image(systemName: "arrow.clockwise") }
                    .help("Refresh threads")
            }
        }
    }

    private var selection: Binding<String?> {
        Binding(
            get: { app.selectedID },
            set: { id in
                // Defer out of the view-update cycle: opening mutates AppModel
                // state and must not run synchronously inside the List's binding.
                if let id, let summary = app.threads.first(where: { $0.id == id }) {
                    Task { @MainActor in app.open(summary) }
                }
            })
    }

    private func row(_ summary: ThreadSummary) -> some View {
        HStack(spacing: 6) {
            VStack(alignment: .leading, spacing: 2) {
                Text(summary.displayTitle).lineLimit(1)
                if !summary.model.isEmpty {
                    Text(summary.model).font(.caption2).foregroundStyle(.secondary).lineLimit(1)
                }
            }
            Spacer()
            if summary.unread == true {
                Circle().fill(.blue).frame(width: 7, height: 7)
            }
        }
        .tag(summary.id)
    }
}

/// The todo panel (right column of the chat pane).
struct TodoPanelView: View {
    let todos: [TodoItem]

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Todos").font(.headline)
            ForEach(todos, id: \.id) { todo in
                HStack(alignment: .top, spacing: 6) {
                    Image(systemName: icon(todo.status))
                        .foregroundStyle(color(todo.status))
                    Text(todo.content)
                        .font(.callout)
                        .strikethrough(todo.status == "completed")
                        .foregroundStyle(todo.status == "completed" ? .secondary : .primary)
                }
            }
            Spacer()
        }
        .padding(12)
        .frame(maxHeight: .infinity, alignment: .top)
        .background(.quaternary.opacity(0.15))
    }

    private func icon(_ status: String) -> String {
        switch status {
        case "completed": return "checkmark.circle.fill"
        case "in_progress": return "circle.dotted"
        default: return "circle"
        }
    }

    private func color(_ status: String) -> Color {
        switch status {
        case "completed": return .green
        case "in_progress": return .blue
        default: return .secondary
        }
    }
}
