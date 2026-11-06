import SwiftData
import SwiftUI

struct EditorVersionsSheet: View {
    let model: DocumentEditorModel
    @Environment(AppModel.self) private var app
    @Environment(\.dismiss) private var dismiss
    @Query private var documents: [Document]
    @Query private var transcripts: [TranscriptRevision]

    init(model: DocumentEditorModel) {
        self.model = model
        let id = model.meetingID
        _documents = Query(filter: #Predicate<Document> { $0.meetingID == id })
        _transcripts = Query(filter: #Predicate<TranscriptRevision> { $0.meetingID == id }, sort: \.revisionNumber, order: .reverse)
    }

    private var current: Document? { documents.first { $0.kind == model.kind } }

    var body: some View {
        NavigationStack {
            List {
                if model.versionsFromDevice {
                    Text(L("version.offline", table: .editor))
                        .font(Typography.footnote)
                        .foregroundStyle(Palette.textSecondary)
                }
                ForEach(model.versions) { meta in
                    row(meta)
                }
            }
            .listStyle(.insetGrouped)
            .overlay {
                if model.versionsLoading, model.versions.isEmpty {
                    ProgressView()
                } else if !model.versionsLoading, model.versions.isEmpty {
                    ContentUnavailableView {
                        Label { Text(L("version.empty", table: .editor)) } icon: { Image(systemName: "clock.arrow.circlepath") }
                    }
                }
            }
            .navigationTitle(Text(L("editor.versions", table: .editor)))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button { dismiss() } label: { Image(systemName: "xmark") }
                        .accessibilityLabel(Text(L("common.close")))
                }
            }
            .task { await model.loadVersions() }
        }
        .presentationDetents([.medium, .large])
    }

    private func row(_ meta: RevisionMeta) -> some View {
        let isCurrent = meta.number == current?.currentRevisionNumber
        return HStack(alignment: .center, spacing: Spacing.md) {
            Button {
                Task {
                    await model.openVersion(meta)
                    dismiss()
                }
            } label: {
                VStack(alignment: .leading, spacing: Spacing.xs) {
                    Text(EditorText.plain(title(meta)))
                        .font(Typography.headline)
                        .foregroundStyle(Palette.textPrimary)
                    if let source = sourceLine(meta) {
                        Text(EditorText.plain(source))
                            .font(Typography.footnote)
                            .foregroundStyle(Palette.textSecondary)
                    }
                    EditorFlowLayout(spacing: Spacing.xs) {
                        if isCurrent {
                            StatusChip(L("version.current", table: .editor), systemImage: "checkmark.circle", tone: .info)
                        }
                        if meta.reviewedAt != nil {
                            StatusChip(L("review.reviewed", table: .editor), systemImage: "checkmark.seal.fill", tone: .success)
                        }
                        if isOutdated(meta, isCurrent: isCurrent) {
                            StatusChip(.outdated)
                        }
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            if !isCurrent, app.isOnline {
                Button {
                    Task { await model.restore(meta.number) }
                } label: {
                    Text(L("version.restoreShort", table: .editor))
                        .font(Typography.subheadline.weight(.semibold))
                        .frame(minHeight: Spacing.minTouchTarget - Spacing.sm)
                }
                .buttonStyle(.bordered)
                .tint(Palette.accent)
                .disabled(model.isRestoring)
                .accessibilityLabel(Text(L("version.restoreA11y", table: .editor, Int64(meta.number))))
            }
        }
        .padding(.vertical, Spacing.xs)
    }

    private func title(_ meta: RevisionMeta) -> String {
        [LS("version.number", table: .editor, Int64(meta.number)), String(localized: meta.originText), EditorVersionFormat.time(meta.createdAt)]
            .joined(separator: " · ")
    }

    private func sourceLine(_ meta: RevisionMeta) -> String? {
        let notes = model.kind == .mom ? meta.aiNotesNumber : nil
        switch (notes, meta.transcriptNumber) {
        case let (n?, t?): return LS("version.source", table: .editor, Int64(n), Int64(t))
        case let (nil, t?): return LS("version.source.transcript", table: .editor, Int64(t))
        default: return nil
        }
    }

    private func isOutdated(_ meta: RevisionMeta, isCurrent: Bool) -> Bool {
        if isCurrent { return current?.isStale == true }
        if let t = meta.transcriptNumber, let latest = transcripts.first?.revisionNumber, t < latest { return true }
        if model.kind == .mom, let n = meta.aiNotesNumber,
           let latest = documents.first(where: { $0.kind == .aiNotes })?.currentRevisionNumber, n < latest { return true }
        return false
    }
}
