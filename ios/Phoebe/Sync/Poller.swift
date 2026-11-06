import Foundation
import SwiftData

// ios.md §11 / api §4.5: foreground-only polling with Backoff.poll while something is active.
@MainActor
final class Poller: PollingAPI {
    typealias Tick = @MainActor (PollTarget) async -> Bool
    typealias Signature = @MainActor (Set<PollTarget>) -> Int

    private let tick: Tick
    private let signature: Signature
    private let sleep: @Sendable (TimeInterval) async -> Void
    private var targets: Set<PollTarget> = []
    private var isForeground = true
    private var backoff = Backoff.poll
    private var loop: Task<Void, Never>?
    private(set) var isRunning = false

    convenience init(env: ServiceEnvironment) {
        let app = env.app
        let store = SyncStore(context: env.context)
        self.init(
            tick: { target in
                switch target {
                case .library:
                    return await app.sync.pollTick()
                case .meeting(let id):
                    await app.sync.refreshMeeting(id)
                    return store.meeting(id).map(SyncEngine.isActive) ?? false
                }
            },
            signature: { targets in
                var hasher = Hasher()
                let meetings = targets.contains(.library) ? store.meetings() : targets.compactMap {
                    if case .meeting(let id) = $0 { store.meeting(id) } else { nil }
                }
                for meeting in meetings.sorted(by: { $0.id.uuidString < $1.id.uuidString }) {
                    hasher.combine(meeting.id)
                    hasher.combine(meeting.pipelineState)
                    hasher.combine(meeting.activeJobTypes)
                    hasher.combine(meeting.localPhaseRaw)
                    hasher.combine(meeting.updatedAt)
                }
                return hasher.finalize()
            },
            sleep: { try? await Task.sleep(for: .seconds($0)) }
        )
    }

    init(tick: @escaping Tick, signature: @escaping Signature, sleep: @escaping @Sendable (TimeInterval) async -> Void) {
        self.tick = tick
        self.signature = signature
        self.sleep = sleep
    }

    func start(_ target: PollTarget) {
        targets.insert(target)
        reset()
    }

    func stop(_ target: PollTarget) {
        targets.remove(target)
        if targets.isEmpty { cancelLoop() }
    }

    func reset() {
        backoff.reset()
        cancelLoop()
        guard isForeground, !targets.isEmpty else { return }
        isRunning = true
        loop = Task { [weak self] in await self?.run() }
    }

    func setForeground(_ active: Bool) {
        isForeground = active
        if active { reset() } else { cancelLoop() }
    }

    func waitUntilStopped() async {
        await loop?.value
    }

    private func cancelLoop() {
        loop?.cancel()
        loop = nil
        isRunning = false
    }

    private func run() async {
        var previous = signature(targets)
        while !Task.isCancelled, isForeground, !targets.isEmpty {
            var active = false
            for target in targets {
                if await tick(target) { active = true }
                if Task.isCancelled { return }
            }
            let current = signature(targets)
            if current != previous { backoff.reset() }
            previous = current
            guard active else { break }
            await sleep(backoff.nextDelay())
        }
        if !Task.isCancelled { isRunning = false }
    }
}
