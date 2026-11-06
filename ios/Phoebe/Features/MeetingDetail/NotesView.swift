import SwiftUI

struct NotesTab: View {
    let data: DetailData
    let model: MeetingDetailModel
    let player: PlayerModel

    var body: some View {
        VStack(alignment: .leading, spacing: Spacing.lg) {
            ManualNotesCard(data: data, model: model, player: player)
            AINotesCard(data: data, model: model, player: player)
        }
    }
}

// MARK: - Manual notes

private struct ManualNotesCard: View {
    @Environment(AppModel.self) private var app
    let data: DetailData
    let model: MeetingDetailModel
    let player: PlayerModel
    @FocusState private var focused: String?
    @State private var showConflict = false

    private var edit: PendingDocumentEdit? { data.edits[.manualNotes] }
    private var docRevision: Int { data.documents[.manualNotes]?.currentRevisionNumber ?? 0 }

    var body: some View {
        VStack(alignment: .leading, spacing: Spacing.md) {
            HStack {
                Label { Text(L("detail.notes.manual", table: .meetingDetail)) } icon: { Image(systemName: "person.fill") }
                    .font(Typography.headline)
                    .foregroundStyle(Palette.textPrimary)
                    .accessibilityAddTraits(.isHeader)
                Spacer()
                SaveIndicator(state: NotesSaveState.make(edit: data.edit(.manualNotes), debouncing: model.manualDebouncing, online: data.isOnline))
            }
            if let conflict = edit?.conflictCurrentRevision {
                Banner(
                    tone: .warning,
                    systemImage: "arrow.triangle.branch",
                    title: L("conflict.title", table: .meetingDetail),
                    message: L("conflict.body", table: .meetingDetail, Int64(conflict), updatedText, Int64(edit?.baseRevision ?? 0)),
                    actions: [BannerAction(L("conflict.choose", table: .meetingDetail)) { showConflict = true }]
                )
            }
            if model.manualDraft.isEmpty {
                Button {
                    focused = model.addPoint()
                } label: {
                    Text(L("detail.notes.manual.empty", table: .meetingDetail))
                        .font(Typography.callout)
                        .foregroundStyle(Palette.textSecondary)
                        .frame(maxWidth: .infinity, minHeight: Spacing.minTouchTarget, alignment: .leading)
                        .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
            }
            ForEach(model.manualDraft) { item in
                HStack(alignment: .firstTextBaseline, spacing: Spacing.sm) {
                    if let at = item.atMs {
                        SourceReferenceButton(ref: .transcript(segmentId: item.id, startMs: at, endMs: at), isPlayable: data.playback.isPlayable) { ms in
                            Task { await player.play(from: ms, meetingID: data.meeting.id, availability: data.playback, app: app) }
                        }
                    } else {
                        Text(verbatim: "•").foregroundStyle(Palette.textSecondary).accessibilityHidden(true)
                    }
                    TextField(text: Binding(
                        get: { item.text },
                        set: { value in
                            if let next = model.editPoint(item.id, text: value, app: app) { focused = next }
                        }
                    ), axis: .vertical) {
                        Text(L("detail.notes.manual.add", table: .meetingDetail))
                    }
                    .font(Typography.body)
                    .focused($focused, equals: item.id)
                }
                .frame(minHeight: Spacing.minTouchTarget)
                .contextMenu {
                    Button(role: .destructive) { model.deletePoint(item.id, app: app) } label: {
                        Label { Text(L("notes.point.delete", table: .meetingDetail)) } icon: { Image(systemName: "trash") }
                    }
                }
                .accessibilityAction(named: Text(L("notes.point.delete", table: .meetingDetail))) { model.deletePoint(item.id, app: app) }
            }
            Button {
                focused = model.addPoint()
            } label: {
                Label { Text(L("detail.notes.manual.add", table: .meetingDetail)) } icon: { Image(systemName: "plus") }
                    .font(Typography.callout)
                    .frame(minHeight: Spacing.minTouchTarget)
            }
            .buttonStyle(.borderless)
            .tint(Palette.accent)
        }
        .padding(Spacing.lg)
        .background(Palette.surface, in: RoundedRectangle(cornerRadius: Radius.lg, style: .continuous))
        .toolbar {
            ToolbarItemGroup(placement: .keyboard) {
                Spacer()
                if let id = focused {
                    Button {
                        focused = nil
                        model.deletePoint(id, app: app)
                    } label: {
                        Label { Text(L("notes.point.delete", table: .meetingDetail)) } icon: { Image(systemName: "trash") }
                    }
                }
            }
        }
        .onAppear { reload() }
        .onChange(of: docRevision) { _, _ in reload() }
        .onChange(of: data.currentRevision(.manualNotes)?.id) { _, _ in reload() }
        .onChange(of: focused) { old, new in
            if old != nil, new == nil { model.saveManualNow(app: app) }
        }
        .sheet(isPresented: $showConflict) {
            ConflictSheet(meetingID: data.meeting.id, kind: .manualNotes) { resolution in
                await app.sync.resolveDocumentConflict(meetingID: data.meeting.id, kind: .manualNotes, resolution)
                reload(force: true)
            }
        }
    }

    private var updatedText: String {
        (data.documents[.manualNotes]?.updatedAt ?? .now).formatted(date: .abbreviated, time: .shortened)
    }

    private func reload(force: Bool = false) {
        guard focused == nil || force else { return }
        model.loadManual(edit: edit, revision: data.currentRevision(.manualNotes), documentRevision: docRevision, force: force)
    }
}

// MARK: - AI notes

private struct AINotesCard: View {
    @Environment(AppModel.self) private var app
    let data: DetailData
    let model: MeetingDetailModel
    let player: PlayerModel

    private var document: Document? { data.documents[.aiNotes] }
    private var notesState: StepState { DetailStatus.notes(data.stage) }
    private var rateLimitedUntil: Date? { GenerationGate.rateLimitedUntil(usage: app.usage, retryAfter: model.retryAfterUntil) }

    private var block: GenerationBlock? {
        GenerationGate.notes(
            online: data.isOnline,
            noSpeech: data.isNoSpeech,
            hasTranscript: data.stage.hasTranscript,
            jobActive: notesState == .active(percent: nil),
            manualEdit: data.edit(.manualNotes),
            rateLimitedUntil: rateLimitedUntil
        )
    }

    var body: some View {
        VStack(alignment: .leading, spacing: Spacing.md) {
            header
            if notesState == .active(percent: nil) {
                Label { Text(L("detail.notes.generating", table: .meetingDetail)) } icon: { ProgressView() }
                    .font(Typography.callout)
                    .foregroundStyle(Palette.textSecondary)
            }
            if notesState == .failed {
                failure
            }
            if let document, document.isStale, !model.dismissedStale.contains(document.currentRevisionID) {
                StaleBanner(reasons: document.staleReasons, actions: [
                    BannerAction(L("doc.regen", table: .meetingDetail), isLoading: model.generating.contains(.generateNotes)) { regenerate() },
                ]) {
                    model.dismissedStale.insert(document.currentRevisionID)
                }
            }
            if case .aiNotes(let content)? = data.content(.aiNotes) {
                let revision = data.currentRevision(.aiNotes)
                AINotesReadView(content: content, refs: RefContext(verifiedSegmentIDs: data.verifiedSegmentIDs(for: revision), isPlayable: data.playback.isPlayable) { ms in
                    Task { await player.play(from: ms, meetingID: data.meeting.id, availability: data.playback, app: app) }
                })
            } else if document != nil {
                ProgressView().frame(maxWidth: .infinity)
            }
            generateControls
        }
        .padding(Spacing.lg)
        .background(Palette.surface, in: RoundedRectangle(cornerRadius: Radius.lg, style: .continuous))
    }

    private var header: some View {
        HStack(spacing: Spacing.sm) {
            Label { Text(L("detail.notes.ai", table: .meetingDetail)) } icon: { Image(systemName: "sparkles") }
                .font(Typography.headline)
                .foregroundStyle(Palette.aiOrigin)
                .accessibilityAddTraits(.isHeader)
            if document?.isStale == true { StatusChip(.outdated) }
            Spacer()
            if document != nil {
                Button { model.showVersions = .aiNotes } label: {
                    Image(systemName: "clock.arrow.circlepath").minTouchTarget()
                }
                .accessibilityLabel(Text(L("doc.versions", table: .meetingDetail)))
                Button(L("detail.notes.edit", table: .meetingDetail)) {
                    app.path.append(.editor(meetingID: data.meeting.id, kind: .aiNotes))
                }
                .buttonStyle(.bordered)
                .tint(Palette.accent)
            }
        }
    }

    private var failure: some View {
        let copy = FailureCopy.make(category: data.job(.generateNotes)?.errorCategory)
        return Banner(
            tone: .danger,
            systemImage: "exclamationmark.triangle",
            title: L("detail.notes.failed", table: .meetingDetail),
            message: copy.showsRequestCode ? L(copy.key, table: .meetingDetail, data.jobs[.generateNotes]?.id.lower ?? "-") : L(copy.key, table: .meetingDetail),
            actions: copy.retryAllowed && block == nil ? [BannerAction(L("common.retry"), isLoading: model.generating.contains(.generateNotes)) { regenerate() }] : []
        )
    }

    @ViewBuilder private var generateControls: some View {
        if notesState != .active(percent: nil) && notesState != .failed {
            VStack(alignment: .leading, spacing: Spacing.xs) {
                SecondaryButton(
                    L(document == nil ? "notes.generate" : "doc.regen", table: .meetingDetail),
                    systemImage: "sparkles",
                    isLoading: model.generating.contains(.generateNotes)
                ) { regenerate() }
                .disabled(block != nil)
                if let block {
                    if block != .inProgress { ActionErrorText(key: block.key, date: rateLimitedUntil, isError: false) }
                } else {
                    Text(L("doc.regen.footnote", table: .meetingDetail))
                        .font(Typography.footnote)
                        .foregroundStyle(Palette.textSecondary)
                }
                if let key = model.actionErrors[.generateNotes] { ActionErrorText(key: key, date: rateLimitedUntil) }
            }
        } else if let key = model.actionErrors[.generateNotes] {
            ActionErrorText(key: key, date: rateLimitedUntil)
        }
    }

    private func regenerate() {
        Task {
            await model.generate(.generateNotes, body: model.notesBody(), flushKinds: [.manualNotes], pendingKinds: data.pendingKinds, app: app)
        }
    }
}

struct StaleBanner: View {
    let reasons: [String]
    let actions: [BannerAction]
    let onClose: () -> Void

    var body: some View {
        Banner(
            tone: .warning,
            systemImage: "clock.arrow.circlepath",
            title: L("doc.outdated.chip", table: .meetingDetail),
            message: message,
            actions: actions,
            onClose: onClose
        )
    }

    private var message: LocalizedStringResource {
        let reasonText = reasons.compactMap { reason -> String? in
            switch reason {
            case "transcript": LS("doc.outdated.reason.transcript", table: .meetingDetail)
            case "manual_notes": LS("doc.outdated.reason.manualNotes", table: .meetingDetail)
            case "ai_notes": LS("doc.outdated.reason.aiNotes", table: .meetingDetail)
            default: nil
            }
        }
        let text = ([LS("doc.outdated.body", table: .meetingDetail)] + reasonText).joined(separator: " ")
        return LocalizedStringResource(String.LocalizationValue(text), table: "Verbatim")
    }
}
