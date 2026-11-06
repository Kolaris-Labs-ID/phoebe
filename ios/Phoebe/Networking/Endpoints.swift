import Foundation

enum FolderQuery: Sendable, Equatable {
    case unfiled
    case folder(UUID)

    var value: String {
        switch self {
        case .unfiled: "none"
        case .folder(let id): id.lower
        }
    }
}

enum Endpoints {
    private static func json<T: Encodable>(_ value: T) -> Data { APICoding.encode(value) }

    private static func idempotent(_ key: UUID) -> [String: String] { ["Idempotency-Key": key.lower] }

    private static func pageQuery(limit: Int?, cursor: String?) -> [URLQueryItem] {
        var q: [URLQueryItem] = []
        if let limit { q.append(URLQueryItem(name: "limit", value: String(limit))) }
        if let cursor { q.append(URLQueryItem(name: "cursor", value: cursor)) }
        return q
    }

    // MARK: 4.0 Health, config

    static func health() -> Endpoint<HealthDTO> {
        Endpoint(method: .get, path: "/healthz", requiresAuth: false)
    }

    static func config() -> Endpoint<ServerConfig> {
        Endpoint(method: .get, path: "/v1/config", requiresAuth: false)
    }

    // MARK: 4.1 Auth, account

    static func signInWithApple(_ body: AppleAuthRequest) -> Endpoint<AuthResponseDTO> {
        Endpoint(method: .post, path: "/v1/auth/apple", body: json(body), requiresAuth: false)
    }

    static func devLogin(subject: String) -> Endpoint<AuthResponseDTO> {
        Endpoint(method: .post, path: "/v1/auth/dev", body: json(DevAuthRequest(subject: subject)), requiresAuth: false)
    }

    static func logout() -> Endpoint<EmptyResponse> {
        Endpoint(method: .post, path: "/v1/auth/logout")
    }

    static func me() -> Endpoint<UserEnvelope> {
        Endpoint(method: .get, path: "/v1/me")
    }

    static func updateMe(_ body: UpdateMeRequest) -> Endpoint<UserEnvelope> {
        Endpoint(method: .patch, path: "/v1/me", body: json(body))
    }

    static func deleteAccount() -> Endpoint<PurgeResponseDTO> {
        Endpoint(method: .delete, path: "/v1/me")
    }

    // MARK: 4.2 Meetings

    static func createMeeting(_ body: CreateMeetingRequest) -> Endpoint<MeetingEnvelope> {
        Endpoint(method: .post, path: "/v1/meetings", body: json(body))
    }

    static func listMeetings(q: String? = nil, folder: FolderQuery? = nil, tagID: UUID? = nil, limit: Int? = nil, cursor: String? = nil) -> Endpoint<Page<MeetingListItemDTO>> {
        var query: [URLQueryItem] = []
        if let q { query.append(URLQueryItem(name: "q", value: q)) }
        if let folder { query.append(URLQueryItem(name: "folder_id", value: folder.value)) }
        if let tagID { query.append(URLQueryItem(name: "tag_id", value: tagID.lower)) }
        return Endpoint(method: .get, path: "/v1/meetings", query: query + pageQuery(limit: limit, cursor: cursor))
    }

    static func meeting(_ id: UUID) -> Endpoint<MeetingEnvelope> {
        Endpoint(method: .get, path: "/v1/meetings/\(id.lower)")
    }

    static func patchMeeting(_ id: UUID, _ body: MeetingPatchRequest) -> Endpoint<MeetingEnvelope> {
        Endpoint(method: .patch, path: "/v1/meetings/\(id.lower)", body: json(body))
    }

    static func patchMeeting(_ id: UUID, rawBody: Data) -> Endpoint<MeetingEnvelope> {
        Endpoint(method: .patch, path: "/v1/meetings/\(id.lower)", body: rawBody)
    }

    static func deleteMeeting(_ id: UUID) -> Endpoint<PurgeResponseDTO> {
        Endpoint(method: .delete, path: "/v1/meetings/\(id.lower)")
    }

    // MARK: 4.3 Folders, tags

    static func folders() -> Endpoint<ItemsResponse<FolderDTO>> {
        Endpoint(method: .get, path: "/v1/folders")
    }

    static func createFolder(id: UUID, name: String) -> Endpoint<FolderEnvelope> {
        Endpoint(method: .post, path: "/v1/folders", body: json(CreateNamedRequest(id: id, name: name)))
    }

    static func renameFolder(_ id: UUID, name: String) -> Endpoint<FolderEnvelope> {
        Endpoint(method: .patch, path: "/v1/folders/\(id.lower)", body: json(RenameRequest(name: name)))
    }

    static func deleteFolder(_ id: UUID) -> Endpoint<EmptyResponse> {
        Endpoint(method: .delete, path: "/v1/folders/\(id.lower)")
    }

    static func tags() -> Endpoint<ItemsResponse<TagDTO>> {
        Endpoint(method: .get, path: "/v1/tags")
    }

    static func createTag(id: UUID, name: String) -> Endpoint<TagEnvelope> {
        Endpoint(method: .post, path: "/v1/tags", body: json(CreateNamedRequest(id: id, name: name)))
    }

    static func renameTag(_ id: UUID, name: String) -> Endpoint<TagEnvelope> {
        Endpoint(method: .patch, path: "/v1/tags/\(id.lower)", body: json(RenameRequest(name: name)))
    }

    static func deleteTag(_ id: UUID) -> Endpoint<EmptyResponse> {
        Endpoint(method: .delete, path: "/v1/tags/\(id.lower)")
    }

    // MARK: 4.4 Uploads

    static func createUpload(meetingID: UUID, _ body: CreateUploadRequest) -> Endpoint<UploadEnvelope> {
        Endpoint(method: .post, path: "/v1/meetings/\(meetingID.lower)/uploads", body: json(body))
    }

    static func upload(_ uploadID: UUID) -> Endpoint<UploadEnvelope> {
        Endpoint(method: .get, path: "/v1/uploads/\(uploadID.lower)")
    }

    // Body is supplied by the (background) upload task from a file; Content-Length is set by URLSession.
    static func uploadChunk(_ uploadID: UUID, offset: Int) -> Endpoint<UploadResultDTO> {
        Endpoint(
            method: .put,
            path: "/v1/uploads/\(uploadID.lower)",
            headers: ["Upload-Offset": String(offset), "Content-Type": "application/octet-stream"]
        )
    }

    static func completeUpload(_ uploadID: UUID) -> Endpoint<UploadResultDTO> {
        Endpoint(method: .post, path: "/v1/uploads/\(uploadID.lower)/complete")
    }

    // MARK: 4.5 Transcription, jobs

    // `body` = the exact bytes persisted in PendingIntent.body (identical on every retry).
    static func startTranscription(meetingID: UUID, idempotencyKey: UUID, body: Data) -> Endpoint<JobEnvelope> {
        Endpoint(method: .post, path: "/v1/meetings/\(meetingID.lower)/transcription", body: body, headers: idempotent(idempotencyKey))
    }

    static func jobs(meetingID: UUID) -> Endpoint<ItemsResponse<JobDTO>> {
        Endpoint(method: .get, path: "/v1/meetings/\(meetingID.lower)/jobs")
    }

    static func job(_ id: UUID) -> Endpoint<JobEnvelope> {
        Endpoint(method: .get, path: "/v1/jobs/\(id.lower)")
    }

    // MARK: 4.6 Transcript

    static func transcript(meetingID: UUID) -> Endpoint<TranscriptRevisionEnvelope> {
        Endpoint(method: .get, path: "/v1/meetings/\(meetingID.lower)/transcript")
    }

    static func transcriptRevisions(meetingID: UUID, limit: Int? = nil, cursor: String? = nil) -> Endpoint<Page<TranscriptRevisionDTO>> {
        Endpoint(method: .get, path: "/v1/meetings/\(meetingID.lower)/transcript/revisions", query: pageQuery(limit: limit, cursor: cursor))
    }

    static func transcriptRevision(meetingID: UUID, number: Int) -> Endpoint<TranscriptRevisionEnvelope> {
        Endpoint(method: .get, path: "/v1/meetings/\(meetingID.lower)/transcript/revisions/\(number)")
    }

    static func correctTranscript(meetingID: UUID, _ body: TranscriptCorrectionRequest) -> Endpoint<TranscriptRevisionEnvelope> {
        Endpoint(method: .post, path: "/v1/meetings/\(meetingID.lower)/transcript/revisions", body: json(body))
    }

    // MARK: 4.7 Documents

    static func document(meetingID: UUID, kind: DocumentKind) -> Endpoint<DocumentResponse> {
        Endpoint(method: .get, path: "/v1/meetings/\(meetingID.lower)/documents/\(kind.rawValue)")
    }

    // `content` = wire-form JSON (DocumentContentValue.wireData()), spliced verbatim so a retry
    // with the same mutation id sends the identical body.
    static func putDocument(meetingID: UUID, kind: DocumentKind, expectedRevision: Int, baseRevision: Int, mutationID: UUID, content: Data) -> Endpoint<DocumentResponse> {
        var body = Data("{\"base_revision\":\(baseRevision),\"content\":".utf8)
        body.append(content)
        body.append(Data(",\"expected_revision\":\(expectedRevision),\"mutation_id\":\"\(mutationID.lower)\"}".utf8))
        return Endpoint(method: .put, path: "/v1/meetings/\(meetingID.lower)/documents/\(kind.rawValue)", body: body)
    }

    static func documentRevisions(meetingID: UUID, kind: DocumentKind, limit: Int? = nil, cursor: String? = nil) -> Endpoint<Page<DocumentRevisionDTO>> {
        Endpoint(method: .get, path: "/v1/meetings/\(meetingID.lower)/documents/\(kind.rawValue)/revisions", query: pageQuery(limit: limit, cursor: cursor))
    }

    static func documentRevision(meetingID: UUID, kind: DocumentKind, number: Int) -> Endpoint<DocumentRevisionEnvelope> {
        Endpoint(method: .get, path: "/v1/meetings/\(meetingID.lower)/documents/\(kind.rawValue)/revisions/\(number)")
    }

    static func restoreDocument(meetingID: UUID, kind: DocumentKind, _ body: RestoreRequest) -> Endpoint<DocumentResponse> {
        Endpoint(method: .post, path: "/v1/meetings/\(meetingID.lower)/documents/\(kind.rawValue)/restore", body: json(body))
    }

    static func setReview(meetingID: UUID, kind: DocumentKind, status: ReviewStatus, expectedRevision: Int) -> Endpoint<DocumentResponse> {
        Endpoint(method: .put, path: "/v1/meetings/\(meetingID.lower)/documents/\(kind.rawValue)/review", body: json(ReviewRequest(status: status, expectedRevision: expectedRevision)))
    }

    static func generateNotes(meetingID: UUID, idempotencyKey: UUID, body: Data) -> Endpoint<JobEnvelope> {
        Endpoint(method: .post, path: "/v1/meetings/\(meetingID.lower)/documents/ai_notes/generate", body: body, headers: idempotent(idempotencyKey))
    }

    static func generateMoM(meetingID: UUID, idempotencyKey: UUID, body: Data) -> Endpoint<JobEnvelope> {
        Endpoint(method: .post, path: "/v1/meetings/\(meetingID.lower)/documents/mom/generate", body: body, headers: idempotent(idempotencyKey))
    }

    // MARK: 4.8 Exports

    static func createExport(documentRevisionID: UUID, format: ExportFormat) -> Endpoint<ExportResponse> {
        Endpoint(method: .post, path: "/v1/exports", body: json(ExportRequest(documentRevisionId: documentRevisionID, format: format)))
    }

    static func export(_ id: UUID) -> Endpoint<ExportResponse> {
        Endpoint(method: .get, path: "/v1/exports/\(id.lower)")
    }

    // MARK: 4.9 Audio; signed /v1/files/... URLs are absolute and fetched without bearer auth.

    static func audioURL(meetingID: UUID) -> Endpoint<SignedURLResponse> {
        Endpoint(method: .get, path: "/v1/meetings/\(meetingID.lower)/audio/url")
    }

    // MARK: 4.10 Usage

    static func usage() -> Endpoint<UsageDTO> {
        Endpoint(method: .get, path: "/v1/usage")
    }

    // MARK: 4.11 Deletion

    static func deleteAudio(meetingID: UUID) -> Endpoint<EmptyResponse> {
        Endpoint(method: .delete, path: "/v1/meetings/\(meetingID.lower)/audio")
    }

    // MARK: 4.12 Events

    static func events(_ events: [EventDTO]) -> Endpoint<EventsAcceptedDTO> {
        Endpoint(method: .post, path: "/v1/events", body: json(EventsRequest(events: events)))
    }
}
