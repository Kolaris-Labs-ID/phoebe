import Foundation
import Testing
@testable import Phoebe

struct RecordingStateMachineTests {
    private typealias M = RecordingStateMachine

    private func state(_ phase: RecordingPhase, banner: RecordingBanner? = nil, canResume: Bool = false) -> RecordingMachineState {
        RecordingMachineState(phase: phase, banner: banner, canResume: canResume)
    }

    private func run(_ events: [RecordingEvent], from start: RecordingMachineState = RecordingMachineState()) -> (RecordingMachineState, [RecordingEffect]) {
        var s = start
        var all: [RecordingEffect] = []
        for e in events {
            let (next, effects) = M.reduce(s, e)
            s = next
            all += effects
        }
        return (s, all)
    }

    @Test func prepareStartAndSucceed() {
        let (ready, none) = M.reduce(state(.idle), .prepare)
        #expect(ready.phase == .ready)
        #expect(none.isEmpty)

        let (starting, startFx) = M.reduce(ready, .start)
        #expect(starting.phase == .starting)
        #expect(startFx == [.activateSession, .openSegment, .startEngine])

        let (recording, fx) = M.reduce(starting, .startSucceeded)
        #expect(recording.phase == .recording)
        #expect(fx == [.haptic(.start), .logEvent(.recordingStarted)])
    }

    @Test func startFailureReturnsToReadyWithBanner() {
        let (s, fx) = run([.prepare, .start, .startFailed])
        #expect(s.phase == .ready)
        #expect(s.banner == .startFailed)
        #expect(s.phase.buttonState == .ready)
        #expect(fx.suffix(3) == [.closeSegment, .stopEngine, .deactivateSession])
    }

    @Test func preflightFailureKeepsReady() {
        let (s, fx) = M.reduce(state(.ready), .preflightFailed(requiredBytes: 300_000_000))
        #expect(s.phase == .ready)
        #expect(s.banner == .storagePreflight(requiredBytes: 300_000_000))
        #expect(fx.isEmpty)
    }

    @Test func pauseClosesSegmentAndStopsEngine() {
        let (s, fx) = M.reduce(state(.recording), .pause)
        #expect(s.phase == .paused)
        #expect(fx == [.closeSegment, .stopEngine, .deactivateSession, .haptic(.light)])
    }

    @Test func resumeFromPausedAndFailureFallsBackToPaused() {
        let (starting, fx) = M.reduce(state(.paused), .resume)
        #expect(starting.phase == .starting)
        #expect(fx == [.activateSession, .openSegment, .startEngine])

        let (ok, okFx) = M.reduce(starting, .startSucceeded)
        #expect(ok.phase == .recording)
        #expect(okFx == [.haptic(.light)])

        let (failed, _) = M.reduce(starting, .startFailed)
        #expect(failed.phase == .paused)
        #expect(failed.banner == .startFailed)
    }

    @Test func stopFromEveryLiveState() {
        let (fromRecording, fx1) = M.reduce(state(.recording), .stop)
        #expect(fromRecording.phase == .stopping)
        #expect(fx1 == [.closeSegment, .stopEngine, .deactivateSession, .haptic(.success), .finalize])

        for phase in [RecordingPhase.paused, .interrupted] {
            let (s, fx) = M.reduce(state(phase), .stop)
            #expect(s.phase == .stopping)
            #expect(fx == [.haptic(.success), .finalize])
        }
        let (stopped, _) = M.reduce(fromRecording, .stopCompleted)
        #expect(stopped.phase == .stopped)
        #expect(M.reduce(state(.ready), .stop).0.phase == .ready)
    }

    @Test func segmentTimerRotatesWithoutEngineRestart() {
        let (s, fx) = M.reduce(state(.recording), .segmentTimerFired)
        #expect(s.phase == .recording)
        #expect(fx == [.rotateSegment])
        #expect(M.reduce(state(.paused), .segmentTimerFired).1.isEmpty)
    }

    @Test func segmentWriteFailedInterrupts() {
        let (s, fx) = M.reduce(state(.recording), .segmentWriteFailed)
        #expect(s.phase == .interrupted)
        #expect(s.banner == .writeFailed)
        #expect(s.canResume)
        #expect(fx.prefix(3) == [.closeSegment, .stopEngine, .deactivateSession])
    }

    @Test func interruptionNeverAutoResumes() {
        let (interrupted, fx) = M.reduce(state(.recording), .interruptionBegan(savedMs: 61_000))
        #expect(interrupted.phase == .interrupted)
        #expect(interrupted.banner == .interrupted(savedMs: 61_000))
        #expect(!interrupted.canResume)
        #expect(fx == [.closeSegment, .stopEngine, .deactivateSession, .haptic(.warning), .logEvent(.recordingInterrupted)])

        for shouldResume in [true, false] {
            let (ended, endFx) = M.reduce(interrupted, .interruptionEnded(canResume: shouldResume))
            #expect(ended.phase == .interrupted)
            #expect(ended.canResume)
            #expect(endFx.isEmpty)
        }
        #expect(M.reduce(interrupted, .resume).0.phase == .interrupted)
    }

    @Test func interruptionWhilePausedChangesNothing() {
        let (s, fx) = M.reduce(state(.paused), .interruptionBegan(savedMs: 1_000))
        #expect(s == state(.paused))
        #expect(fx.isEmpty)
    }

    @Test func routeChangeSwapsInputAndKeepsRecording() {
        let (s, fx) = M.reduce(state(.recording), .routeChanged(newInput: "AirPods"))
        #expect(s.phase == .recording)
        #expect(s.banner == .routeChanged(inputName: "AirPods"))
        #expect(fx == [.closeSegment, .openSegment, .restartEngine])

        let (cfg, cfgFx) = M.reduce(state(.recording), .engineConfigurationChanged)
        #expect(cfg.phase == .recording)
        #expect(cfgFx == [.closeSegment, .openSegment, .restartEngine])
    }

    @Test func routeLostInputInterruptsUntilInputReturns() {
        let (lost, fx) = M.reduce(state(.recording), .routeLostInput)
        #expect(lost.phase == .interrupted)
        #expect(lost.banner == .noInput)
        #expect(!lost.canResume)
        #expect(fx.prefix(3) == [.closeSegment, .stopEngine, .deactivateSession])

        let (back, backFx) = M.reduce(lost, .routeChanged(newInput: "iPhone Microphone"))
        #expect(back.phase == .interrupted)
        #expect(back.canResume)
        #expect(backFx.isEmpty)
    }

    @Test func mediaResetInterruptsAndRebuilds() {
        let (s, fx) = M.reduce(state(.recording), .mediaServicesReset)
        #expect(s.phase == .interrupted)
        #expect(s.banner == .mediaReset)
        #expect(s.canResume)
        #expect(fx.prefix(2) == [.closeSegment, .rebuildEngine])

        let (idle, idleFx) = M.reduce(state(.ready), .mediaServicesReset)
        #expect(idle.phase == .ready)
        #expect(idleFx == [.rebuildEngine])
    }

    @Test func storageLowWarnsOnceAndCriticalStops() {
        let (low, _) = M.reduce(state(.recording), .storageLow(freeBytes: 250_000_000))
        #expect(low.banner == .storageLow(freeBytes: 250_000_000))
        #expect(low.storageWarned)
        #expect(M.reduce(low, .storageLow(freeBytes: 200_000_000)).0.banner == .storageLow(freeBytes: 250_000_000))

        let (critical, fx) = M.reduce(low, .storageCritical(savedMs: 90_000))
        #expect(critical.phase == .stopping)
        #expect(critical.banner == .storageStopped(savedMs: 90_000))
        #expect(fx.contains(.closeSegment))
        #expect(fx.last == .finalize)
        #expect(M.reduce(critical, .stopCompleted).0.phase == .stopped)
    }

    @Test func storageThresholds() {
        let config = RecordingConfig.standard
        #expect(M.storageEvent(freeBytes: 400_000_000, savedMs: 0, warned: false, config: config) == nil)
        #expect(M.storageEvent(freeBytes: 299_999_999, savedMs: 0, warned: false, config: config) == .storageLow(freeBytes: 299_999_999))
        #expect(M.storageEvent(freeBytes: 299_999_999, savedMs: 0, warned: true, config: config) == nil)
        #expect(M.storageEvent(freeBytes: 99_999_999, savedMs: 5_000, warned: true, config: config) == .storageCritical(savedMs: 5_000))
    }

    @Test func durationLimitWarnsAtTenMinutesAndStopsAtFiveSecondsBefore() {
        let limit = 7_200_000
        #expect(M.limitEvent(activeMs: limit - 600_001, limitMs: limit, warned: false) == nil)
        #expect(M.limitEvent(activeMs: limit - 600_000, limitMs: limit, warned: false) == .durationLimitNear(limitMinutes: 120, minutesLeft: 10))
        #expect(M.limitEvent(activeMs: limit - 300_000, limitMs: limit, warned: true) == nil)
        #expect(M.limitEvent(activeMs: limit - 5_001, limitMs: limit, warned: true) == nil)
        #expect(M.limitEvent(activeMs: limit - 5_000, limitMs: limit, warned: true) == .durationLimitReached(limitMinutes: 120))

        let (near, _) = M.reduce(state(.recording), .durationLimitNear(limitMinutes: 120, minutesLeft: 10))
        #expect(near.banner == .limitNear(limitMinutes: 120, minutesLeft: 10))
        #expect(near.limitWarned)

        let (reached, fx) = M.reduce(near, .durationLimitReached(limitMinutes: 120))
        #expect(reached.phase == .stopping)
        #expect(reached.banner == .limitReached(limitMinutes: 120))
        #expect(fx == [.closeSegment, .stopEngine, .deactivateSession, .haptic(.success), .finalize])
    }

    @Test func persistentBannersSurviveResume() {
        let (s, _) = run([.resume, .startSucceeded], from: state(.paused, banner: .storageLow(freeBytes: 1)))
        #expect(s.banner == .storageLow(freeBytes: 1))
        let (t, _) = run([.resume, .startSucceeded], from: state(.interrupted, banner: .mediaReset, canResume: true))
        #expect(t.banner == nil)
        #expect(t.phase == .recording)
    }

    @Test func dismissBannerClears() {
        #expect(M.reduce(state(.recording, banner: .routeChanged(inputName: "x")), .dismissBanner).0.banner == nil)
    }
}
