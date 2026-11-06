import AuthenticationServices
import Foundation
import os
import SwiftData
import SwiftUI

enum OnboardingStep: Int, Sendable, CaseIterable {
    case welcome, privacy, signIn, language
}

enum SessionState: Equatable, Sendable {
    case signedOut
    case onboarding(OnboardingStep)
    case signedIn(UserDTO)
}

enum SignOutReason: Sendable, Equatable {
    case userInitiated, revoked, unauthenticated, accountDeleted
}

enum Route: Hashable, Sendable {
    case meeting(UUID)
    case editor(meetingID: UUID, kind: DocumentKind)
    case settings
}

@MainActor
private final class UnauthenticatedRelay {
    weak var target: AppModel?
    func fire() { target?.handleUnauthenticated() }
}

@MainActor @Observable
final class AppModel {
    private static let logger = Logger(subsystem: "com.kolarislabs.phoebe", category: "app")
    private enum DefaultsKey {
        static let onboardingCompleted = "phoebe.onboardingCompleted"
        static let currentUser = "phoebe.currentUser"
        static let serverConfig = "phoebe.serverConfig"
        static let usage = "phoebe.usage"
        static let usageFetchedAt = "phoebe.usageFetchedAt"
    }

    var session: SessionState
    private(set) var user: UserDTO?
    var signOutReason: SignOutReason?
    var path: [Route] = []
    var isRecordingPresented = false
    var pendingRecovery: LocalRecording?
    private(set) var serverConfig: ServerConfig?
    private(set) var usage: UsageDTO?
    private(set) var usageFetchedAt: Date?
    private(set) var modelContainer: ModelContainer?
    private(set) var storeError = false
    private(set) var services: Services = .placeholder()

    @ObservationIgnored let api: APIClient
    @ObservationIgnored let auth: AuthStore
    @ObservationIgnored private let defaults: UserDefaults
    @ObservationIgnored private var started = false
    @ObservationIgnored private var isSigningOut = false
    @ObservationIgnored private var revokeObserver: NSObjectProtocol?
    let connectivity = Connectivity()
    let toasts = ToastCenter()

    var isOnline: Bool { connectivity.isOnline }
    var recorder: any RecordingEngineAPI { services.recorder }
    var uploads: any UploadManaging { services.uploads }
    var sync: any SyncEngineAPI { services.sync }
    var poller: any PollingAPI { services.poller }
    var events: any EventQueueAPI { services.events }
    var onboardingCompleted: Bool { defaults.bool(forKey: DefaultsKey.onboardingCompleted) }
    var maxAudioDurationMs: Int { serverConfig?.maxAudioDurationMs ?? ServerConfig.defaultMaxAudioDurationMs }
    var modelContext: ModelContext? { modelContainer?.mainContext }

    init(
        auth: AuthStore = AuthStore(),
        session urlSession: URLSession = .shared,
        baseURL: URL? = APIClient.baseURLFromBundle(),
        defaults: UserDefaults = .standard
    ) {
        guard let baseURL, Self.isAcceptableBaseURL(baseURL) else {
            fatalError("PhoebeAPIBaseURL is missing or not https")
        }
        let relay = UnauthenticatedRelay()
        self.auth = auth
        self.defaults = defaults
        self.api = APIClient(
            baseURL: baseURL,
            session: urlSession,
            tokens: auth,
            onUnauthenticated: { Task { @MainActor in relay.fire() } }
        )
        self.serverConfig = Self.load(ServerConfig.self, key: DefaultsKey.serverConfig, from: defaults)
        self.usage = Self.load(UsageDTO.self, key: DefaultsKey.usage, from: defaults)
        self.usageFetchedAt = defaults.object(forKey: DefaultsKey.usageFetchedAt) as? Date

        if auth.tokens != nil, let userID = auth.userID,
           let user = Self.load(UserDTO.self, key: DefaultsKey.currentUser, from: defaults), user.id == userID {
            self.user = user
            self.session = defaults.bool(forKey: DefaultsKey.onboardingCompleted) ? .signedIn(user) : .onboarding(.language)
        } else {
            self.session = defaults.bool(forKey: DefaultsKey.onboardingCompleted) ? .signedOut : .onboarding(.welcome)
        }
        relay.target = self
        if let user { openStore(for: user) }
    }

    static func isAcceptableBaseURL(_ url: URL) -> Bool {
        #if DEBUG
        return url.scheme == "https" || url.scheme == "http"
        #else
        return url.scheme == "https"
        #endif
    }

    // MARK: Lifecycle

    func start() async {
        guard !started else { return }
        started = true
        connectivity.onChange = { [weak self] online in
            guard let self, online else { return }
            Task {
                await self.uploads.resumeAll()
                await self.sync.pushAll()
            }
        }
        connectivity.start()
        revokeObserver = NotificationCenter.default.addObserver(
            forName: ASAuthorizationAppleIDProvider.credentialRevokedNotification, object: nil, queue: .main
        ) { [weak self] _ in
            MainActor.assumeIsolated {
                guard let self, self.auth.signInMethod == .apple else { return }
                Task { await self.signOut(reason: .revoked, callServer: false) }
            }
        }
        await refreshConfig()
    }

    func runSignedInLaunchTasks() async {
        pendingRecovery = await recorder.recoverOnLaunch()
        await checkAppleCredentialState()
        guard case .signedIn = session else { return }
        await uploads.resumeAll()
        await sync.fullPull()
        await refreshUsage()
        await events.flush()
    }

    func scenePhaseChanged(_ phase: ScenePhase) {
        switch phase {
        case .active:
            poller.setForeground(true)
            poller.reset()
            if recorder.phase.hasLiveSession { isRecordingPresented = true }
            Task {
                await recorder.retryPendingMerges()
                await uploads.resumeAll()
                await events.flush()
            }
        case .background:
            poller.setForeground(false)
        default:
            break
        }
    }

    // MARK: Session

    func advanceOnboarding(to step: OnboardingStep) {
        session = .onboarding(step)
    }

    func didSignIn(_ response: AuthResponseDTO, method: SignInMethod, appleUserID: String? = nil) {
        auth.saveSession(tokens: response.tokens, userID: response.user.id, method: method)
        if let appleUserID { auth.set(appleUserID, for: .appleUserID) }
        auth.set(nil, for: .pendingDisplayName)
        signOutReason = nil
        updateUser(response.user)
        openStore(for: response.user)
        session = onboardingCompleted ? .signedIn(response.user) : .onboarding(.language)
    }

    func completeOnboarding() {
        defaults.set(true, forKey: DefaultsKey.onboardingCompleted)
        events.log(.onboardingCompleted, meetingID: nil)
        if let user { session = .signedIn(user) }
    }

    func updateUser(_ user: UserDTO) {
        self.user = user
        Self.save(user, key: DefaultsKey.currentUser, to: defaults)
        if case .signedIn = session { session = .signedIn(user) }
        if let context = modelContext { upsertProfile(user, in: context) }
    }

    func signOut(reason: SignOutReason, callServer: Bool = true) async {
        guard !isSigningOut else { return }
        isSigningOut = true
        defer { isSigningOut = false }
        if callServer { _ = try? await api.send(Endpoints.logout()) }
        services = .placeholder()
        // Signed-in views still run @Query updates while being torn down; deallocating the container under them traps in SwiftData.
        let retired = modelContainer
        Task { @MainActor in
            try? await Task.sleep(for: .seconds(1))
            withExtendedLifetime(retired) {}
        }
        modelContainer = nil
        pendingRecovery = nil
        auth.clearSession()
        defaults.removeObject(forKey: DefaultsKey.currentUser)
        defaults.removeObject(forKey: DefaultsKey.usage)
        defaults.removeObject(forKey: DefaultsKey.usageFetchedAt)
        if reason == .accountDeleted { defaults.removeObject(forKey: DefaultsKey.onboardingCompleted) }
        user = nil
        usage = nil
        usageFetchedAt = nil
        path = []
        isRecordingPresented = false
        signOutReason = reason
        session = .signedOut
    }

    // Terminal 401 only: a refresh that fails over the network never lands here.
    func handleUnauthenticated() {
        guard user != nil, !isSigningOut else { return }
        Task { await signOut(reason: .unauthenticated, callServer: false) }
    }

    private func checkAppleCredentialState() async {
        guard auth.signInMethod == .apple, let appleID = auth.appleUserID else { return }
        guard let state = try? await ASAuthorizationAppleIDProvider().credentialState(forUserID: appleID) else { return }
        if state == .revoked || state == .notFound {
            await signOut(reason: .revoked, callServer: true)
        }
    }

    private func openStore(for user: UserDTO) {
        do {
            let container = try ModelContainerFactory.make(userID: user.id)
            modelContainer = container
            storeError = false
            upsertProfile(user, in: container.mainContext)
            services = .live(ServiceEnvironment(api: api, context: container.mainContext, userID: user.id, auth: auth, app: self))
        } catch {
            Self.logger.error("store open failed: \(String(describing: error), privacy: .public)")
            storeError = true
        }
    }

    private func upsertProfile(_ user: UserDTO, in context: ModelContext) {
        let id = user.id
        let existing = try? context.fetch(FetchDescriptor<UserProfile>(predicate: #Predicate { $0.id == id })).first
        if let existing {
            existing.displayName = user.displayName
            existing.language = user.language
            existing.timezone = user.timezone
        } else {
            context.insert(UserProfile(id: user.id, displayName: user.displayName, language: user.language, timezone: user.timezone))
        }
        try? context.save()
    }

    // MARK: Navigation

    func presentRecording() { isRecordingPresented = true }

    func openMeeting(_ id: UUID) {
        isRecordingPresented = false
        path.append(.meeting(id))
    }

    // MARK: Server config & usage

    func refreshConfig() async {
        guard var config = try? await api.send(Endpoints.config()) else { return }
        config.fetchedAt = .now
        serverConfig = config
        Self.save(config, key: DefaultsKey.serverConfig, to: defaults)
    }

    @discardableResult
    func refreshUsage() async -> UsageDTO? {
        guard let fresh = try? await api.send(Endpoints.usage()) else { return nil }
        usage = fresh
        usageFetchedAt = .now
        Self.save(fresh, key: DefaultsKey.usage, to: defaults)
        defaults.set(usageFetchedAt, forKey: DefaultsKey.usageFetchedAt)
        await uploads.retryWaitingIntents(usage: fresh)
        return fresh
    }

    // MARK: Persistence helpers (plain JSON coders: symmetric, cache-only)

    private static func load<T: Decodable>(_ type: T.Type, key: String, from defaults: UserDefaults) -> T? {
        defaults.data(forKey: key).flatMap { try? JSONDecoder().decode(T.self, from: $0) }
    }

    private static func save<T: Encodable>(_ value: T, key: String, to defaults: UserDefaults) {
        if let data = try? JSONEncoder().encode(value) { defaults.set(data, forKey: key) }
    }
}
