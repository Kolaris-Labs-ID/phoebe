import Foundation
import SwiftData

// ios.md §8: resumable, checksummed upload per PendingUpload, then the transcribe intent.
@MainActor @Observable
final class UploadManager: UploadManaging {
    @ObservationIgnored private let env: ServiceEnvironment
    @ObservationIgnored private let transport: any ChunkTransport
    @ObservationIgnored private let sleep: @Sendable (TimeInterval) async -> Void
    @ObservationIgnored private var runners: [UUID: Task<Void, Never>] = [:]
    @ObservationIgnored private var rerun: Set<UUID> = []
    @ObservationIgnored private var chunkMax: [UUID: Int] = [:]
    private var liveProgress: [UUID: Double] = [:]

    private var store: SyncStore { SyncStore(context: env.context) }
    private var gateOpen: Bool { env.app.serverConfig?.fetchedAt != nil }

    convenience init(env: ServiceEnvironment) {
        self.init(env: env, transport: BackgroundChunkTransport(), sleep: { try? await Task.sleep(for: .seconds($0)) })
        BackgroundUploadSession.shared.setHandler { [weak self] event in
            Task { @MainActor in self?.handle(event) }
        }
    }

    init(env: ServiceEnvironment, transport: any ChunkTransport, sleep: @escaping @Sendable (TimeInterval) async -> Void) {
        self.env = env
        self.transport = transport
        self.sleep = sleep
    }

    // MARK: UploadManaging

    func resumeAll() async {
        guard gateOpen else { return }
        for upload in store.pendingUploads() where upload.ownerUserID == env.userID {
            switch upload.state {
            case .failed, .rejected: continue
            case .verified:
                if store.intents(upload.meetingID).contains(where: { $0.type == .transcribe && $0.lastErrorCode == nil }) { start(upload.meetingID) }
            default: start(upload.meetingID)
            }
        }
    }

    func kick(meetingID: UUID) async {
        start(meetingID)
    }

    func retry(meetingID: UUID) async {
        if let upload = store.pendingUpload(meetingID), upload.state == .failed || upload.state == .rejected {
            upload.state = .waiting
            upload.uploadID = nil
            upload.receivedBytes = 0
            upload.attempts = 0
            upload.rejectCode = nil
            if let meeting = store.meeting(meetingID) {
                meeting.localPhase = .queued
                meeting.rejectCode = nil
            }
        }
        store.intents(meetingID).filter { $0.type == .transcribe }.forEach { $0.lastErrorCode = nil }
        store.save()
        start(meetingID)
    }

    func cancel(meetingID: UUID) {
        runners[meetingID]?.cancel()
        if let uploadID = store.pendingUpload(meetingID)?.uploadID { transport.cancel(uploadID.lower) }
        liveProgress[meetingID] = nil
    }

    func progress(meetingID: UUID) -> Double? {
        if let live = liveProgress[meetingID] { return min(live, 1) }
        guard let upload = store.pendingUpload(meetingID), upload.sizeBytes > 0,
              [.uploading, .completing].contains(upload.state) else { return nil }
        return Double(upload.receivedBytes) / Double(upload.sizeBytes)
    }

    func retryWaitingIntents(usage: UsageDTO) async {
        guard usage.entitlement.status == .active else { return }
        for intent in store.allIntents() where intent.type == .transcribe {
            guard let code = intent.lastErrorCode, code == "quota_exceeded" || code == "entitlement_inactive" else { continue }
            let durationMs = store.meeting(intent.meetingID)?.durationMs ?? store.pendingUpload(intent.meetingID)?.durationMs ?? 0
            let required = Int((Double(durationMs) / 1000).rounded(.up))
            guard usage.remainingSeconds >= required else { continue }
            intent.lastErrorCode = nil
            start(intent.meetingID)
        }
        store.save()
    }

    func waitUntilIdle() async {
        while let task = runners.values.first { await task.value }
    }

    // MARK: Runner

    private func start(_ meetingID: UUID) {
        guard runners[meetingID] == nil else {
            rerun.insert(meetingID)
            return
        }
        runners[meetingID] = Task { [weak self] in
            guard let self else { return }
            repeat {
                self.rerun.remove(meetingID)
                await self.drive(meetingID)
            } while self.rerun.contains(meetingID) && !Task.isCancelled
            self.runners[meetingID] = nil
        }
    }

    private func drive(_ meetingID: UUID) async {
        var backoff = Backoff.upload
        var pending: UploadPlanner.Action?
        while !Task.isCancelled {
            guard gateOpen, let meeting = store.meeting(meetingID), meeting.localPhase != .orphaned else { return }
            let upload = store.pendingUpload(meetingID)
            if let upload, upload.ownerUserID != env.userID || upload.state == .failed || upload.state == .rejected { return }
            guard env.app.isOnline else {
                if let upload, upload.state != .verified { setPhase(meeting, .waitingConnection) }
                return
            }

            guard let upload, upload.state != .verified else {
                guard await sendTranscribeIntent(meetingID, backoff: &backoff) else { return }
                continue
            }

            if !meeting.isServerCreated {
                setPhase(meeting, .uploading)
                do {
                    guard try await SyncRemote.ensureMeeting(meetingID, env: env) else { return }
                } catch let error as APIError {
                    guard isLive(upload, meeting) else { return }
                    let next = UploadPlanner.next(after: error, stage: .createMeeting, checksumRestarts: upload.attempts)
                    guard let after = await resolve(next, upload: upload, meeting: meeting, backoff: &backoff) else { return }
                    pending = after
                    continue
                } catch {
                    return
                }
            }

            let action = pending ?? (upload.uploadID == nil ? .createSession : .refreshSession)
            pending = nil
            do {
                guard let next = try await perform(action, upload: upload, meeting: meeting, backoff: &backoff), isLive(upload, meeting) else { return }
                pending = next
            } catch let error as APIError {
                guard isLive(upload, meeting) else { return }
                let next = UploadPlanner.next(after: error, stage: stage(of: action), checksumRestarts: upload.attempts)
                guard let after = await resolve(next, upload: upload, meeting: meeting, backoff: &backoff) else { return }
                pending = after
            } catch {
                return
            }
        }
    }

    // Deleting or orphaning a meeting cancels its run and may delete these rows while a request is awaited.
    private func isLive(_ upload: PendingUpload, _ meeting: Meeting) -> Bool {
        !Task.isCancelled && !upload.isDeleted && upload.modelContext != nil && !meeting.isDeleted && meeting.modelContext != nil
    }

    private func stage(of action: UploadPlanner.Action) -> UploadPlanner.Stage {
        switch action {
        case .createSession, .checkServerAudio, .restartAfterChecksum: .createSession
        case .uploadChunks, .resume: .putChunk
        case .complete: .complete
        default: .refreshSession
        }
    }

    // Executes one network step. Returns the next action, or nil to stop this run.
    private func perform(_ action: UploadPlanner.Action, upload: PendingUpload, meeting: Meeting, backoff: inout Backoff) async throws -> UploadPlanner.Action? {
        switch action {
        case .createSession:
            setPhase(meeting, .uploading)
            upload.state = .creating
            let intent = store.intents(meeting.id).first { $0.type == .transcribe }
            let transcription = intent.map {
                TranscriptionStart(idempotencyKey: $0.idempotencyKey, language: IntentSender.language(of: $0) ?? meeting.transcriptionLanguage, notes: IntentSender.notes(of: $0))
            }
            intent?.keySent = true
            store.save()
            let body = CreateUploadRequest(
                sizeBytes: upload.sizeBytes, sha256: upload.sha256, contentType: upload.contentType,
                durationMs: upload.durationMs, transcription: transcription
            )
            let session = try await env.api.send(Endpoints.createUpload(meetingID: meeting.id, body)).upload
            guard isLive(upload, meeting) else { return nil }
            backoff.reset()
            apply(session, to: upload)
            return UploadPlanner.next(after: session, checksumRestarts: upload.attempts)

        case .refreshSession:
            guard let uploadID = upload.uploadID else { return .createSession }
            let session = try await env.api.send(Endpoints.upload(uploadID)).upload
            guard isLive(upload, meeting) else { return nil }
            apply(session, to: upload)
            return UploadPlanner.next(after: session, checksumRestarts: upload.attempts)

        case .resume(let offset):
            upload.receivedBytes = offset
            store.save()
            return .uploadChunks

        case .uploadChunks:
            return try await uploadNextChunk(upload: upload, meeting: meeting, backoff: &backoff)

        case .complete:
            guard let uploadID = upload.uploadID else { return .createSession }
            setPhase(meeting, .verifying)
            upload.state = .completing
            store.save()
            let result = try await env.api.send(Endpoints.completeUpload(uploadID))
            guard isLive(upload, meeting) else { return nil }
            backoff.reset()
            return handle(result, upload: upload, meeting: meeting)

        case .pollVerifying:
            setPhase(meeting, .verifying)
            await sleep(backoff.nextDelay())
            return .refreshSession

        case .checkServerAudio:
            let dto = try await env.api.send(Endpoints.meeting(meeting.id)).meeting
            guard isLive(upload, meeting) else { return nil }
            MeetingMirror(store: store).apply(dto, to: meeting)
            if let audio = dto.audio {
                if audio.sha256 != upload.sha256 { SyncLog.logger.info("server audio differs from local upload") }
                markVerified(upload: upload, meeting: meeting, audio: audio, job: nil)
                return .uploadChunks
            }
            return .busy(then: .createSession)

        case .restartAfterChecksum:
            upload.attempts += 1
            let file = SyncFiles.resolve(upload.filePath)
            let digest = try? await Task.detached { try Checksum.sha256Hex(fileURL: file) }.value
            guard isLive(upload, meeting) else { return nil }
            if let digest { upload.sha256 = digest }
            if let uploadID = upload.uploadID { SyncFiles.remove(SyncFiles.stagingDir(uploadID)) }
            upload.uploadID = nil
            upload.receivedBytes = 0
            store.save()
            return .createSession

        case .backoff(let then):
            await sleep(backoff.nextDelay())
            return then

        case .busy(let then):
            await sleep(Double.random(in: 1...5))
            return then

        case .reject(let code):
            finishFailed(upload: upload, meeting: meeting, state: .rejected, code: code)
            return nil

        case .fail(let code):
            finishFailed(upload: upload, meeting: meeting, state: .failed, code: code)
            return nil

        case .orphan:
            store.markOrphaned(meeting.id, uploads: self)
            return nil

        case .confirmMissing:
            await store.confirmMissing(meeting.id, api: env.api, uploads: self)
            return nil

        case .waitForConnection:
            setPhase(meeting, .waitingConnection)
            return nil

        case .stop:
            return nil
        }
    }

    // Maps a planner decision for an error to the next action; nil stops the run.
    private func resolve(_ action: UploadPlanner.Action, upload: PendingUpload, meeting: Meeting, backoff: inout Backoff) async -> UploadPlanner.Action? {
        switch action {
        case .backoff(let then):
            await sleep(backoff.nextDelay())
            return then
        case .busy(let then):
            await sleep(Double.random(in: 1...5))
            return then
        case .reject, .fail, .orphan, .confirmMissing, .waitForConnection, .stop:
            _ = try? await perform(action, upload: upload, meeting: meeting, backoff: &backoff)
            return nil
        default:
            return action
        }
    }

    private func uploadNextChunk(upload: PendingUpload, meeting: Meeting, backoff: inout Backoff) async throws -> UploadPlanner.Action? {
        guard let uploadID = upload.uploadID else { return .createSession }
        let maxBytes = chunkMax[uploadID] ?? env.app.serverConfig?.uploadChunkMaxBytes ?? 33_554_432
        guard let chunk = UploadPlanner.nextChunk(sizeBytes: upload.sizeBytes, receivedBytes: upload.receivedBytes, chunkMaxBytes: maxBytes) else {
            return .complete
        }
        let source = SyncFiles.resolve(upload.filePath)
        guard FileManager.default.fileExists(atPath: source.path) else {
            return .fail("file_missing")
        }
        if await transport.hasRunningTask(uploadID.lower) { return nil }
        guard isLive(upload, meeting) else { return nil }

        setPhase(meeting, .uploading)
        upload.state = .uploading
        store.save()
        let file = chunk.usesSourceFile ? source : try await Self.stage(chunk, from: source, uploadID: uploadID)
        guard isLive(upload, meeting) else { return nil }
        let endpoint = Endpoints.uploadChunk(uploadID, offset: chunk.offset)
        let request = try await env.api.authorizedRequest(endpoint)
        guard isLive(upload, meeting) else { return nil }
        liveProgress[meeting.id] = Double(chunk.offset) / Double(upload.sizeBytes)
        let data: Data, response: URLResponse
        do {
            (data, response) = try await transport.upload(request, fromFile: file, taskDescription: uploadID.lower)
        } catch {
            liveProgress[meeting.id] = nil
            throw APIError.from(error)
        }
        liveProgress[meeting.id] = nil
        guard isLive(upload, meeting) else { return nil }
        let result = try env.api.handle(endpoint, data: data, response: response)
        backoff.reset()
        return handle(result, upload: upload, meeting: meeting)
    }

    private func handle(_ result: UploadResultDTO, upload: PendingUpload, meeting: Meeting) -> UploadPlanner.Action {
        apply(result.upload, to: upload)
        if let audio = result.audio {
            // Nothing was reserved; park the intent until the user picks wait or transcribe-only (no extra POST).
            if result.job == nil, let blocked = result.transcriptionBlocked {
                store.intents(meeting.id).filter { $0.type == .transcribe }.forEach { $0.lastErrorCode = blocked.code }
            }
            markVerified(upload: upload, meeting: meeting, audio: audio, job: result.job)
            return .uploadChunks
        }
        let next = UploadPlanner.next(after: result.upload, checksumRestarts: upload.attempts)
        return next == .complete && result.upload.status == .completed ? .pollVerifying : next
    }

    nonisolated private static func stage(_ chunk: UploadPlanner.Chunk, from source: URL, uploadID: UUID) async throws -> URL {
        try await Task.detached {
            let dir = SyncFiles.stagingDir(uploadID)
            try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
            let target = dir.appending(path: "chunk")
            FileManager.default.createFile(atPath: target.path, contents: nil)
            let reader = try FileHandle(forReadingFrom: source)
            let writer = try FileHandle(forWritingTo: target)
            defer {
                try? reader.close()
                try? writer.close()
            }
            try writer.truncate(atOffset: 0)
            try reader.seek(toOffset: UInt64(chunk.offset))
            var remaining = chunk.length
            while remaining > 0, let data = try reader.read(upToCount: min(Checksum.chunkSize, remaining)), !data.isEmpty {
                try writer.write(contentsOf: data)
                remaining -= data.count
            }
            try writer.synchronize()
            return target
        }.value
    }

    // MARK: State

    private func apply(_ session: UploadSessionDTO, to upload: PendingUpload) {
        upload.uploadID = session.id
        upload.receivedBytes = session.receivedBytes
        chunkMax[session.id] = session.chunkMaxBytes
        if upload.state != .verified { upload.state = session.status == .verifying ? .completing : .uploading }
        store.save()
    }

    private func markVerified(upload: PendingUpload, meeting: Meeting, audio: AudioAssetDTO, job: JobDTO?) {
        upload.state = .verified
        upload.receivedBytes = upload.sizeBytes
        meeting.audioAvailable = audio.available
        meeting.durationMs = audio.durationMs
        meeting.localPhase = .none
        meeting.rejectCode = nil
        if let job {
            MeetingMirror(store: store).upsert(job)
            store.intents(meeting.id).filter { $0.type == .transcribe }.forEach(env.context.delete)
            if job.status.isActive, !meeting.activeJobTypes.contains(JobType.transcribe.rawValue) {
                meeting.activeJobTypes.append(JobType.transcribe.rawValue)
            }
        }
        deleteSegments(meeting.id, keeping: SyncFiles.resolve(upload.filePath))
        if let uploadID = upload.uploadID { SyncFiles.remove(SyncFiles.stagingDir(uploadID)) }
        liveProgress[meeting.id] = nil
        store.save()
    }

    private func deleteSegments(_ meetingID: UUID, keeping source: URL) {
        if let recording = store.localRecording(meetingID) {
            for segment in recording.segments { env.context.delete(segment) }
        }
        guard let dir = SyncFiles.existingRecordingsDir(meetingID),
              let names = try? FileManager.default.contentsOfDirectory(atPath: dir.path) else { return }
        for name in names where name.hasPrefix("seg-") && name.hasSuffix(".m4a") {
            let url = dir.appending(path: name)
            if url.standardizedFileURL != source.standardizedFileURL { SyncFiles.remove(url) }
        }
    }

    private func finishFailed(upload: PendingUpload, meeting: Meeting, state: PendingUploadState, code: String) {
        upload.state = state
        upload.rejectCode = code
        meeting.localPhase = .rejected
        meeting.rejectCode = code
        liveProgress[meeting.id] = nil
        store.save()
    }

    private func setPhase(_ meeting: Meeting, _ phase: LocalPhase) {
        guard meeting.localPhase != phase else { return }
        meeting.localPhase = phase
        store.save()
    }

    // Returns true when the run should loop again (after a backoff for an ambiguous failure).
    private func sendTranscribeIntent(_ meetingID: UUID, backoff: inout Backoff) async -> Bool {
        guard let intent = store.intents(meetingID).first(where: { $0.type == .transcribe }), intent.lastErrorCode == nil else {
            return false
        }
        switch await IntentSender.send(intent, env: env) {
        case .retryLater(let error?) where error.isAmbiguous || error == .uploadBusy:
            await sleep(backoff.nextDelay())
            return true
        case .missing:
            await store.confirmMissing(meetingID, api: env.api, uploads: self)
            return false
        default:
            return false
        }
    }

    // MARK: Background session events

    private func handle(_ event: BackgroundUploadSession.Event) {
        switch event {
        case .progress(let description, let sent):
            guard let upload = upload(forTask: description), upload.sizeBytes > 0 else { return }
            liveProgress[upload.meetingID] = Double(upload.receivedBytes + Int(sent)) / Double(upload.sizeBytes)
        case .detached(let description, let data, let response, let error):
            guard let upload = upload(forTask: description), let uploadID = upload.uploadID, let meeting = store.meeting(upload.meetingID) else { return }
            liveProgress[upload.meetingID] = nil
            if error == nil, let response,
               let result = try? env.api.handle(Endpoints.uploadChunk(uploadID, offset: upload.receivedBytes), data: data, response: response) {
                _ = handle(result, upload: upload, meeting: meeting)
            }
            start(upload.meetingID)
        }
    }

    private func upload(forTask description: String) -> PendingUpload? {
        guard let id = UUID(uuidString: description) else { return nil }
        return store.pendingUploads().first { $0.uploadID == id }
    }
}
