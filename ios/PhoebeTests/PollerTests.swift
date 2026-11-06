import Foundation
import Testing
@testable import Phoebe

@MainActor
struct PollerTests {
    final class Delays: @unchecked Sendable {
        private let lock = NSLock()
        private var values: [TimeInterval] = []
        var all: [TimeInterval] { lock.withLock { values } }
        func append(_ value: TimeInterval) { lock.withLock { values.append(value) } }
    }

    @MainActor
    final class Script {
        var activeTicks: Int
        var ticks: [PollTarget] = []
        var signature = 0
        var changeSignatureAtTick: Int?

        init(activeTicks: Int) { self.activeTicks = activeTicks }

        func tick(_ target: PollTarget) -> Bool {
            ticks.append(target)
            if ticks.count == changeSignatureAtTick { signature += 1 }
            guard activeTicks > 0 else { return false }
            activeTicks -= 1
            return true
        }
    }

    func makePoller(_ script: Script, _ delays: Delays) -> Poller {
        Poller(
            tick: { target in script.tick(target) },
            signature: { _ in script.signature },
            sleep: { delays.append($0) }
        )
    }

    @Test func delaysGrowByOnePointFiveFromTwoAndStopWhenNothingIsActive() async {
        let script = Script(activeTicks: 5), delays = Delays()
        let poller = makePoller(script, delays)
        poller.start(.library)
        await poller.waitUntilStopped()

        #expect(delays.all == [2, 3, 4.5, 6.75, 10.125])
        #expect(script.ticks.count == 6)
        #expect(!poller.isRunning)
    }

    @Test func delayIsCappedAtThirtySeconds() async {
        let script = Script(activeTicks: 12), delays = Delays()
        let poller = makePoller(script, delays)
        poller.start(.meeting(UUID()))
        await poller.waitUntilStopped()

        #expect(delays.all.max() == 30)
        #expect(delays.all.suffix(3) == [30, 30, 30])
    }

    @Test func resetStartsAgainAtTwoSeconds() async {
        let script = Script(activeTicks: 3), delays = Delays()
        let poller = makePoller(script, delays)
        poller.start(.library)
        await poller.waitUntilStopped()
        script.activeTicks = 1
        poller.reset()
        await poller.waitUntilStopped()

        #expect(delays.all == [2, 3, 4.5, 2])
    }

    @Test func stateChangeResetsBackoff() async {
        let script = Script(activeTicks: 5), delays = Delays()
        script.changeSignatureAtTick = 3
        let poller = makePoller(script, delays)
        poller.start(.library)
        await poller.waitUntilStopped()

        #expect(delays.all == [2, 3, 2, 3, 4.5])
    }

    @Test func backgroundStopsPollingAndForegroundResumes() async {
        let script = Script(activeTicks: 1), delays = Delays()
        let poller = makePoller(script, delays)
        poller.setForeground(false)
        poller.start(.library)
        await poller.waitUntilStopped()
        #expect(script.ticks.isEmpty)
        #expect(!poller.isRunning)

        poller.setForeground(true)
        await poller.waitUntilStopped()
        #expect(script.ticks.count == 2)
    }

    @Test func stoppingTheLastTargetStopsTheLoop() async {
        let script = Script(activeTicks: 1), delays = Delays()
        let poller = makePoller(script, delays)
        poller.stop(.library)
        poller.start(.library)
        poller.stop(.library)
        await poller.waitUntilStopped()
        #expect(!poller.isRunning)
        poller.reset()
        #expect(!poller.isRunning)
    }

    @Test func meetingTargetIsActiveWhileJobsOrUploadsAreActive() throws {
        let meeting = Meeting(source: .recording)
        #expect(!SyncEngine.isActive(meeting))
        meeting.activeJobTypes = ["transcribe"]
        #expect(SyncEngine.isActive(meeting))
        meeting.activeJobTypes = []
        meeting.localPhase = .uploading
        #expect(SyncEngine.isActive(meeting))
        meeting.localPhase = .waitingQuota
        #expect(!SyncEngine.isActive(meeting))
    }
}
