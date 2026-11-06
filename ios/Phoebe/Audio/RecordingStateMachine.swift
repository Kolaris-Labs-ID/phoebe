import Foundation

struct RecordingConfig: Equatable, Sendable {
    var sampleRate: Double = 16_000
    var bitRate = 24_000
    var segmentSeconds = 120
    var storagePreflightBytes: Int64 = 300 * 1_000_000
    var storageLowBytes: Int64 = 300 * 1_000_000
    var storageCriticalBytes: Int64 = 100 * 1_000_000
    var mergeHeadroomBytes: Int64 = 50 * 1_000_000
    var storageCheckSeconds = 10
    var limitWarnMs = 10 * 60_000
    var limitMarginMs = 5_000
    var firstBufferTimeout: Duration = .seconds(3)

    static let standard = RecordingConfig()

    var segmentFrames: Int64 { Int64(Double(segmentSeconds) * sampleRate) }
}

enum HapticKind: Sendable, Equatable {
    case start, light, warning, success
}

enum RecordingEvent: Equatable, Sendable {
    case prepare
    case start
    case preflightFailed(requiredBytes: Int64)
    case startSucceeded
    case startFailed
    case pause
    case resume
    case stop
    case stopCompleted
    case segmentTimerFired
    case segmentWriteFailed
    case interruptionBegan(savedMs: Int)
    case interruptionEnded(canResume: Bool)
    case routeLostInput
    case routeChanged(newInput: String)
    case engineConfigurationChanged
    case mediaServicesReset
    case storageLow(freeBytes: Int64)
    case storageCritical(savedMs: Int)
    case durationLimitNear(limitMinutes: Int, minutesLeft: Int)
    case durationLimitReached(limitMinutes: Int)
    case dismissBanner
}

enum RecordingEffect: Equatable, Sendable {
    case activateSession, deactivateSession
    case openSegment, closeSegment, rotateSegment
    case startEngine, stopEngine, restartEngine, rebuildEngine
    case finalize
    case haptic(HapticKind)
    case logEvent(AnalyticsEvent)
}

struct RecordingMachineState: Equatable, Sendable {
    var phase: RecordingPhase = .idle
    var banner: RecordingBanner?
    var canResume = false
    var resumeFallback: RecordingPhase?
    var limitWarned = false
    var storageWarned = false
}

enum RecordingStateMachine {
    private static let openCapture: [RecordingEffect] = [.activateSession, .openSegment, .startEngine]
    private static let closeCapture: [RecordingEffect] = [.closeSegment, .stopEngine, .deactivateSession]
    private static let swapInput: [RecordingEffect] = [.closeSegment, .openSegment, .restartEngine]

    static func reduce(_ state: RecordingMachineState, _ event: RecordingEvent) -> (RecordingMachineState, [RecordingEffect]) {
        var s = state
        let phase = state.phase
        let live: Set<RecordingPhase> = [.recording, .paused, .interrupted]

        switch event {
        case .prepare:
            guard [.idle, .ready, .stopped].contains(phase) else { return (state, []) }
            s = RecordingMachineState(phase: .ready)
            return (s, [])

        case .start:
            guard [.idle, .ready, .stopped].contains(phase) else { return (state, []) }
            s = RecordingMachineState(phase: .starting)
            return (s, openCapture)

        case .preflightFailed(let required):
            guard [.idle, .ready, .stopped].contains(phase) else { return (state, []) }
            s.phase = .ready
            s.banner = .storagePreflight(requiredBytes: required)
            return (s, [])

        case .startSucceeded:
            guard phase == .starting else { return (state, []) }
            let resumed = s.resumeFallback != nil
            s.phase = .recording
            s.resumeFallback = nil
            s.canResume = false
            if let banner = s.banner, !banner.persistsAcrossResume { s.banner = nil }
            return (s, resumed ? [.haptic(.light)] : [.haptic(.start), .logEvent(.recordingStarted)])

        case .startFailed:
            guard phase == .starting else { return (state, []) }
            s.phase = s.resumeFallback ?? .ready
            s.resumeFallback = nil
            s.banner = .startFailed
            return (s, closeCapture)

        case .pause:
            guard phase == .recording else { return (state, []) }
            s.phase = .paused
            return (s, closeCapture + [.haptic(.light)])

        case .resume:
            guard phase == .paused || (phase == .interrupted && state.canResume) else { return (state, []) }
            s.resumeFallback = phase
            s.phase = .starting
            return (s, openCapture)

        case .stop:
            guard live.contains(phase) else { return (state, []) }
            s.phase = .stopping
            s.canResume = false
            return (s, (phase == .recording ? closeCapture : []) + [.haptic(.success), .finalize])

        case .stopCompleted:
            guard phase == .stopping else { return (state, []) }
            s.phase = .stopped
            return (s, [])

        case .segmentTimerFired:
            guard phase == .recording else { return (state, []) }
            return (s, [.rotateSegment])

        case .segmentWriteFailed:
            guard phase == .recording else { return (state, []) }
            s.phase = .interrupted
            s.banner = .writeFailed
            s.canResume = true
            return (s, closeCapture + [.haptic(.warning), .logEvent(.recordingInterrupted)])

        case .interruptionBegan(let savedMs):
            switch phase {
            case .recording:
                s.phase = .interrupted
                s.banner = .interrupted(savedMs: savedMs)
                s.canResume = false
                return (s, closeCapture + [.haptic(.warning), .logEvent(.recordingInterrupted)])
            case .interrupted:
                s.banner = .interrupted(savedMs: savedMs)
                s.canResume = false
                return (s, [])
            default:
                return (state, [])
            }

        case .interruptionEnded:
            // Resume is only enabled, never automatic, even when the system says shouldResume.
            guard phase == .interrupted, case .interrupted = state.banner else { return (state, []) }
            s.canResume = true
            return (s, [])

        case .routeLostInput:
            guard phase == .recording else { return (state, []) }
            s.phase = .interrupted
            s.banner = .noInput
            s.canResume = false
            return (s, closeCapture + [.haptic(.warning), .logEvent(.recordingInterrupted)])

        case .routeChanged(let input):
            switch phase {
            case .recording:
                s.banner = .routeChanged(inputName: input)
                return (s, swapInput)
            case .interrupted where state.banner == .noInput:
                s.canResume = true
                return (s, [])
            default:
                return (state, [])
            }

        case .engineConfigurationChanged:
            guard phase == .recording else { return (state, []) }
            return (s, swapInput)

        case .mediaServicesReset:
            guard live.contains(phase) else { return (state, [.rebuildEngine]) }
            s.phase = .interrupted
            s.banner = .mediaReset
            s.canResume = true
            return (s, [.closeSegment, .rebuildEngine] + (phase == .recording ? [.haptic(.warning), .logEvent(.recordingInterrupted)] : []))

        case .storageLow(let free):
            guard phase == .recording, !state.storageWarned else { return (state, []) }
            s.storageWarned = true
            s.banner = .storageLow(freeBytes: free)
            return (s, [])

        case .storageCritical(let savedMs):
            guard live.contains(phase) else { return (state, []) }
            s.phase = .stopping
            s.canResume = false
            s.banner = .storageStopped(savedMs: savedMs)
            return (s, (phase == .recording ? closeCapture : []) + [.haptic(.warning), .finalize])

        case .durationLimitNear(let limit, let left):
            guard phase == .recording, !state.limitWarned else { return (state, []) }
            s.limitWarned = true
            s.banner = .limitNear(limitMinutes: limit, minutesLeft: left)
            return (s, [])

        case .durationLimitReached(let limit):
            guard phase == .recording else { return (state, []) }
            s.phase = .stopping
            s.canResume = false
            s.banner = .limitReached(limitMinutes: limit)
            return (s, closeCapture + [.haptic(.success), .finalize])

        case .dismissBanner:
            s.banner = nil
            return (s, [])
        }
    }

    static func limitEvent(activeMs: Int, limitMs: Int, warned: Bool, config: RecordingConfig = .standard) -> RecordingEvent? {
        let limitMinutes = limitMs / 60_000
        if activeMs >= limitMs - config.limitMarginMs {
            return .durationLimitReached(limitMinutes: limitMinutes)
        }
        if !warned, activeMs >= limitMs - config.limitWarnMs {
            let left = (limitMs - activeMs + 59_999) / 60_000
            return .durationLimitNear(limitMinutes: limitMinutes, minutesLeft: left)
        }
        return nil
    }

    static func storageEvent(freeBytes: Int64, savedMs: Int, warned: Bool, config: RecordingConfig = .standard) -> RecordingEvent? {
        if freeBytes < config.storageCriticalBytes { return .storageCritical(savedMs: savedMs) }
        if !warned, freeBytes < config.storageLowBytes { return .storageLow(freeBytes: freeBytes) }
        return nil
    }
}

extension RecordingBanner {
    var persistsAcrossResume: Bool {
        switch self {
        case .storageLow, .limitNear: true
        default: false
        }
    }
}
