import Foundation
import SwiftData

// Applies server DTOs to the SwiftData mirror. Local metadata with a pending PATCH or an open
// metadata conflict is not overwritten, so optimistic edits never flicker back.
@MainActor
struct MeetingMirror {
    let store: SyncStore
    var context: ModelContext { store.context }

    func preservesMetadata(_ meeting: Meeting) -> Bool {
        meeting.metadataConflict || store.mutations(meeting.id).contains { $0.kind == .meetingPatch }
    }

    @discardableResult
    func upsert(_ item: MeetingListItemDTO) -> Meeting {
        let meeting = store.meeting(item.id) ?? insertMeeting(id: item.id, source: item.source, startedAt: item.startedAt, timezone: item.timezone)
        meeting.sourceRaw = item.source.rawValue
        if !preservesMetadata(meeting) {
            meeting.title = item.title
            meeting.startedAt = item.startedAt
            meeting.timezone = item.timezone
            meeting.folder = item.folderId.flatMap(store.folder)
            meeting.tags = item.tagIds.compactMap(store.tag)
        }
        meeting.pipelineState = item.pipelineState.rawValue
        meeting.durationMs = item.durationMs
        meeting.audioAvailable = item.audioAvailable
        meeting.momReviewStatus = item.momReviewStatus?.rawValue
        meeting.activeJobTypes = item.activeJobTypes.map(\.rawValue)
        meeting.failedStage = item.failedStage
        meeting.transcriptionErrorCategory = item.transcriptionErrorCategory
        meeting.createdAt = item.createdAt
        meeting.updatedAt = item.updatedAt
        meeting.isServerCreated = true
        meeting.refreshSearchText()
        return meeting
    }

    @discardableResult
    func upsert(_ dto: MeetingDTO) -> Meeting {
        let meeting = store.meeting(dto.id) ?? insertMeeting(id: dto.id, source: dto.source, startedAt: dto.startedAt, timezone: dto.timezone)
        apply(dto, to: meeting)
        return meeting
    }

    func apply(_ dto: MeetingDTO, to meeting: Meeting, forceMetadata: Bool = false) {
        meeting.sourceRaw = dto.source.rawValue
        if forceMetadata || !preservesMetadata(meeting) {
            meeting.title = dto.title
            meeting.startedAt = dto.startedAt
            meeting.timezone = dto.timezone
            meeting.language = dto.language.rawValue
            meeting.participants = dto.participants
            meeting.folder = dto.folderId.flatMap(store.folder)
            meeting.tags = dto.tagIds.compactMap(store.tag)
        }
        meeting.metadataRevision = dto.metadataRevision
        meeting.pipelineState = dto.pipelineState.rawValue
        meeting.createdAt = dto.createdAt
        meeting.updatedAt = dto.updatedAt
        meeting.isServerCreated = true
        if let audio = dto.audio {
            meeting.audioAvailable = audio.available
            meeting.audioDeletedReason = audio.deletedReason
            meeting.durationMs = audio.durationMs
        } else {
            meeting.audioAvailable = false
        }
        meeting.hasTranscript = dto.transcript != nil
        meeting.momReviewStatus = dto.documents.mom?.reviewStatus.rawValue

        var active: [String] = []
        for type in JobType.allCases {
            guard let job = dto.jobs[type] else { continue }
            upsert(job)
            if job.status.isActive { active.append(type.rawValue) }
        }
        meeting.activeJobTypes = active
        meeting.failedStage = switch dto.pipelineState {
        case .transcriptionFailed: "transcription"
        case .notesFailed: "notes"
        default: dto.jobs.generateMom?.status == .failed ? "mom" : nil
        }
        meeting.transcriptionErrorCategory = dto.jobs.transcribe?.status == .failed ? dto.jobs.transcribe?.errorCategory : nil

        for kind in [DocumentKind.manualNotes, .aiNotes, .mom] {
            if let summary = dto.documents[kind] { upsert(summary, meetingID: dto.id) }
        }
        meeting.refreshSearchText()
    }

    func upsert(_ job: JobDTO) {
        let id = job.id
        if let existing = try? context.fetch(FetchDescriptor<ProcessingJob>(predicate: #Predicate { $0.id == id })).first {
            existing.apply(job)
        } else {
            context.insert(ProcessingJob(dto: job))
        }
    }

    @discardableResult
    func upsert(_ summary: DocumentSummaryDTO, meetingID: UUID) -> Document {
        let id = summary.id
        if let existing = (try? context.fetch(FetchDescriptor<Document>(predicate: #Predicate { $0.id == id })).first)
            ?? store.document(meetingID, summary.kind) {
            existing.id = summary.id
            existing.apply(summary)
            return existing
        }
        let doc = Document(
            id: summary.id, meetingID: meetingID, kind: summary.kind,
            currentRevisionNumber: summary.currentRevisionNumber, currentRevisionID: summary.currentRevisionId,
            reviewStatus: summary.reviewStatus, updatedAt: summary.updatedAt
        )
        doc.isStale = summary.isStale
        doc.staleReasons = summary.staleReasons
        context.insert(doc)
        return doc
    }

    func upsert(_ revision: DocumentRevisionDTO, meetingID: UUID) {
        if let existing = store.documentRevision(revision.id) {
            existing.reviewedAt = revision.reviewedAt
            if existing.content == nil, let content = try? revision.content?.encoded() { existing.content = content }
            return
        }
        if let row = try? DocumentRevision(meetingID: meetingID, dto: revision) { context.insert(row) }
    }

    func upsert(_ response: DocumentResponse, meetingID: UUID) {
        upsert(response.document, meetingID: meetingID)
        if let revision = response.revision { upsert(revision, meetingID: meetingID) }
        if response.document.kind == .mom { store.meeting(meetingID)?.momReviewStatus = response.document.reviewStatus.rawValue }
    }

    func upsert(_ transcript: TranscriptRevisionDTO, meetingID: UUID) {
        guard !store.transcriptRevisions(meetingID).contains(where: { $0.id == transcript.id }),
              let segments = transcript.segments else { return }
        context.insert(TranscriptRevision(
            id: transcript.id, meetingID: meetingID, revisionNumber: transcript.revisionNumber, kind: transcript.kind,
            language: transcript.language, createdAt: transcript.createdAt, segmentsJSON: APICoding.encode(segments)
        ))
    }

    private func insertMeeting(id: UUID, source: MeetingSource, startedAt: Date?, timezone: String) -> Meeting {
        let meeting = Meeting(id: id, source: source, startedAt: startedAt, timezone: timezone)
        meeting.isServerCreated = true
        context.insert(meeting)
        return meeting
    }

    // MARK: Folders and tags (full lists: absence of a server-created, not-pending row means deleted)

    func applyFolders(_ items: [FolderDTO]) {
        let pendingIDs = Set(store.mutations(nil).compactMap(\.targetID))
        let all = (try? context.fetch(FetchDescriptor<Folder>())) ?? []
        let seen = Set(items.map(\.id))
        for folder in all where folder.isServerCreated && !seen.contains(folder.id) && !pendingIDs.contains(folder.id) {
            context.delete(folder)
        }
        for item in items {
            if let folder = all.first(where: { $0.id == item.id }) {
                if !pendingIDs.contains(item.id) { folder.name = item.name }
                folder.meetingCount = item.meetingCount
                folder.updatedAt = item.updatedAt
                folder.isServerCreated = true
            } else {
                let folder = Folder(id: item.id, name: item.name, updatedAt: item.updatedAt, isServerCreated: true)
                folder.meetingCount = item.meetingCount
                context.insert(folder)
            }
        }
    }

    func applyTags(_ items: [TagDTO]) {
        let pendingIDs = Set(store.mutations(nil).compactMap(\.targetID))
        let all = (try? context.fetch(FetchDescriptor<Tag>())) ?? []
        let seen = Set(items.map(\.id))
        for tag in all where tag.isServerCreated && !seen.contains(tag.id) && !pendingIDs.contains(tag.id) {
            tag.meetings.forEach { $0.tags.removeAll { $0.id == tag.id }; $0.refreshSearchText() }
            context.delete(tag)
        }
        for item in items {
            if let tag = all.first(where: { $0.id == item.id }) {
                if !pendingIDs.contains(item.id), tag.name != item.name {
                    tag.name = item.name
                    tag.meetings.forEach { $0.refreshSearchText() }
                }
                tag.meetingCount = item.meetingCount
                tag.updatedAt = item.updatedAt
                tag.isServerCreated = true
            } else {
                let tag = Tag(id: item.id, name: item.name, updatedAt: item.updatedAt, isServerCreated: true)
                tag.meetingCount = item.meetingCount
                context.insert(tag)
            }
        }
    }
}
