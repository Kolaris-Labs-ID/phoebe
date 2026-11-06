import SwiftData
import SwiftUI

@MainActor
struct DetailData {
    let meeting: Meeting
    let documents: [DocumentKind: Document]
    let transcript: TranscriptRevision?
    let originalTranscript: TranscriptRevision?
    let revisions: [DocumentRevision]
    let jobs: [JobType: ProcessingJob]
    let pendingUpload: PendingUpload?
    let edits: [DocumentKind: PendingDocumentEdit]
    let isOnline: Bool
    let uploadProgress: Double?

    func currentRevision(_ kind: DocumentKind) -> DocumentRevision? {
        guard let doc = documents[kind] else { return nil }
        return revisions.first { $0.kindRaw == kind.rawValue && $0.revisionNumber == doc.currentRevisionNumber && $0.content != nil }
    }

    func content(_ kind: DocumentKind) -> DocumentContentValue? {
        try? currentRevision(kind)?.decodedContent()
    }

    func job(_ type: JobType) -> JobSnapshot? {
        jobs[type].map { JobSnapshot(status: $0.status, errorCategory: $0.errorCategory, updatedAt: $0.updatedAt) }
    }

    func edit(_ kind: DocumentKind) -> EditSnapshot? {
        edits[kind].map { EditSnapshot(hasPendingWork: $0.hasPendingWork, inFlight: $0.inFlightMutationID != nil, conflict: $0.conflictCurrentRevision != nil, failed: $0.failedCode != nil) }
    }

    var pendingKinds: Set<DocumentKind> { Set(edits.filter { $0.value.hasPendingWork }.keys) }

    var stage: StageInputs {
        StageInputs(
            localPhase: meeting.localPhase,
            pipeline: meeting.pipeline,
            uploadState: pendingUpload?.state,
            uploadProgress: uploadProgress,
            serverHasAudio: meeting.audioAvailable || meeting.audioDeletedReason != nil,
            hasTranscript: meeting.hasTranscript || transcript != nil,
            hasAINotes: documents[.aiNotes] != nil,
            transcribeJob: job(.transcribe),
            notesJob: job(.generateNotes)
        )
    }

    var isNoSpeech: Bool { DetailStatus.isNoSpeech(stage) || meeting.transcriptionErrorCategory == "no_speech" && !stage.hasTranscript }

    var playback: PlaybackAvailability {
        PlaybackAvailability.make(
            audioDeletedReason: meeting.audioDeletedReason,
            audioAvailable: meeting.audioAvailable,
            localFile: PlayerModel.localFile(meetingID: meeting.id, uploadPath: pendingUpload?.filePath),
            isOnline: isOnline
        )
    }

    func transcriptSegments(original: Bool) -> [TranscriptSegment] {
        ((original ? originalTranscript : transcript).flatMap { try? $0.segments() }) ?? []
    }

    func verifiedSegmentIDs(for revision: DocumentRevision?) -> Set<String>? {
        guard let sourceID = revision?.sourceTranscriptRevisionID else { return nil }
        let match = [transcript, originalTranscript].compactMap { $0 }.first { $0.id == sourceID }
        return match.flatMap { try? Set($0.segments().map(\.id)) }
    }
}

struct MeetingDetailView: View {
    let meetingID: UUID

    @Environment(AppModel.self) private var app
    @Query private var meetings: [Meeting]
    @Query private var documents: [Document]
    @Query private var transcripts: [TranscriptRevision]
    @Query private var revisions: [DocumentRevision]
    @Query private var jobs: [ProcessingJob]
    @Query private var uploads: [PendingUpload]
    @Query private var edits: [PendingDocumentEdit]
    @SceneStorage private var tabRaw: String
    @State private var model: MeetingDetailModel
    @State private var player = PlayerModel()
    @AccessibilityFocusState private var titleFocused: Bool

    init(meetingID: UUID) {
        self.meetingID = meetingID
        _meetings = Query(filter: #Predicate<Meeting> { $0.id == meetingID })
        _documents = Query(filter: #Predicate<Document> { $0.meetingID == meetingID })
        _transcripts = Query(filter: #Predicate<TranscriptRevision> { $0.meetingID == meetingID }, sort: \TranscriptRevision.revisionNumber, order: .reverse)
        _revisions = Query(filter: #Predicate<DocumentRevision> { $0.meetingID == meetingID }, sort: \DocumentRevision.revisionNumber, order: .reverse)
        _jobs = Query(filter: #Predicate<ProcessingJob> { $0.meetingID == meetingID }, sort: \ProcessingJob.updatedAt, order: .reverse)
        _uploads = Query(filter: #Predicate<PendingUpload> { $0.meetingID == meetingID })
        _edits = Query(filter: #Predicate<PendingDocumentEdit> { $0.meetingID == meetingID })
        _tabRaw = SceneStorage(wrappedValue: DetailTab.transcript.rawValue, "detail.tab.\(meetingID.lower)")
        _model = State(initialValue: MeetingDetailModel(meetingID: meetingID))
    }

    private var tab: Binding<DetailTab> {
        Binding(get: { DetailTab(rawValue: tabRaw) ?? .transcript }, set: { tabRaw = $0.rawValue })
    }

    private var data: DetailData? {
        guard let meeting = meetings.first else { return nil }
        var docs: [DocumentKind: Document] = [:]
        for d in documents { docs[d.kind] = d }
        var latestJobs: [JobType: ProcessingJob] = [:]
        for j in jobs where latestJobs[j.type] == nil { latestJobs[j.type] = j }
        var editMap: [DocumentKind: PendingDocumentEdit] = [:]
        for e in edits { editMap[e.kind] = e }
        return DetailData(
            meeting: meeting,
            documents: docs,
            transcript: transcripts.first,
            originalTranscript: transcripts.first { $0.revisionNumber == 1 },
            revisions: revisions,
            jobs: latestJobs,
            pendingUpload: uploads.first,
            edits: editMap,
            isOnline: app.isOnline,
            uploadProgress: app.uploads.progress(meetingID: meetingID)
        )
    }

    private var contentKey: String {
        let docs = documents.map { "\($0.kindRaw)\($0.currentRevisionNumber)" }.sorted().joined()
        return "\(meetings.first?.hasTranscript == true)\(transcripts.count)\(docs)\(revisions.count)\(app.isOnline)"
    }

    var body: some View {
        Group {
            if let data {
                content(data)
            } else {
                LoadingSkeleton()
            }
        }
        .background(Palette.bg.ignoresSafeArea())
        .navigationBarTitleDisplayMode(.inline)
        .navigationTitle(Text(L("detail.nav.title", table: .meetingDetail)))
        .toolbar {
            // The title stays as the back label of pushed screens; the header already shows the meeting title.
            ToolbarItem(placement: .principal) { Color.clear.frame(width: 1, height: 1).accessibilityHidden(true) }
            if let data {
                ToolbarItem(placement: .topBarTrailing) { overflowMenu(data) }
            }
        }
        .overlay {
            if model.deleting {
                ZStack {
                    Palette.scrim.ignoresSafeArea()
                    ProgressView { Text(L("detail.deleting", table: .meetingDetail)) }
                        .padding(Spacing.xl)
                        .background(Palette.surface, in: RoundedRectangle(cornerRadius: Radius.lg, style: .continuous))
                }
            }
        }
        .task {
            app.poller.start(.meeting(meetingID))
            await app.sync.refreshMeeting(meetingID)
        }
        .task(id: contentKey) {
            await model.ensureContent(app: app, meeting: meetings.first, transcripts: transcripts, documents: documents, revisions: revisions)
        }
        .onDisappear {
            app.poller.stop(.meeting(meetingID))
            player.pause()
            let pending = model.manualHasUnsaved || edits.contains { $0.kind == .manualNotes && $0.hasPendingWork && $0.conflictCurrentRevision == nil }
            guard !model.deleting else { return }
            model.saveManualNow(app: app)
            if pending, !model.leaveToastShown {
                model.leaveToastShown = true
                app.toasts.show(L("editor.leave.saved", table: .meetingDetail))
            }
        }
        .onChange(of: meetings.first?.hasTranscript ?? false) { old, new in
            guard !old, new else { return }
            AccessibilityNotification.Announcement(LS("detail.transcript.ready", table: .meetingDetail)).post()
        }
    }

    @ViewBuilder private func content(_ data: DetailData) -> some View {
        ScrollView {
            LazyVStack(alignment: .leading, spacing: Spacing.lg, pinnedViews: [.sectionHeaders]) {
                DetailHeader(meeting: data.meeting, model: model, titleFocused: $titleFocused)
                PipelineStrip(stage: data.stage)
                stageBanners(data)
                PlayerCard(player: player, availability: data.playback, meetingID: meetingID, durationMs: data.meeting.durationMs)
                Section {
                    Group {
                        switch tab.wrappedValue {
                        case .transcript: TranscriptTab(data: data, model: model, player: player)
                        case .notes: NotesTab(data: data, model: model, player: player)
                        case .mom: MoMTab(data: data, model: model)
                        }
                    }
                    .transition(.opacity)
                } header: {
                    Picker(selection: tab) {
                        Text(L("detail.tab.transcript", table: .meetingDetail)).tag(DetailTab.transcript)
                        Text(L("detail.tab.notes", table: .meetingDetail)).tag(DetailTab.notes)
                        Text(L("detail.tab.mom", table: .meetingDetail)).tag(DetailTab.mom)
                    } label: {
                        Text(L("detail.tabs.a11y", table: .meetingDetail))
                    }
                    .pickerStyle(.segmented)
                    .padding(.vertical, Spacing.sm)
                    .background(Palette.bg)
                }
            }
            .padding(.horizontal, Spacing.screenInsetCompact)
            .padding(.bottom, Spacing.xxl)
            .readingWidth()
        }
        .onAppear { titleFocused = true }
        .sheet(isPresented: $model.showMetadata) { MetadataSheet(data: data) }
        .sheet(isPresented: Binding(get: { data.meeting.metadataConflict }, set: { _ in })) {
            MetadataConflictSheet(meeting: data.meeting)
        }
        .sheet(isPresented: $model.showFolder) { FolderPickerSheet(meeting: data.meeting) }
        .sheet(isPresented: $model.showTags) { TagPickerSheet(meeting: data.meeting) }
        .sheet(isPresented: $model.showExport) { ExportSheet(meetingID: meetingID, kind: exportKind(data)) { tabRaw = DetailTab.mom.rawValue } }
        .sheet(item: Binding(get: { model.showVersions.map(VersionsTarget.init) }, set: { model.showVersions = $0?.kind })) { target in
            VersionsSheet(kind: target.kind, data: data, model: model)
        }
        .sheet(item: $model.correctionTarget) { segment in
            TranscriptCorrectionSheet(segment: segment, expectedRevision: data.transcript?.revisionNumber ?? 1, model: model)
        }
        .confirmationDialog(Text(L("meeting.delete.title", table: .meetingDetail)), isPresented: $model.confirmDeleteMeeting, titleVisibility: .visible) {
            Button(L("meeting.delete.confirm", table: .meetingDetail), role: .destructive) {
                Task { await model.deleteMeeting(player: player, app: app) }
            }
        } message: {
            Text(L("meeting.delete.body", table: .meetingDetail))
        }
        .confirmationDialog(Text(L("audio.delete.title", table: .meetingDetail)), isPresented: $model.confirmDeleteAudio, titleVisibility: .visible) {
            Button(L("audio.delete", table: .meetingDetail), role: .destructive) { model.deleteAudio(player: player, app: app) }
        } message: {
            Text(L("audio.delete.body", table: .meetingDetail))
        }
        .alert(Text(L("audio.delete", table: .meetingDetail)), isPresented: Binding(get: { model.audioAlertKey != nil }, set: { if !$0 { model.audioAlertKey = nil } })) {
            Button(L("common.close")) { model.audioAlertKey = nil }
        } message: {
            Text(L(model.audioAlertKey ?? "audio.delete.busy", table: .meetingDetail))
        }
    }

    private func exportKind(_ data: DetailData) -> DocumentKind {
        if data.documents[.mom] != nil { return .mom }
        if data.documents[.aiNotes] != nil { return .aiNotes }
        return .manualNotes
    }

    private func overflowMenu(_ data: DetailData) -> some View {
        Menu {
            Section {
                Button { model.showMetadata = true } label: { Label { Text(L("detail.menu.edit", table: .meetingDetail)) } icon: { Image(systemName: "pencil") } }
                Button { model.showFolder = true } label: { Label { Text(L("meeting.move", table: .meetingDetail)) } icon: { Image(systemName: "folder") } }
                Button { model.showTags = true } label: { Label { Text(L("meeting.tags", table: .meetingDetail)) } icon: { Image(systemName: "tag") } }
                Button { model.showExport = true } label: { Label { Text(L("detail.menu.export", table: .meetingDetail)) } icon: { Image(systemName: "square.and.arrow.up") } }
                    .disabled(data.documents.isEmpty)
            }
            Section {
                if data.meeting.audioDeletedReason == nil {
                    Button(role: .destructive) {
                        let unverified = (data.pendingUpload.map { $0.state != .verified } ?? false) || (!data.meeting.audioAvailable && !data.stage.hasTranscript)
                        model.requestDeleteAudio(transcribeActive: data.job(.transcribe)?.status.isActive == true, uploadUnverified: unverified)
                    } label: {
                        Label { Text(L("audio.delete", table: .meetingDetail)) } icon: { Image(systemName: "speaker.slash") }
                    }
                }
                Button(role: .destructive) { model.confirmDeleteMeeting = true } label: {
                    Label { Text(L("meeting.delete", table: .meetingDetail)) } icon: { Image(systemName: "trash") }
                }
            }
        } label: {
            Image(systemName: "ellipsis.circle").minTouchTarget()
        }
        .accessibilityLabel(Text(L("detail.menu.a11y", table: .meetingDetail)))
    }

    // MARK: Stage banners (upload, quota, plan, offline)

    @ViewBuilder private func stageBanners(_ data: DetailData) -> some View {
        let meeting = data.meeting
        if !data.isOnline {
            Banner(tone: .warning, systemImage: "wifi.slash", message: L("detail.offline", table: .meetingDetail))
        }
        if meeting.localPhase == .rejected || data.pendingUpload?.state == .rejected {
            Banner(
                tone: .danger,
                systemImage: "exclamationmark.icloud",
                title: L("status.failed.upload"),
                message: rejectMessage(data),
                actions: [BannerAction(L("meeting.delete", table: .meetingDetail)) { model.confirmDeleteMeeting = true }]
            )
        } else if data.pendingUpload?.state == .failed {
            Banner(
                tone: .danger,
                systemImage: "exclamationmark.icloud",
                message: L("detail.upload.failed", table: .meetingDetail),
                actions: [BannerAction(L("common.retry"), isLoading: model.uploadRetrying) { Task { await model.retryUpload(app: app) } }]
            )
        } else if meeting.localPhase == .waitingConnection || (meeting.localPhase == .queued && !data.isOnline) {
            Banner(tone: .warning, systemImage: "wifi.slash", message: L("detail.upload.waiting", table: .meetingDetail))
        }
        if meeting.localPhase == .waitingQuota {
            Banner(
                tone: .warning,
                systemImage: "gauge.with.dots.needle.100percent",
                message: L("detail.quota.waiting", table: .meetingDetail),
                actions: quotaActions
            )
        } else if meeting.localPhase == .waitingEntitlement {
            Banner(
                tone: .warning,
                systemImage: "calendar.badge.exclamationmark",
                message: L("detail.entitlement.expired", table: .meetingDetail),
                actions: quotaActions
            )
        }
    }

    private var quotaActions: [BannerAction] {
        [
            BannerAction(L("detail.quota.cta", table: .meetingDetail)) { app.path.append(.settings) },
            BannerAction(L("common.retry"), isLoading: model.uploadRetrying) {
                Task {
                    await app.refreshUsage()
                    await model.retryUpload(app: app)
                }
            },
        ]
    }

    private func rejectMessage(_ data: DetailData) -> LocalizedStringResource {
        let code = data.pendingUpload?.rejectCode ?? data.meeting.rejectCode
        let key = UploadRejectCopy.key(for: code)
        switch key {
        case "import.error.tooLong":
            return L(key, table: .meetingDetail, Formatting.timer(ms: data.pendingUpload?.durationMs ?? data.meeting.durationMs ?? 0), Int64(app.maxAudioDurationMs / 60_000))
        case "import.error.tooBig":
            let size = ByteCountFormatter.string(fromByteCount: Int64(data.pendingUpload?.sizeBytes ?? 0), countStyle: .file)
            return L(key, table: .meetingDetail, size, Int64((app.serverConfig?.maxUploadBytes ?? 250_000_000) / 1_000_000))
        default:
            return L(key, table: .meetingDetail)
        }
    }
}

private struct VersionsTarget: Identifiable {
    let kind: DocumentKind
    var id: String { kind.rawValue }
}

// MARK: - Header

private struct DetailHeader: View {
    @Environment(AppModel.self) private var app
    let meeting: Meeting
    let model: MeetingDetailModel
    var titleFocused: AccessibilityFocusState<Bool>.Binding

    var body: some View {
        VStack(alignment: .leading, spacing: Spacing.sm) {
            Button { model.showMetadata = true } label: {
                Text(verbatim: meeting.title.nonBlank ?? LS("detail.untitled", table: .meetingDetail))
                    .font(Typography.title)
                    .foregroundStyle(Palette.textPrimary)
                    .multilineTextAlignment(.leading)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            .buttonStyle(.plain)
            .accessibilityAddTraits(.isHeader)
            .accessibilityHint(Text(L("detail.menu.edit", table: .meetingDetail)))
            .accessibilityFocused(titleFocused)

            Text(verbatim: subtitle)
                .font(Typography.subheadline.monospacedDigit())
                .foregroundStyle(Palette.textSecondary)

            HStack(alignment: .firstTextBaseline, spacing: Spacing.sm) {
                Image(systemName: "person.2").foregroundStyle(Palette.textSecondary).accessibilityHidden(true)
                if meeting.participants.isEmpty {
                    Text(L("detail.participants.none", table: .meetingDetail)).foregroundStyle(Palette.textSecondary)
                } else {
                    Text(verbatim: meeting.participants.joined(separator: ", ")).foregroundStyle(Palette.textPrimary)
                }
                Button { model.showMetadata = true } label: {
                    Text(L("detail.participants.add", table: .meetingDetail)).minTouchTarget()
                }
                .buttonStyle(.borderless)
                .tint(Palette.accent)
            }
            .font(Typography.subheadline)
            .accessibilityElement(children: .contain)

            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: Spacing.sm) {
                    Button { model.showFolder = true } label: {
                        Label { Text(verbatim: meeting.folder?.name ?? LS("library.filter.noFolder", table: .meetingDetail)) } icon: { Image(systemName: "folder") }
                            .modifier(ChipStyle())
                    }
                    ForEach(meeting.tags.sorted { $0.name < $1.name }, id: \.id) { tag in
                        Button { model.showTags = true } label: { Text(verbatim: "#\(tag.name)").modifier(ChipStyle()) }
                    }
                    Button { model.showTags = true } label: {
                        Label { Text(L("tag.new", table: .meetingDetail)) } icon: { Image(systemName: "plus") }
                            .modifier(ChipStyle())
                    }
                }
                .buttonStyle(.plain)
            }
        }
        .padding(.top, Spacing.sm)
    }

    private var subtitle: String {
        let zone = TimeZone(identifier: meeting.timezone) ?? .current
        var parts: [String] = []
        if let date = meeting.startedAt {
            var style = Date.FormatStyle(date: .abbreviated, time: .shortened).weekday(.abbreviated)
            style.timeZone = zone
            parts.append(date.formatted(style) + (zone.abbreviation(for: date).map { " " + $0 } ?? ""))
        } else {
            parts.append(LS("detail.date.none", table: .meetingDetail))
        }
        if let ms = meeting.durationMs { parts.append(Formatting.timer(ms: ms)) }
        return parts.joined(separator: " · ")
    }
}

struct ChipStyle: ViewModifier {
    func body(content: Content) -> some View {
        content
            .font(Typography.caption)
            .foregroundStyle(Palette.textPrimary)
            .padding(.vertical, Spacing.xs)
            .padding(.horizontal, Spacing.sm)
            .background(Palette.surface2, in: RoundedRectangle(cornerRadius: Radius.sm, style: .continuous))
            .frame(minHeight: Spacing.minTouchTarget)
            .contentShape(Rectangle())
    }
}

private struct LoadingSkeleton: View {
    var body: some View {
        VStack(alignment: .leading, spacing: Spacing.md) {
            Text(verbatim: "Weekly sync produk").font(Typography.title)
            Text(verbatim: "Kam, 6 Nov 2026 · 09.30 · 42:10").font(Typography.subheadline)
            RoundedRectangle(cornerRadius: Radius.md).fill(Palette.surface2).frame(height: 56)
            ForEach(0..<6, id: \.self) { _ in
                Text(verbatim: "00:00  Lorem ipsum dolor sit amet consectetur adipiscing.").font(Typography.body)
            }
            Spacer()
        }
        .padding(Spacing.screenInsetCompact)
        .redacted(reason: .placeholder)
        .accessibilityLabel(Text(L("detail.loading", table: .meetingDetail)))
    }
}
