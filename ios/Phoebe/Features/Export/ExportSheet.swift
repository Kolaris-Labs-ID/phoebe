import SwiftData
import SwiftUI

struct ExportSheet: View {
    let meetingID: UUID
    let kind: DocumentKind
    var onCreateMoM: (() -> Void)?
    @Environment(AppModel.self) private var app
    @State private var model: ExportModel?

    init(meetingID: UUID, kind: DocumentKind, onCreateMoM: (() -> Void)? = nil) {
        self.meetingID = meetingID
        self.kind = kind
        self.onCreateMoM = onCreateMoM
    }

    var body: some View {
        Group {
            if let model {
                ExportSheetContent(model: model, onCreateMoM: onCreateMoM)
            } else {
                Color.clear
            }
        }
        .presentationDetents([.large])
        .task {
            if model == nil { model = ExportModel(meetingID: meetingID, kind: kind, app: app) }
        }
    }
}

struct ExportSheetContent: View {
    @Bindable var model: ExportModel
    let onCreateMoM: (() -> Void)?
    @Environment(AppModel.self) private var app
    @Environment(\.dismiss) private var dismiss
    @Environment(\.modelContext) private var context
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @Environment(\.dynamicTypeSize) private var typeSize
    @Query private var documents: [Document]
    @Query private var pendingRows: [PendingDocumentEdit]

    init(model: ExportModel, onCreateMoM: (() -> Void)?) {
        self.model = model
        self.onCreateMoM = onCreateMoM
        let id = model.meetingID
        _documents = Query(filter: #Predicate<Document> { $0.meetingID == id })
        _pendingRows = Query(filter: #Predicate<PendingDocumentEdit> { $0.meetingID == id })
    }

    private var document: Document? { documents.first { $0.kind == model.kind } }

    private var inputs: ExportModel.Inputs {
        let row = pendingRows.first { $0.kind == model.kind }
        return ExportModel.Inputs(
            kind: model.kind,
            format: model.format,
            revisionID: document?.currentRevisionID,
            reviewStatus: document?.reviewStatus ?? .draft,
            pendingWork: row?.hasPendingWork == true,
            conflict: row?.conflictCurrentRevision != nil,
            online: app.isOnline,
            retry: model.retryToken)
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: Spacing.lg) {
                    documentPicker
                    formatList
                    if model.state != .noDocument {
                        notice
                        if let line = revisionLine {
                            Text(EditorText.plain(line))
                                .font(Typography.footnote)
                                .foregroundStyle(Palette.textSecondary)
                        }
                    }
                    previewCard
                }
                .padding(Spacing.screenInsetCompact)
                .readingWidth()
            }
            .background(Palette.bg)
            .safeAreaInset(edge: .bottom) { bottomBar }
            .navigationTitle(Text(L("export.title", table: .export)))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button { dismiss() } label: { Image(systemName: "xmark") }
                        .accessibilityLabel(Text(L("common.close")))
                }
            }
        }
        .task(id: inputs) { await model.refresh(inputs) }
        .sheet(isPresented: $model.showConflict) { ConflictSheet(meetingID: model.meetingID, kind: model.kind) }
        .fileExporter(
            isPresented: $model.showFileExporter,
            document: model.fileDocument,
            contentType: model.artifact?.format.contentType ?? .data,
            defaultFilename: model.artifact.map { ($0.filename as NSString).deletingPathExtension },
            onCompletion: { model.savedToFiles($0) }
        )
        .toastHost(app.toasts)
    }

    // MARK: Pickers

    private var documentPicker: some View {
        VStack(alignment: .leading, spacing: Spacing.sm) {
            sectionLabel(L("export.document", table: .export))
            Picker(selection: $model.kind) {
                Text(L("export.doc.mom", table: .export)).tag(DocumentKind.mom)
                Text(L("export.doc.notes", table: .export)).tag(DocumentKind.aiNotes)
                Text(L("export.doc.manual", table: .export)).tag(DocumentKind.manualNotes)
            } label: {
                Text(L("export.document", table: .export))
            }
            .pickerStyle(.segmented)
        }
    }

    private static let formats: [(ExportFormat, String)] = [(.pdf, "doc.richtext"), (.docx, "doc"), (.md, "number"), (.whatsapp, "message")]

    private var formatList: some View {
        VStack(alignment: .leading, spacing: Spacing.sm) {
            sectionLabel(L("export.format", table: .export))
            VStack(spacing: 0) {
                ForEach(Array(Self.formats.enumerated()), id: \.element.0) { index, entry in
                    if index > 0 { Divider().overlay(Palette.separator).padding(.leading, Spacing.xxxl) }
                    formatRow(entry.0, icon: entry.1)
                }
            }
            .background(Palette.surface, in: RoundedRectangle(cornerRadius: Radius.lg, style: .continuous))
            .accessibilityRepresentation {
                Picker(selection: $model.format) {
                    ForEach(Self.formats, id: \.0) { entry in
                        Text(EditorText.plain(LS(titleKey(entry.0), table: .export) + ", " + LS(titleKey(entry.0) + ".sub", table: .export))).tag(entry.0)
                    }
                } label: {
                    Text(L("export.format", table: .export))
                }
                .pickerStyle(.inline)
            }
        }
    }

    private func titleKey(_ format: ExportFormat) -> String {
        switch format {
        case .pdf: "export.pdf"
        case .docx: "export.docx"
        case .md: "export.md"
        case .whatsapp: "export.wa"
        }
    }

    private func formatRow(_ format: ExportFormat, icon: String) -> some View {
        let selected = model.format == format
        return Button { model.format = format } label: {
            HStack(spacing: Spacing.md) {
                Image(systemName: icon)
                    .foregroundStyle(Palette.accent)
                    .frame(width: Spacing.xl)
                VStack(alignment: .leading, spacing: Spacing.xxs) {
                    Text(L(titleKey(format), table: .export))
                        .font(Typography.body)
                        .foregroundStyle(Palette.textPrimary)
                    Text(L(titleKey(format) + ".sub", table: .export))
                        .font(Typography.footnote)
                        .foregroundStyle(Palette.textSecondary)
                }
                Spacer(minLength: 0)
                if selected {
                    Image(systemName: "checkmark")
                        .font(Typography.body.weight(.semibold))
                        .foregroundStyle(Palette.accent)
                }
            }
            .padding(.horizontal, Spacing.md)
            .padding(.vertical, Spacing.sm)
            .frame(minHeight: 56)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(selected ? .isSelected : [])
    }

    private func sectionLabel(_ text: LocalizedStringResource) -> some View {
        Text(text)
            .font(Typography.subheadline.weight(.semibold))
            .foregroundStyle(Palette.textSecondary)
            .accessibilityAddTraits(.isHeader)
    }

    // MARK: Notice + revision

    private var isDraft: Bool { model.artifact?.isDraft ?? (document?.reviewStatus != .reviewed) }

    @ViewBuilder private var notice: some View {
        if isDraft {
            noticeRow(L("export.draftNotice", table: .export), icon: "pencil.circle", tone: .warning)
        } else {
            noticeRow(L("export.reviewedNotice", table: .export), icon: "checkmark.seal.fill", tone: .success)
        }
    }

    private func noticeRow(_ text: LocalizedStringResource, icon: String, tone: Tone) -> some View {
        HStack(alignment: .top, spacing: Spacing.sm) {
            Image(systemName: icon).foregroundStyle(tone.foreground).accessibilityHidden(true)
            Text(text).font(Typography.callout).foregroundStyle(Palette.textPrimary).fixedSize(horizontal: false, vertical: true)
        }
        .padding(Spacing.md)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(tone.fill, in: RoundedRectangle(cornerRadius: Radius.md, style: .continuous))
        .accessibilityElement(children: .combine)
    }

    private var revisionLine: String? {
        guard let document else { return nil }
        let revID = document.currentRevisionID
        let created = (try? context.fetch(FetchDescriptor<DocumentRevision>(predicate: #Predicate { $0.id == revID })).first)?.createdAt
        return LS("export.revision", table: .export, Int64(document.currentRevisionNumber), EditorVersionFormat.time(created ?? document.updatedAt))
    }

    // MARK: Preview

    private var formatName: String { LS(titleKey(model.format), table: .export) }

    private var previewCard: some View {
        VStack(alignment: .leading, spacing: Spacing.sm) {
            sectionLabel(L("export.preview", table: .export))
            previewBody
                .frame(maxWidth: .infinity, minHeight: 360)
                .background(Palette.surface, in: RoundedRectangle(cornerRadius: Radius.lg, style: .continuous))
                .overlay(RoundedRectangle(cornerRadius: Radius.lg, style: .continuous).stroke(Palette.separator, lineWidth: 1))
                .animation(Motion.standard(reduceMotion: reduceMotion), value: model.state)
        }
    }

    @ViewBuilder private var previewBody: some View {
        switch model.state {
        case .idle, .generating:
            progress(L("export.generating", table: .export))
        case .syncing:
            progress(L("export.syncingFirst", table: .export))
        case .offlinePending:
            message(L("export.offline.pending", table: .export), icon: "wifi.slash", tone: .warning)
        case .offline:
            message(L("export.offline", table: .export), icon: "wifi.slash", tone: .warning)
        case .conflict:
            VStack(spacing: Spacing.md) {
                message(L("conflict.title", table: .editor), icon: "arrow.triangle.branch", tone: .warning)
                SecondaryButton(L("conflict.resolve", table: .editor)) { model.showConflict = true }
            }
        case .failed:
            VStack(spacing: Spacing.md) {
                message(L("export.failed", table: .export), icon: "exclamationmark.triangle", tone: .danger)
                SecondaryButton(L("common.retry")) { model.retryToken += 1 }
            }
        case .noDocument:
            emptyState
        case .ready(let artifact):
            if artifact.format.isText {
                ScrollView {
                    Text(EditorText.plain(artifact.text ?? ""))
                        .font(Typography.callout.monospaced())
                        .foregroundStyle(Palette.textPrimary)
                        .textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(Spacing.md)
                }
                .frame(maxHeight: 360)
                .accessibilityLabel(Text(L("export.preview.a11y", table: .export, formatName)))
                .transition(.opacity)
            } else {
                Button { model.openPreview() } label: {
                    VStack(spacing: Spacing.sm) {
                        if let thumbnail = model.thumbnail {
                            Image(uiImage: thumbnail)
                                .resizable()
                                .scaledToFit()
                                .frame(maxHeight: 300)
                        } else {
                            Image(systemName: artifact.format == .pdf ? "doc.richtext" : "doc")
                                .font(.largeTitle)
                                .foregroundStyle(Palette.textSecondary)
                        }
                        Text(EditorText.plain(artifact.filename))
                            .font(Typography.footnote)
                            .foregroundStyle(Palette.textSecondary)
                        Label { Text(L("export.preview.open", table: .export)) } icon: { Image(systemName: "arrow.up.left.and.arrow.down.right") }
                            .font(Typography.callout)
                            .foregroundStyle(Palette.accent)
                    }
                    .padding(Spacing.md)
                    .frame(maxWidth: .infinity, minHeight: 360)
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityLabel(Text(L("export.preview.a11y", table: .export, formatName)))
                .accessibilityHint(Text(L("export.preview.hint", table: .export)))
                .transition(.opacity)
            }
        }
    }

    @ViewBuilder private var emptyState: some View {
        switch model.kind {
        case .mom:
            ContentUnavailableView {
                Label { Text(L("export.noMom", table: .export)) } icon: { Image(systemName: "doc.text") }
            } actions: {
                SecondaryButton(L("export.createMom", table: .export)) {
                    dismiss()
                    onCreateMoM?()
                }
            }
        case .aiNotes:
            ContentUnavailableView { Label { Text(L("export.noNotes", table: .export)) } icon: { Image(systemName: "doc.text") } }
        case .manualNotes:
            ContentUnavailableView { Label { Text(L("export.noManual", table: .export)) } icon: { Image(systemName: "doc.text") } }
        }
    }

    private func progress(_ text: LocalizedStringResource) -> some View {
        VStack(spacing: Spacing.md) {
            ProgressView()
            Text(text).font(Typography.callout).foregroundStyle(Palette.textSecondary).multilineTextAlignment(.center)
        }
        .padding(Spacing.lg)
        .accessibilityElement(children: .combine)
    }

    private func message(_ text: LocalizedStringResource, icon: String, tone: Tone) -> some View {
        VStack(spacing: Spacing.sm) {
            Image(systemName: icon).font(.title2).foregroundStyle(tone.foreground).accessibilityHidden(true)
            Text(text).font(Typography.callout).foregroundStyle(Palette.textPrimary).multilineTextAlignment(.center)
        }
        .padding(Spacing.lg)
        .accessibilityElement(children: .combine)
    }

    // MARK: Bottom bar

    private var disabledReason: LocalizedStringResource? {
        switch model.state {
        case .ready: nil
        case .idle, .generating: L("export.generating", table: .export)
        case .syncing: L("export.syncingFirst", table: .export)
        case .offlinePending: L("export.offline.pending", table: .export)
        case .offline: L("export.offline", table: .export)
        case .conflict: L("conflict.title", table: .editor)
        case .failed: L("export.failed", table: .export)
        case .noDocument:
            switch model.kind {
            case .mom: L("export.noMom", table: .export)
            case .aiNotes: L("export.noNotes", table: .export)
            case .manualNotes: L("export.noManual", table: .export)
            }
        }
    }

    private var bottomBar: some View {
        let ready = model.artifact != nil
        let loading = model.state == .generating || model.state == .syncing || model.state == .idle
        return VStack(spacing: Spacing.sm) {
            if let reason = disabledReason, !loading {
                Text(reason)
                    .font(Typography.footnote)
                    .foregroundStyle(Palette.textSecondary)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .fixedSize(horizontal: false, vertical: true)
            }
            PrimaryButton(L("export.share", table: .export), systemImage: "square.and.arrow.up", isLoading: loading) { model.share() }
                .disabled(!ready)
                .accessibilityHint(disabledReason.map { Text($0) } ?? Text(EditorText.plain("")))
            let layout = typeSize.isAccessibilitySize
                ? AnyLayout(VStackLayout(spacing: Spacing.sm))
                : AnyLayout(HStackLayout(spacing: Spacing.sm))
            layout {
                if model.format.isText {
                    SecondaryButton(L("export.copy", table: .export), systemImage: "doc.on.doc") { model.copyText() }
                        .frame(maxWidth: .infinity)
                        .disabled(model.artifact?.text == nil)
                }
                SecondaryButton(L("export.saveFiles", table: .export), systemImage: "folder") { model.showFileExporter = true }
                    .frame(maxWidth: .infinity)
                    .disabled(model.fileDocument == nil)
            }
        }
        .padding(.horizontal, Spacing.screenInsetCompact)
        .padding(.vertical, Spacing.sm)
        .background(Palette.surface)
        .overlay(alignment: .top) { Rectangle().fill(Palette.separator).frame(height: 1) }
        .zIndex(ZLayer.bottomBar)
    }
}
