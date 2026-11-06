import SwiftUI

@main
struct PhoebeApp: App {
    @UIApplicationDelegateAdaptor(AppDelegate.self) private var delegate
    @State private var appModel = AppModel()

    var body: some Scene {
        WindowGroup {
            RootView()
                .environment(appModel)
                .toastHost(appModel.toasts)
                .tint(Palette.accent)
                .labeledContentStyle(AdaptiveLabeledContentStyle())
        }
    }
}
