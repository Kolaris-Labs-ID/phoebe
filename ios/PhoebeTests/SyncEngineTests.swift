import Foundation
import SwiftData
import Testing
@testable import Phoebe

@MainActor
struct SyncEngineTests {
    static let v1 = #"{"items":[{"at_ms":null,"id":"n1","text":"v1"}],"schema_version":1}"#
    static let v2 = #"{"items":[{"at_ms":null,"id":"n1","text":"v2"}],"schema_version":1}"#

    // MARK: Document edits

    @Test func editCreatedStoresRevisionAndRemovesRow() async throws {
        let h = try SyncHarness()
        let meeting = h.seedMeeting()
        let engine = h.engine()
        h.server.enqueue("PUT", "/v1/meetings/\(meeting.id.lower)/documents/manual_notes",
                         .json(201, Fx.documentResponse(meetingID: meeting.id, revision: 1, content: Self.v1)))
        engine.saveDraft(meetingID: meeting.id, kind: .manualNotes, wireContent: Data(Self.v1.utf8), loadedRevision: 0)
        await engine.pushAll()

        let put = try #require(h.server.requests(.put, "/documents/manual_notes").first)
        let body = put.json
        #expect(body["expected_revision"] as? Int == 0)
        #expect(body["base_revision"] as? Int == 0)
        #expect(UUID(uuidString: body["mutation_id"] as? String ?? "") != nil)
        #expect(h.store.edit(meeting.id, .manualNotes) == nil)
        #expect(h.store.document(meeting.id, .manualNotes)?.currentRevisionNumber == 1)
        #expect(h.store.documentRevisions(meeting.id, .manualNotes).first?.content != nil)
    }

    @Test func typingDuringInFlightPutKeepsNewerContentAndSendsItOnReturnedRevision() async throws {
        let h = try SyncHarness()
        let meeting = h.seedMeeting()
        let engine = h.engine()
        let path = "/v1/meetings/\(meeting.id.lower)/documents/manual_notes"
        let meetingID = meeting.id
        h.server.enqueue("PUT", path, .json(201, Fx.documentResponse(meetingID: meetingID, revision: 1, content: Self.v1)) {
            DispatchQueue.main.sync {
                MainActor.assumeIsolated {
                    engine.saveDraft(meetingID: meetingID, kind: .manualNotes, wireContent: Data(Self.v2.utf8), loadedRevision: 0)
                }
            }
        })
        h.server.enqueue("PUT", path, .json(201, Fx.documentResponse(meetingID: meetingID, revision: 2, content: Self.v2)))
        engine.saveDraft(meetingID: meetingID, kind: .manualNotes, wireContent: Data(Self.v1.utf8), loadedRevision: 0)
        await engine.pushAll()

        let puts = h.server.requests(.put, "/documents/manual_notes")
        try #require(puts.count == 2)
        #expect(puts[0].bodyString.contains("\"v1\""))
        #expect(puts[1].bodyString.contains("\"v2\""))
        #expect(puts[1].json["base_revision"] as? Int == 1)
        #expect(puts[1].json["expected_revision"] as? Int == 1)
        #expect(puts[0].json["mutation_id"] as? String != puts[1].json["mutation_id"] as? String)
        #expect(h.store.edit(meetingID, .manualNotes) == nil)
        #expect(h.store.document(meetingID, .manualNotes)?.currentRevisionNumber == 2)
    }

    @Test func lostResponseThenMoreEditsReplaysSameMutationWithoutConflictOrLoss() async throws {
        let h = try SyncHarness()
        let meeting = h.seedMeeting()
        let engine = h.engine()
        let path = "/v1/meetings/\(meeting.id.lower)/documents/manual_notes"
        h.server.enqueue("PUT", path, .transport(.networkConnectionLost))
        h.server.enqueue("PUT", path, .json(200, Fx.documentResponse(meetingID: meeting.id, revision: 1, content: Self.v1)))
        h.server.enqueue("PUT", path, .json(201, Fx.documentResponse(meetingID: meeting.id, revision: 2, content: Self.v2)))

        engine.saveDraft(meetingID: meeting.id, kind: .manualNotes, wireContent: Data(Self.v1.utf8), loadedRevision: 0)
        await engine.pushAll()
        let row = try #require(h.store.edit(meeting.id, .manualNotes))
        #expect(row.inFlightMutationID != nil)

        engine.saveDraft(meetingID: meeting.id, kind: .manualNotes, wireContent: Data(Self.v2.utf8), loadedRevision: 0)
        await engine.pushAll()

        let puts = h.server.requests(.put, "/documents/manual_notes")
        try #require(puts.count == 3)
        #expect(puts[0].body == puts[1].body)
        #expect(puts[2].json["mutation_id"] as? String != puts[0].json["mutation_id"] as? String)
        #expect(puts[2].bodyString.contains("\"v2\""))
        #expect(puts[2].json["expected_revision"] as? Int == 1)
        #expect(puts[2].json["base_revision"] as? Int == 1)
        #expect(h.store.edit(meeting.id, .manualNotes) == nil)
        #expect(h.store.document(meeting.id, .manualNotes)?.currentRevisionNumber == 2)
    }

    @Test func revisionConflictKeepsDraftAndBlocksFurtherPushes() async throws {
        let h = try SyncHarness()
        let meeting = h.seedMeeting()
        let engine = h.engine()
        let path = "/v1/meetings/\(meeting.id.lower)/documents/manual_notes"
        h.server.enqueue("PUT", path, .error(409, "revision_conflict", #"{"current_revision_number":5,"current_revision_id":null}"#))
        h.server.always("GET", path, .json(200, Fx.documentResponse(meetingID: meeting.id, revision: 5, content: Self.v1)))
        engine.saveDraft(meetingID: meeting.id, kind: .manualNotes, wireContent: Data(Self.v2.utf8), loadedRevision: 1)
        await engine.pushAll()

        let row = try #require(h.store.edit(meeting.id, .manualNotes))
        #expect(row.conflictCurrentRevision == 5)
        #expect(row.inFlightMutationID == nil)
        #expect(row.content == Data(Self.v2.utf8))
        #expect(h.store.document(meeting.id, .manualNotes)?.currentRevisionNumber == 5)

        await engine.pushAll()
        #expect(h.server.requests(.put, "/documents/manual_notes").count == 1)
    }

    @Test func keepMineSendsCurrentAsExpectedOriginalBaseAndNewMutationID() async throws {
        let h = try SyncHarness()
        let meeting = h.seedMeeting()
        let engine = h.engine()
        let path = "/v1/meetings/\(meeting.id.lower)/documents/manual_notes"
        h.server.enqueue("PUT", path, .error(409, "revision_conflict", #"{"current_revision_number":5,"current_revision_id":null}"#))
        h.server.enqueue("PUT", path, .json(201, Fx.documentResponse(meetingID: meeting.id, revision: 6, content: Self.v2)))
        h.server.always("GET", path, .json(200, Fx.documentResponse(meetingID: meeting.id, revision: 5, content: Self.v1)))
        engine.saveDraft(meetingID: meeting.id, kind: .manualNotes, wireContent: Data(Self.v2.utf8), loadedRevision: 4)
        await engine.pushAll()
        await engine.resolveDocumentConflict(meetingID: meeting.id, kind: .manualNotes, .keepMine)

        let puts = h.server.requests(.put, "/documents/manual_notes")
        try #require(puts.count == 2)
        #expect(puts[1].json["expected_revision"] as? Int == 5)
        #expect(puts[1].json["base_revision"] as? Int == 4)
        #expect(puts[1].json["mutation_id"] as? String != puts[0].json["mutation_id"] as? String)
        #expect(h.store.edit(meeting.id, .manualNotes) == nil)
    }

    @Test func useLatestDropsDraftAndLoadsCurrent() async throws {
        let h = try SyncHarness()
        let meeting = h.seedMeeting()
        let engine = h.engine()
        let path = "/v1/meetings/\(meeting.id.lower)/documents/manual_notes"
        h.server.enqueue("PUT", path, .error(409, "revision_conflict", #"{"current_revision_number":5,"current_revision_id":null}"#))
        h.server.always("GET", path, .json(200, Fx.documentResponse(meetingID: meeting.id, revision: 5, content: Self.v1)))
        engine.saveDraft(meetingID: meeting.id, kind: .manualNotes, wireContent: Data(Self.v2.utf8), loadedRevision: 4)
        await engine.pushAll()
        await engine.resolveDocumentConflict(meetingID: meeting.id, kind: .manualNotes, .useLatest)

        #expect(h.store.edit(meeting.id, .manualNotes) == nil)
        #expect(h.server.requests(.put, "/documents/manual_notes").count == 1)
    }

    @Test func reviewWaitsForPendingEdits() async throws {
        let h = try SyncHarness()
        let meeting = h.seedMeeting()
        let engine = h.engine()
        let base = "/v1/meetings/\(meeting.id.lower)/documents/mom"
        h.seedDocument(meetingID: meeting.id, kind: .mom, revision: 1)
        h.server.enqueue("PUT", base, .json(201, Fx.documentResponse(meetingID: meeting.id, kind: "mom", revision: 2, content: Fx.momContent)))
        h.server.enqueue("PUT", base + "/review", .json(200, Fx.documentResponse(meetingID: meeting.id, kind: "mom", revision: 2, content: nil, review: "reviewed")))
        engine.saveDraft(meetingID: meeting.id, kind: .mom, wireContent: Data(Fx.momContent.utf8), loadedRevision: 1)

        try await engine.setReview(meetingID: meeting.id, kind: .mom, status: .reviewed)

        let paths = h.server.requests.filter { $0.method == "PUT" }.map(\.path)
        #expect(paths == [base, base + "/review"])
        #expect(h.server.requests(.put, "/review").first?.json["expected_revision"] as? Int == 2)
        #expect(h.store.document(meeting.id, .mom)?.reviewStatus == .reviewed)
    }

    @Test func reviewIsNotSentWhileEditCannotReachServer() async throws {
        let h = try SyncHarness()
        let meeting = h.seedMeeting()
        let engine = h.engine()
        h.seedDocument(meetingID: meeting.id, kind: .mom, revision: 1)
        engine.saveDraft(meetingID: meeting.id, kind: .mom, wireContent: Data(Fx.momContent.utf8), loadedRevision: 1)
        h.app.connectivity.update(false)

        await #expect(throws: SyncError.pendingEdits) {
            try await engine.setReview(meetingID: meeting.id, kind: .mom, status: .reviewed)
        }
        #expect(h.server.requests.isEmpty)
    }

    // MARK: Pull

    @Test func meetingMissingFromListingButFoundIsKept() async throws {
        let h = try SyncHarness()
        let meeting = h.seedMeeting()
        h.stubFoldersAndTags()
        h.server.enqueue("GET", "/v1/meetings", .json(200, Fx.page([], next: nil)))
        h.server.always("GET", "/v1/meetings/\(meeting.id.lower)", .json(200, Fx.meeting(id: meeting.id, title: "Moved")))
        await h.engine().fullPull()

        #expect(h.store.meeting(meeting.id)?.title == "Moved")
        #expect(h.store.meeting(meeting.id)?.localPhase == LocalPhase.none)
    }

    @Test func notFoundWithPendingEditIsOrphanedNotDeleted() async throws {
        let h = try SyncHarness()
        let meeting = h.seedMeeting()
        let edit = PendingDocumentEdit(meetingID: meeting.id, kind: .manualNotes, content: Data(Self.v1.utf8), baseRevision: 2)
        edit.conflictCurrentRevision = 3
        h.context.insert(edit)
        h.store.save()
        h.stubFoldersAndTags()
        h.server.enqueue("GET", "/v1/meetings", .json(200, Fx.page([], next: nil)))
        h.server.always("GET", "/v1/meetings/\(meeting.id.lower)", .error(404, "not_found"))
        await h.engine().fullPull()

        #expect(h.store.meeting(meeting.id)?.localPhase == .orphaned)
        #expect(h.store.edit(meeting.id, .manualNotes)?.content == Data(Self.v1.utf8))
    }

    @Test func notFoundWithoutLocalDataIsDeletedLocally() async throws {
        let h = try SyncHarness()
        let meeting = h.seedMeeting()
        h.stubFoldersAndTags()
        h.server.enqueue("GET", "/v1/meetings", .json(200, Fx.page([], next: nil)))
        h.server.always("GET", "/v1/meetings/\(meeting.id.lower)", .error(404, "not_found"))
        await h.engine().fullPull()

        #expect(h.store.meeting(meeting.id) == nil)
    }

    @Test func startedAtChangedMidPaginationDeletesNothing() async throws {
        let h = try SyncHarness()
        let a = h.seedMeeting(), b = h.seedMeeting(), c = h.seedMeeting()
        h.stubFoldersAndTags()
        h.server.enqueue("GET", "/v1/meetings", .json(200, Fx.page([Fx.listItem(id: a.id)], next: "c1")))
        h.server.enqueue("GET", "/v1/meetings", .json(200, Fx.page([Fx.listItem(id: c.id)], next: nil)))
        h.server.always("GET", "/v1/meetings/\(b.id.lower)", .json(200, Fx.meeting(id: b.id)))
        await h.engine().fullPull()

        #expect(h.server.requests(.get, "/v1/meetings").filter { $0.path == "/v1/meetings" }.count == 2)
        #expect(h.server.requests(.get, "/v1/meetings").last { $0.path == "/v1/meetings" }?.query?.contains("cursor=c1") == true)
        #expect([a, b, c].allSatisfy { h.store.meeting($0.id) != nil })
    }

    @Test func pageErrorIsNotTreatedAsEnd() async throws {
        let h = try SyncHarness()
        let a = h.seedMeeting(), b = h.seedMeeting()
        h.stubFoldersAndTags()
        h.server.enqueue("GET", "/v1/meetings", .json(200, Fx.page([Fx.listItem(id: a.id)], next: "c1")))
        h.server.enqueue("GET", "/v1/meetings", .error(503, "unavailable"))
        let engine = h.engine()
        await engine.fullPull()

        #expect(engine.lastPullFailed)
        #expect(h.server.requests(.get, "/v1/meetings/\(b.id.lower)").isEmpty)
        #expect(h.store.meeting(b.id) != nil)
    }

    // MARK: Outbox

    @Test func failingItemInOneMeetingDoesNotBlockAnother() async throws {
        let h = try SyncHarness()
        let a = h.seedMeeting(), b = h.seedMeeting()
        let engine = h.engine()
        h.server.always("PATCH", "/v1/meetings/\(a.id.lower)", .error(500, "internal"))
        h.server.always("PATCH", "/v1/meetings/\(b.id.lower)", .json(200, Fx.meeting(id: b.id, title: "B", metadataRevision: 2)))
        engine.enqueue(.patchMeeting(a.id, MeetingPatchRequest(title: .set("A"))))
        engine.enqueue(.patchMeeting(b.id, MeetingPatchRequest(title: .set("B"))))
        await engine.pushAll()

        #expect(h.server.requests(.patch, "/v1/meetings/\(b.id.lower)").count == 1)
        #expect(h.store.mutations(a.id).count == 1)
        #expect(h.store.mutations(b.id).isEmpty)
        #expect(h.store.meeting(b.id)?.metadataRevision == 2)
        #expect(h.store.meeting(a.id)?.title == "A")
    }

    @Test func folderNotFoundIsRetriedOnceWithoutFolder() async throws {
        let h = try SyncHarness()
        let meeting = h.seedMeeting()
        let folder = Folder(id: UUID(), name: "Klien A", isServerCreated: true)
        h.context.insert(folder)
        h.store.save()
        let engine = h.engine()
        let path = "/v1/meetings/\(meeting.id.lower)"
        h.stubFoldersAndTags()
        h.server.enqueue("PATCH", path, .error(422, "validation_failed", #"{"fields":[{"field":"folder_id","reason":"not_found"}]}"#))
        h.server.enqueue("PATCH", path, .json(200, Fx.meeting(id: meeting.id, title: "T", metadataRevision: 2)))
        engine.enqueue(.patchMeeting(meeting.id, MeetingPatchRequest(title: .set("T"), folderId: .set(folder.id))))
        await engine.pushAll()

        let patches = h.server.requests(.patch, path)
        try #require(patches.count == 2)
        #expect(patches[0].json["folder_id"] as? String == folder.id.lower)
        #expect(patches[1].json["folder_id"] == nil)
        #expect(patches[1].json["title"] as? String == "T")
        #expect(h.store.mutations(meeting.id).isEmpty)
    }

    @Test func metadataConflictFlagsMeetingAndKeepMineRepatchesOnCurrent() async throws {
        let h = try SyncHarness()
        let meeting = h.seedMeeting(metadataRevision: 3)
        let engine = h.engine()
        let path = "/v1/meetings/\(meeting.id.lower)"
        h.server.enqueue("PATCH", path, .error(409, "metadata_conflict", #"{"current_metadata_revision":4}"#))
        h.server.enqueue("PATCH", path, .json(200, Fx.meeting(id: meeting.id, title: "Mine", metadataRevision: 5)))
        h.server.always("GET", path, .json(200, Fx.meeting(id: meeting.id, title: "Theirs", metadataRevision: 4)))
        engine.enqueue(.patchMeeting(meeting.id, MeetingPatchRequest(title: .set("Mine"))))
        await engine.pushAll()

        #expect(h.store.meeting(meeting.id)?.metadataConflict == true)
        #expect(h.store.meeting(meeting.id)?.title == "Mine")
        await engine.resolveMetadataConflict(meetingID: meeting.id, .keepMine)

        let patches = h.server.requests(.patch, path)
        try #require(patches.count == 2)
        #expect(patches[0].json["expected_metadata_revision"] as? Int == 3)
        #expect(patches[1].json["expected_metadata_revision"] as? Int == 4)
        #expect(h.store.meeting(meeting.id)?.metadataConflict == false)
        #expect(h.store.meeting(meeting.id)?.metadataRevision == 5)
    }

    @Test func replayedPatchWhoseFirstAttemptLandedIsNotAConflict() async throws {
        let h = try SyncHarness()
        let meeting = h.seedMeeting(metadataRevision: 3)
        let engine = h.engine()
        let path = "/v1/meetings/\(meeting.id.lower)"
        h.server.enqueue("PATCH", path, .transport(.timedOut))
        h.server.enqueue("PATCH", path, .error(409, "metadata_conflict", #"{"current_metadata_revision":4}"#))
        h.server.always("GET", path, .json(200, Fx.meeting(id: meeting.id, title: "Mine", metadataRevision: 4)))
        engine.enqueue(.patchMeeting(meeting.id, MeetingPatchRequest(title: .set("Mine"))))
        await engine.pushAll()
        await engine.pushAll()

        let patches = h.server.requests(.patch, path)
        #expect(patches.count == 2)
        #expect(patches[0].body == patches[1].body)
        #expect(h.store.meeting(meeting.id)?.metadataConflict == false)
        #expect(h.store.mutations(meeting.id).isEmpty)
    }

    @Test func folderNameTakenRemapsToExistingServerFolder() async throws {
        let h = try SyncHarness()
        let meeting = h.seedMeeting()
        let engine = h.engine()
        let localID = UUID(), serverID = UUID()
        engine.enqueue(.createFolder(id: localID, name: "Klien A"))
        engine.enqueue(.patchMeeting(meeting.id, MeetingPatchRequest(folderId: .set(localID))))
        #expect(h.store.meeting(meeting.id)?.folder?.id == localID)
        h.server.enqueue("POST", "/v1/folders", .error(409, "name_taken"))
        h.server.always("GET", "/v1/folders", .json(200, #"{"items":[\#(Fx.folder(id: serverID, name: "klien a"))]}"#))
        h.server.always("GET", "/v1/tags", .json(200, #"{"items":[]}"#))
        h.server.always("PATCH", "/v1/meetings/\(meeting.id.lower)", .json(200, Fx.meeting(id: meeting.id, folderID: serverID)))
        await engine.pushAll()

        #expect(h.store.folder(localID) == nil)
        #expect(h.store.meeting(meeting.id)?.folder?.id == serverID)
        #expect(h.server.requests(.patch, "/v1/meetings/").first?.json["folder_id"] as? String == serverID.lower)
        #expect(h.store.mutations(nil).isEmpty)
    }

    @Test func offlineCreatedMeetingIsCreatedBeforeItsEdits() async throws {
        let h = try SyncHarness()
        let meeting = h.seedMeeting(serverCreated: false)
        let engine = h.engine()
        h.server.enqueue("POST", "/v1/meetings", .json(201, Fx.meeting(id: meeting.id)))
        h.server.enqueue("PUT", "/v1/meetings/\(meeting.id.lower)/documents/manual_notes",
                         .json(201, Fx.documentResponse(meetingID: meeting.id, revision: 1, content: Self.v1)))
        engine.enqueue(.createMeeting(meeting.id))
        engine.saveDraft(meetingID: meeting.id, kind: .manualNotes, wireContent: Data(Self.v1.utf8), loadedRevision: 0)
        await engine.pushAll()

        #expect(h.server.requests.map(\.method) == ["POST", "PUT"])
        #expect(h.server.requests[0].json["id"] as? String == meeting.id.lower)
        #expect(h.store.meeting(meeting.id)?.isServerCreated == true)
        #expect(h.store.mutations(meeting.id).isEmpty)
    }

    @Test func deleteMeetingPurgesLocallyAndSendsDelete() async throws {
        let h = try SyncHarness()
        let meeting = h.seedMeeting()
        h.context.insert(PendingDocumentEdit(meetingID: meeting.id, kind: .manualNotes, content: Data(Self.v1.utf8), baseRevision: 0))
        h.store.save()
        let engine = h.engine()
        h.server.enqueue("DELETE", "/v1/meetings/\(meeting.id.lower)", .error(404, "not_found"))
        engine.enqueue(.deleteMeeting(meeting.id))
        #expect(h.store.meeting(meeting.id) == nil)
        #expect(h.store.edits(meeting.id).isEmpty)
        await engine.pushAll()

        #expect(h.server.requests.map(\.method) == ["DELETE"])
        #expect(h.store.allMutations().isEmpty)
    }

    @Test func generateWaitsForManualNotesEditThenSendsPersistedKey() async throws {
        let h = try SyncHarness()
        let meeting = h.seedMeeting()
        let engine = h.engine()
        let body = APICoding.encode(GenerateNotesRequest(language: nil))
        engine.saveDraft(meetingID: meeting.id, kind: .manualNotes, wireContent: Data(Self.v1.utf8), loadedRevision: 0)
        h.app.connectivity.update(false)
        try await engine.requestGeneration(meetingID: meeting.id, type: .generateNotes, body: body)
        let intent = try #require(h.store.intents(meeting.id).first)
        let key = intent.idempotencyKey

        h.app.connectivity.update(true)
        h.server.enqueue("PUT", "/v1/meetings/\(meeting.id.lower)/documents/manual_notes",
                         .json(201, Fx.documentResponse(meetingID: meeting.id, revision: 1, content: Self.v1)))
        h.server.enqueue("POST", "/v1/meetings/\(meeting.id.lower)/documents/ai_notes/generate", .transport(.networkConnectionLost))
        h.server.enqueue("POST", "/v1/meetings/\(meeting.id.lower)/documents/ai_notes/generate", .json(202, Fx.jobEnvelope(meetingID: meeting.id, type: "generate_notes")))
        await engine.pushAll()
        await engine.pushAll()

        let generates = h.server.requests(.post, "/ai_notes/generate")
        #expect(h.server.requests.map(\.method) == ["PUT", "POST", "POST"])
        #expect(generates.allSatisfy { $0.headers["Idempotency-Key"] == key.lower && $0.body == body })
        #expect(h.store.intents(meeting.id).isEmpty)
    }

    @Test func usageRefreshUnblocksWaitingQuotaIntents() async throws {
        let h = try SyncHarness()
        await h.loadConfig()
        let meeting = h.seedMeeting()
        meeting.durationMs = 120_000
        meeting.localPhase = .waitingQuota
        let upload = try h.seedUpload(meetingID: meeting.id, bytes: Data(count: 8))
        upload.state = .verified
        let intent = h.seedIntent(meetingID: meeting.id)
        intent.lastErrorCode = "quota_exceeded"
        h.store.save()
        let uploads = h.uploads()
        h.server.always("POST", "/v1/meetings/\(meeting.id.lower)/transcription", .json(202, Fx.jobEnvelope(meetingID: meeting.id)))

        await uploads.retryWaitingIntents(usage: Fx.usage(remaining: 119))
        await uploads.waitUntilIdle()
        await uploads.retryWaitingIntents(usage: Fx.usage(remaining: 3600, status: .expired))
        await uploads.waitUntilIdle()
        #expect(h.server.requests(.post, "/transcription").isEmpty)

        await uploads.retryWaitingIntents(usage: Fx.usage(remaining: 120))
        await uploads.waitUntilIdle()
        #expect(h.server.requests(.post, "/transcription").count == 1)
        #expect(h.server.requests(.post, "/transcription").first?.headers["Idempotency-Key"] == intent.idempotencyKey.lower)
        #expect(h.store.intents(meeting.id).isEmpty)
        #expect(h.store.meeting(meeting.id)?.localPhase == LocalPhase.none)
    }

    @Test func localAudioIsDeletedOnlyOnceTranscribedAndOptedIn() async throws {
        let h = try SyncHarness()
        let meeting = h.seedMeeting()
        let upload = try h.seedUpload(meetingID: meeting.id, bytes: Data(count: 4))
        upload.state = .verified
        let filePath = upload.filePath
        let profile = UserProfile(id: h.userID, displayName: "Ari", language: "id", timezone: "Asia/Jakarta")
        h.context.insert(profile)
        h.store.save()
        let engine = h.engine()
        let path = "/v1/meetings/\(meeting.id.lower)"

        h.server.enqueue("GET", path, .json(200, Fx.meeting(id: meeting.id, audio: true, transcript: true)))
        await engine.refreshMeeting(meeting.id)
        #expect(FileManager.default.fileExists(atPath: filePath))

        profile.deleteLocalAudioAfterTranscript = true
        h.store.save()
        h.server.enqueue("GET", path, .json(200, Fx.meeting(id: meeting.id, audio: true)))
        await engine.refreshMeeting(meeting.id)
        #expect(FileManager.default.fileExists(atPath: filePath))

        h.server.enqueue("GET", path, .json(200, Fx.meeting(id: meeting.id, audio: true, transcript: true)))
        await engine.refreshMeeting(meeting.id)
        #expect(!FileManager.default.fileExists(atPath: filePath))
        #expect(h.store.pendingUpload(meeting.id) == nil)
        #expect(h.store.meeting(meeting.id)?.hasTranscript == true)
    }

    @Test func orphanKeptAsNewMeetingRekeysUploadNotesAndIntents() async throws {
        let h = try SyncHarness()
        let meeting = h.seedMeeting()
        meeting.localPhase = .orphaned
        let upload = try h.seedUpload(meetingID: meeting.id, bytes: Data(count: 8))
        upload.uploadID = UUID()
        let intent = h.seedIntent(meetingID: meeting.id)
        let oldKey = intent.idempotencyKey
        let edit = PendingDocumentEdit(meetingID: meeting.id, kind: .manualNotes, content: Data(Self.v1.utf8), baseRevision: 3)
        edit.inFlightMutationID = UUID()
        h.context.insert(edit)
        h.store.save()

        let newID = try #require(await h.engine().keepOrphanAsNewMeeting(meeting.id))
        #expect(h.store.meeting(meeting.id) == nil)
        let fresh = try #require(h.store.meeting(newID))
        #expect(!fresh.isServerCreated)
        #expect(h.store.pendingUpload(newID)?.uploadID == nil)
        let newEdit = try #require(h.store.edit(newID, .manualNotes))
        #expect(newEdit.baseRevision == 0 && newEdit.expectedRevision == 0 && newEdit.inFlightMutationID == nil)
        let newIntent = try #require(h.store.intents(newID).first)
        #expect(newIntent.idempotencyKey != oldKey)
        #expect(h.store.mutations(newID).map(\.kind) == [.meetingCreate])
    }
}

// MARK: - Shared stub server and fixtures (used by the Sync test suites)

final class StubServer: @unchecked Sendable {
    enum Method: String { case get = "GET", post = "POST", put = "PUT", patch = "PATCH", delete = "DELETE" }

    struct Request: Sendable {
        let method: String
        let path: String
        let query: String?
        let headers: [String: String]
        let body: Data

        var bodyString: String { String(decoding: body, as: UTF8.self) }
        var json: [String: Any] { (try? JSONSerialization.jsonObject(with: body)) as? [String: Any] ?? [:] }
    }

    struct Reply: Sendable {
        var status: Int
        var body: String
        var error: URLError?
        var onReceive: (@Sendable () -> Void)?

        static func json(_ status: Int, _ body: String, onReceive: (@Sendable () -> Void)? = nil) -> Reply {
            Reply(status: status, body: body, error: nil, onReceive: onReceive)
        }

        static func error(_ status: Int, _ code: String, _ details: String = "{}") -> Reply {
            .json(status, #"{"error":{"code":"\#(code)","message":"m","details":\#(details),"request_id":"rid"}}"#)
        }

        static func transport(_ code: URLError.Code) -> Reply {
            Reply(status: 0, body: "", error: URLError(code), onReceive: nil)
        }
    }

    private static let lock = NSLock()
    nonisolated(unsafe) private static var servers: [String: StubServer] = [:]

    let host = "s\(UUID().uuidString.prefix(12).lowercased()).test"
    private let lock = NSLock()
    private var queued: [String: [Reply]] = [:]
    private var sticky: [String: Reply] = [:]
    private var log: [Request] = []

    var baseURL: URL { URL(string: "http://\(host)")! }
    var requests: [Request] { lock.withLock { log } }

    init() {
        Self.lock.withLock { Self.servers[host] = self }
    }

    static func server(for host: String?) -> StubServer? {
        guard let host else { return nil }
        return lock.withLock { servers[host] }
    }

    func enqueue(_ method: String, _ path: String, _ reply: Reply) {
        lock.withLock { queued["\(method) \(path)", default: []].append(reply) }
    }

    func always(_ method: String, _ path: String, _ reply: Reply) {
        lock.withLock { sticky["\(method) \(path)"] = reply }
    }

    func requests(_ method: Method, _ pathContains: String) -> [Request] {
        requests.filter { $0.method == method.rawValue && $0.path.contains(pathContains) }
    }

    func respond(to request: Request) -> Reply {
        let key = "\(request.method) \(request.path)"
        return lock.withLock {
            log.append(request)
            if var replies = queued[key], !replies.isEmpty {
                let reply = replies.removeFirst()
                queued[key] = replies
                return reply
            }
            return sticky[key] ?? .error(599, "unstubbed")
        }
    }
}

final class SyncStubProtocol: URLProtocol, @unchecked Sendable {
    override class func canInit(with request: URLRequest) -> Bool { StubServer.server(for: request.url?.host) != nil }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        guard let url = request.url, let server = StubServer.server(for: url.host) else { return }
        let captured = StubServer.Request(
            method: request.httpMethod ?? "GET",
            path: url.path,
            query: url.query,
            headers: request.allHTTPHeaderFields ?? [:],
            body: Self.readBody(request)
        )
        let reply = server.respond(to: captured)
        reply.onReceive?()
        if let error = reply.error {
            client?.urlProtocol(self, didFailWithError: error)
            return
        }
        let response = HTTPURLResponse(url: url, statusCode: reply.status, httpVersion: "HTTP/1.1", headerFields: ["Content-Type": "application/json"])!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data(reply.body.utf8))
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}

    static func readBody(_ request: URLRequest) -> Data {
        if let body = request.httpBody { return body }
        guard let stream = request.httpBodyStream else { return Data() }
        stream.open()
        defer { stream.close() }
        var data = Data()
        var buffer = [UInt8](repeating: 0, count: 4096)
        while stream.hasBytesAvailable {
            let n = stream.read(&buffer, maxLength: buffer.count)
            if n <= 0 { break }
            data.append(buffer, count: n)
        }
        return data
    }
}

@MainActor
final class StubChunkTransport: ChunkTransport {
    struct Sent {
        let offset: String?
        let file: URL
        let bytes: Data
    }

    let session: URLSession
    private(set) var sent: [Sent] = []

    init(session: URLSession) {
        self.session = session
    }

    func upload(_ request: URLRequest, fromFile file: URL, taskDescription: String) async throws -> (Data, URLResponse) {
        sent.append(Sent(offset: request.value(forHTTPHeaderField: "Upload-Offset"), file: file, bytes: (try? Data(contentsOf: file)) ?? Data()))
        return try await session.upload(for: request, fromFile: file)
    }

    func hasRunningTask(_ taskDescription: String) async -> Bool { false }
    func cancel(_ taskDescription: String) {}
}

@MainActor
final class SyncHarness {
    let server = StubServer()
    let session: URLSession
    let app: AppModel
    let container: ModelContainer
    let env: ServiceEnvironment
    let userID = UUID()

    var context: ModelContext { container.mainContext }
    var store: SyncStore { SyncStore(context: context) }

    init() throws {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [SyncStubProtocol.self]
        session = URLSession(configuration: config)
        app = AppModel(
            auth: AuthStore(service: "com.kolarislabs.phoebe.tests.\(UUID().uuidString)"),
            session: session,
            baseURL: server.baseURL,
            defaults: UserDefaults(suiteName: "phoebe.tests.\(UUID().uuidString)")!
        )
        container = try ModelContainerFactory.inMemory()
        env = ServiceEnvironment(api: app.api, context: container.mainContext, userID: userID, auth: app.auth, app: app)
    }

    func engine() -> SyncEngine { SyncEngine(env: env, sleep: { _ in }, automatic: false) }

    func uploads(transport: StubChunkTransport? = nil) -> UploadManager {
        UploadManager(env: env, transport: transport ?? StubChunkTransport(session: session), sleep: { _ in })
    }

    func loadConfig(chunkMax: Int = 33_554_432) async {
        server.always("GET", "/v1/config", .json(200, Fx.config(chunkMax: chunkMax)))
        await app.refreshConfig()
    }

    func stubFoldersAndTags() {
        server.always("GET", "/v1/folders", .json(200, #"{"items":[]}"#))
        server.always("GET", "/v1/tags", .json(200, #"{"items":[]}"#))
    }

    @discardableResult
    func seedMeeting(serverCreated: Bool = true, metadataRevision: Int = 1) -> Meeting {
        let meeting = Meeting(source: .recording, title: "Weekly", startedAt: APICoding.parseDate(Fx.time))
        meeting.isServerCreated = serverCreated
        meeting.metadataRevision = metadataRevision
        meeting.updatedAt = APICoding.parseDate(Fx.time)
        context.insert(meeting)
        store.save()
        return meeting
    }

    func seedDocument(meetingID: UUID, kind: DocumentKind, revision: Int) {
        context.insert(Document(id: UUID(), meetingID: meetingID, kind: kind, currentRevisionNumber: revision, currentRevisionID: UUID(), reviewStatus: .draft, updatedAt: .now))
        store.save()
    }

    func seedUpload(meetingID: UUID, bytes: Data) throws -> PendingUpload {
        let dir = FileManager.default.temporaryDirectory.appending(path: "sync-tests/\(UUID().uuidString)", directoryHint: .isDirectory)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let file = dir.appending(path: "audio.m4a")
        try bytes.write(to: file)
        let upload = PendingUpload(meetingID: meetingID, ownerUserID: userID, filePath: file.path, sizeBytes: bytes.count,
                                   sha256: Checksum.sha256Hex(bytes), contentType: "audio/mp4", durationMs: 60_000)
        context.insert(upload)
        store.save()
        return upload
    }

    @discardableResult
    func seedIntent(meetingID: UUID, language: TranscriptionLanguage = .id) -> PendingIntent {
        let intent = PendingIntent.transcribe(meetingID: meetingID, language: language)
        context.insert(intent)
        store.save()
        return intent
    }
}

enum Fx {
    static let time = "2026-11-06T02:00:00.000Z"

    static func config(chunkMax: Int) -> String {
        #"{"max_audio_duration_ms":7200000,"max_upload_bytes":262144000,"upload_chunk_max_bytes":\#(chunkMax),"supported_content_types":["audio/mp4"],"audio_retention_days":30,"languages":["id","en"]}"#
    }

    static func session(id: UUID, meetingID: UUID, status: String = "open", size: Int, received: Int, chunkMax: Int = 33_554_432, failure: String? = nil) -> String {
        let reason = failure.map { "\"\($0)\"" } ?? "null"
        return #"{"id":"\#(id.lower)","meeting_id":"\#(meetingID.lower)","status":"\#(status)","content_type":"audio/mp4","size_bytes":\#(size),"received_bytes":\#(received),"sha256":"abc","duration_ms":60000,"chunk_max_bytes":\#(chunkMax),"failure_reason":\#(reason),"auto_transcription":true,"expires_at":"\#(time)","created_at":"\#(time)"}"#
    }

    static func uploadEnvelope(_ session: String) -> String { #"{"upload":\#(session)}"# }

    static func uploadResult(_ session: String, audio: Bool, job: String? = nil) -> String {
        #"{"upload":\#(session),"audio":\#(audio ? audioJSON() : "null"),"job":\#(job ?? "null")}"#
    }

    static func audioJSON(size: Int = 10) -> String {
        #"{"id":"\#(UUID().uuidString.lowercased())","content_type":"audio/mp4","size_bytes":\#(size),"duration_ms":60000,"sha256":"abc","available":true,"deleted_reason":null,"retain_until":null,"created_at":"\#(time)"}"#
    }

    static func job(meetingID: UUID, type: String = "transcribe", status: String = "queued") -> String {
        #"{"id":"\#(UUID().uuidString.lowercased())","meeting_id":"\#(meetingID.lower)","type":"\#(type)","trigger":"user","status":"\#(status)","attempts":0,"max_attempts":3,"error_category":null,"result":null,"created_at":"\#(time)","started_at":null,"finished_at":null}"#
    }

    static func jobEnvelope(meetingID: UUID, type: String = "transcribe") -> String { #"{"job":\#(job(meetingID: meetingID, type: type))}"# }

    static func meeting(id: UUID, title: String = "Weekly", metadataRevision: Int = 1, folderID: UUID? = nil, audio: Bool = false, transcript: Bool = false, updatedAt: String = time) -> String {
        let folder = folderID.map { "\"\($0.lower)\"" } ?? "null"
        let pointer = transcript ? #"{"current_revision_id":"\#(UUID().uuidString.lowercased())","current_revision_number":1}"# : "null"
        return #"{"meeting":{"id":"\#(id.lower)","source":"recording","title":"\#(title)","started_at":"\#(time)","timezone":"Asia/Jakarta","language":"auto","participants":[],"metadata_revision":\#(metadataRevision),"folder_id":\#(folder),"tag_ids":[],"pipeline_state":"\#(audio ? "uploaded" : "awaiting_audio")","created_at":"\#(time)","updated_at":"\#(updatedAt)","audio":\#(audio ? audioJSON() : "null"),"upload":null,"transcript":\#(pointer),"documents":{"manual_notes":null,"ai_notes":null,"mom":null},"jobs":{"transcribe":null,"generate_notes":null,"generate_mom":null}}}"#
    }

    static func listItem(id: UUID, updatedAt: String = time) -> String {
        #"{"id":"\#(id.lower)","source":"recording","title":"Weekly","started_at":"\#(time)","timezone":"Asia/Jakarta","folder_id":null,"tag_ids":[],"pipeline_state":"notes_ready","duration_ms":60000,"audio_available":true,"mom_review_status":null,"active_job_types":[],"failed_stage":null,"transcription_error_category":null,"created_at":"\#(time)","updated_at":"\#(updatedAt)"}"#
    }

    static func page(_ items: [String], next: String?) -> String {
        #"{"items":[\#(items.joined(separator: ","))],"next_cursor":\#(next.map { "\"\($0)\"" } ?? "null")}"#
    }

    static func folder(id: UUID, name: String) -> String {
        #"{"id":"\#(id.lower)","name":"\#(name)","meeting_count":0,"created_at":"\#(time)","updated_at":"\#(time)"}"#
    }

    static let momContent = #"{"language":"id","meeting_info":{"agenda":[],"participants":[],"started_at":null,"timezone":"Asia/Jakarta","title":"Weekly"},"schema_version":1,"sections":[]}"#

    static func documentResponse(meetingID: UUID, kind: String = "manual_notes", revision: Int, content: String?, review: String = "draft") -> String {
        let docID = UUID(uuidString: "00000000-0000-4000-8000-\(String(format: "%012d", kind.count))")!.lower
        let summary = #"{"id":"\#(docID)","kind":"\#(kind)","current_revision_number":\#(revision),"current_revision_id":"\#(revisionID(revision).lower)","review_status":"\#(review)","is_stale":false,"stale_reasons":[],"updated_at":"\#(time)"}"#
        guard let content else { return #"{"document":\#(summary),"revision":null}"# }
        let rev = #"{"id":"\#(revisionID(revision).lower)","document_id":"\#(docID)","kind":"\#(kind)","revision_number":\#(revision),"origin":"user_edit","restored_from_revision_number":null,"reviewed_at":null,"created_at":"\#(time)","source":{"transcript_revision_id":null,"transcript_revision_number":null,"manual_notes_revision_id":null,"manual_notes_revision_number":null,"ai_notes_revision_id":null,"ai_notes_revision_number":null},"content":\#(content)}"#
        return #"{"document":\#(summary),"revision":\#(rev)}"#
    }

    static func revisionID(_ n: Int) -> UUID {
        UUID(uuidString: "00000000-0000-4000-9000-\(String(format: "%012d", n))")!
    }

    static func usage(remaining: Int, status: EntitlementStatus = .active) -> UsageDTO {
        UsageDTO(
            entitlement: EntitlementDTO(plan: "trial", status: status, periodStart: nil, periodEnd: nil, quotaSeconds: 7200),
            consumedSeconds: 0, reservedSeconds: 0, remainingSeconds: remaining,
            aiGenerations: AIGenerationsDTO(limitPerDay: 30, usedLast24h: 0, nextAvailableAt: nil)
        )
    }
}
