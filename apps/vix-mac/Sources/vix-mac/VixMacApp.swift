import SwiftUI
import VixMacCore
import VixClient
import VixProtocol

@main
struct VixMacApp: App {
    @State private var app = AppModel()

    init() {
        // Help `swift run` (no bundle) show a normal, focusable window.
        NSApplication.shared.setActivationPolicy(.regular)
    }

    var body: some Scene {
        WindowGroup {
            RootView(app: app)
                .frame(minWidth: 900, minHeight: 560)
        }
        .windowStyle(.titleBar)
    }
}

/// Split view: thread sidebar + active chat detail.
struct RootView: View {
    @Bindable var app: AppModel

    var body: some View {
        NavigationSplitView {
            ThreadListView(app: app)
                .navigationSplitViewColumnWidth(min: 220, ideal: 260)
        } detail: {
            if let model = app.active {
                ContentView(model: model)
            } else {
                ContentUnavailableView(
                    "No thread",
                    systemImage: "bubble.left.and.bubble.right",
                    description: Text("Create a new thread to start."))
            }
        }
        .task {
            app.refresh()
            if app.active == nil { app.newThread() }
        }
    }
}
