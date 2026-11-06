import SwiftData
import SwiftUI
import UniformTypeIdentifiers

struct LibraryView: View {
    @Environment(AppModel.self) private var app
    @State private var model = LibraryModel()
    @State private var importModel = ImportModel()
    @Query(sort: \Folder.name) private var folders: [Folder]
    @Query(sort: \Tag.name) private var tags: [Tag]
    @Query private var allMeetings: [Meeting]

    var body: some View {
        @Bindable var model = model
        @Bindable var importModel = importModel
        LibraryMeetingList(query: model.query, model: model, importModel: importModel, folders: folders, tags: tags, allMeetings: allMeetings)
            .navigationTitle(Text(L("library.title", table: .library)))
            .searchable(text: $model.searchText, placement: .navigationBarDrawer(displayMode: .always), prompt: Text(L("library.search.prompt", table: .library)))
            .toolbar { toolbar }
            .safeAreaInset(edge: .bottom) { recordBar }
            .refreshable { await model.refresh(app: app, meetings: allMeetings) }
            .fileImporter(isPresented: $model.showImporter, allowedContentTypes: [.mpeg4Audio, .mp3, .wav]) { result in
                guard case .success(let url) = result else { return }
                Task { await importModel.begin(url, app: app) }
            }
            .sheet(item: $importModel.candidate) { candidate in
                ImportConfirmSheet(candidate: candidate) { language in
                    Task { await importModel.confirm(language: language, app: app) }
                } onCancel: {
                    importModel.cancel()
                }
            }
            .sheet(item: $model.sheet) { sheet in
                switch sheet {
                case .manageFolders: FolderSheet(mode: .manage, model: model)
                case .moveMeeting(let id): FolderSheet(mode: .move(id), model: model)
                case .manageTags: TagSheet(mode: .manage, model: model)
                case .tagMeeting(let id): TagSheet(mode: .assign(id), model: model)
                }
            }
            .confirmationDialog(
                Text(L("meeting.delete.title", table: .library)),
                isPresented: Binding { model.pendingDelete != nil } set: { if !$0 { model.pendingDelete = nil } },
                titleVisibility: .visible
            ) {
                Button(role: .destructive) {
                    if let id = model.pendingDelete { model.delete(id, app: app) }
                } label: { Text(L("meeting.delete.confirm", table: .library)) }
                Button(role: .cancel) {} label: { Text(L("common.cancel")) }
            } message: {
                Text(L("meeting.delete.body", table: .library))
            }
            .alert(Text(L("meeting.rename", table: .library)), isPresented: Binding { model.renameTarget != nil } set: { if !$0 { model.renameTarget = nil } }) {
                TextField(text: $model.renameText) { Text(L("meeting.rename", table: .library)) }
                Button {
                    if let id = model.renameTarget, let meeting = allMeetings.first(where: { $0.id == id }) {
                        model.rename(meeting, to: model.renameText, app: app)
                    }
                } label: { Text(L("common.save")) }
                Button(role: .cancel) {} label: { Text(L("common.cancel")) }
            }
            .task {
                await ProfilePreferences.pushPending(app: app)
            }
            .onChange(of: app.isOnline) { _, online in
                if online {
                    Task { await ProfilePreferences.pushPending(app: app) }
                } else {
                    model.offlineBannerDismissed = false
                }
            }
    }

    // MARK: Toolbar

    @ToolbarContentBuilder private var toolbar: some ToolbarContent {
        ToolbarItem(placement: .topBarLeading) {
            Button { app.path.append(.settings) } label: {
                Image(systemName: "gearshape")
            }
            .minTouchTarget()
            .accessibilityLabel(Text(L("library.settings", table: .library)))
        }
        ToolbarItemGroup(placement: .topBarTrailing) {
            filterMenu
            Menu {
                Button { app.presentRecording() } label: {
                    Label { Text(L("library.new", table: .library)) } icon: { Image(systemName: "record.circle") }
                }
                Button { model.showImporter = true } label: {
                    Label { Text(L("library.import", table: .library)) } icon: { Image(systemName: "square.and.arrow.down") }
                }
                .disabled(importModel.isBusy)
            } label: {
                Image(systemName: "plus")
            }
            .minTouchTarget()
            .accessibilityLabel(Text(L("library.add", table: .library)))
        }
    }

    private var filterMenu: some View {
        @Bindable var model = model
        return Menu {
            Picker(selection: $model.folderFilter) {
                Text(L("library.filter.all", table: .library)).tag(FolderFilter.all)
                ForEach(folders) { folder in
                    Text(verbatim: folder.name).tag(FolderFilter.folder(folder.id))
                }
                Text(L("library.filter.noFolder", table: .library)).tag(FolderFilter.unfiled)
            } label: {
                Label { Text(L("folder.title", table: .library)) } icon: { Image(systemName: "folder") }
            }
            .pickerStyle(.menu)
            Picker(selection: $model.tagFilter) {
                Text(L("library.filter.allTags", table: .library)).tag(UUID?.none)
                ForEach(tags) { tag in
                    Text(verbatim: "#\(tag.name)").tag(UUID?.some(tag.id))
                }
            } label: {
                Label { Text(L("library.filter.tags", table: .library)) } icon: { Image(systemName: "tag") }
            }
            .pickerStyle(.menu)
            Divider()
            Button { model.sheet = .manageFolders } label: {
                Label { Text(L("library.filter.manageFolders", table: .library)) } icon: { Image(systemName: "folder.badge.gearshape") }
            }
            Button { model.sheet = .manageTags } label: {
                Label { Text(L("library.filter.manageTags", table: .library)) } icon: { Image(systemName: "tag") }
            }
            if model.isFiltering {
                Button(role: .destructive) { model.folderFilter = .all; model.tagFilter = nil } label: {
                    Label { Text(L("library.filter.clear", table: .library)) } icon: { Image(systemName: "xmark.circle") }
                }
            }
        } label: {
            Image(systemName: model.isFiltering ? "line.3.horizontal.decrease.circle.fill" : "line.3.horizontal.decrease.circle")
        }
        .minTouchTarget()
        .accessibilityLabel(Text(L("library.filter", table: .library)))
        .accessibilityValue(Text(verbatim: activeFilterDescription))
    }

    private var activeFilterNames: [String] {
        var names: [String] = []
        switch model.folderFilter {
        case .all: break
        case .unfiled: names.append(LS("library.filter.noFolder", table: .library))
        case .folder(let id): if let folder = folders.first(where: { $0.id == id }) { names.append(folder.name) }
        }
        if let id = model.tagFilter, let tag = tags.first(where: { $0.id == id }) { names.append("#\(tag.name)") }
        return names
    }

    private var activeFilterDescription: String {
        activeFilterNames.isEmpty ? "" : LS("library.filter.active", table: .library, activeFilterNames.joined(separator: ", "))
    }

    private var recordBar: some View {
        PrimaryButton(L("library.record", table: .library), systemImage: "record.circle") { app.presentRecording() }
            .accessibilityHint(Text(L("library.record.hint", table: .library)))
            .padding(.horizontal, Spacing.screenInsetCompact)
            .padding(.vertical, Spacing.sm)
            .frame(maxWidth: Spacing.readingMaxWidth)
            .frame(maxWidth: .infinity)
            .background(Palette.bg.opacity(0.94))
            .zIndex(ZLayer.bottomBar)
    }
}

// MARK: - List

private struct LibraryMeetingList: View {
    let model: LibraryModel
    let importModel: ImportModel
    let folders: [Folder]
    let tags: [Tag]
    let allMeetings: [Meeting]
    @Environment(AppModel.self) private var app
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    @Query private var matches: [Meeting]

    init(query: String, model: LibraryModel, importModel: ImportModel, folders: [Folder], tags: [Tag], allMeetings: [Meeting]) {
        self.model = model
        self.importModel = importModel
        self.folders = folders
        self.tags = tags
        self.allMeetings = allMeetings
        let q = query.lowercased()
        let filter: Predicate<Meeting>? = q.isEmpty ? nil : #Predicate<Meeting> { $0.searchText.localizedStandardContains(q) }
        _matches = Query(filter: filter, sort: \Meeting.createdAt, order: .reverse)
    }

    private var rows: [MeetingRowData] {
        matches
            .filter { model.matchesFilters(folderID: $0.folder?.id, tagIDs: $0.tags.map(\.id)) }
            .sorted { $0.sortDate > $1.sortDate }
            .map { MeetingRowData(meeting: $0, app: app) }
    }

    private var contentRows: [MeetingRowData] {
        let local = Set(matches.map(\.id))
        let byID = Dictionary(allMeetings.map { ($0.id, $0) }, uniquingKeysWith: { a, _ in a })
        return model.contentResults
            .filter { !local.contains($0.id) && model.matchesFilters(folderID: $0.folderId, tagIDs: $0.tagIds) }
            .map { item in
                byID[item.id].map { MeetingRowData(meeting: $0, app: app) }
                    ?? MeetingRowData(item: item, folders: folders, tags: tags, isOnline: app.isOnline)
            }
    }

    private var sections: [(day: Date, rows: [MeetingRowData])] {
        let calendar = Calendar.current
        let grouped = Dictionary(grouping: rows) { calendar.startOfDay(for: $0.date) }
        return grouped.keys.sorted(by: >).map { ($0, grouped[$0] ?? []) }
    }

    private var anyActive: Bool {
        allMeetings.contains { MeetingStatus.isActive(MeetingStatusInput(localPhase: $0.localPhase, activeJobs: $0.activeJobs)) }
    }

    private var isFirstLoad: Bool { allMeetings.isEmpty && app.sync.isPulling }

    var body: some View {
        let rows = self.rows
        let contentRows = self.contentRows
        List {
            LibraryBanners(model: model, importModel: importModel, allMeetings: allMeetings)
            if !dynamicTypeSize.isAccessibilitySize, !folders.isEmpty {
                folderChips
            }
            if dynamicTypeSize.isAccessibilitySize, !folders.isEmpty {
                folderMenuRow
            }
            if model.isFiltering { activeFilterRow }
            if app.sync.lastPullFailed {
                Section {
                    Label { Text(L("library.refresh.error", table: .library)) } icon: { Image(systemName: "exclamationmark.triangle") }
                        .font(Typography.footnote)
                        .foregroundStyle(Palette.textSecondary)
                }
                .listRowBackground(Color.clear)
            }
            importStatusRow
            if isFirstLoad {
                Section {
                    ForEach(0..<6, id: \.self) { _ in MeetingRow(data: Self.placeholderRow) }
                }
                .redacted(reason: .placeholder)
                .accessibilityHidden(true)
            } else if allMeetings.isEmpty {
                emptyLibrary
            } else if rows.isEmpty && contentRows.isEmpty && model.isSearchingOrFiltering {
                emptySearch
            } else {
                ForEach(sections, id: \.day) { section in
                    Section {
                        ForEach(section.rows) { row in meetingRow(row) }
                    } header: {
                        Text(Self.header(for: section.day))
                    }
                }
                if !contentRows.isEmpty {
                    Section {
                        ForEach(contentRows) { row in
                            Button {
                                Task { await model.openRemote(row.id, app: app) }
                            } label: { MeetingRow(data: row) }
                            .buttonStyle(.plain)
                        }
                    } header: {
                        Text(L("library.search.contentSection", table: .library))
                    }
                }
            }
            if !model.query.isEmpty && !app.isOnline {
                Section {} footer: {
                    Text(L("library.search.offlineNote", table: .library))
                }
            }
        }
        .listStyle(.insetGrouped)
        .scrollContentBackground(.hidden)
        .background(Palette.bg)
        .animation(Motion.standard, value: rows.map(\.id))
        .task(id: model.query) { await model.runServerSearch(app: app) }
        .onChange(of: anyActive, initial: true) { _, active in
            if active { app.poller.start(.library) } else { app.poller.stop(.library) }
        }
        .onDisappear { app.poller.stop(.library) }
        .onChange(of: lastOpenedStatus) { old, new in
            guard old != nil, let new, old != new else { return }
            var text = AttributedString(String(localized: new.text))
            text.accessibilitySpeechAnnouncementPriority = .low
            AccessibilityNotification.Announcement(text).post()
        }
    }

    private var lastOpenedStatus: ChipStatus? {
        guard let id = model.lastOpenedID, let meeting = allMeetings.first(where: { $0.id == id }) else { return nil }
        return MeetingRowData(meeting: meeting, app: app).status
    }

    private func meetingRow(_ row: MeetingRowData) -> some View {
        Button { model.open(row.id, app: app) } label: { MeetingRow(data: row) }
            .buttonStyle(.plain)
            .swipeActions(edge: .leading) {
                Button { model.sheet = .moveMeeting(row.id) } label: {
                    Label { Text(L("meeting.move", table: .library)) } icon: { Image(systemName: "folder.badge.plus") }
                }
                .tint(Palette.accent)
            }
            .swipeActions(edge: .trailing) {
                Button(role: .destructive) { model.pendingDelete = row.id } label: {
                    Label { Text(L("meeting.delete", table: .library)) } icon: { Image(systemName: "trash") }
                }
            }
            .contextMenu {
                Button { startRename(row) } label: {
                    Label { Text(L("meeting.rename", table: .library)) } icon: { Image(systemName: "pencil") }
                }
                Button { model.sheet = .moveMeeting(row.id) } label: {
                    Label { Text(L("meeting.move", table: .library)) } icon: { Image(systemName: "folder.badge.plus") }
                }
                Button { model.sheet = .tagMeeting(row.id) } label: {
                    Label { Text(L("meeting.tags", table: .library)) } icon: { Image(systemName: "tag") }
                }
                Button(role: .destructive) { model.pendingDelete = row.id } label: {
                    Label { Text(L("meeting.delete", table: .library)) } icon: { Image(systemName: "trash") }
                }
            }
            .accessibilityActions {
                Button { startRename(row) } label: { Text(L("meeting.rename", table: .library)) }
                Button { model.sheet = .moveMeeting(row.id) } label: { Text(L("meeting.move", table: .library)) }
                Button { model.sheet = .tagMeeting(row.id) } label: { Text(L("meeting.tags", table: .library)) }
                Button { model.pendingDelete = row.id } label: { Text(L("meeting.delete", table: .library)) }
            }
    }

    private func startRename(_ row: MeetingRowData) {
        model.renameText = row.title
        model.renameTarget = row.id
    }

    // MARK: Filters

    private var folderChips: some View {
        Section {
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: Spacing.sm) {
                    folderChip(L("library.filter.all", table: .library), systemImage: nil, filter: .all)
                    ForEach(folders) { folder in
                        folderChip(resourceVerbatim(folder.name), systemImage: "folder", filter: .folder(folder.id))
                    }
                }
                .padding(.horizontal, Spacing.screenInsetCompact)
            }
            .listRowInsets(EdgeInsets())
            .listRowBackground(Color.clear)
        }
    }

    private func folderChip(_ title: LocalizedStringResource, systemImage: String?, filter: FolderFilter) -> some View {
        let selected = model.folderFilter == filter
        return Button {
            model.folderFilter = filter
        } label: {
            HStack(spacing: Spacing.xs) {
                if let systemImage { Image(systemName: selected ? "\(systemImage).fill" : systemImage).imageScale(.small) }
                Text(title).fontWeight(selected ? .semibold : .regular)
            }
            .font(Typography.subheadline)
            .foregroundStyle(selected ? Palette.accent : Palette.textPrimary)
            .padding(.horizontal, Spacing.md)
            .frame(minHeight: Spacing.minTouchTarget)
            .background(selected ? Palette.accentSoft : Palette.surface, in: Capsule())
            .overlay(Capsule().stroke(selected ? Palette.accent : Palette.separator, lineWidth: 1))
        }
        .buttonStyle(PressScaleButtonStyle())
        .accessibilityAddTraits(selected ? .isSelected : [])
    }

    private var folderMenuRow: some View {
        @Bindable var model = model
        return Section {
            Picker(selection: $model.folderFilter) {
                Text(L("library.filter.all", table: .library)).tag(FolderFilter.all)
                ForEach(folders) { Text(verbatim: $0.name).tag(FolderFilter.folder($0.id)) }
                Text(L("library.filter.noFolder", table: .library)).tag(FolderFilter.unfiled)
            } label: {
                Label { Text(L("folder.title", table: .library)) } icon: { Image(systemName: "folder") }
            }
            .pickerStyle(.menu)
        }
    }

    private var activeFilterRow: some View {
        Section {
            ChipFlowLayout(spacing: Spacing.sm) {
                switch model.folderFilter {
                case .all: EmptyView()
                case .unfiled: removableChip(L("library.filter.noFolder", table: .library), systemImage: "tray.fill") { model.folderFilter = .all }
                case .folder(let id):
                    if let folder = folders.first(where: { $0.id == id }) {
                        removableChip(resourceVerbatim(folder.name), systemImage: "folder.fill") { model.folderFilter = .all }
                    }
                }
                if let id = model.tagFilter, let tag = tags.first(where: { $0.id == id }) {
                    removableChip(resourceVerbatim("#\(tag.name)"), systemImage: "tag.fill") { model.tagFilter = nil }
                }
            }
            .listRowBackground(Color.clear)
            .listRowInsets(EdgeInsets(top: 0, leading: Spacing.screenInsetCompact, bottom: 0, trailing: Spacing.screenInsetCompact))
        }
    }

    private func removableChip(_ title: LocalizedStringResource, systemImage: String, remove: @escaping () -> Void) -> some View {
        Button(action: remove) {
            HStack(spacing: Spacing.xs) {
                Image(systemName: systemImage).imageScale(.small)
                Text(title)
                Image(systemName: "xmark").imageScale(.small)
            }
            .font(Typography.subheadline)
            .foregroundStyle(Palette.accent)
            .padding(.horizontal, Spacing.md)
            .frame(minHeight: Spacing.minTouchTarget)
            .background(Palette.accentSoft, in: Capsule())
        }
        .buttonStyle(PressScaleButtonStyle())
        .accessibilityLabel(Text(title))
        .accessibilityHint(Text(L("library.filter.remove.hint", table: .library)))
    }

    // MARK: Import progress

    @ViewBuilder private var importStatusRow: some View {
        switch importModel.phase {
        case .validating:
            Section {
                HStack(spacing: Spacing.md) {
                    ProgressView()
                    Text(L("import.validating", table: .library))
                        .font(Typography.callout)
                        .foregroundStyle(Palette.textPrimary)
                }
                .frame(minHeight: Spacing.minTouchTarget)
                .accessibilityElement(children: .combine)
                .onAppear { AccessibilityNotification.Announcement(String(localized: L("import.validating", table: .library))).post() }
            }
        case .copying(let fileName, let progress):
            Section {
                VStack(alignment: .leading, spacing: Spacing.sm) {
                    Text(verbatim: (fileName as NSString).deletingPathExtension)
                        .font(Typography.headline)
                        .foregroundStyle(Palette.textPrimary)
                        .lineLimit(2)
                    StatusChip(.importing)
                    ProgressView(value: progress) {
                        Text(L("import.copying", table: .library, Int64((progress * 100).rounded(.down))))
                            .font(Typography.footnote)
                            .foregroundStyle(Palette.textSecondary)
                    }
                    .tint(Palette.accent)
                }
                .padding(.vertical, Spacing.xs)
                .accessibilityElement(children: .combine)
            }
        default:
            EmptyView()
        }
    }

    // MARK: Empty states

    private var emptyLibrary: some View {
        Section {
            ContentUnavailableView {
                Label { Text(L("library.empty.title", table: .library)) } icon: { Image(systemName: "waveform.badge.plus") }
            } description: {
                Text(L("library.empty.body", table: .library))
            } actions: {
                PrimaryButton(L("library.record", table: .library), systemImage: "record.circle") { app.presentRecording() }
                    .frame(maxWidth: 280)
                Button { model.showImporter = true } label: {
                    Label { Text(L("library.import", table: .library)) } icon: { Image(systemName: "square.and.arrow.down") }
                }
                .buttonStyle(.borderless)
                .tint(Palette.accent)
                .minTouchTarget()
            }
            .listRowBackground(Color.clear)
        }
    }

    private var emptySearch: some View {
        Section {
            ContentUnavailableView {
                Label { Text(L("library.search.noResults", table: .library)) } icon: { Image(systemName: "magnifyingglass") }
            } description: {
                if model.query.isEmpty {
                    Text(L("library.filter.empty", table: .library))
                } else {
                    Text(L("library.search.empty", table: .library, model.query))
                }
            } actions: {
                Button { model.clearFilters() } label: { Text(L("library.filter.clear", table: .library)) }
                    .buttonStyle(.bordered)
                    .tint(Palette.accent)
                    .minTouchTarget()
            }
            .listRowBackground(Color.clear)
        }
    }

    // MARK: Helpers

    static func header(for day: Date) -> LocalizedStringResource {
        let calendar = Calendar.current
        if calendar.isDateInToday(day) { return L("library.section.today", table: .library) }
        if calendar.isDateInYesterday(day) { return L("library.section.yesterday", table: .library) }
        let sameYear = calendar.isDate(day, equalTo: .now, toGranularity: .year)
        let style: Date.FormatStyle = sameYear
            ? .dateTime.weekday(.wide).day().month(.wide)
            : .dateTime.day().month(.wide).year()
        return resourceVerbatim(day.formatted(style))
    }

    static let placeholderRow = MeetingRowData(
        id: UUID(), title: "Weekly sync produk", date: .now, durationMs: 2_530_000, source: .recording,
        status: .notesReady, folderName: "Internal", tagNames: []
    )
}

// MARK: - Banners

private struct LibraryBanners: View {
    let model: LibraryModel
    let importModel: ImportModel
    let allMeetings: [Meeting]
    @Environment(AppModel.self) private var app

    private var orphans: [Meeting] { allMeetings.filter { $0.localPhase == .orphaned } }
    private var waitingStorage: Bool { allMeetings.contains { $0.localPhase == .waitingStorage } }

    private var planEnded: Bool {
        allMeetings.contains { $0.localPhase == .waitingEntitlement } || (app.usage.map { $0.entitlement.status != .active } ?? false)
    }

    private var quotaExhausted: Bool {
        allMeetings.contains { $0.localPhase == .waitingQuota }
            || (app.usage.map { $0.entitlement.status == .active && $0.remainingSeconds <= 0 } ?? false)
    }

    var body: some View {
        @Bindable var model = model
        let banners = Group {
            if let recording = app.pendingRecovery { recoveryBanner(recording) }
            if case .failed(let failure) = importModel.phase {
                let limits = ImportModel.limits(app: app)
                Banner(
                    tone: .danger, systemImage: "exclamationmark.triangle",
                    message: failure.localizedMessage(maxUploadBytes: limits.maxUploadBytes, maxDurationMs: limits.maxDurationMs),
                    actions: [BannerAction(L("import.error.pickAnother", table: .library)) {
                        importModel.dismissFailure()
                        model.showImporter = true
                    }],
                    onClose: { importModel.dismissFailure() }
                )
            }
            if !app.isOnline && !model.offlineBannerDismissed {
                Banner(tone: .warning, systemImage: "wifi.slash", message: L("library.offline", table: .library), onClose: {
                    model.offlineBannerDismissed = true
                })
            }
            if planEnded {
                Banner(tone: .warning, systemImage: "calendar.badge.exclamationmark", message: L("library.plan.expired", table: .library), actions: [
                    BannerAction(L("library.quota.cta", table: .library)) { app.path.append(.settings) },
                ])
            } else if quotaExhausted {
                Banner(tone: .warning, systemImage: "gauge.with.dots.needle.100percent", message: L("library.quota.exhausted", table: .library), actions: [
                    BannerAction(L("library.quota.cta", table: .library)) { app.path.append(.settings) },
                ])
            }
            if waitingStorage {
                Banner(tone: .warning, systemImage: "externaldrive.badge.exclamationmark", message: L("library.storage.merge", table: .library))
            }
            ForEach(orphans) { meeting in orphanBanner(meeting) }
        }
        Section {
            banners
                .listRowInsets(EdgeInsets(top: Spacing.xs, leading: 0, bottom: Spacing.xs, trailing: 0))
                .listRowBackground(Color.clear)
                .listRowSeparator(.hidden)
        }
        .confirmationDialog(
            Text(L("library.recovery.discard.confirm", table: .library)),
            isPresented: $model.discardRecoveryConfirm,
            titleVisibility: .visible
        ) {
            Button(role: .destructive) {
                if let recording = app.pendingRecovery { model.discardRecovery(recording, app: app) }
            } label: { Text(L("library.recovery.discard", table: .library)) }
            Button(role: .cancel) {} label: { Text(L("common.cancel")) }
        }
        .confirmationDialog(
            Text(L("library.orphan.discard.confirm", table: .library)),
            isPresented: Binding { model.orphanDiscardTarget != nil } set: { if !$0 { model.orphanDiscardTarget = nil } },
            titleVisibility: .visible
        ) {
            Button(role: .destructive) {
                if let id = model.orphanDiscardTarget { app.sync.discardOrphan(id) }
            } label: { Text(L("library.orphan.discard", table: .library)) }
            Button(role: .cancel) {} label: { Text(L("common.cancel")) }
        }
    }

    private func title(of meetingID: UUID) -> String {
        let meeting = allMeetings.first { $0.id == meetingID }
        let title = meeting?.title.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        guard title.isEmpty else { return title }
        let date = meeting?.sortDate ?? .now
        return LS("meeting.untitled", table: .library, date.formatted(.dateTime.day().month(.abbreviated).hour().minute()))
    }

    private func recoveryBanner(_ recording: LocalRecording) -> some View {
        let saved = Formatting.spokenDuration(ms: LibraryModel.recoverableMs(recording), includeSeconds: true)
        return Banner(
            tone: .warning,
            systemImage: "arrow.counterclockwise.circle",
            title: L("library.recovery.title", table: .library),
            message: L("library.recovery.body", table: .library, title(of: recording.meetingID), saved),
            actions: [
                BannerAction(L("library.recovery.restore", table: .library), isLoading: model.isRecovering) {
                    Task { await model.recover(recording, app: app) }
                },
                BannerAction(L("library.recovery.discard", table: .library)) {
                    model.discardRecoveryConfirm = true
                },
            ]
        )
    }

    private func orphanBanner(_ meeting: Meeting) -> some View {
        Banner(
            tone: .warning,
            systemImage: "iphone.slash",
            title: L("library.orphan.title", table: .library),
            message: L("library.orphan.body", table: .library, title(of: meeting.id)),
            actions: [
                BannerAction(L("library.orphan.keep", table: .library)) {
                    let id = meeting.id
                    Task { _ = await app.sync.keepOrphanAsNewMeeting(id) }
                },
                BannerAction(L("library.orphan.discard", table: .library)) {
                    model.orphanDiscardTarget = meeting.id
                },
            ]
        )
    }
}
