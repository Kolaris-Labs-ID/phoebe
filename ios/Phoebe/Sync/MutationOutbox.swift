import Foundation
import SwiftData

// PendingMutation local effects (on enqueue) and push handlers (ios.md §10 Push).
@MainActor
struct MutationOutbox {
    let env: ServiceEnvironment
    private var store: SyncStore { SyncStore(context: env.context) }
    private var mirror: MeetingMirror { MeetingMirror(store: store) }

    static let metadataKeys = ["title", "started_at", "timezone", "language", "participants"]

    // MARK: Enqueue

    func enqueue(_ mutation: OutboxMutation) {
        switch mutation {
        case .createMeeting(let id):
            guard let meeting = store.meeting(id), !meeting.isServerCreated,
                  !store.mutations(id).contains(where: { $0.kind == .meetingCreate }) else { return }
            env.context.insert(PendingMutation(meetingID: id, kind: .meetingCreate))
        case .patchMeeting(let id, let request):
            patchMeeting(id, request)
        case .deleteMeeting(let id):
            deleteMeeting(id)
        case .deleteAudio(let id):
            deleteAudio(id)
        case .createFolder(let id, let name):
            if store.folder(id) == nil { env.context.insert(Folder(id: id, name: name)) }
            env.context.insert(PendingMutation(meetingID: nil, kind: .folderCreate, targetID: id, payload: APICoding.encode(CreateNamedRequest(id: id, name: name))))
        case .renameFolder(let id, let name):
            store.folder(id)?.name = name
            env.context.insert(PendingMutation(meetingID: nil, kind: .folderRename, targetID: id, payload: APICoding.encode(RenameRequest(name: name))))
        case .deleteFolder(let id):
            let wasCreated = store.folder(id)?.isServerCreated == true || attemptedCreate(id, kind: .folderCreate)
            dropGlobal(target: id)
            store.folder(id).map(env.context.delete)
            if wasCreated { env.context.insert(PendingMutation(meetingID: nil, kind: .folderDelete, targetID: id)) }
        case .createTag(let id, let name):
            if store.tag(id) == nil { env.context.insert(Tag(id: id, name: name)) }
            env.context.insert(PendingMutation(meetingID: nil, kind: .tagCreate, targetID: id, payload: APICoding.encode(CreateNamedRequest(id: id, name: name))))
        case .renameTag(let id, let name):
            if let tag = store.tag(id) {
                tag.name = name
                tag.meetings.forEach { $0.refreshSearchText() }
            }
            env.context.insert(PendingMutation(meetingID: nil, kind: .tagRename, targetID: id, payload: APICoding.encode(RenameRequest(name: name))))
        case .deleteTag(let id):
            let wasCreated = store.tag(id)?.isServerCreated == true || attemptedCreate(id, kind: .tagCreate)
            dropGlobal(target: id)
            if let tag = store.tag(id) {
                let meetings = tag.meetings
                meetings.forEach { $0.tags.removeAll { $0.id == id } }
                env.context.delete(tag)
                meetings.forEach { $0.refreshSearchText() }
            }
            if wasCreated { env.context.insert(PendingMutation(meetingID: nil, kind: .tagDelete, targetID: id)) }
        }
        store.save()
    }

    private func attemptedCreate(_ id: UUID, kind: MutationKind) -> Bool {
        store.mutations(nil).contains { $0.targetID == id && $0.kind == kind && $0.attempts > 0 }
    }

    private func dropGlobal(target id: UUID) {
        store.mutations(nil).filter { $0.targetID == id }.forEach(env.context.delete)
    }

    private func patchMeeting(_ id: UUID, _ request: MeetingPatchRequest) {
        guard let meeting = store.meeting(id) else { return }
        var request = request
        if case .set(let names) = request.participants {
            request.participants = .set(names.map { $0.trimmingCharacters(in: .whitespacesAndNewlines) }.filter { !$0.isEmpty })
        }
        if request.hasMetadataChanges, request.expectedMetadataRevision == nil {
            request.expectedMetadataRevision = meeting.metadataRevision
        }
        switch request.title { case .set(let t): meeting.title = t; case .clear: meeting.title = ""; case .unchanged: break }
        switch request.startedAt { case .set(let d): meeting.startedAt = d; case .clear: meeting.startedAt = nil; case .unchanged: break }
        if case .set(let tz) = request.timezone { meeting.timezone = tz }
        switch request.participants { case .set(let p): meeting.participants = p; case .clear: meeting.participants = []; case .unchanged: break }
        switch request.folderId { case .set(let f): meeting.folder = store.folder(f); case .clear: meeting.folder = nil; case .unchanged: break }
        switch request.tagIds { case .set(let ids): meeting.tags = ids.compactMap(store.tag); case .clear: meeting.tags = []; case .unchanged: break }
        if case .set(let language) = request.language {
            meeting.transcriptionLanguage = language
            for intent in store.intents(id) where intent.type == .transcribe {
                intent.body = APICoding.encode(TranscriptionRequest(language: language))
                if intent.keySent {
                    intent.idempotencyKey = UUID()
                    intent.keySent = false
                }
            }
        }
        meeting.refreshSearchText()
        guard !request.isEmpty else { return }
        env.context.insert(PendingMutation(meetingID: id, kind: .meetingPatch, payload: APICoding.encode(request)))
    }

    private func deleteMeeting(_ id: UUID) {
        let meeting = store.meeting(id)
        let maybeOnServer = meeting?.isServerCreated == true
            || store.mutations(id).contains { $0.kind == .meetingCreate && $0.attempts > 0 }
            || store.pendingUpload(id)?.uploadID != nil
        store.purgeMeeting(id, uploads: env.app.uploads)
        if maybeOnServer { env.context.insert(PendingMutation(meetingID: id, kind: .meetingDelete)) }
    }

    private func deleteAudio(_ id: UUID) {
        if let upload = store.pendingUpload(id), upload.state != .verified { return }
        SyncFiles.recordingsDirs(id).forEach(SyncFiles.remove)
        store.pendingUpload(id).map(env.context.delete)
        store.localRecording(id).map(env.context.delete)
        if let meeting = store.meeting(id) {
            meeting.audioAvailable = false
            meeting.audioDeletedReason = "user"
        }
        env.context.insert(PendingMutation(meetingID: id, kind: .audioDelete))
    }

    // MARK: Push

    func push(_ mutation: PendingMutation) async -> OutboxResult {
        mutation.attempts += 1
        store.save()
        do {
            switch mutation.kind {
            case .meetingCreate:
                guard let id = mutation.meetingID, store.meeting(id) != nil else { return drop(mutation) }
                return try await SyncRemote.ensureMeeting(id, env: env) ? drop(mutation) : .halt
            case .meetingPatch:
                return try await pushPatch(mutation)
            case .meetingDelete:
                guard let id = mutation.meetingID else { return drop(mutation) }
                _ = try await env.api.sendDelete(Endpoints.deleteMeeting(id))
                return drop(mutation)
            case .audioDelete:
                guard let id = mutation.meetingID else { return drop(mutation) }
                do {
                    _ = try await env.api.sendDelete(Endpoints.deleteAudio(meetingID: id))
                } catch APIError.audioNotReady {}
                return drop(mutation)
            case .folderCreate, .tagCreate:
                return try await pushCreateNamed(mutation)
            case .folderRename, .tagRename:
                return try await pushRename(mutation)
            case .folderDelete, .tagDelete:
                guard let id = mutation.targetID else { return drop(mutation) }
                if mutation.kind == .folderDelete {
                    _ = try await env.api.sendDelete(Endpoints.deleteFolder(id))
                } else {
                    _ = try await env.api.sendDelete(Endpoints.deleteTag(id))
                }
                return drop(mutation)
            }
        } catch let error as APIError {
            return fail(mutation, error)
        } catch {
            return .halt
        }
    }

    private func isLive(_ mutation: PendingMutation) -> Bool {
        !mutation.isDeleted && mutation.modelContext != nil
    }

    private func drop(_ mutation: PendingMutation) -> OutboxResult {
        if isLive(mutation) { env.context.delete(mutation) }
        store.save()
        return .done
    }

    private func fail(_ mutation: PendingMutation, _ error: APIError) -> OutboxResult {
        guard isLive(mutation) else { return .done }
        let result = OutboxResult.classify(error)
        switch result {
        case .blocked:
            mutation.failedCode = error.code
            store.save()
            env.app.toasts.show(L("sync.notSynced", table: .sync))
        case .missing:
            _ = drop(mutation)
        default:
            break
        }
        return result
    }

    // MARK: Meeting PATCH

    private func pushPatch(_ mutation: PendingMutation) async throws -> OutboxResult {
        guard let id = mutation.meetingID else { return drop(mutation) }
        var retriedWithoutReferences = false
        while true {
            do {
                let dto = try await env.api.send(Endpoints.patchMeeting(id, rawBody: mutation.payload)).meeting
                guard isLive(mutation) else { return .done }
                return applyPatchSuccess(mutation, dto)
            } catch APIError.metadataConflict {
                let dto = try await env.api.send(Endpoints.meeting(id)).meeting
                guard isLive(mutation) else { return .done }
                if Self.serverMatches(mutation.payload, dto) { return applyPatchSuccess(mutation, dto) }
                mutation.failedCode = APIError.metadataConflict(currentMetadataRevision: dto.metadataRevision).code
                if let meeting = store.meeting(id) {
                    meeting.metadataConflict = true
                    mirror.apply(dto, to: meeting)
                }
                store.save()
                return .blocked
            } catch APIError.validationFailed(let fields) where !retriedWithoutReferences && fields.contains(where: SyncRemote.isMissingReference) {
                retriedWithoutReferences = true
                await SyncRemote.refreshFoldersAndTags(env: env)
                guard isLive(mutation) else { return .done }
                var object = JSONPayload.object(mutation.payload)
                object.removeValue(forKey: "folder_id")
                object.removeValue(forKey: "tag_ids")
                if Set(object.keys).isDisjoint(with: Self.metadataKeys) { return drop(mutation) }
                mutation.payload = JSONPayload.data(object)
                store.save()
            }
        }
    }

    private func applyPatchSuccess(_ mutation: PendingMutation, _ dto: MeetingDTO) -> OutboxResult {
        if let expected = JSONPayload.object(mutation.payload)["expected_metadata_revision"] as? Int, let id = mutation.meetingID {
            // Later edits were based on this one locally; rebase them onto the revision it produced.
            for later in store.mutations(id) where later !== mutation && later.kind == .meetingPatch {
                var object = JSONPayload.object(later.payload)
                if object["expected_metadata_revision"] as? Int == expected {
                    object["expected_metadata_revision"] = dto.metadataRevision
                    later.payload = JSONPayload.data(object)
                }
            }
        }
        _ = drop(mutation)
        if let meeting = store.meeting(dto.id) { mirror.apply(dto, to: meeting) }
        store.save()
        return .done
    }

    // A replayed PATCH whose first attempt landed (lost response) conflicts with its own write;
    // if the server already holds exactly these values it is a success, not a conflict.
    static func serverMatches(_ payload: Data, _ dto: MeetingDTO) -> Bool {
        let object = JSONPayload.object(payload)
        let keys = metadataKeys.filter { object[$0] != nil }
        guard !keys.isEmpty else { return false }
        return keys.allSatisfy { key in
            let value = object[key]
            switch key {
            case "title": return value as? String == dto.title
            case "timezone": return value as? String == dto.timezone
            case "language": return value as? String == dto.language.rawValue
            case "participants": return value as? [String] == dto.participants
            case "started_at":
                if value is NSNull { return dto.startedAt == nil }
                guard let raw = value as? String, let date = APICoding.parseDate(raw), let server = dto.startedAt else { return false }
                return APICoding.formatDate(date) == APICoding.formatDate(server)
            default: return false
            }
        }
    }

    func resolveMetadataConflict(_ id: UUID, _ resolution: ConflictResolution) async {
        guard let meeting = store.meeting(id) else { return }
        let conflicted = store.mutations(id).filter { $0.kind == .meetingPatch && $0.failedCode == "metadata_conflict" }
        meeting.metadataConflict = false
        switch resolution {
        case .keepMine:
            for mutation in conflicted {
                var object = JSONPayload.object(mutation.payload)
                object["expected_metadata_revision"] = meeting.metadataRevision
                mutation.payload = JSONPayload.data(object)
                mutation.failedCode = nil
            }
            store.save()
        case .useLatest:
            conflicted.forEach(env.context.delete)
            store.save()
            if let dto = try? await env.api.send(Endpoints.meeting(id)).meeting {
                mirror.apply(dto, to: meeting, forceMetadata: true)
                store.save()
            }
        }
    }

    // MARK: Folders and tags

    private func pushCreateNamed(_ mutation: PendingMutation) async throws -> OutboxResult {
        guard var id = mutation.targetID else { return drop(mutation) }
        let isFolder = mutation.kind == .folderCreate
        let name = JSONPayload.object(mutation.payload)["name"] as? String ?? ""
        var replacedID = false
        while true {
            do {
                if isFolder {
                    let dto = try await env.api.send(Endpoints.createFolder(id: id, name: name)).folder
                    if let folder = store.folder(id) {
                        folder.isServerCreated = true
                        folder.meetingCount = dto.meetingCount
                        folder.updatedAt = dto.updatedAt
                    }
                } else {
                    let dto = try await env.api.send(Endpoints.createTag(id: id, name: name)).tag
                    if let tag = store.tag(id) {
                        tag.isServerCreated = true
                        tag.meetingCount = dto.meetingCount
                        tag.updatedAt = dto.updatedAt
                    }
                }
                return drop(mutation)
            } catch APIError.nameTaken {
                await SyncRemote.refreshFoldersAndTags(env: env)
                guard isLive(mutation) else { return .done }
                let existingID: UUID? = isFolder
                    ? ((try? env.context.fetch(FetchDescriptor<Folder>())) ?? []).first { $0.isServerCreated && $0.id != id && $0.name.caseInsensitiveCompare(name) == .orderedSame }?.id
                    : ((try? env.context.fetch(FetchDescriptor<Tag>())) ?? []).first { $0.isServerCreated && $0.id != id && $0.name.caseInsensitiveCompare(name) == .orderedSame }?.id
                guard let existingID else {
                    mutation.failedCode = APIError.nameTaken.code
                    store.save()
                    return .blocked
                }
                remap(from: id, to: existingID, isFolder: isFolder, deleteLocal: true)
                return drop(mutation)
            } catch APIError.idConflict where !replacedID {
                guard isLive(mutation) else { return .done }
                replacedID = true
                let newID = UUID()
                remap(from: id, to: newID, isFolder: isFolder, deleteLocal: false)
                id = newID
                mutation.targetID = newID
                mutation.payload = APICoding.encode(CreateNamedRequest(id: newID, name: name))
                store.save()
            }
        }
    }

    private func pushRename(_ mutation: PendingMutation) async throws -> OutboxResult {
        guard let id = mutation.targetID else { return drop(mutation) }
        let name = JSONPayload.object(mutation.payload)["name"] as? String ?? ""
        do {
            if mutation.kind == .folderRename {
                _ = try await env.api.send(Endpoints.renameFolder(id, name: name))
            } else {
                _ = try await env.api.send(Endpoints.renameTag(id, name: name))
            }
            return drop(mutation)
        } catch APIError.notFound {
            return drop(mutation)
        } catch APIError.nameTaken {
            _ = drop(mutation)
            await SyncRemote.refreshFoldersAndTags(env: env)
            return .done
        }
    }

    // Points every local reference (meetings, queued PATCH bodies, queued folder/tag ops) at `newID`.
    private func remap(from oldID: UUID, to newID: UUID, isFolder: Bool, deleteLocal: Bool) {
        if isFolder {
            if deleteLocal {
                let target = store.folder(newID)
                for meeting in store.meetings() where meeting.folder?.id == oldID { meeting.folder = target }
                store.folder(oldID).map(env.context.delete)
            } else {
                store.folder(oldID)?.id = newID
            }
        } else {
            if deleteLocal {
                guard let target = store.tag(newID) else { return }
                for meeting in store.meetings() where meeting.tags.contains(where: { $0.id == oldID }) {
                    meeting.tags.removeAll { $0.id == oldID }
                    if !meeting.tags.contains(where: { $0.id == newID }) { meeting.tags.append(target) }
                    meeting.refreshSearchText()
                }
                store.tag(oldID).map(env.context.delete)
            } else {
                store.tag(oldID)?.id = newID
            }
        }
        for mutation in store.allMutations() {
            if mutation.meetingID == nil, mutation.targetID == oldID { mutation.targetID = newID }
            guard mutation.kind == .meetingPatch else { continue }
            var object = JSONPayload.object(mutation.payload)
            if isFolder, object["folder_id"] as? String == oldID.lower {
                object["folder_id"] = newID.lower
            } else if !isFolder, let tags = object["tag_ids"] as? [String], tags.contains(oldID.lower) {
                var replaced: [String] = []
                for tag in tags.map({ $0 == oldID.lower ? newID.lower : $0 }) where !replaced.contains(tag) { replaced.append(tag) }
                object["tag_ids"] = replaced
            } else {
                continue
            }
            mutation.payload = JSONPayload.data(object)
        }
        store.save()
    }
}
