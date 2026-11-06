import Foundation
import Testing
@testable import Phoebe

@MainActor struct AllowanceTests {
    static let now = APICoding.parseDate("2026-11-06T02:00:00.000Z")!
    static let utc = TimeZone(identifier: "UTC")!

    static func generations(remaining: Int, nextSlotAt: Date? = nil) -> AIGenerationsDTO {
        AIGenerationsDTO(limit: 30, windowHours: 24, used: 30 - remaining, reserved: 0, remaining: remaining, nextSlotAt: nextSlotAt)
    }

    static func usage(remaining: Int, nextSlotAt: Date? = nil) -> UsageDTO {
        UsageDTO(
            entitlement: EntitlementDTO(plan: "trial", status: .active, periodStart: nil, periodEnd: nil, quotaSeconds: 7200),
            consumedSeconds: 0, reservedSeconds: 0, remainingSeconds: 7200,
            aiGenerations: generations(remaining: remaining, nextSlotAt: nextSlotAt)
        )
    }

    @Test func decodesUsageAIGenerations() throws {
        let json = #"{"limit":30,"window_hours":24,"used":28,"reserved":2,"remaining":0,"next_slot_at":"2026-11-06T05:00:00.000Z"}"#
        let dto = try APICoding.decoder().decode(AIGenerationsDTO.self, from: Data(json.utf8))
        #expect(dto == AIGenerationsDTO(limit: 30, windowHours: 24, used: 28, reserved: 2, remaining: 0, nextSlotAt: APICoding.parseDate("2026-11-06T05:00:00.000Z")))
    }

    @Test func labelRemainingAndNextSlotInBothLanguages() {
        let allowance = AIAllowance(Self.generations(remaining: 0, nextSlotAt: Self.now.addingTimeInterval(2 * 3600)))
        #expect(allowance.label(localization: "id") == "30 generasi AI per 24 jam")
        #expect(allowance.label(localization: "en") == "30 AI generations per 24 hours")
        #expect(allowance.remainingText(localization: "id") == "Sisa 0")
        #expect(allowance.remainingText(localization: "en") == "0 left")
        let id = allowance.nextSlotText(now: Self.now, locale: Locale(identifier: "id_ID"), timeZone: Self.utc, localization: "id")
        let en = allowance.nextSlotText(now: Self.now, locale: Locale(identifier: "en_US"), timeZone: Self.utc, localization: "en")
        #expect(id == "Slot berikutnya tersedia 4.00 (dalam 2 jam)")
        #expect(en?.hasPrefix("Next slot available 4:00") == true)
        #expect(en?.hasSuffix("AM (in 2 hours)") == true)
    }

    @Test func nextSlotOnlyWhenExhaustedAndDateShownWhenNotToday() {
        #expect(AIAllowance(Self.generations(remaining: 3, nextSlotAt: Self.now)).nextSlotText(now: Self.now) == nil)
        let tomorrow = Self.now.addingTimeInterval(23 * 3600)
        let text = AIAllowance.slotTime(tomorrow, now: Self.now, locale: Locale(identifier: "en_US"), timeZone: Self.utc)
        #expect(text.hasPrefix("Nov 7, 2026"))
        #expect(text.hasSuffix("(in 23 hours)"))
    }

    @Test func summaryJoinsLabelRemainingAndNextSlot() {
        let open = AIAllowance(Self.generations(remaining: 5))
        #expect(open.summary(now: Self.now, localization: "en") == "30 AI generations per 24 hours · 5 left")
        let full = AIAllowance(Self.generations(remaining: 0, nextSlotAt: Self.now.addingTimeInterval(3600)))
        let text = full.summary(now: Self.now, locale: Locale(identifier: "id_ID"), timeZone: Self.utc, localization: "id")
        #expect(text == "30 generasi AI per 24 jam · Sisa 0. Slot berikutnya tersedia 3.00 (dalam 1 jam).")
    }

    @Test func choiceIsNeededOnlyForAutoNotesWithoutASlot() {
        #expect(AIAllowance.needsChoice(usage: Self.usage(remaining: 0), notes: .auto))
        #expect(!AIAllowance.needsChoice(usage: Self.usage(remaining: 0), notes: .skip))
        #expect(!AIAllowance.needsChoice(usage: Self.usage(remaining: 1), notes: .auto))
        #expect(!AIAllowance.needsChoice(usage: nil, notes: .auto))
    }

    @Test func generationGateUsesNextSlotOnlyWhenExhausted() {
        let later = Self.now.addingTimeInterval(600)
        #expect(GenerationGate.rateLimitedUntil(usage: Self.usage(remaining: 0, nextSlotAt: later), retryAfter: nil) == later)
        #expect(GenerationGate.rateLimitedUntil(usage: Self.usage(remaining: 2, nextSlotAt: later), retryAfter: nil) == nil)
        let blocked = GenerationGate.notes(online: true, noSpeech: false, hasTranscript: true, jobActive: false, manualEdit: nil,
                                           rateLimitedUntil: GenerationGate.rateLimitedUntil(usage: Self.usage(remaining: 0, nextSlotAt: later), retryAfter: nil), now: Self.now)
        #expect(blocked == .rateLimited(until: later))
        #expect(blocked?.key == AILimitCopy.key)
    }

    @Test func limitFootnoteFallsBackToTheErrorTimeWhenUsageIsMissing() {
        #expect(ActionErrorText.aiLimitText(usage: nil, fallbackNextSlot: nil, localization: "en") == "AI generation limit reached.")
        let text = ActionErrorText.aiLimitText(usage: Self.usage(remaining: 0), fallbackNextSlot: Self.now.addingTimeInterval(3600), now: Self.now, localization: "en")
        #expect(text.hasPrefix("30 AI generations per 24 hours · 0 left. Next slot available "))
    }

    @Test func recordingStopExplainsTheChoiceWhenNoSlotIsLeft() {
        #expect(RecordingNextStep.make(isOnline: true, usage: Self.usage(remaining: 0), durationMs: 60_000) == .aiSlot)
        #expect(RecordingNextStep.make(isOnline: true, usage: Self.usage(remaining: 1), durationMs: 60_000) == .upload)
        #expect(LS(RecordingNextStep.aiSlot.key, table: .recording, localization: "en") == "No AI slot is left right now. After upload you can wait for one or transcribe only.")
    }

    @Test func transcriptReadyWithoutNotesAndAISlotWaiting() {
        #expect(DetailStatus.notes(StageInputs(pipeline: .transcriptReady, hasTranscript: true)) == .notGenerated)
        #expect(DetailStatus.notes(StageInputs(pipeline: .transcriptReady, hasTranscript: true, hasAINotes: true)) == .done)
        #expect(DetailStatus.upload(StageInputs(pipeline: .transcriptReady)) == .done)
        #expect(DetailStatus.transcript(StageInputs(pipeline: .uploaded, aiSlotBlocked: true)) == .waitingAISlot)
        #expect(DetailStatus.transcript(StageInputs(pipeline: .uploaded, hasTranscript: true, aiSlotBlocked: true)) == .done)
        #expect(PipelineStep.stateKey(.transcript, .waitingAISlot) == "step.waitingAISlot")
        #expect(LS("step.waitingAISlot", table: .meetingDetail, localization: "id") == "Menunggu slot AI")
        #expect(MeetingStatus.chip(for: MeetingStatusInput(pipeline: .transcriptReady)) == .transcriptReady)
    }

    @Test func reviewNavigationWrapsAround() {
        let ids = ["s2", "s5", "s9"]
        #expect(TranscriptReview.step(ids, from: nil, forward: true) == "s2")
        #expect(TranscriptReview.step(ids, from: nil, forward: false) == "s9")
        #expect(TranscriptReview.step(ids, from: "s9", forward: true) == "s2")
        #expect(TranscriptReview.step(ids, from: "s2", forward: false) == "s9")
        #expect(TranscriptReview.step(ids, from: "s5", forward: true) == "s9")
        #expect(TranscriptReview.step(ids, from: "gone", forward: true) == "s2")
        #expect(TranscriptReview.step([], from: "s2", forward: true) == nil)
    }

    @Test func uncertainSegmentCanBeConfirmedUnchanged() {
        let uncertain = TranscriptSegment(id: "s2", startMs: 30_000, endMs: 60_000, text: "Budget", uncertain: true, uncertainReason: "low_confidence", timing: .chunk)
        let certain = TranscriptSegment(id: "s3", startMs: 60_000, endMs: 90_000, text: "Oke")
        #expect(TranscriptReview.canSave(segment: uncertain, text: "Budget"))
        #expect(!TranscriptReview.canSave(segment: certain, text: "Oke"))
        #expect(TranscriptReview.canSave(segment: certain, text: "Oke sip"))
        #expect(!TranscriptReview.canSave(segment: uncertain, text: String(repeating: "a", count: 2001)))
        #expect(TranscriptReview.resolvedIDs(segment: uncertain) == ["s2"])
        #expect(TranscriptReview.resolvedIDs(segment: certain).isEmpty)
        #expect(TranscriptReview.uncertainIDs([certain, uncertain]) == ["s2"])
    }

    @Test func newStringsExistInBothLanguages() {
        #expect(LS("transcript.uncertain", table: .meetingDetail, localization: "id") == "Perlu ditinjau")
        #expect(LS("transcript.uncertain", table: .meetingDetail, localization: "en") == "Needs review")
        #expect(LS("transcript.timing.chunk", table: .meetingDetail, localization: "id") == "Waktu bersifat perkiraan per bagian rekaman (sekitar 30 detik).")
        #expect(LS("transcript.timing.chunk", table: .meetingDetail, localization: "en") == "Times are approximate, per recording section (about 30 seconds).")
        #expect(LS("ai.choice.wait", table: .meetingDetail, localization: "id") == "Tunggu slot AI")
        #expect(LS("ai.choice.wait", table: .meetingDetail, localization: "en") == "Wait for AI slot")
        #expect(LS("ai.choice.transcribeOnly", table: .meetingDetail, localization: "id") == "Transkrip saja")
        #expect(LS("ai.choice.transcribeOnly", table: .meetingDetail, localization: "en") == "Transcribe only")
        #expect(LS("notes.generate", table: .meetingDetail, localization: "id") == "Buat catatan")
        #expect(LS("notes.generate", table: .meetingDetail, localization: "en") == "Generate notes")
    }
}
