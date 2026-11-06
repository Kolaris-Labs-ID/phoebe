import Foundation

extension Services {
    @MainActor
    static func live(_ env: ServiceEnvironment) -> Services {
        Services(
            recorder: RecordingEngine(env: env),
            uploads: UploadManager(env: env),
            sync: SyncEngine(env: env),
            poller: Poller(env: env),
            events: EventQueue(env: env)
        )
    }
}
