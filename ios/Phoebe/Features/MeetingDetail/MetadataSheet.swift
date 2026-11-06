import SwiftData
import SwiftUI

struct MetadataSheet: View {
    @Environment(AppModel.self) private var app
    @Environment(\.dismiss) private var dismiss
    let data: DetailData

    @State private var title = ""
    @State private var hasDate = true
    @State private var date = Date.now
    @State private var timezone = TimeZone.current.identifier
    @State private var language: TranscriptionLanguage = .auto
    @State private var participants: [String] = []
    @State private var confirmDiscard = false
    @State private var loaded = false
    @FocusState private var focusedParticipant: Int?

    private var meeting: Meeting { data.meeting }

    private var original: (title: String, startedAt: Date?, timezone: String, language: TranscriptionLanguage, participants: [String]) {
        (meeting.title, meeting.startedAt, meeting.timezone, meeting.transcriptionLanguage, meeting.participants)
    }

    private var edited: (title: String, startedAt: Date?, timezone: String, language: TranscriptionLanguage, participants: [String]) {
        (title, hasDate ? date : nil, timezone, language, participants)
    }

    private var patch: MeetingPatchRequest {
        MetadataRules.patch(baseRevision: meeting.metadataRevision, original: original, edited: edited)
    }

    private var languageEditable: Bool {
        MetadataRules.languageEditable(localPhase: meeting.localPhase, pipeline: meeting.pipeline, transcribeJob: data.job(.transcribe), hasTranscript: data.stage.hasTranscript)
    }

    private var titleTooLong: Bool { title.trimmingCharacters(in: .whitespacesAndNewlines).count > MetadataRules.maxTitle }

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    TextField(text: $title, prompt: Text(L("detail.untitled", table: .meetingDetail))) {
                        Text(L("meta.title", table: .meetingDetail))
                    }
                } header: {
                    Text(L("meta.title", table: .meetingDetail))
                } footer: {
                    if titleTooLong {
                        Label { Text(L("meta.title.error.long", table: .meetingDetail)) } icon: { Image(systemName: "exclamationmark.circle") }
                            .foregroundStyle(Palette.danger)
                    }
                }

                Section {
                    Toggle(isOn: $hasDate) { Text(L("meta.date", table: .meetingDetail)) }
                    if hasDate {
                        DatePicker(selection: $date) { Text(L("meta.date", table: .meetingDetail)) }
                            .environment(\.timeZone, TimeZone(identifier: timezone) ?? .current)
                    }
                    Picker(selection: $timezone) {
                        ForEach(timezoneOptions, id: \.self) { id in
                            Text(verbatim: id).tag(id)
                        }
                    } label: {
                        Text(L("meta.timezone", table: .meetingDetail))
                    }
                    .pickerStyle(.navigationLink)
                }

                Section {
                    ForEach(participants.indices, id: \.self) { index in
                        TextField(text: $participants[index]) {
                            Text(L("detail.participants", table: .meetingDetail))
                        }
                        .focused($focusedParticipant, equals: index)
                    }
                    .onDelete { participants.remove(atOffsets: $0) }
                    Button {
                        participants.append("")
                        focusedParticipant = participants.count - 1
                    } label: {
                        Label { Text(L("detail.participants.add", table: .meetingDetail)) } icon: { Image(systemName: "plus") }
                    }
                } header: {
                    Text(L("detail.participants", table: .meetingDetail))
                }

                Section {
                    Picker(selection: $language) {
                        ForEach(TranscriptionLanguage.allCases, id: \.self) { lang in
                            Text(L("meta.language.\(lang.rawValue)", table: .meetingDetail)).tag(lang)
                        }
                    } label: {
                        Text(L("meta.language", table: .meetingDetail))
                    }
                    .disabled(!languageEditable)
                } footer: {
                    if !languageEditable { Text(L("meta.language.locked", table: .meetingDetail)) }
                }
            }
            .navigationTitle(Text(L("detail.menu.edit", table: .meetingDetail)))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button(L("common.cancel")) {
                        if patch.isEmpty { dismiss() } else { confirmDiscard = true }
                    }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button(L("common.save"), action: save).disabled(titleTooLong)
                }
            }
            .confirmationDialog(Text(L("meta.discard.title", table: .meetingDetail)), isPresented: $confirmDiscard, titleVisibility: .visible) {
                Button(L("meta.discard.confirm", table: .meetingDetail), role: .destructive) { dismiss() }
            }
        }
        .interactiveDismissDisabled(!patch.isEmpty)
        .onAppear(perform: load)
    }

    private var timezoneOptions: [String] {
        var ids = TimeZone.knownTimeZoneIdentifiers
        if !ids.contains(timezone) { ids.insert(timezone, at: 0) }
        return ids
    }

    private func load() {
        guard !loaded else { return }
        loaded = true
        title = meeting.title
        hasDate = meeting.startedAt != nil
        date = meeting.startedAt ?? .now
        timezone = meeting.timezone
        language = meeting.transcriptionLanguage
        participants = meeting.participants
    }

    private func save() {
        let request = patch
        if !request.isEmpty { app.sync.enqueue(.patchMeeting(meeting.id, request)) }
        dismiss()
    }
}

// MARK: - Metadata conflict

struct MetadataConflictSheet: View {
    @Environment(AppModel.self) private var app
    let meeting: Meeting
    @State private var confirmUseLatest = false
    @State private var working = false

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: Spacing.lg) {
                Label { Text(L("meta.conflict.title", table: .meetingDetail)) } icon: { Image(systemName: "arrow.triangle.branch") }
                    .font(Typography.title)
                    .foregroundStyle(Palette.textPrimary)
                    .accessibilityAddTraits(.isHeader)
                Text(L("meta.conflict.body", table: .meetingDetail))
                    .font(Typography.body)
                    .foregroundStyle(Palette.textSecondary)
                VStack(alignment: .leading, spacing: Spacing.sm) {
                    Text(L("meta.conflict.latest", table: .meetingDetail))
                        .font(Typography.headline)
                    LabeledContent { Text(verbatim: meeting.title) } label: { Text(L("meta.title", table: .meetingDetail)) }
                    LabeledContent {
                        Text(verbatim: meeting.startedAt?.formatted(date: .abbreviated, time: .shortened) ?? LS("detail.date.none", table: .meetingDetail))
                    } label: { Text(L("meta.date", table: .meetingDetail)) }
                    LabeledContent { Text(verbatim: meeting.timezone) } label: { Text(L("meta.timezone", table: .meetingDetail)) }
                    LabeledContent {
                        Text(verbatim: meeting.participants.isEmpty ? LS("detail.participants.none", table: .meetingDetail) : meeting.participants.joined(separator: ", "))
                    } label: { Text(L("detail.participants", table: .meetingDetail)) }
                }
                .font(Typography.callout)
                .padding(Spacing.md)
                .background(Palette.surface2, in: RoundedRectangle(cornerRadius: Radius.md, style: .continuous))
                PrimaryButton(L("meta.conflict.keepMine", table: .meetingDetail), isLoading: working) { resolve(.keepMine) }
                Button(role: .destructive) { confirmUseLatest = true } label: {
                    Text(L("meta.conflict.useLatest", table: .meetingDetail)).frame(maxWidth: .infinity, minHeight: Spacing.minTouchTarget)
                }
                .buttonStyle(.bordered)
                .disabled(working)
            }
            .padding(Spacing.xl)
        }
        .interactiveDismissDisabled(true)
        .presentationDetents([.medium, .large])
        .confirmationDialog(Text(L("meta.conflict.useLatest", table: .meetingDetail)), isPresented: $confirmUseLatest, titleVisibility: .visible) {
            Button(L("meta.discard.confirm", table: .meetingDetail), role: .destructive) { resolve(.useLatest) }
        }
    }

    private func resolve(_ resolution: ConflictResolution) {
        working = true
        Task {
            await app.sync.resolveMetadataConflict(meetingID: meeting.id, resolution)
            working = false
        }
    }
}

// MARK: - Folder & tags

struct FolderPickerSheet: View {
    @Environment(AppModel.self) private var app
    @Environment(\.dismiss) private var dismiss
    @Query(sort: \Folder.name) private var folders: [Folder]
    let meeting: Meeting
    @State private var newName = ""

    var body: some View {
        NavigationStack {
            List {
                Button { move(to: nil) } label: { row(LS("library.filter.noFolder", table: .meetingDetail), selected: meeting.folder == nil) }
                ForEach(folders, id: \.id) { folder in
                    Button { move(to: folder) } label: { row(folder.name, selected: meeting.folder?.id == folder.id) }
                }
                Section {
                    HStack {
                        TextField(text: $newName) { Text(L("folder.name", table: .meetingDetail)) }
                            .submitLabel(.done)
                            .onSubmit(create)
                        Button(L("folder.new", table: .meetingDetail), action: create)
                            .disabled(trimmed.isEmpty || trimmed.count > 60)
                    }
                } footer: {
                    if folders.contains(where: { $0.name.caseInsensitiveCompare(trimmed) == .orderedSame }) {
                        Text(L("folder.name.error.duplicate", table: .meetingDetail)).foregroundStyle(Palette.danger)
                    }
                }
            }
            .navigationTitle(Text(L("meeting.move", table: .meetingDetail)))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button(L("common.cancel")) { dismiss() } }
            }
        }
    }

    private var trimmed: String { newName.trimmingCharacters(in: .whitespacesAndNewlines) }

    private func row(_ name: String, selected: Bool) -> some View {
        HStack {
            Label { Text(verbatim: name) } icon: { Image(systemName: "folder") }
                .foregroundStyle(Palette.textPrimary)
            Spacer()
            if selected { Image(systemName: "checkmark").foregroundStyle(Palette.accent) }
        }
        .accessibilityAddTraits(selected ? .isSelected : [])
    }

    private func create() {
        guard !trimmed.isEmpty, trimmed.count <= 60 else { return }
        if let existing = folders.first(where: { $0.name.caseInsensitiveCompare(trimmed) == .orderedSame }) {
            move(to: existing)
            return
        }
        let id = UUID()
        app.sync.enqueue(.createFolder(id: id, name: trimmed))
        app.sync.enqueue(.patchMeeting(meeting.id, MeetingPatchRequest(folderId: .set(id))))
        app.toasts.show(L("folder.moved", table: .meetingDetail, trimmed))
        dismiss()
    }

    private func move(to folder: Folder?) {
        if folder?.id != meeting.folder?.id {
            app.sync.enqueue(.patchMeeting(meeting.id, MeetingPatchRequest(folderId: folder.map { .set($0.id) } ?? .clear)))
            app.toasts.show(L("folder.moved", table: .meetingDetail, folder?.name ?? LS("library.filter.noFolder", table: .meetingDetail)))
        }
        dismiss()
    }
}

struct TagPickerSheet: View {
    @Environment(AppModel.self) private var app
    @Environment(\.dismiss) private var dismiss
    @Query(sort: \Tag.name) private var tags: [Tag]
    let meeting: Meeting
    @State private var selected: Set<UUID> = []
    @State private var created: [(id: UUID, name: String)] = []
    @State private var query = ""
    @State private var loaded = false

    var body: some View {
        NavigationStack {
            List {
                if canCreate {
                    Button {
                        let id = UUID()
                        created.append((id, trimmed))
                        selected.insert(id)
                        query = ""
                    } label: {
                        Label { Text(L("tag.create", table: .meetingDetail, trimmed)) } icon: { Image(systemName: "plus") }
                    }
                }
                ForEach(filtered, id: \.id) { item in
                    Button {
                        if selected.contains(item.id) { selected.remove(item.id) } else if selected.count < 20 { selected.insert(item.id) }
                    } label: {
                        HStack {
                            Text(verbatim: "#\(item.name)").foregroundStyle(Palette.textPrimary)
                            Spacer()
                            if selected.contains(item.id) { Image(systemName: "checkmark").foregroundStyle(Palette.accent) }
                        }
                    }
                    .accessibilityAddTraits(selected.contains(item.id) ? .isSelected : [])
                }
            }
            .searchable(text: $query, prompt: Text(L("tag.search", table: .meetingDetail)))
            .navigationTitle(Text(L("meeting.tags", table: .meetingDetail)))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button(L("common.cancel")) { dismiss() } }
                ToolbarItem(placement: .confirmationAction) { Button(L("common.done"), action: save) }
            }
        }
        .onAppear {
            guard !loaded else { return }
            loaded = true
            selected = Set(meeting.tags.map(\.id))
        }
    }

    private var trimmed: String { query.trimmingCharacters(in: .whitespacesAndNewlines) }

    // save() inserts created tags into the store before dismiss, so @Query can already contain them.
    private var all: [(id: UUID, name: String)] {
        let stored = Set(tags.map(\.id))
        return tags.map { ($0.id, $0.name) } + created.filter { !stored.contains($0.id) }
    }

    private var filtered: [(id: UUID, name: String)] {
        trimmed.isEmpty ? all : all.filter { $0.name.localizedStandardContains(trimmed) }
    }

    private var canCreate: Bool {
        !trimmed.isEmpty && trimmed.count <= 60 && !all.contains { $0.name.caseInsensitiveCompare(trimmed) == .orderedSame }
    }

    private func save() {
        for tag in created where selected.contains(tag.id) {
            app.sync.enqueue(.createTag(id: tag.id, name: tag.name))
        }
        if selected != Set(meeting.tags.map(\.id)) {
            let ids = all.map(\.id).filter { selected.contains($0) }
            app.sync.enqueue(.patchMeeting(meeting.id, MeetingPatchRequest(tagIds: .set(ids))))
        }
        dismiss()
    }
}
