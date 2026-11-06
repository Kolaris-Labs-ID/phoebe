import SwiftData
import SwiftUI

// Driven by PendingDocumentEdit.conflictCurrentRevision; reusable for any document kind.
struct ConflictSheet: View {
    let meetingID: UUID
    let kind: DocumentKind
    let resolve: ((ConflictResolution) async -> Void)?
    @Environment(AppModel.self) private var app
    @Environment(\.dismiss) private var dismiss
    @Query private var rows: [PendingDocumentEdit]
    @State private var server: DocumentRevisionDTO?
    @State private var showServer = false
    @State private var confirmDiscard = false
    @State private var working: ConflictResolution?

    init(meetingID: UUID, kind: DocumentKind, resolve: ((ConflictResolution) async -> Void)? = nil) {
        self.meetingID = meetingID
        self.kind = kind
        self.resolve = resolve
        let id = meetingID, raw = kind.rawValue
        _rows = Query(filter: #Predicate<PendingDocumentEdit> { $0.meetingID == id && $0.kindRaw == raw })
    }

    private var row: PendingDocumentEdit? { rows.first }
    private var serverRevision: Int? { row?.conflictCurrentRevision }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: Spacing.lg) {
                    Label { Text(L("conflict.title", table: .editor)) } icon: { Image(systemName: "arrow.triangle.branch") }
                        .font(Typography.title)
                        .foregroundStyle(Palette.textPrimary)
                        .accessibilityAddTraits(.isHeader)
                    Text(L("conflict.body", table: .editor,
                           Int64(serverRevision ?? 0),
                           server.map { EditorVersionFormat.time($0.createdAt) } ?? "…",
                           Int64(row?.baseRevision ?? 0)))
                        .readingTextStyle()
                        .foregroundStyle(Palette.textPrimary)
                    SecondaryButton(L("conflict.view", table: .editor), systemImage: "eye") { showServer = true }
                        .disabled(server?.content == nil)
                    PrimaryButton(L("conflict.keepMine", table: .editor), isLoading: working == .keepMine) {
                        Task { await run(.keepMine) }
                    }
                    Button(role: .destructive) { confirmDiscard = true } label: {
                        Text(L("conflict.useServer", table: .editor))
                            .font(Typography.callout.weight(.semibold))
                            .frame(maxWidth: .infinity, minHeight: Spacing.minTouchTarget)
                    }
                    .buttonStyle(.bordered)
                    .disabled(working != nil)
                }
                .padding(Spacing.screenInsetCompact)
                .readingWidth()
            }
            .background(Palette.bg)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button { dismiss() } label: { Image(systemName: "xmark") }
                        .accessibilityLabel(Text(L("common.close")))
                }
            }
            .navigationDestination(isPresented: $showServer) {
                ScrollView {
                    if let content = server?.content, let document = EditorDocument(content) {
                        ReadOnlyDocumentView(document: document)
                            .padding(Spacing.screenInsetCompact)
                            .readingWidth()
                    }
                }
                .background(Palette.bg)
                .navigationTitle(Text(L("version.viewing", table: .editor, Int64(serverRevision ?? 0))))
                .navigationBarTitleDisplayMode(.inline)
            }
            .confirmationDialog(Text(L("conflict.useServer", table: .editor)), isPresented: $confirmDiscard, titleVisibility: .visible) {
                Button(role: .destructive) { Task { await run(.useLatest) } } label: { Text(L("conflict.useServer", table: .editor)) }
                Button(role: .cancel) {} label: { Text(L("common.cancel")) }
            } message: {
                Text(L("conflict.useServer.confirm", table: .editor))
            }
        }
        .task(id: serverRevision) { await fetchServer() }
        .onChange(of: serverRevision) { _, value in if value == nil, working == nil { dismiss() } }
    }

    private func fetchServer() async {
        guard let number = serverRevision, app.isOnline else { return }
        server = try? await app.api.send(Endpoints.documentRevision(meetingID: meetingID, kind: kind, number: number)).revision
    }

    private func run(_ resolution: ConflictResolution) async {
        working = resolution
        if let resolve {
            await resolve(resolution)
        } else {
            await app.sync.resolveDocumentConflict(meetingID: meetingID, kind: kind, resolution)
        }
        working = nil
        dismiss()
    }
}
