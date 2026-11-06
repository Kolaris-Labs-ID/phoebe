import Foundation
import SwiftData
import Testing
@testable import Phoebe

struct LibraryStatusTests {
    private func chip(_ configure: (inout MeetingStatusInput) -> Void) -> ChipStatus? {
        var input = MeetingStatusInput()
        configure(&input)
        return MeetingStatus.chip(for: input)
    }

    @Test func liveSessionWinsOverEverything() {
        #expect(chip { $0.livePhase = .recording; $0.failedStage = "transcription" } == .recording)
        #expect(chip { $0.livePhase = .paused } == .recording)
        #expect(chip { $0.livePhase = .interrupted } == .interrupted)
        #expect(chip { $0.livePhase = .stopped; $0.pipeline = .notesReady } == .notesReady)
    }

    @Test func uncleanLocalRecordingShowsInterrupted() {
        #expect(chip { $0.localPhase = .recording } == .interrupted)
    }

    @Test func orphanAndNoSpeechBeforeFailure() {
        #expect(chip { $0.localPhase = .orphaned; $0.failedStage = "notes" } == .orphaned)
        #expect(chip { $0.transcriptionErrorCategory = "no_speech"; $0.failedStage = "transcription" } == .noSpeech)
    }

    @Test func failedStagesMapToFailureChips() {
        #expect(chip { $0.failedStage = "transcription" } == .failed(.transcription))
        #expect(chip { $0.failedStage = "notes"; $0.pipeline = .notesFailed } == .failed(.notes))
        #expect(chip { $0.failedStage = "mom"; $0.pipeline = .notesReady } == .failed(.mom))
        #expect(chip { $0.localPhase = .rejected } == .failed(.upload))
    }

    @Test func uploadPhasesAndOffline() {
        #expect(chip { $0.localPhase = .waitingStorage } == .waitingStorage)
        #expect(chip { $0.localPhase = .waitingConnection } == .waitingConnection)
        #expect(chip { $0.localPhase = .uploading; $0.isOnline = false } == .waitingConnection)
        #expect(chip { $0.localPhase = .queued; $0.isOnline = false } == .waitingConnection)
        #expect(chip { $0.localPhase = .waitingEntitlement } == .planExpired)
        #expect(chip { $0.localPhase = .waitingQuota } == .waitingQuota)
        #expect(chip { $0.localPhase = .queued } == .queued)
        #expect(chip { $0.localPhase = .uploading; $0.uploadProgress = 0.456 } == .uploading(percent: 45))
        #expect(chip { $0.localPhase = .verifying } == .verifying)
    }

    @Test func processingJobsInOrder() {
        #expect(chip { $0.activeJobs = [.generateMom, .transcribe] } == .transcribing)
        #expect(chip { $0.activeJobs = [.generateNotes] } == .generatingNotes)
        #expect(chip { $0.activeJobs = [.generateMom]; $0.momReview = .reviewed } == .generatingMom)
    }

    @Test func readyStates() {
        #expect(chip { $0.momReview = .reviewed; $0.pipeline = .notesReady } == .reviewed)
        #expect(chip { $0.momReview = .draft; $0.pipeline = .notesReady } == .momDraft)
        #expect(chip { $0.pipeline = .notesReady } == .notesReady)
        #expect(chip { $0.pipeline = .generatingNotes } == .transcriptReady)
        #expect(chip { $0.hasTranscript = true } == .transcriptReady)
        #expect(chip { $0.isServerCreated = false } == .localOnly)
        #expect(chip { _ in } == nil)
    }

    @Test func activeDrivesPolling() {
        #expect(MeetingStatus.isActive(MeetingStatusInput(activeJobs: [.transcribe])))
        #expect(MeetingStatus.isActive(MeetingStatusInput(localPhase: .uploading)))
        #expect(!MeetingStatus.isActive(MeetingStatusInput(localPhase: .waitingQuota)))
        #expect(!MeetingStatus.isActive(MeetingStatusInput()))
    }

    @Test func folderAndTagNameValidation() {
        let existing: [(id: UUID, name: String)] = [(UUID(), "Klien A")]
        #expect(NameValidation.check("   ", existing: existing) == .empty)
        #expect(NameValidation.check("klien a", existing: existing) == .duplicate)
        #expect(NameValidation.check("klien a", existing: existing, excluding: existing[0].id) == .valid("klien a"))
        #expect(NameValidation.check("  Internal ", existing: existing) == .valid("Internal"))
        #expect(NameValidation.check(String(repeating: "x", count: 80), existing: []) == .valid(String(repeating: "x", count: 60)))
    }

    @MainActor @Test func filtersCombineFolderAndTag() {
        let model = LibraryModel()
        let folder = UUID(), tag = UUID()
        #expect(model.matchesFilters(folderID: nil, tagIDs: []))
        model.folderFilter = .unfiled
        #expect(model.matchesFilters(folderID: nil, tagIDs: []))
        #expect(!model.matchesFilters(folderID: folder, tagIDs: []))
        model.folderFilter = .folder(folder)
        model.tagFilter = tag
        #expect(model.matchesFilters(folderID: folder, tagIDs: [tag]))
        #expect(!model.matchesFilters(folderID: folder, tagIDs: []))
        model.clearFilters()
        #expect(!model.isSearchingOrFiltering)
    }

    @MainActor @Test func importCreatesMeetingUploadAndIntentTogether() throws {
        let container = try ModelContainerFactory.inMemory()
        let context = container.mainContext
        let id = UUID(), user = UUID()
        let candidate = ImportCandidate(sourceURL: URL(filePath: "/tmp/Rapat klien.m4a"), fileName: "Rapat klien.m4a", fileExtension: "m4a", contentType: "audio/mp4", sizeBytes: 1000, durationMs: 61_000)
        let file = ImportedFile(relativePath: "recordings/\(id.lower)/import.m4a", sizeBytes: 1000, sha256: "abc")
        ImportModel.createMeeting(id: id, candidate: candidate, file: file, language: .en, userID: user, context: context)

        let meeting = try #require(try context.fetch(FetchDescriptor<Meeting>()).first)
        #expect(meeting.id == id)
        #expect(meeting.source == .import)
        #expect(meeting.title == "Rapat klien")
        #expect(meeting.startedAt == nil)
        #expect(meeting.transcriptionLanguage == .en)
        #expect(meeting.localPhase == .queued)
        let upload = try #require(try context.fetch(FetchDescriptor<PendingUpload>()).first)
        #expect(upload.filePath == file.relativePath)
        #expect(upload.sha256 == "abc")
        #expect(upload.ownerUserID == user)
        let intent = try #require(try context.fetch(FetchDescriptor<PendingIntent>()).first)
        #expect(intent.type == .transcribe)
        #expect(intent.body == APICoding.encode(TranscriptionRequest(language: .en)))
    }

    @MainActor @Test func devSubjectValidation() {
        #expect(SignInModel.isValidDevSubject("alice"))
        #expect(SignInModel.isValidDevSubject("a.b_c-1"))
        #expect(!SignInModel.isValidDevSubject(""))
        #expect(!SignInModel.isValidDevSubject("Alice"))
        #expect(!SignInModel.isValidDevSubject("al ice"))
        #expect(!SignInModel.isValidDevSubject(String(repeating: "a", count: 65)))
    }
}

struct UsageSummaryTests {
    private func usage(status: EntitlementStatus = .active, plan: String = "trial", quota: Int = 7200, consumed: Int, reserved: Int = 0) -> UsageDTO {
        UsageDTO(
            entitlement: EntitlementDTO(plan: plan, status: status, periodStart: nil, periodEnd: nil, quotaSeconds: quota),
            consumedSeconds: consumed, reservedSeconds: reserved, remainingSeconds: max(0, quota - consumed - reserved),
            aiGenerations: AIGenerationsDTO(limit: 30, windowHours: 24, used: 0, reserved: 0, remaining: 30, nextSlotAt: nil)
        )
    }

    @Test func minutesAreFlooredFromSeconds() {
        let summary = UsageSummary(usage(consumed: 3601, reserved: 719))
        #expect(summary.usedMinutes == 60)
        #expect(summary.quotaMinutes == 120)
        #expect(summary.reservedMinutes == 11)
        #expect(summary.leftMinutes == 48)
        #expect(summary.condition == .normal)
        #expect(summary.isTrial)
    }

    @Test func lowAtTenPercentLeft() {
        #expect(UsageSummary(usage(consumed: 6480)).condition == .low)
        #expect(UsageSummary(usage(consumed: 6479)).condition == .normal)
    }

    @Test func exhaustedAndPlanEnded() {
        #expect(UsageSummary(usage(consumed: 7200)).condition == .exhausted)
        #expect(UsageSummary(usage(consumed: 7000, reserved: 200)).condition == .exhausted)
        #expect(UsageSummary(usage(status: .expired, consumed: 0)).condition == .planEnded)
        #expect(UsageSummary(usage(status: .inactive, plan: "pro", consumed: 0)).isTrial == false)
    }
}
