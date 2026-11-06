import Foundation

// Server calls shared by SyncEngine and UploadManager.
@MainActor
enum SyncRemote {
    // true once the server has the meeting; false when it was orphaned (409 meeting_id_conflict).
    static func ensureMeeting(_ meetingID: UUID, env: ServiceEnvironment) async throws -> Bool {
        let store = SyncStore(context: env.context)
        guard let meeting = store.meeting(meetingID) else { throw SyncError.meetingMissing }
        guard !meeting.isServerCreated else { return true }
        var dropReferences = false
        while true {
            let request = CreateMeetingRequest(
                id: meeting.id, source: meeting.source, title: meeting.title, startedAt: meeting.startedAt,
                timezone: meeting.timezone, language: meeting.transcriptionLanguage,
                participants: meeting.participants.map { $0.trimmingCharacters(in: .whitespacesAndNewlines) }.filter { !$0.isEmpty },
                folderId: dropReferences ? nil : meeting.folder?.id,
                tagIds: dropReferences ? [] : meeting.tags.map(\.id)
            )
            do {
                let dto = try await env.api.send(Endpoints.createMeeting(request)).meeting
                guard !meeting.isDeleted, meeting.modelContext != nil else { return false }
                MeetingMirror(store: store).apply(dto, to: meeting)
                store.mutations(meetingID).filter { $0.kind == .meetingCreate }.forEach(env.context.delete)
                store.save()
                return true
            } catch APIError.meetingIdConflict {
                store.mutations(meetingID).filter { $0.kind == .meetingCreate }.forEach(env.context.delete)
                store.markOrphaned(meetingID, uploads: env.app.uploads)
                return false
            } catch APIError.validationFailed(let fields) where !dropReferences && fields.contains(where: isMissingReference) {
                await refreshFoldersAndTags(env: env)
                dropReferences = true
            }
        }
    }

    static func isMissingReference(_ field: FieldError) -> Bool {
        (field.field == "folder_id" || field.field == "tag_ids") && field.reason == "not_found"
    }

    static func refreshFoldersAndTags(env: ServiceEnvironment) async {
        let mirror = MeetingMirror(store: SyncStore(context: env.context))
        if let folders = try? await env.api.send(Endpoints.folders()) { mirror.applyFolders(folders.items) }
        if let tags = try? await env.api.send(Endpoints.tags()) { mirror.applyTags(tags.items) }
        mirror.store.save()
    }

    static func refreshDocument(_ meetingID: UUID, kind: DocumentKind, env: ServiceEnvironment) async {
        guard let response = try? await env.api.send(Endpoints.document(meetingID: meetingID, kind: kind)) else { return }
        let mirror = MeetingMirror(store: SyncStore(context: env.context))
        mirror.upsert(response, meetingID: meetingID)
        mirror.store.save()
    }
}
