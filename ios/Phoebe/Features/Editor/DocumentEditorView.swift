import SwiftData
import SwiftUI

struct DocumentEditorView: View {
    let meetingID: UUID
    let kind: DocumentKind
    @Environment(AppModel.self) private var app
    @State private var model: DocumentEditorModel?

    var body: some View {
        Group {
            if let model {
                DocumentEditorScreen(model: model)
            } else {
                Color.clear
            }
        }
        .background(Palette.bg)
        .task {
            guard model == nil else { return }
            let m = DocumentEditorModel(meetingID: meetingID, kind: kind, app: app)
            model = m
            await m.load()
        }
    }
}

private struct StoreState: Equatable {
    var pending: [DocumentKind: PendingEditSnapshot]
    var doc: EditorDocState?
}

struct DocumentEditorScreen: View {
    @Bindable var model: DocumentEditorModel
    @Environment(AppModel.self) private var app
    @Environment(\.dynamicTypeSize) private var typeSize
    @Environment(\.scenePhase) private var scenePhase
    @Query private var pendingRows: [PendingDocumentEdit]
    @Query private var documents: [Document]
    @Query private var meetings: [Meeting]
    @Query private var jobs: [ProcessingJob]
    @FocusState private var focus: EditTarget?

    init(model: DocumentEditorModel) {
        self.model = model
        let id = model.meetingID
        _pendingRows = Query(filter: #Predicate<PendingDocumentEdit> { $0.meetingID == id })
        _documents = Query(filter: #Predicate<Document> { $0.meetingID == id })
        _meetings = Query(filter: #Predicate<Meeting> { $0.id == id })
        _jobs = Query(filter: #Predicate<ProcessingJob> { $0.meetingID == id }, sort: \.updatedAt, order: .reverse)
    }

    private var kind: DocumentKind { model.kind }
    private var meeting: Meeting? { meetings.first }
    private var jobType: JobType { kind == .mom ? .generateMom : .generateNotes }
    private var isGenerating: Bool { meeting?.activeJobs.contains(jobType) == true }
    private var isPlayable: Bool {
        (meeting?.audioAvailable == true || model.hasLocalAudio) && !app.recorder.phase.hasLiveSession
    }

    private var storeState: StoreState {
        var pending: [DocumentKind: PendingEditSnapshot] = [:]
        for row in pendingRows { pending[row.kind] = PendingEditSnapshot(row) }
        return StoreState(pending: pending, doc: documents.first { $0.kind == kind }.map(EditorDocState.init))
    }

    private var regenFailed: Bool {
        guard !isGenerating, !model.failedBannerDismissed,
              let latest = jobs.first(where: { $0.type == jobType }), latest.status == .failed else { return false }
        return latest.updatedAt > (model.loadedMeta?.createdAt ?? .distantPast)
    }

    var body: some View {
        content
            .navigationTitle(Text(L(kind == .mom ? "editor.title.mom" : "editor.title.aiNotes", table: .editor)))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { toolbar }
            .safeAreaInset(edge: .bottom) { if model.phase == .ready { bottomBar } }
            .onChange(of: storeState, initial: true) { _, state in
                var others = state.pending
                let mine = others.removeValue(forKey: kind)
                model.observe(pending: mine, others: others, doc: state.doc)
            }
            .onChange(of: app.isOnline) { _, online in model.connectivityChanged(online: online) }
            .onChange(of: scenePhase) { _, phase in if phase != .active { model.persistNow() } }
            .onDisappear { model.leave() }
            .sensoryFeedback(.success, trigger: model.reviewedCount)
            .sheet(isPresented: $model.showConflict) {
                ConflictSheet(meetingID: model.meetingID, kind: kind) { await model.resolveConflict($0) }
            }
            .sheet(isPresented: $model.showVersions) { EditorVersionsSheet(model: model) }
            .sheet(isPresented: $model.showExport) { ExportSheet(meetingID: model.meetingID, kind: kind) }
            .sheet(item: $model.playing) { SourcePlayerSheet(request: $0) }
            .confirmationDialog(
                Text(L(kind == .mom ? "mom.regen.confirm.title" : "notes.regen.confirm.title", table: .editor)),
                isPresented: $model.showRegenConfirm, titleVisibility: .visible
            ) {
                Button { Task { await model.regenerate() } } label: { Text(L("doc.regen", table: .editor)) }
                Button(role: .cancel) {} label: { Text(L("common.cancel")) }
            } message: {
                Text(L("mom.regen.confirm.body", table: .editor))
            }
            .confirmationDialog(
                Text(L("section.delete.confirm", table: .editor, model.sectionPendingDelete?.heading ?? "")),
                isPresented: Binding(get: { model.sectionPendingDelete != nil }, set: { if !$0 { model.sectionPendingDelete = nil } }),
                titleVisibility: .visible,
                presenting: model.sectionPendingDelete
            ) { section in
                Button(role: .destructive) { model.deleteSection(section.id) } label: { Text(L("mom.section.delete", table: .editor)) }
                Button(role: .cancel) {} label: { Text(L("common.cancel")) }
            }
    }

    @ViewBuilder private var content: some View {
        switch model.phase {
        case .loading:
            skeleton
        case .failed:
            ContentUnavailableView {
                Label { Text(L("editor.loadFailed.title", table: .editor)) } icon: { Image(systemName: "doc.text") }
            } description: {
                Text(L("editor.loadFailed", table: .editor))
            } actions: {
                SecondaryButton(L("common.retry")) { Task { await model.load() } }
            }
        case .ready:
            ScrollView {
                LazyVStack(alignment: .leading, spacing: Spacing.section) {
                    header
                    banners
                    if let viewing = model.viewing {
                        ReadOnlyDocumentView(document: viewing.document)
                    } else if let document = model.document {
                        editable(document)
                    }
                }
                .padding(.horizontal, Spacing.screenInsetCompact)
                .padding(.vertical, Spacing.lg)
                .readingWidth()
            }
            .scrollDismissesKeyboard(.interactively)
            .background(Palette.bg)
        }
    }

    private var skeleton: some View {
        VStack(alignment: .leading, spacing: Spacing.lg) {
            ForEach(0..<6, id: \.self) { _ in
                VStack(alignment: .leading, spacing: Spacing.sm) {
                    Text(EditorText.plain("Placeholder heading")).font(Typography.title)
                    Text(EditorText.plain("Placeholder line of document text that wraps")).font(Typography.body)
                }
            }
            Spacer()
        }
        .padding(Spacing.screenInsetCompact)
        .redacted(reason: .placeholder)
        .accessibilityHidden(true)
    }

    // MARK: Header

    private var header: some View {
        let reviewed = model.reviewStatus == .reviewed
        return EditorFlowLayout(spacing: Spacing.sm) {
            StatusChip(L(reviewed ? "review.reviewed" : "review.draft", table: .editor),
                       systemImage: reviewed ? "checkmark.seal.fill" : "pencil.circle",
                       tone: reviewed ? .success : .neutral)
                .accessibilityLabel(Text(L("review.status.a11y", table: .editor, LS(reviewed ? "review.reviewed" : "review.draft", table: .editor))))
            if let line = versionLine(model.viewing?.meta ?? model.loadedMeta) {
                Text(EditorText.plain(line))
                    .font(Typography.footnote)
                    .foregroundStyle(Palette.textSecondary)
            }
        }
    }

    private func versionLine(_ meta: RevisionMeta?) -> String? {
        guard let meta else { return nil }
        return [LS("version.number", table: .editor, Int64(meta.number)), String(localized: meta.originText), EditorVersionFormat.time(meta.createdAt)]
            .joined(separator: " · ")
    }

    // MARK: Banners

    @ViewBuilder private var banners: some View {
        if let revision = model.conflictRevision {
            Banner(tone: .warning, systemImage: "arrow.triangle.branch", title: L("conflict.title", table: .editor),
                   message: L("conflict.banner", table: .editor, Int64(revision)),
                   actions: [BannerAction(L("conflict.resolve", table: .editor)) { model.showConflict = true }])
        }
        if let viewing = model.viewing {
            Banner(tone: .info, systemImage: "clock.arrow.circlepath",
                   message: L("version.viewing", table: .editor, Int64(viewing.meta.number)),
                   actions: (app.isOnline && viewing.meta.number != model.doc?.currentRevisionNumber
                       ? [BannerAction(L("version.restore", table: .editor), isLoading: model.isRestoring) {
                           Task { await model.restore(viewing.meta.number) }
                       }] : [])
                       + [BannerAction(L("version.backToLatest", table: .editor)) { model.viewing = nil }])
        }
        if model.viewing == nil, let doc = model.doc, doc.isStale, model.loadedRevision == doc.currentRevisionNumber {
            Banner(tone: .warning, systemImage: "clock.arrow.circlepath", title: L("doc.outdated.chip", table: .editor),
                   message: EditorText.plain(([LS("doc.outdated.body", table: .editor)] + doc.staleReasons.compactMap(staleReason)).joined(separator: " ")),
                   actions: regenBlock == nil && !isGenerating
                       ? [BannerAction(L("doc.regen", table: .editor), isLoading: model.isRequestingRegen) { model.showRegenConfirm = true }]
                       : [])
        }
        if isGenerating {
            HStack(spacing: Spacing.md) {
                ProgressView()
                Text(L(kind == .mom ? "mom.generating" : "notes.generating", table: .editor))
                    .font(Typography.callout)
                    .foregroundStyle(Palette.textPrimary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            .padding(Spacing.md)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Tone.info.fill, in: RoundedRectangle(cornerRadius: Radius.md, style: .continuous))
            .accessibilityElement(children: .combine)
        }
        if model.viewing == nil, let newer = model.newerRevision {
            Banner(tone: .info, systemImage: "sparkles", message: L("version.newReady", table: .editor, Int64(newer)),
                   actions: [BannerAction(L("version.open", table: .editor)) { Task { await model.openNewer() } }])
        }
        if regenFailed {
            Banner(tone: .danger, systemImage: "exclamationmark.triangle",
                   message: L(kind == .mom ? "mom.failed" : "notes.failed", table: .editor),
                   actions: regenBlock == nil
                       ? [BannerAction(L("common.retry"), isLoading: model.isRequestingRegen) { Task { await model.regenerate() } }]
                       : [],
                   onClose: { model.failedBannerDismissed = true })
        }
        if !app.isOnline, !model.offlineBannerDismissed {
            Banner(tone: .warning, systemImage: "wifi.slash", message: L("editor.offline", table: .editor),
                   onClose: { model.offlineBannerDismissed = true })
        }
    }

    private func staleReason(_ reason: String) -> String? {
        switch reason {
        case "transcript": LS("doc.outdated.reason.transcript", table: .editor)
        case "manual_notes": LS("doc.outdated.reason.manualNotes", table: .editor)
        case "ai_notes": LS("doc.outdated.reason.aiNotes", table: .editor)
        default: nil
        }
    }

    private var regenBlock: LocalizedStringResource? { model.regenerateBlockReason(usage: app.usage) }

    // MARK: Editable content

    @ViewBuilder private func editable(_ document: EditorDocument) -> some View {
        if let info = document.meetingInfo {
            EditorMeetingInfoView(info: info, isPlayable: isPlayable, model: model, focus: $focus)
        }
        if document.sections.isEmpty {
            Text(L("mom.section.empty", table: .editor))
                .font(Typography.callout)
                .foregroundStyle(Palette.textSecondary)
        }
        ForEach(Array(document.sections.enumerated()), id: \.element.id) { index, section in
            EditorSectionView(section: section, isFirst: index == 0, isLast: index == document.sections.count - 1,
                              isPlayable: isPlayable, model: model, focus: $focus)
        }
        addSectionMenu(document)
    }

    private func addSectionMenu(_ document: EditorDocument) -> some View {
        Menu {
            addSectionItems(document)
        } label: {
            Label { Text(L("mom.section.add", table: .editor)) } icon: { Image(systemName: "plus.rectangle.on.rectangle") }
                .font(Typography.headline)
                .frame(minHeight: Spacing.minTouchTarget)
        } primaryAction: {
            if document.missingStandardTypes.isEmpty { addSection(.custom) }
        }
        .tint(Palette.accent)
    }

    @ViewBuilder private func addSectionItems(_ document: EditorDocument) -> some View {
        ForEach(document.missingStandardTypes, id: \.self) { type in
            Button { addSection(type) } label: { Text(EditorText.plain(type.defaultHeading(language: document.language))) }
        }
        Button { addSection(.custom) } label: { Text(L("mom.section.new", table: .editor)) }
    }

    private func addSection(_ type: SectionType) {
        if let target = model.addSection(type) { Task { focus = target } }
    }

    // MARK: Toolbar

    @ToolbarContentBuilder private var toolbar: some ToolbarContent {
        ToolbarItem(placement: .principal) {
            Button {
                if model.conflictRevision != nil { model.showConflict = true } else if model.saveState == .notSynced { model.retryNow() }
            } label: {
                SaveIndicator(state: model.saveState)
            }
            .buttonStyle(.plain)
            .disabled(model.saveState != .notSynced)
            .accessibilityHint(model.saveState == .notSynced ? Text(L("common.retry")) : Text(EditorText.plain("")))
        }
        ToolbarItemGroup(placement: .topBarTrailing) {
            Button { model.undo() } label: { Image(systemName: "arrow.uturn.backward") }
                .disabled(model.draft?.canUndo != true || model.isReadOnly)
                .accessibilityLabel(Text(L("editor.undo", table: .editor)))
            Button { model.redo() } label: { Image(systemName: "arrow.uturn.forward") }
                .disabled(model.draft?.canRedo != true || model.isReadOnly)
                .accessibilityLabel(Text(L("editor.redo", table: .editor)))
            Menu {
                Button { model.showVersions = true } label: {
                    Label { Text(L("editor.versions", table: .editor)) } icon: { Image(systemName: "clock.arrow.circlepath") }
                }
                if let document = model.document, !model.isReadOnly {
                    Menu {
                        addSectionItems(document)
                    } label: {
                        Label { Text(L("mom.section.add", table: .editor)) } icon: { Image(systemName: "plus.rectangle.on.rectangle") }
                    }
                }
                Button { openExport() } label: {
                    Label { Text(L("editor.export", table: .editor)) } icon: { Image(systemName: "square.and.arrow.up") }
                }
                Button { model.showRegenConfirm = true } label: {
                    Label { Text(L("doc.regen", table: .editor)) } icon: { Image(systemName: "sparkles") }
                    if let reason = regenBlock { Text(reason) }
                }
                .disabled(regenBlock != nil || isGenerating || model.isRequestingRegen)
            } label: {
                Image(systemName: "ellipsis.circle")
            }
            .accessibilityLabel(Text(L("editor.menu.more", table: .editor)))
        }
        ToolbarItemGroup(placement: .keyboard) {
            if let sectionID = focus?.sectionID, let section = model.document?.section(sectionID), section.type != .actionItems {
                Menu {
                    Picker(selection: Binding(get: { section.style ?? .bullet }, set: { model.setStyle(section: sectionID, $0) })) {
                        ForEach(ListStyle.allCases, id: \.self) { style in
                            Text(L("section.style.\(style.rawValue)", table: .editor)).tag(style)
                        }
                    } label: { Text(L("section.style", table: .editor)) }
                } label: {
                    Image(systemName: "list.bullet")
                }
                .accessibilityLabel(Text(L("section.style", table: .editor)))
            }
            Button {
                if let target = focus { focus = model.delete(target) }
            } label: {
                Image(systemName: "trash")
            }
            .disabled(!canDeleteFocused)
            .accessibilityLabel(Text(L("item.deleteFocused", table: .editor)))
            Spacer()
            Button { focus = nil } label: { Text(L("common.done")) }
        }
    }

    private var canDeleteFocused: Bool {
        switch focus {
        case .agenda, .item, .point, .pic, .participant: true
        case .title, .heading, nil: false
        }
    }

    private func openExport() {
        model.persistNow()
        model.showExport = true
    }

    // MARK: Bottom bar

    private var bottomBar: some View {
        VStack(alignment: .leading, spacing: Spacing.sm) {
            if model.viewing == nil {
                if model.reviewStatus == .reviewed, model.showsReviewFootnote {
                    footnote(L("review.footnote", table: .editor))
                } else if model.reviewStatus == .draft, !app.isOnline {
                    footnote(L("review.offline", table: .editor))
                }
            }
            let layout = typeSize.isAccessibilitySize
                ? AnyLayout(VStackLayout(spacing: Spacing.sm))
                : AnyLayout(HStackLayout(spacing: Spacing.sm))
            layout {
                if model.viewing == nil { reviewControl }
                SecondaryButton(L("editor.export", table: .editor), systemImage: "square.and.arrow.up") { openExport() }
                    .fixedSize(horizontal: !typeSize.isAccessibilitySize, vertical: false)
            }
        }
        .padding(.horizontal, Spacing.screenInsetCompact)
        .padding(.vertical, Spacing.sm)
        .frame(maxWidth: .infinity)
        .background(Palette.surface)
        .overlay(alignment: .top) { Rectangle().fill(Palette.separator).frame(height: 1) }
        .zIndex(ZLayer.bottomBar)
    }

    @ViewBuilder private var reviewControl: some View {
        if model.reviewStatus == .reviewed {
            HStack(spacing: Spacing.sm) {
                Label { Text(L("review.reviewed", table: .editor)) } icon: { Image(systemName: "checkmark.seal.fill") }
                    .font(Typography.headline)
                    .foregroundStyle(Palette.success)
                Spacer(minLength: 0)
                Button { Task { await model.setReview(.draft) } } label: {
                    Text(L("review.revertDraft", table: .editor))
                        .font(Typography.callout)
                        .minTouchTarget()
                }
                .buttonStyle(.borderless)
                .tint(Palette.accent)
                .disabled(!app.isOnline || model.isReviewing)
            }
            .frame(maxWidth: .infinity)
        } else {
            PrimaryButton(L("review.markReviewed", table: .editor), systemImage: "checkmark.seal", isLoading: model.isReviewing) {
                Task { await model.setReview(.reviewed) }
            }
            .disabled(!app.isOnline)
            .accessibilityHint(app.isOnline ? Text(EditorText.plain("")) : Text(L("review.offline", table: .editor)))
        }
    }

    private func footnote(_ text: LocalizedStringResource) -> some View {
        Text(text)
            .font(Typography.footnote)
            .foregroundStyle(Palette.textSecondary)
            .fixedSize(horizontal: false, vertical: true)
    }
}

enum EditorVersionFormat {
    static func time(_ date: Date, now: Date = .now, calendar: Calendar = .current) -> String {
        calendar.isDate(date, inSameDayAs: now)
            ? date.formatted(date: .omitted, time: .shortened)
            : date.formatted(.dateTime.day().month(.abbreviated).hour().minute())
    }
}
