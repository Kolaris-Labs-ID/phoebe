import Foundation
import Testing
@testable import Phoebe

@MainActor struct RecordingFeatureTests {
    private func usage(status: EntitlementStatus = .active, remaining: Int) -> UsageDTO {
        UsageDTO(
            entitlement: EntitlementDTO(plan: "trial", status: status, periodStart: nil, periodEnd: nil, quotaSeconds: 7200),
            consumedSeconds: 0, reservedSeconds: 0, remainingSeconds: remaining,
            aiGenerations: AIGenerationsDTO(limitPerDay: 30, usedLast24h: 0, nextAvailableAt: nil)
        )
    }

    @Test func nextStepAfterStop() {
        #expect(RecordingNextStep.make(isOnline: true, usage: usage(remaining: 3600), durationMs: 60_000) == .upload)
        #expect(RecordingNextStep.make(isOnline: false, usage: usage(remaining: 3600), durationMs: 60_000) == .offline)
        #expect(RecordingNextStep.make(isOnline: true, usage: usage(remaining: 59), durationMs: 59_001) == .quota)
        #expect(RecordingNextStep.make(isOnline: true, usage: usage(status: .expired, remaining: 3600), durationMs: 1000) == .plan)
        #expect(RecordingNextStep.make(isOnline: true, usage: nil, durationMs: 1000) == .upload)
    }

    @Test func readyNoticeNeverBlocksRecordingButWarns() {
        #expect(ReadyNotice.make(usage: nil) == nil)
        #expect(ReadyNotice.make(usage: usage(remaining: 10)) == nil)
        #expect(ReadyNotice.make(usage: usage(remaining: 0)) == .quota)
        #expect(ReadyNotice.make(usage: usage(status: .inactive, remaining: 100)) == .plan)
    }

    @Test func bannerMappingUsesIconAndTextNotColorAlone() {
        for banner: RecordingBanner in [
            .startFailed, .writeFailed, .interrupted(savedMs: 1000), .interruptionWaiting, .mediaReset,
            .routeChanged(inputName: "AirPods"), .noInput, .storageLow(freeBytes: 1), .storageStopped(savedMs: 1),
            .storagePreflight(requiredBytes: 1), .limitNear(limitMinutes: 120, minutesLeft: 10), .limitReached(limitMinutes: 120),
        ] {
            let content = RecordingBannerContent.make(banner)
            #expect(!content.systemImage.isEmpty)
            #expect(content.messageKey.hasPrefix("rec."))
        }
    }

    @Test func interruptionBannerIsPersistentAndRouteBannerAutoDismisses() {
        let interrupted = RecordingBannerContent.make(.interrupted(savedMs: 5000))
        #expect(interrupted.titleKey == "rec.interrupt.title")
        #expect(interrupted.closable == false)
        #expect(interrupted.autoDismissAfter == nil)
        let route = RecordingBannerContent.make(.routeChanged(inputName: "Mic"))
        #expect(route.tone == .info)
        #expect(route.autoDismissAfter == .seconds(5))
        #expect(RecordingBannerContent.make(.startFailed).retriesStart)
        #expect(RecordingBannerContent.make(.storageStopped(savedMs: 1)).tone == .danger)
    }

    @Test func hapticsFollowPhaseTransitions() {
        #expect(RecordingView.haptic(from: .starting, to: .recording) == .start)
        #expect(RecordingView.haptic(from: .recording, to: .paused) == .impact(weight: .light))
        #expect(RecordingView.haptic(from: .recording, to: .interrupted) == .warning)
        #expect(RecordingView.haptic(from: .stopping, to: .stopped) == .stop)
        #expect(RecordingView.haptic(from: .idle, to: .ready) == nil)
    }

    @Test func recordingStringsExistInBothLanguages() throws {
        let en = try #require(strings(table: "Recording", localization: "en"))
        let id = try #require(strings(table: "Recording", localization: "id"))
        #expect(!en.isEmpty)
        #expect(Set(en.keys) == Set(id.keys))
        #expect(en.values.allSatisfy { !$0.isEmpty } && id.values.allSatisfy { !$0.isEmpty })
        #expect(en["rec.stop.confirm"] == "Finish & save")
        #expect(id["rec.stop.confirm"] == "Selesai & simpan")
    }
}

func strings(table: String, localization: String) -> [String: String]? {
    guard let path = Bundle.main.path(forResource: table, ofType: "strings", inDirectory: nil, forLocalization: localization) else { return nil }
    return NSDictionary(contentsOfFile: path) as? [String: String]
}
