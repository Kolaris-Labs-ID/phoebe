import SwiftUI

struct MoMTab: View {
    @Environment(AppModel.self) private var app
    let data: DetailData
    @Bindable var model: MeetingDetailModel

    private var document: Document? { data.documents[.mom] }
    private var job: JobSnapshot? { data.job(.generateMom) }
    private var isGenerating: Bool { job?.status.isActive == true }
    private var failed: Bool {
        guard let job, job.status == .failed else { return false }
        return document.map { job.updatedAt > $0.updatedAt } ?? true
    }
    private var rateLimitedUntil: Date? { GenerationGate.rateLimitedUntil(usage: app.usage, retryAfter: model.retryAfterUntil) }

    private var block: GenerationBlock? {
        GenerationGate.mom(
            online: data.isOnline,
            noSpeech: data.isNoSpeech,
            hasAINotes: data.documents[.aiNotes] != nil,
            jobActive: isGenerating,
            edits: [data.edit(.manualNotes), data.edit(.aiNotes)].compactMap { $0 },
            rateLimitedUntil: rateLimitedUntil
        )
    }

    var body: some View {
        VStack(alignment: .leading, spacing: Spacing.lg) {
            if isGenerating {
                generatingCard
            }
            if failed {
                failureBanner
            }
            if document != nil || data.edits[.mom]?.hasPendingWork == true {
                readyCard
                otherVersions
            } else if !isGenerating {
                emptyState
            }
        }
        .task(id: data.documents[.aiNotes]?.currentRevisionNumber) {
            if data.documents[.aiNotes] != nil { await model.loadVersions(.aiNotes, app: app) }
        }
        .task(id: document?.currentRevisionNumber) {
            if document != nil { await model.loadVersions(.mom, app: app) }
        }
    }

    private var generatingCard: some View {
        Label { Text(L("mom.generating", table: .meetingDetail)) } icon: { ProgressView() }
            .font(Typography.callout)
            .foregroundStyle(Palette.textPrimary)
            .padding(Spacing.lg)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Palette.surface, in: RoundedRectangle(cornerRadius: Radius.lg, style: .continuous))
    }

    private var failureBanner: some View {
        let copy = FailureCopy.make(category: job?.errorCategory)
        return Banner(
            tone: .danger,
            systemImage: "exclamationmark.triangle",
            title: L("mom.failed", table: .meetingDetail),
            message: copy.showsRequestCode ? L(copy.key, table: .meetingDetail, data.jobs[.generateMom]?.id.lower ?? "-") : L(copy.key, table: .meetingDetail),
            actions: copy.retryAllowed && block == nil ? [BannerAction(L("common.retry"), isLoading: model.generating.contains(.generateMom)) { generate() }] : []
        )
    }

    // MARK: Empty

    private var emptyState: some View {
        VStack(spacing: Spacing.md) {
            ContentUnavailableView {
                Label { Text(L("mom.none.title", table: .meetingDetail)) } icon: { Image(systemName: "doc.text") }
            } description: {
                Text(L("mom.none.body", table: .meetingDetail))
            }
            if data.documents[.aiNotes] != nil { sourcePicker }
            PrimaryButton(L("mom.create", table: .meetingDetail), systemImage: "sparkles", isLoading: model.generating.contains(.generateMom)) { generate() }
                .disabled(block != nil)
            blockFootnote
            SecondaryButton(L("mom.createManual", table: .meetingDetail), systemImage: "square.and.pencil") {
                model.createManualMoM(meeting: data.meeting, app: app)
            }
        }
    }

    @ViewBuilder private var blockFootnote: some View {
        if let block, block != .inProgress {
            ActionErrorText(key: block.key, date: rateLimitedUntil, isError: false)
        }
        if let key = model.actionErrors[.generateMom] {
            ActionErrorText(key: key, date: rateLimitedUntil)
        }
    }

    private var sourcePicker: some View {
        let options = model.versions[.aiNotes] ?? []
        let latest = data.documents[.aiNotes]?.currentRevisionNumber ?? 0
        let selected = options.first { $0.id == model.momSourceRevisionID }
        let label = selected.map { "v\($0.revisionNumber)" } ?? LS("mom.source.latest", table: .meetingDetail, Int64(latest))
        return Menu {
            Button(L("mom.source.latest", table: .meetingDetail, Int64(latest))) { model.momSourceRevisionID = nil }
            ForEach(options.filter { $0.revisionNumber != latest }, id: \.id) { rev in
                Button("v\(rev.revisionNumber) · \(rev.createdAt.formatted(date: .abbreviated, time: .shortened))" as String) {
                    model.momSourceRevisionID = rev.id
                }
            }
        } label: {
            Label { Text(L("mom.source", table: .meetingDetail, label)) } icon: { Image(systemName: "note.text") }
                .font(Typography.subheadline)
                .frame(minHeight: Spacing.minTouchTarget)
        }
        .tint(Palette.accent)
    }

    // MARK: Ready

    private var readyCard: some View {
        let title: String = {
            if case .mom(let c)? = data.content(.mom), let t = c.meetingInfo.title.nonBlank { return t }
            return data.meeting.title.nonBlank ?? LS("detail.untitled", table: .meetingDetail)
        }()
        return VStack(alignment: .leading, spacing: Spacing.md) {
            Text(verbatim: title)
                .font(Typography.headline)
                .foregroundStyle(Palette.textPrimary)
            HStack(spacing: Spacing.sm) {
                if document?.reviewStatus == .reviewed {
                    StatusChip(.reviewed)
                } else {
                    StatusChip(.momDraft)
                }
                if document?.isStale == true { StatusChip(.outdated) }
                if let updated = document?.updatedAt {
                    Text(verbatim: updated.formatted(date: .abbreviated, time: .shortened))
                        .font(Typography.caption)
                        .foregroundStyle(Palette.textSecondary)
                }
            }
            if let document, document.isStale, !model.dismissedStale.contains(document.currentRevisionID) {
                StaleBanner(reasons: document.staleReasons, actions: [
                    BannerAction(L("doc.regen", table: .meetingDetail), isLoading: model.generating.contains(.generateMom)) { generate() },
                    BannerAction(L("mom.openAnyway", table: .meetingDetail)) { openEditor() },
                ]) {
                    model.dismissedStale.insert(document.currentRevisionID)
                }
            }
            PrimaryButton(L("mom.open", table: .meetingDetail), systemImage: "square.and.pencil") { openEditor() }
            SecondaryButton(L("mom.export", table: .meetingDetail), systemImage: "square.and.arrow.up") { model.showExport = true }
                .disabled(document == nil)
            if document != nil && !isGenerating {
                if data.documents[.aiNotes] != nil { sourcePicker }
                SecondaryButton(L("doc.regen", table: .meetingDetail), systemImage: "sparkles", isLoading: model.generating.contains(.generateMom)) { generate() }
                    .disabled(block != nil)
                if block == nil {
                    Text(L("doc.regen.footnote", table: .meetingDetail))
                        .font(Typography.footnote)
                        .foregroundStyle(Palette.textSecondary)
                }
                blockFootnote
            }
        }
        .padding(Spacing.lg)
        .background(Palette.surface, in: RoundedRectangle(cornerRadius: Radius.lg, style: .continuous))
    }

    @ViewBuilder private var otherVersions: some View {
        let current = document?.currentRevisionNumber
        let others = (model.versions[.mom] ?? []).filter { $0.revisionNumber != current }
        if !others.isEmpty {
            VStack(alignment: .leading, spacing: Spacing.sm) {
                Text(L("mom.otherVersions", table: .meetingDetail))
                    .font(Typography.headline)
                    .foregroundStyle(Palette.textPrimary)
                    .accessibilityAddTraits(.isHeader)
                ForEach(others, id: \.id) { rev in
                    Button { model.showVersions = .mom } label: {
                        VersionRowLabel(revision: rev)
                    }
                    .buttonStyle(.plain)
                }
            }
        }
    }

    private func openEditor() {
        app.path.append(.editor(meetingID: data.meeting.id, kind: .mom))
    }

    private func generate() {
        Task {
            await model.generate(.generateMom, body: model.momBody(), flushKinds: [.manualNotes, .aiNotes], pendingKinds: data.pendingKinds, app: app)
        }
    }
}

struct VersionRowLabel: View {
    let revision: DocumentRevisionDTO

    var body: some View {
        HStack {
            VStack(alignment: .leading, spacing: Spacing.xxs) {
                Text(L("doc.version.row", table: .meetingDetail, Int64(revision.revisionNumber), originText, revision.createdAt.formatted(date: .abbreviated, time: .shortened)))
                    .font(Typography.body)
                    .foregroundStyle(Palette.textPrimary)
                if revision.reviewedAt != nil {
                    Text(L("review.reviewed", table: .meetingDetail))
                        .font(Typography.caption)
                        .foregroundStyle(Palette.success)
                }
            }
            Spacer()
            Image(systemName: "chevron.right")
                .foregroundStyle(Palette.textTertiary)
                .accessibilityHidden(true)
        }
        .frame(minHeight: Spacing.minTouchTarget)
        .contentShape(Rectangle())
    }

    private var originText: String {
        switch revision.origin {
        case .aiGenerated: LS("doc.version.generated", table: .meetingDetail)
        case .userEdit: LS("doc.version.edited", table: .meetingDetail)
        case .restore: LS("doc.version.restored", table: .meetingDetail, Int64(revision.restoredFromRevisionNumber ?? 0))
        }
    }
}

// MARK: - Versions sheet & read-only preview

struct VersionsSheet: View {
    @Environment(AppModel.self) private var app
    @Environment(\.dismiss) private var dismiss
    let kind: DocumentKind
    let data: DetailData
    let model: MeetingDetailModel

    var body: some View {
        NavigationStack {
            List {
                if !app.isOnline && (model.versions[kind] ?? []).isEmpty {
                    Text(L("detail.ai.offline", table: .meetingDetail))
                        .foregroundStyle(Palette.textSecondary)
                }
                ForEach(model.versions[kind] ?? [], id: \.id) { rev in
                    NavigationLink {
                        RevisionPreview(kind: kind, revision: rev, data: data, model: model)
                    } label: {
                        HStack {
                            VersionRowLabel(revision: rev)
                            if rev.revisionNumber == data.documents[kind]?.currentRevisionNumber {
                                Text(L("doc.version.current", table: .meetingDetail))
                                    .font(Typography.caption)
                                    .foregroundStyle(Palette.textSecondary)
                            }
                        }
                    }
                }
            }
            .overlay {
                if model.versionsLoading.contains(kind) && (model.versions[kind] ?? []).isEmpty { ProgressView() }
            }
            .navigationTitle(Text(L("doc.versions", table: .meetingDetail)))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button(L("common.done")) { dismiss() }
                }
            }
        }
        .task { await model.loadVersions(kind, app: app) }
    }
}

private struct RevisionPreview: View {
    @Environment(AppModel.self) private var app
    @Environment(\.dismiss) private var dismiss
    let kind: DocumentKind
    let revision: DocumentRevisionDTO
    let data: DetailData
    let model: MeetingDetailModel

    @State private var content: DocumentContentValue?
    @State private var loading = true
    @State private var restoring = false
    @State private var errorKey: String?
    @State private var mutationID = UUID()

    private var isCurrent: Bool { revision.revisionNumber == data.documents[kind]?.currentRevisionNumber }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: Spacing.lg) {
                Banner(tone: .info, systemImage: "eye", message: L("version.viewing", table: .meetingDetail, Int64(revision.revisionNumber)))
                let refs = RefContext(verifiedSegmentIDs: nil, isPlayable: false) { _ in }
                switch content {
                case .aiNotes(let c)?: AINotesReadView(content: c, refs: refs)
                case .mom(let c)?: MoMReadView(content: c, refs: refs)
                default:
                    if loading { ProgressView().frame(maxWidth: .infinity) } else { ActionErrorText(key: "detail.action.failed") }
                }
                if let errorKey { ActionErrorText(key: errorKey) }
            }
            .padding(Spacing.screenInsetCompact)
            .readingWidth()
        }
        .safeAreaInset(edge: .bottom) {
            if !isCurrent {
                PrimaryButton(L("version.restore", table: .meetingDetail), systemImage: "arrow.uturn.backward", isLoading: restoring) { restore() }
                    .disabled(!app.isOnline || content == nil)
                    .padding(Spacing.screenInsetCompact)
                    .background(Palette.bg)
            }
        }
        .navigationTitle(Text(verbatim: "v\(revision.revisionNumber)"))
        .task {
            content = await model.fetchRevision(kind, number: revision.revisionNumber, app: app)
            loading = false
        }
    }

    private func restore() {
        guard let expected = data.documents[kind]?.currentRevisionNumber else { return }
        restoring = true
        errorKey = nil
        Task {
            errorKey = await model.restore(kind, number: revision.revisionNumber, expectedRevision: expected, mutationID: mutationID, app: app)
            restoring = false
            if errorKey == nil {
                dismiss()
            } else if errorKey == "version.restore.conflict" {
                mutationID = UUID()
            }
        }
    }
}
