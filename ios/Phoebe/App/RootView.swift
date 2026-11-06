import SwiftData
import SwiftUI

struct RootView: View {
    @Environment(AppModel.self) private var app
    @Environment(\.scenePhase) private var scenePhase

    var body: some View {
        content
            .task { await app.start() }
            .onChange(of: scenePhase) { _, phase in app.scenePhaseChanged(phase) }
    }

    @ViewBuilder private var content: some View {
        switch app.session {
        case .signedOut, .onboarding:
            if let container = app.modelContainer {
                OnboardingView().modelContainer(container)
            } else {
                OnboardingView()
            }
        case .signedIn(let user):
            if let container = app.modelContainer {
                SignedInRootView()
                    .modelContainer(container)
                    .task(id: user.id) { await app.runSignedInLaunchTasks() }
            } else {
                ContentUnavailableView {
                    Label { Text(L("common.storeError.title")) } icon: { Image(systemName: "exclamationmark.triangle") }
                } description: {
                    Text(L("common.storeError.body"))
                }
            }
        }
    }
}

struct SignedInRootView: View {
    @Environment(AppModel.self) private var app

    var body: some View {
        @Bindable var app = app
        NavigationStack(path: $app.path) {
            LibraryView()
                .navigationDestination(for: Route.self) { route in
                    switch route {
                    case .meeting(let id): MeetingDetailView(meetingID: id)
                    case .editor(let meetingID, let kind): DocumentEditorView(meetingID: meetingID, kind: kind)
                    case .settings: SettingsView()
                    }
                }
        }
        .fullScreenCover(isPresented: $app.isRecordingPresented) {
            RecordingView()
                .toastHost(app.toasts)
        }
    }
}
