import SwiftData
import SwiftUI

struct FolderSheet: View {
    enum Mode: Equatable { case manage, move(UUID) }

    let mode: Mode
    let model: LibraryModel
    @Environment(AppModel.self) private var app
    @Environment(\.dismiss) private var dismiss
    @Query(sort: \Folder.name) private var folders: [Folder]
    @Query private var meetings: [Meeting]
    @State private var newName = ""
    @State private var error: NameValidation?
    @State private var renameTarget: Folder?
    @State private var renameText = ""
    @State private var deleteTarget: Folder?

    init(mode: Mode, model: LibraryModel) {
        self.mode = mode
        self.model = model
        let id: UUID = if case .move(let meetingID) = mode { meetingID } else { UUID() }
        _meetings = Query(filter: #Predicate<Meeting> { $0.id == id })
    }

    private var meeting: Meeting? { meetings.first }
    private var existing: [(id: UUID, name: String)] { folders.map { ($0.id, $0.name) } }

    var body: some View {
        NavigationStack {
            List {
                if mode == .manage {
                    Section {
                        ForEach(folders) { folder in manageRow(folder) }
                    }
                } else if let meeting {
                    Section {
                        moveRow(name: L("library.filter.noFolder", table: .library), systemImage: "tray", selected: meeting.folder == nil) {
                            model.move(meeting, to: nil, app: app)
                            dismiss()
                        }
                        ForEach(folders) { folder in
                            moveRow(name: resourceVerbatim(folder.name), systemImage: "folder", selected: meeting.folder?.id == folder.id) {
                                model.move(meeting, to: folder, app: app)
                                dismiss()
                            }
                        }
                    }
                }
                Section {
                    HStack(spacing: Spacing.sm) {
                        TextField(text: $newName) { Text(L("folder.name", table: .library)) }
                            .onChange(of: newName) { _, value in
                                error = nil
                                if value.count > NameValidation.maxLength { newName = String(value.prefix(NameValidation.maxLength)) }
                            }
                            .onSubmit(create)
                            .submitLabel(.done)
                        Button(action: create) {
                            Image(systemName: "plus.circle.fill").imageScale(.large)
                        }
                        .minTouchTarget()
                        .disabled(newName.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                        .accessibilityLabel(Text(L("folder.new", table: .library)))
                    }
                    if let message = errorMessage {
                        Label { Text(message) } icon: { Image(systemName: "exclamationmark.circle") }
                            .font(Typography.footnote)
                            .foregroundStyle(Palette.danger)
                            .onAppear { AccessibilityNotification.Announcement(String(localized: message)).post() }
                    }
                } header: {
                    Text(L("folder.new", table: .library))
                }
            }
            .navigationTitle(Text(L(mode == .manage ? "folder.title" : "meeting.move", table: .library)))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button { dismiss() } label: { Text(L("common.done")) }
                }
            }
            .alert(Text(L("folder.rename", table: .library)), isPresented: Binding { renameTarget != nil } set: { if !$0 { renameTarget = nil } }) {
                TextField(text: $renameText) { Text(L("folder.name", table: .library)) }
                Button { rename() } label: { Text(L("common.save")) }
                Button(role: .cancel) {} label: { Text(L("common.cancel")) }
            }
            .confirmationDialog(
                Text(L("folder.delete.confirm", table: .library, deleteTarget?.name ?? "")),
                isPresented: Binding { deleteTarget != nil } set: { if !$0 { deleteTarget = nil } },
                titleVisibility: .visible
            ) {
                Button(role: .destructive) {
                    if let target = deleteTarget {
                        if case .folder(target.id) = model.folderFilter { model.folderFilter = .all }
                        app.sync.enqueue(.deleteFolder(target.id))
                    }
                } label: { Text(L("folder.delete", table: .library)) }
                Button(role: .cancel) {} label: { Text(L("common.cancel")) }
            }
        }
    }

    private var errorMessage: LocalizedStringResource? {
        switch error {
        case .empty?: L("folder.name.error.empty", table: .library)
        case .duplicate?: L("folder.name.error.duplicate", table: .library)
        default: nil
        }
    }

    private func manageRow(_ folder: Folder) -> some View {
        LabeledContent {
            Text(verbatim: "\(folder.meetings.count)").monospacedDigit()
        } label: {
            Label { Text(verbatim: folder.name) } icon: { Image(systemName: "folder") }
        }
        .frame(minHeight: Spacing.minTouchTarget)
        .swipeActions(edge: .trailing) {
            Button(role: .destructive) { deleteTarget = folder } label: { Label { Text(L("folder.delete", table: .library)) } icon: { Image(systemName: "trash") } }
            Button { startRename(folder) } label: { Label { Text(L("folder.rename", table: .library)) } icon: { Image(systemName: "pencil") } }
        }
        .contextMenu {
            Button { startRename(folder) } label: { Label { Text(L("folder.rename", table: .library)) } icon: { Image(systemName: "pencil") } }
            Button(role: .destructive) { deleteTarget = folder } label: { Label { Text(L("folder.delete", table: .library)) } icon: { Image(systemName: "trash") } }
        }
        .accessibilityActions {
            Button { startRename(folder) } label: { Text(L("folder.rename", table: .library)) }
            Button { deleteTarget = folder } label: { Text(L("folder.delete", table: .library)) }
        }
    }

    private func moveRow(name: LocalizedStringResource, systemImage: String, selected: Bool, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            HStack {
                Label { Text(name) } icon: { Image(systemName: selected ? "\(systemImage).fill" : systemImage) }
                    .foregroundStyle(Palette.textPrimary)
                Spacer()
                if selected {
                    Image(systemName: "checkmark").foregroundStyle(Palette.accent).accessibilityHidden(true)
                }
            }
            .frame(minHeight: Spacing.minTouchTarget)
            .contentShape(Rectangle())
        }
        .accessibilityAddTraits(selected ? .isSelected : [])
    }

    private func create() {
        switch NameValidation.check(newName, existing: existing) {
        case .valid(let name):
            let id = UUID()
            app.sync.enqueue(.createFolder(id: id, name: name))
            newName = ""
            error = nil
        case let failure:
            error = failure
        }
    }

    private func startRename(_ folder: Folder) {
        renameText = folder.name
        renameTarget = folder
    }

    private func rename() {
        guard let target = renameTarget else { return }
        switch NameValidation.check(renameText, existing: existing, excluding: target.id) {
        case .valid(let name):
            if name != target.name { app.sync.enqueue(.renameFolder(id: target.id, name: name)) }
        case let failure:
            error = failure
        }
        renameTarget = nil
    }
}
