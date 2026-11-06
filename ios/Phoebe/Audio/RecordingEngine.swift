import AVFoundation
import Foundation
import SwiftData
import UIKit

@MainActor @Observable
final class RecordingEngine: RecordingEngineAPI {
    private(set) var phase: RecordingPhase = .idle
    private(set) var meetingID: UUID?
    private(set) var activeDurationMs = 0
    private(set) var segmentCount = 0
    private(set) var inputLevel: Float = 0
    private(set) var banner: RecordingBanner?
    private(set) var micPermission: MicPermission = .undetermined
    private(set) var canResume = false

    @ObservationIgnored private var machine = RecordingMachineState()
    @ObservationIgnored private let context: ModelContext
    @ObservationIgnored private let userID: UUID
    @ObservationIgnored private let capture: any AudioCapturing
    @ObservationIgnored private let session: any AudioSessioning
    @ObservationIgnored private let storage: any StorageProviding
    @ObservationIgnored private let config: RecordingConfig
    @ObservationIgnored private let root: URL
    @ObservationIgnored private let limitMs: @MainActor () -> Int
    @ObservationIgnored private let logEvent: @MainActor (AnalyticsEvent, UUID?) -> Void
    @ObservationIgnored private let haptics: @MainActor (HapticKind) -> Void
    @ObservationIgnored private let tickInterval: Duration?
    @ObservationIgnored let finalizer: RecordingFinalizer

    @ObservationIgnored private var recording: LocalRecording?
    @ObservationIgnored private var openIndex: Int?
    @ObservationIgnored private var lastIndex = 0
    @ObservationIgnored private var rotationPending = false
    @ObservationIgnored private var lastStorageCheck = ContinuousClock.now
    @ObservationIgnored private var tail: Task<Void, Never>?
    @ObservationIgnored private var ticker: Task<Void, Never>?
    @ObservationIgnored private nonisolated(unsafe) var observers: [NSObjectProtocol] = []

    convenience init(env: ServiceEnvironment) {
        let app = env.app
        self.init(
            context: env.context,
            userID: env.userID,
            capture: CaptureEngine(),
            session: AudioSessionController(),
            limitMs: { [weak app] in app?.maxAudioDurationMs ?? ServerConfig.defaultMaxAudioDurationMs },
            logEvent: { [weak app] event, id in app?.events.log(event, meetingID: id) },
            // RecordingView plays haptics from phase changes (.sensoryFeedback); playing them here too would double them.
            onQueued: { [weak app] id in
                guard let app else { return }
                Task { await app.uploads.kick(meetingID: id) }
            },
            observeSystemNotifications: true
        )
    }

    init(
        context: ModelContext,
        userID: UUID,
        capture: any AudioCapturing,
        session: any AudioSessioning,
        storage: any StorageProviding = StorageMonitor(),
        config: RecordingConfig = .standard,
        root: URL = RecordingPaths.root,
        limitMs: @escaping @MainActor () -> Int = { ServerConfig.defaultMaxAudioDurationMs },
        logEvent: @escaping @MainActor (AnalyticsEvent, UUID?) -> Void = { _, _ in },
        haptics: @escaping @MainActor (HapticKind) -> Void = { _ in },
        onQueued: @escaping @MainActor (UUID) -> Void = { _ in },
        tickInterval: Duration? = .milliseconds(500),
        observeSystemNotifications: Bool = false
    ) {
        self.context = context
        self.userID = userID
        self.capture = capture
        self.session = session
        self.storage = storage
        self.config = config
        self.root = root
        self.limitMs = limitMs
        self.logEvent = logEvent
        self.haptics = haptics
        self.tickInterval = tickInterval
        self.finalizer = RecordingFinalizer(context: context, root: root, storage: storage, config: config, onQueued: onQueued)
        self.micPermission = session.permission
        capture.onConfigurationChange = { [weak self] in self?.handleConfigurationChange() }
        if observeSystemNotifications { observeNotifications() }
    }

    deinit {
        for observer in observers { NotificationCenter.default.removeObserver(observer) }
    }

    // MARK: RecordingEngineAPI

    func refreshPermission() {
        micPermission = session.permission
    }

    func requestMicPermission() async -> Bool {
        let granted = await session.requestPermission()
        micPermission = session.permission
        return granted
    }

    func prepare() async {
        refreshPermission()
        await send(.prepare)
    }

    func start(meetingID: UUID) async {
        await tail?.value
        guard [.idle, .ready, .stopped].contains(machine.phase) else { return }
        let free = storage.freeBytes()
        guard free >= config.storagePreflightBytes else {
            await send(.preflightFailed(requiredBytes: config.storagePreflightBytes))
            return
        }
        if session.permission != .granted {
            guard await requestMicPermission() else { return }
        }
        let recording: LocalRecording
        do {
            try RecordingPaths.prepareDirectory(for: meetingID, root: root)
            recording = existingRecording(meetingID) ?? LocalRecording(meetingID: meetingID, ownerUserID: userID)
            if recording.modelContext == nil { context.insert(recording) }
            recording.state = .recording
            fetchMeeting(meetingID)?.localPhase = .recording
            try context.save()
        } catch {
            machine.banner = .startFailed
            publish()
            return
        }
        self.recording = recording
        self.meetingID = meetingID
        lastIndex = recording.segments.map(\.index).max() ?? 0
        openIndex = nil
        rotationPending = false
        lastStorageCheck = .now
        capture.resetSession()
        activeDurationMs = 0
        segmentCount = recording.segments.count
        await send(.start)
        if machine.phase != .recording { abandonIfEmpty() }
    }

    func pause() {
        enqueue(.pause)
    }

    func resume() async {
        await send(.resume)
    }

    func stop() async -> RecordingSummary? {
        await send(.stop)
        guard machine.phase == .stopped, let meetingID else { return nil }
        return RecordingSummary(meetingID: meetingID, durationMs: activeDurationMs, segmentCount: segmentCount)
    }

    func dismissBanner() {
        enqueue(.dismissBanner)
    }

    func recoverOnLaunch() async -> LocalRecording? {
        let live = phase.hasLiveSession ? meetingID : nil
        return await RecordingRecovery(context: context, root: root, finalizer: finalizer).run(excluding: live)
    }

    func recover(_ recording: LocalRecording) async {
        recording.state = .stopped
        try? context.save()
        await finalizer.finalize(recording)
    }

    func discard(_ recording: LocalRecording) {
        let id = recording.meetingID
        try? FileManager.default.removeItem(at: RecordingPaths.directory(for: id, root: root))
        context.delete(recording)
        fetchMeeting(id)?.localPhase = .none
        try? context.save()
    }

    func retryPendingMerges() async {
        let stopped = LocalRecordingState.stopped.rawValue
        let merged = LocalRecordingState.merged.rawValue
        let pending = (try? context.fetch(FetchDescriptor<LocalRecording>(
            predicate: #Predicate { $0.stateRaw == stopped || $0.stateRaw == merged }
        ))) ?? []
        for recording in pending {
            let id = recording.meetingID
            if phase.hasLiveSession, id == meetingID { continue }
            if recording.state == .stopped, fetchMeeting(id)?.localPhase == .waitingStorage, !finalizer.hasSpaceToMerge(recording) {
                continue
            }
            await finalizer.finalize(recording)
        }
    }

    // MARK: System events

    func handleInterruption(began: Bool, shouldResume: Bool) {
        if began {
            enqueue(.interruptionBegan(savedMs: currentActiveMs))
        } else {
            enqueue(.interruptionEnded(canResume: shouldResume))
        }
    }

    func handleRouteChange(reason: AVAudioSession.RouteChangeReason) {
        if reason == .oldDeviceUnavailable, machine.phase == .recording {
            if let input = session.currentInputName {
                enqueue(.routeChanged(newInput: input))
            } else {
                enqueue(.routeLostInput)
            }
        } else if machine.phase == .interrupted, machine.banner == .noInput, let input = session.currentInputName {
            enqueue(.routeChanged(newInput: input))
        }
    }

    func handleConfigurationChange() {
        guard machine.phase == .recording else { return }
        enqueue(.engineConfigurationChanged)
    }

    func handleMediaServicesReset() {
        enqueue(.mediaServicesReset)
    }

    func tick() {
        inputLevel = machine.phase == .recording ? capture.level : 0
        guard machine.phase == .recording else { return }
        activeDurationMs = currentActiveMs
        if capture.writeFailed {
            enqueue(.segmentWriteFailed)
            return
        }
        if !rotationPending, capture.segmentFrames >= config.segmentFrames {
            rotationPending = true
            enqueue(.segmentTimerFired)
        }
        if let event = RecordingStateMachine.limitEvent(activeMs: activeDurationMs, limitMs: limitMs(), warned: machine.limitWarned, config: config) {
            enqueue(event)
        }
        let now = ContinuousClock.now
        if now - lastStorageCheck >= .seconds(config.storageCheckSeconds) {
            lastStorageCheck = now
            checkStorage()
        }
    }

    func checkStorage() {
        guard machine.phase == .recording,
              let event = RecordingStateMachine.storageEvent(
                freeBytes: storage.freeBytes(), savedMs: currentActiveMs, warned: machine.storageWarned, config: config
              )
        else { return }
        enqueue(event)
    }

    // MARK: Event pipeline (serialized: effects of one event finish before the next is reduced)

    private var currentActiveMs: Int { Int(capture.totalFrames * 1000 / Int64(config.sampleRate)) }

    @discardableResult
    private func enqueue(_ event: RecordingEvent) -> Task<Void, Never> {
        let previous = tail
        let task = Task { @MainActor in
            await previous?.value
            await self.process(event)
        }
        tail = task
        return task
    }

    func waitForIdle() async {
        await tail?.value
    }

    private func send(_ event: RecordingEvent) async {
        await enqueue(event).value
    }

    private func process(_ event: RecordingEvent) async {
        let (next, effects) = RecordingStateMachine.reduce(machine, event)
        let changedPhase = next.phase != machine.phase
        machine = next
        publish()
        if changedPhase { persistPhase() }
        for effect in effects {
            if let followUp = await execute(effect) {
                await process(followUp)
                return
            }
        }
    }

    private func execute(_ effect: RecordingEffect) async -> RecordingEvent? {
        switch effect {
        case .activateSession:
            do { try session.activate() } catch { return .startFailed }
        case .deactivateSession:
            session.deactivate()
        case .openSegment:
            let index = lastIndex + 1
            do {
                try capture.openSegment(at: segmentURL(index))
                lastIndex = index
                openIndex = index
            } catch {
                return machine.phase == .starting ? .startFailed : .segmentWriteFailed
            }
        case .closeSegment:
            if let closed = capture.closeSegment(), let index = openIndex {
                openIndex = nil
                await persistSegment(closed, index: index)
            }
        case .rotateSegment:
            rotationPending = false
            let index = lastIndex + 1
            do {
                let closed = try capture.rotateSegment(to: segmentURL(index))
                let closedIndex = openIndex
                lastIndex = index
                openIndex = index
                if let closed, let closedIndex { await persistSegment(closed, index: closedIndex) }
            } catch {
                return .segmentWriteFailed
            }
        case .startEngine:
            do {
                try await capture.startEngine()
                return .startSucceeded
            } catch {
                return .startFailed
            }
        case .stopEngine:
            capture.stopEngine()
        case .restartEngine:
            do { try capture.restartEngine() } catch {
                if session.currentInputName == nil { return .routeLostInput }
                return .segmentWriteFailed
            }
        case .rebuildEngine:
            capture.rebuildEngine()
        case .finalize:
            await finalizeRecording()
            return .stopCompleted
        case .haptic(let kind):
            haptics(kind)
        case .logEvent(let event):
            logEvent(event, meetingID)
        }
        return nil
    }

    private func publish() {
        if phase != machine.phase { phase = machine.phase }
        if banner != machine.banner { banner = machine.banner }
        if canResume != machine.canResume { canResume = machine.canResume }
        updateTicker()
    }

    private func updateTicker() {
        let shouldRun = machine.phase == .recording && tickInterval != nil
        if shouldRun, ticker == nil, let interval = tickInterval {
            ticker = Task { @MainActor [weak self] in
                while !Task.isCancelled {
                    try? await Task.sleep(for: interval)
                    guard let self else { return }
                    self.tick()
                }
            }
        } else if !shouldRun, let ticker {
            ticker.cancel()
            self.ticker = nil
            inputLevel = 0
        }
    }

    private func persistPhase() {
        guard let recording else { return }
        switch machine.phase {
        case .recording: recording.state = .recording
        case .paused: recording.state = .paused
        case .interrupted: recording.state = .interrupted
        default: return
        }
        try? context.save()
    }

    // Manifest: the closed segment is recorded and saved before anything else happens.
    private func persistSegment(_ closed: CapturedSegment, index: Int) async {
        activeDurationMs = currentActiveMs
        guard closed.frames > 0, let recording else {
            try? FileManager.default.removeItem(at: closed.url)
            return
        }
        guard let durationMs = await AudioFiles.playableDurationMs(closed.url) else { return }
        let segment = RecordingSegment(
            index: index, fileName: closed.url.lastPathComponent, durationMs: durationMs,
            bytes: Int(RecordingPaths.fileSize(closed.url))
        )
        context.insert(segment)
        segment.recording = recording
        recording.activeDurationMs = activeDurationMs
        try? context.save()
        segmentCount = recording.segments.count
    }

    private func finalizeRecording() async {
        guard let recording else { return }
        activeDurationMs = currentActiveMs
        recording.state = .stopped
        recording.activeDurationMs = activeDurationMs
        try? context.save()
        await finalizer.finalize(recording)
    }

    private func abandonIfEmpty() {
        guard let recording, recording.segments.isEmpty, machine.phase != .recording else { return }
        let id = recording.meetingID
        try? FileManager.default.removeItem(at: RecordingPaths.directory(for: id, root: root))
        context.delete(recording)
        if let meeting = fetchMeeting(id), meeting.localPhase == .recording { meeting.localPhase = .none }
        try? context.save()
        self.recording = nil
    }

    private func segmentURL(_ index: Int) -> URL {
        RecordingPaths.directory(for: meetingID ?? UUID(), root: root).appending(path: RecordingSegment.fileName(index: index))
    }

    private func existingRecording(_ id: UUID) -> LocalRecording? {
        try? context.fetch(FetchDescriptor<LocalRecording>(predicate: #Predicate { $0.meetingID == id })).first
    }

    private func fetchMeeting(_ id: UUID) -> Meeting? {
        try? context.fetch(FetchDescriptor<Meeting>(predicate: #Predicate { $0.id == id })).first
    }

    // MARK: Notifications

    private func observeNotifications() {
        let center = NotificationCenter.default
        let av = AVAudioSession.sharedInstance()
        observers.append(center.addObserver(forName: AVAudioSession.interruptionNotification, object: av, queue: .main) { [weak self] note in
            let type = (note.userInfo?[AVAudioSessionInterruptionTypeKey] as? UInt).flatMap(AVAudioSession.InterruptionType.init(rawValue:))
            let options = AVAudioSession.InterruptionOptions(rawValue: note.userInfo?[AVAudioSessionInterruptionOptionKey] as? UInt ?? 0)
            guard let type else { return }
            MainActor.assumeIsolated {
                self?.handleInterruption(began: type == .began, shouldResume: options.contains(.shouldResume))
            }
        })
        observers.append(center.addObserver(forName: AVAudioSession.routeChangeNotification, object: av, queue: .main) { [weak self] note in
            let raw = note.userInfo?[AVAudioSessionRouteChangeReasonKey] as? UInt ?? 0
            guard let reason = AVAudioSession.RouteChangeReason(rawValue: raw) else { return }
            MainActor.assumeIsolated { self?.handleRouteChange(reason: reason) }
        })
        observers.append(center.addObserver(forName: AVAudioSession.mediaServicesWereResetNotification, object: av, queue: .main) { [weak self] _ in
            MainActor.assumeIsolated { self?.handleMediaServicesReset() }
        })
    }
}
