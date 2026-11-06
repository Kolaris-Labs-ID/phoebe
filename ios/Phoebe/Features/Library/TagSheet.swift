import SwiftData
import SwiftUI

struct TagSheet: View {
    enum Mode: Equatable { case manage, assign(UUID) }

    static let maxTagsPerMeeting = 20

    let mode: Mode
    let model: LibraryModel
    @Environment(AppModel.self) private var app
    @Environment(\.dismiss) private var dismiss
    @Query(sort: \Tag.name) private var tags: [Tag]
    @Query private var meetings: [Meeting]
    @State private var search = ""
    @State private var error: NameValidation?
    @State private var renameTarget: Tag?
    @State private var renameText = ""
    @State private var deleteTarget: Tag?

    init(mode: Mode, model: LibraryModel) {
        self.mode = mode
        self.model = model
        let id: UUID = if case .assign(let meetingID) = mode { meetingID } else { UUID() }
        _meetings = Query(filter: #Predicate<Meeting> { $0.id == id })
    }

    private var meeting: Meeting? { meetings.first }
    private var query: String { search.trimmingCharacters(in: .whitespacesAndNewlines) }
    private var existing: [(id: UUID, name: String)] { tags.map { ($0.id, $0.name) } }

    private var visibleTags: [Tag] {
        query.isEmpty ? tags : tags.filter { $0.name.localizedStandardContains(query) }
    }

    private var canCreate: Bool {
        !query.isEmpty && !tags.contains { $0.name.compare(query, options: .caseInsensitive) == .orderedSame }
    }

    private var assignedIDs: [UUID] { meeting?.tags.map(\.id) ?? [] }

    var body: some View {
        NavigationStack {
            List {
                Section {
                    TextField(text: $search) { Text(L("tag.search", table: .library)) }
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .onChange(of: search) { _, value in
                            error = nil
                            if value.count > NameValidation.maxLength { search = String(value.prefix(NameValidation.maxLength)) }
                        }
                        .onSubmit { if canCreate { create() } }
                        .frame(minHeight: Spacing.minTouchTarget)
                    if canCreate {
                        Button(action: create) {
                            Label { Text(L("tag.create", table: .library, query)) } icon: { Image(systemName: "plus") }
                        }
                        .frame(minHeight: Spacing.minTouchTarget)
                        .disabled(!canAssignMore && mode != .manage)
                    }
                    if let message = errorMessage {
                        Label { Text(message) } icon: { Image(systemName: "exclamationmark.circle") }
                            .font(Typography.footnote)
                            .foregroundStyle(Palette.danger)
                            .onAppear { AccessibilityNotification.Announcement(String(localized: message)).post() }
                    }
                }
                Section {
                    ForEach(visibleTags) { tag in
                        if mode == .manage { manageRow(tag) } else { assignRow(tag) }
                    }
                }
            }
            .navigationTitle(Text(L(mode == .manage ? "tag.title" : "meeting.tags", table: .library)))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button { dismiss() } label: { Text(L("common.done")) }
                }
            }
            .alert(Text(L("tag.rename", table: .library)), isPresented: Binding { renameTarget != nil } set: { if !$0 { renameTarget = nil } }) {
                TextField(text: $renameText) { Text(L("tag.name", table: .library)) }
                Button { rename() } label: { Text(L("common.save")) }
                Button(role: .cancel) {} label: { Text(L("common.cancel")) }
            }
            .confirmationDialog(
                Text(L("tag.delete.confirm", table: .library, deleteTarget?.name ?? "")),
                isPresented: Binding { deleteTarget != nil } set: { if !$0 { deleteTarget = nil } },
                titleVisibility: .visible
            ) {
                Button(role: .destructive) {
                    if let target = deleteTarget {
                        if model.tagFilter == target.id { model.tagFilter = nil }
                        app.sync.enqueue(.deleteTag(target.id))
                    }
                } label: { Text(L("tag.delete", table: .library)) }
                Button(role: .cancel) {} label: { Text(L("common.cancel")) }
            }
        }
    }

    private var canAssignMore: Bool { assignedIDs.count < Self.maxTagsPerMeeting }

    private var errorMessage: LocalizedStringResource? {
        switch error {
        case .empty?: L("tag.name.error.empty", table: .library)
        case .duplicate?: L("tag.name.error.duplicate", table: .library)
        default: nil
        }
    }

    private func assignRow(_ tag: Tag) -> some View {
        let selected = assignedIDs.contains(tag.id)
        return Button {
            guard let meeting else { return }
            let ids = selected ? assignedIDs.filter { $0 != tag.id } : assignedIDs + [tag.id]
            model.setTags(meeting, to: ids, app: app)
        } label: {
            HStack {
                Label { Text(verbatim: tag.name) } icon: { Image(systemName: selected ? "tag.fill" : "tag") }
                    .foregroundStyle(Palette.textPrimary)
                Spacer()
                if selected { Image(systemName: "checkmark").foregroundStyle(Palette.accent).accessibilityHidden(true) }
            }
            .frame(minHeight: Spacing.minTouchTarget)
            .contentShape(Rectangle())
        }
        .disabled(!selected && !canAssignMore)
        .accessibilityAddTraits(selected ? .isSelected : [])
    }

    private func manageRow(_ tag: Tag) -> some View {
        LabeledContent {
            Text(verbatim: "\(tag.meetings.count)").monospacedDigit()
        } label: {
            Label { Text(verbatim: tag.name) } icon: { Image(systemName: "tag") }
        }
        .frame(minHeight: Spacing.minTouchTarget)
        .swipeActions(edge: .trailing) {
            Button(role: .destructive) { deleteTarget = tag } label: { Label { Text(L("tag.delete", table: .library)) } icon: { Image(systemName: "trash") } }
            Button { startRename(tag) } label: { Label { Text(L("tag.rename", table: .library)) } icon: { Image(systemName: "pencil") } }
        }
        .contextMenu {
            Button { startRename(tag) } label: { Label { Text(L("tag.rename", table: .library)) } icon: { Image(systemName: "pencil") } }
            Button(role: .destructive) { deleteTarget = tag } label: { Label { Text(L("tag.delete", table: .library)) } icon: { Image(systemName: "trash") } }
        }
        .accessibilityActions {
            Button { startRename(tag) } label: { Text(L("tag.rename", table: .library)) }
            Button { deleteTarget = tag } label: { Text(L("tag.delete", table: .library)) }
        }
    }

    private func create() {
        switch NameValidation.check(search, existing: existing) {
        case .valid(let name):
            let id = UUID()
            app.sync.enqueue(.createTag(id: id, name: name))
            if let meeting, canAssignMore { model.setTags(meeting, to: assignedIDs + [id], app: app) }
            search = ""
        case let failure:
            error = failure
        }
    }

    private func startRename(_ tag: Tag) {
        renameText = tag.name
        renameTarget = tag
    }

    private func rename() {
        guard let target = renameTarget else { return }
        switch NameValidation.check(renameText, existing: existing, excluding: target.id) {
        case .valid(let name):
            if name != target.name { app.sync.enqueue(.renameTag(id: target.id, name: name)) }
        case let failure:
            error = failure
        }
        renameTarget = nil
    }
}
