import SwiftUI

@MainActor
enum EditorText {
    static func plain(_ text: String) -> LocalizedStringResource {
        LocalizedStringResource(String.LocalizationValue(text), table: "Verbatim")
    }

    static func originA11y(_ origin: Origin, _ edited: Bool) -> LocalizedStringResource {
        L(OriginMarker.key(origin: origin, edited: edited) + ".a11y")
    }

    static func marker(_ style: ListStyle?, index: Int) -> String? {
        switch style {
        case .bullet: "•"
        case .numbered: "\(index + 1)."
        case .paragraph, nil: nil
        }
    }
}

struct EditorFieldStyle: ViewModifier {
    func body(content: Content) -> some View {
        content
            .padding(.horizontal, Spacing.sm)
            .padding(.vertical, Spacing.sm)
            .frame(minHeight: Spacing.minTouchTarget)
            .background(Palette.surface2, in: RoundedRectangle(cornerRadius: Radius.md, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: Radius.md, style: .continuous).stroke(Palette.borderStrong, lineWidth: 1))
    }
}

struct EditorCardStyle: ViewModifier {
    func body(content: Content) -> some View {
        content
            .padding(Spacing.md)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Palette.surface, in: RoundedRectangle(cornerRadius: Radius.lg, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: Radius.lg, style: .continuous).stroke(Palette.separator, lineWidth: 1))
    }
}

extension View {
    func editorField() -> some View { modifier(EditorFieldStyle()) }
    func editorCard() -> some View { modifier(EditorCardStyle()) }
}

struct EditorFlowLayout: Layout {
    var spacing: CGFloat = Spacing.sm

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let rows = arrange(width: proposal.width ?? .infinity, subviews: subviews)
        let width = rows.map(\.width).max() ?? 0
        let height = rows.map(\.height).reduce(0, +) + spacing * CGFloat(max(rows.count - 1, 0))
        return CGSize(width: proposal.width ?? width, height: height)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        var y = bounds.minY
        for row in arrange(width: bounds.width, subviews: subviews) {
            var x = bounds.minX
            for index in row.indices {
                let size = subviews[index].sizeThatFits(.unspecified)
                subviews[index].place(at: CGPoint(x: x, y: y + (row.height - size.height) / 2), proposal: ProposedViewSize(size))
                x += size.width + spacing
            }
            y += row.height + spacing
        }
    }

    private struct Row { var indices: [Int] = []; var width: CGFloat = 0; var height: CGFloat = 0 }

    private func arrange(width: CGFloat, subviews: Subviews) -> [Row] {
        var rows: [Row] = [Row()]
        for (index, view) in subviews.enumerated() {
            let size = view.sizeThatFits(.unspecified)
            let needed = rows[rows.count - 1].indices.isEmpty ? size.width : rows[rows.count - 1].width + spacing + size.width
            if needed > width, !rows[rows.count - 1].indices.isEmpty { rows.append(Row()) }
            let row = rows.count - 1
            rows[row].width = rows[row].indices.isEmpty ? size.width : rows[row].width + spacing + size.width
            rows[row].height = max(rows[row].height, size.height)
            rows[row].indices.append(index)
        }
        return rows.filter { !$0.indices.isEmpty }
    }
}

// Origin badge + source references under an item. Origin is also in the field's VoiceOver label.
struct EditorItemMeta: View {
    let origin: Origin
    let edited: Bool
    let sources: [SourceRef]
    let model: DocumentEditorModel?
    let isPlayable: Bool

    var body: some View {
        EditorFlowLayout(spacing: Spacing.sm) {
            OriginMarker(origin: origin, edited: edited)
                .accessibilityHidden(model != nil)
            ForEach(Array(sources.enumerated()), id: \.offset) { _, ref in
                SourceReferenceButton(ref: ref, isVerified: model?.isVerified(ref) ?? true, isPlayable: isPlayable && model != nil) { _ in
                    model?.play(ref)
                }
            }
        }
    }
}

struct EditorRefActions: ViewModifier {
    let sources: [SourceRef]
    let model: DocumentEditorModel
    let isPlayable: Bool

    func body(content: Content) -> some View {
        content.accessibilityActions {
            if isPlayable {
                ForEach(Array(sources.enumerated()), id: \.offset) { _, ref in
                    if case .transcript(_, let startMs, _) = ref, model.isVerified(ref) {
                        Button { model.play(ref) } label: { Text(L("ref.playAt", Formatting.timestamp(ms: startMs))) }
                    }
                }
            }
        }
    }
}

struct EditorTextField: View {
    let target: EditTarget
    let text: String
    let prompt: LocalizedStringResource
    var font: Font = Typography.body
    var a11yLabel: LocalizedStringResource?
    let model: DocumentEditorModel
    var focus: FocusState<EditTarget?>.Binding

    var body: some View {
        TextField(text: Binding(get: { text }, set: { value in
            if let next = model.setText(target, value) { Task { focus.wrappedValue = next } }
        }), prompt: Text(prompt).foregroundStyle(Palette.textSecondary), axis: .vertical) {
            Text(a11yLabel ?? prompt)
        }
        .font(font)
        .foregroundStyle(Palette.textPrimary)
        .focused(focus, equals: target)
        .editorField()
        .accessibilityLabel(Text(a11yLabel ?? prompt))
    }
}

struct EditorItemMenu: ViewModifier {
    let section: String
    let item: String
    let model: DocumentEditorModel

    func body(content: Content) -> some View {
        content.contextMenu {
            Button { model.moveItem(section: section, item: item, by: -1) } label: {
                Label { Text(L("mom.section.moveUp", table: .editor)) } icon: { Image(systemName: "arrow.up") }
            }
            Button { model.moveItem(section: section, item: item, by: 1) } label: {
                Label { Text(L("mom.section.moveDown", table: .editor)) } icon: { Image(systemName: "arrow.down") }
            }
            Button(role: .destructive) { model.delete(.item(section, item)) } label: {
                Label { Text(L("item.delete", table: .editor)) } icon: { Image(systemName: "trash") }
            }
        }
    }
}

struct EditorSectionView: View {
    let section: MoMSection
    let isFirst: Bool
    let isLast: Bool
    let isPlayable: Bool
    let model: DocumentEditorModel
    var focus: FocusState<EditTarget?>.Binding

    var body: some View {
        VStack(alignment: .leading, spacing: Spacing.md) {
            HStack(alignment: .center, spacing: Spacing.sm) {
                TextField(text: Binding(get: { section.heading }, set: { model.setText(.heading(section.id), $0) }),
                          prompt: Text(L("mom.section.rename", table: .editor))) {
                    Text(L("mom.section.rename", table: .editor))
                }
                .font(Typography.title)
                .foregroundStyle(Palette.textPrimary)
                .focused(focus, equals: .heading(section.id))
                .accessibilityAddTraits(.isHeader)
                sectionMenu
            }
            if section.items.isEmpty {
                HStack {
                    Text(L("mom.section.empty", table: .editor))
                        .font(Typography.callout)
                        .foregroundStyle(Palette.textSecondary)
                    Spacer()
                    Button { add(after: nil) } label: { Text(L("item.add", table: .editor)).minTouchTarget() }
                        .buttonStyle(.borderless)
                        .tint(Palette.accent)
                }
            } else {
                ForEach(Array(section.items.enumerated()), id: \.element.id) { index, item in
                    row(item, index: index)
                }
                Button { add(after: nil) } label: {
                    Label { Text(addTitle) } icon: { Image(systemName: "plus") }
                        .font(Typography.callout)
                        .frame(minHeight: Spacing.minTouchTarget)
                }
                .buttonStyle(.borderless)
                .tint(Palette.accent)
            }
        }
    }

    private var addTitle: LocalizedStringResource {
        switch section.type {
        case .actionItems: L("mom.action.add", table: .editor)
        case .decisions: L("mom.decision.add", table: .editor)
        case .discussion: L("mom.topic.add", table: .editor)
        default: L("item.add", table: .editor)
        }
    }

    private func add(after id: String?) {
        if let target = model.addItem(section: section.id, after: id) { Task { focus.wrappedValue = target } }
    }

    @ViewBuilder private func row(_ item: MoMItem, index: Int) -> some View {
        switch item {
        case .text(let t):
            EditorTextItemRow(sectionID: section.id, item: t, marker: EditorText.marker(section.style, index: index),
                        isCard: section.type == .decisions, isPlayable: isPlayable, model: model, focus: focus)
        case .topic(let t):
            EditorTopicRow(sectionID: section.id, topic: t, marker: EditorText.marker(section.style, index: index), isPlayable: isPlayable, model: model, focus: focus)
        case .action(let a):
            EditorActionCard(sectionID: section.id, item: a, isPlayable: isPlayable, model: model, focus: focus)
        }
    }

    private var sectionMenu: some View {
        Menu {
            Button { focus.wrappedValue = .heading(section.id) } label: {
                Label { Text(L("mom.section.rename", table: .editor)) } icon: { Image(systemName: "pencil") }
            }
            if section.type != .actionItems {
                Picker(selection: Binding(get: { section.style ?? .bullet }, set: { model.setStyle(section: section.id, $0) })) {
                    ForEach(ListStyle.allCases, id: \.self) { style in
                        Text(L("section.style.\(style.rawValue)", table: .editor)).tag(style)
                    }
                } label: {
                    Text(L("section.style", table: .editor))
                }
                .pickerStyle(.menu)
            }
            Button { model.moveSection(section.id, by: -1) } label: {
                Label { Text(L("mom.section.moveUp", table: .editor)) } icon: { Image(systemName: "arrow.up") }
            }
            .disabled(isFirst)
            Button { model.moveSection(section.id, by: 1) } label: {
                Label { Text(L("mom.section.moveDown", table: .editor)) } icon: { Image(systemName: "arrow.down") }
            }
            .disabled(isLast)
            Divider()
            Button(role: .destructive) { model.sectionPendingDelete = section } label: {
                Label { Text(L("mom.section.delete", table: .editor)) } icon: { Image(systemName: "trash") }
            }
        } label: {
            Image(systemName: "ellipsis")
                .font(Typography.body.weight(.semibold))
                .foregroundStyle(Palette.textSecondary)
                .minTouchTarget()
        }
        .accessibilityLabel(Text(L("section.menu", table: .editor, section.heading)))
    }
}

struct EditorTextItemRow: View {
    let sectionID: String
    let item: TextItem
    let marker: String?
    let isCard: Bool
    let isPlayable: Bool
    let model: DocumentEditorModel
    var focus: FocusState<EditTarget?>.Binding

    var body: some View {
        let content = HStack(alignment: .firstTextBaseline, spacing: Spacing.sm) {
            if let marker {
                Text(EditorText.plain(marker))
                    .font(Typography.body.monospacedDigit())
                    .foregroundStyle(Palette.textSecondary)
                    .accessibilityHidden(true)
            }
            VStack(alignment: .leading, spacing: Spacing.xs) {
                EditorTextField(target: .item(sectionID, item.id), text: item.text, prompt: L("item.placeholder", table: .editor),
                             a11yLabel: EditorText.originA11y(item.origin, item.edited), model: model, focus: focus)
                    .modifier(EditorRefActions(sources: item.sources, model: model, isPlayable: isPlayable))
                EditorItemMeta(origin: item.origin, edited: item.edited, sources: item.sources, model: model, isPlayable: isPlayable)
            }
        }
        .modifier(EditorItemMenu(section: sectionID, item: item.id, model: model))
        if isCard { content.editorCard() } else { content }
    }
}

struct EditorTopicRow: View {
    let sectionID: String
    let topic: TopicItem
    let marker: String?
    let isPlayable: Bool
    let model: DocumentEditorModel
    var focus: FocusState<EditTarget?>.Binding

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: Spacing.sm) {
            if let marker {
                Text(EditorText.plain(marker))
                    .font(Typography.headline.monospacedDigit())
                    .foregroundStyle(Palette.textSecondary)
                    .accessibilityHidden(true)
            }
            VStack(alignment: .leading, spacing: Spacing.sm) {
                EditorTextField(target: .item(sectionID, topic.id), text: topic.title, prompt: L("mom.topic.title", table: .editor),
                             font: Typography.headline, a11yLabel: EditorText.originA11y(topic.origin, topic.edited), model: model, focus: focus)
                    .modifier(EditorRefActions(sources: topic.sources, model: model, isPlayable: isPlayable))
                EditorItemMeta(origin: topic.origin, edited: topic.edited, sources: topic.sources, model: model, isPlayable: isPlayable)
                ForEach(topic.points) { point in
                    HStack(alignment: .firstTextBaseline, spacing: Spacing.sm) {
                        Text(EditorText.plain("•")).foregroundStyle(Palette.textSecondary).accessibilityHidden(true)
                        VStack(alignment: .leading, spacing: Spacing.xs) {
                            EditorTextField(target: .point(sectionID, topic.id, point.id), text: point.text, prompt: L("item.placeholder", table: .editor),
                                         a11yLabel: EditorText.originA11y(point.origin, point.edited), model: model, focus: focus)
                                .modifier(EditorRefActions(sources: point.sources, model: model, isPlayable: isPlayable))
                            EditorItemMeta(origin: point.origin, edited: point.edited, sources: point.sources, model: model, isPlayable: isPlayable)
                        }
                    }
                    .contextMenu {
                        Button(role: .destructive) { model.delete(.point(sectionID, topic.id, point.id)) } label: {
                            Label { Text(L("item.delete", table: .editor)) } icon: { Image(systemName: "trash") }
                        }
                    }
                }
                Button {
                    if let target = model.addPoint(section: sectionID, topic: topic.id) { Task { focus.wrappedValue = target } }
                } label: {
                    Label { Text(L("mom.topic.addPoint", table: .editor)) } icon: { Image(systemName: "plus") }
                        .font(Typography.callout)
                        .frame(minHeight: Spacing.minTouchTarget)
                }
                .buttonStyle(.borderless)
                .tint(Palette.accent)
            }
        }
        .modifier(EditorItemMenu(section: sectionID, item: topic.id, model: model))
    }
}

struct EditorLabeledField<Content: View>: View {
    let label: LocalizedStringResource
    @ViewBuilder let content: Content
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        let layout = typeSize.isAccessibilitySize
            ? AnyLayout(VStackLayout(alignment: .leading, spacing: Spacing.xs))
            : AnyLayout(HStackLayout(alignment: .firstTextBaseline, spacing: Spacing.md))
        layout {
            Text(label)
                .font(Typography.subheadline)
                .foregroundStyle(Palette.textSecondary)
                .frame(minWidth: typeSize.isAccessibilitySize ? nil : 88, alignment: .leading)
                .accessibilityHidden(true)
            content.frame(maxWidth: .infinity, alignment: .leading)
        }
    }
}

struct EditorActionCard: View {
    let sectionID: String
    let item: ActionItem
    let isPlayable: Bool
    let model: DocumentEditorModel
    var focus: FocusState<EditTarget?>.Binding
    @State private var pickingDeadline = false

    var body: some View {
        VStack(alignment: .leading, spacing: Spacing.sm) {
            EditorTextField(target: .item(sectionID, item.id), text: item.description, prompt: L("mom.action.task", table: .editor),
                         font: Typography.headline,
                         a11yLabel: EditorText.plain(LS("mom.action.task", table: .editor) + ", " + String(localized: EditorText.originA11y(item.origin, item.edited))),
                         model: model, focus: focus)
                .modifier(EditorRefActions(sources: item.sources, model: model, isPlayable: isPlayable))
            EditorLabeledField(label: L("mom.action.pic", table: .editor)) {
                TextField(text: Binding(get: { item.pic ?? "" }, set: { model.setText(.pic(sectionID, item.id), $0) }),
                          prompt: Text(L("mom.unspecified", table: .editor)).foregroundStyle(Palette.textSecondary)) {
                    Text(L("mom.action.pic", table: .editor))
                }
                .focused(focus, equals: .pic(sectionID, item.id))
                .editorField()
                .accessibilityLabel(Text(L("mom.action.pic", table: .editor)))
                .accessibilityValue(Text(EditorText.plain(item.pic ?? LS("mom.unspecified", table: .editor))))
            }
            EditorLabeledField(label: L("mom.action.deadline", table: .editor)) {
                VStack(alignment: .leading, spacing: Spacing.xs) {
                    Button { pickingDeadline = true } label: {
                        Text(EditorText.plain(Formatting.deadline(date: item.deadline, phrase: item.deadlinePhrase)))
                            .font(Typography.body)
                            .foregroundStyle(item.deadline == nil ? Palette.textSecondary : Palette.textPrimary)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .editorField()
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel(Text(L("mom.action.deadline", table: .editor)))
                    .accessibilityValue(Text(EditorText.plain(Formatting.deadline(date: item.deadline, phrase: item.deadlinePhrase))))
                    .accessibilityHint(Text(L("mom.action.deadline.set", table: .editor)))
                    if item.deadline != nil, let phrase = item.deadlinePhrase {
                        Text(L("mom.action.original", table: .editor, phrase))
                            .font(Typography.footnote)
                            .foregroundStyle(Palette.textSecondary)
                    }
                    if item.deadline != nil {
                        Button { model.setDeadline(section: sectionID, item: item.id, day: nil) } label: {
                            Text(L("mom.action.deadline.clear", table: .editor)).font(Typography.callout).frame(minHeight: Spacing.minTouchTarget)
                        }
                        .buttonStyle(.borderless)
                        .tint(Palette.accent)
                    }
                }
            }
            EditorItemMeta(origin: item.origin, edited: item.edited, sources: item.sources, model: model, isPlayable: isPlayable)
        }
        .editorCard()
        .accessibilityElement(children: .contain)
        .modifier(EditorItemMenu(section: sectionID, item: item.id, model: model))
        .sheet(isPresented: $pickingDeadline) {
            EditorDatePickSheet(
                title: L("mom.action.deadline", table: .editor),
                initial: item.deadline.flatMap { DeadlineDay.date(from: $0) } ?? .now,
                components: .date,
                timeZone: .current
            ) { date in
                model.setDeadline(section: sectionID, item: item.id, day: DeadlineDay.string(from: date))
            }
        }
    }
}

struct EditorDatePickSheet: View {
    let title: LocalizedStringResource
    let initial: Date
    let components: DatePickerComponents
    let timeZone: TimeZone
    let onSave: (Date) -> Void
    @State private var date = Date.now
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            DatePicker(selection: $date, displayedComponents: components) { Text(title) }
                .datePickerStyle(.graphical)
                .environment(\.timeZone, timeZone)
                .padding(.horizontal, Spacing.screenInsetCompact)
                .navigationTitle(Text(title))
                .navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    ToolbarItem(placement: .cancellationAction) {
                        Button { dismiss() } label: { Text(L("common.cancel")) }
                    }
                    ToolbarItem(placement: .confirmationAction) {
                        Button { onSave(date); dismiss() } label: { Text(L("common.save")) }
                    }
                }
        }
        .presentationDetents([.medium, .large])
        .onAppear { date = initial }
    }
}

struct EditorMeetingInfoView: View {
    let info: MeetingInfo
    let isPlayable: Bool
    let model: DocumentEditorModel
    var focus: FocusState<EditTarget?>.Binding
    @State private var pickingDate = false

    private var timeZone: TimeZone { TimeZone(identifier: info.timezone) ?? .current }

    var body: some View {
        VStack(alignment: .leading, spacing: Spacing.md) {
            Text(L("mom.section.info", table: .editor))
                .font(Typography.title)
                .foregroundStyle(Palette.textPrimary)
                .accessibilityAddTraits(.isHeader)
            EditorLabeledField(label: L("mom.field.title", table: .editor)) {
                EditorTextField(target: .title, text: info.title, prompt: L("mom.field.title", table: .editor), model: model, focus: focus)
            }
            EditorLabeledField(label: L("mom.field.date", table: .editor)) {
                Button { pickingDate = true } label: {
                    Text(EditorText.plain(EditorDateFormat.date(info.startedAt, timeZone: timeZone)))
                        .foregroundStyle(info.startedAt == nil ? Palette.textSecondary : Palette.textPrimary)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .editorField()
                }
                .buttonStyle(.plain)
                .accessibilityLabel(Text(L("mom.field.date", table: .editor)))
                .accessibilityValue(Text(EditorText.plain(EditorDateFormat.date(info.startedAt, timeZone: timeZone))))
            }
            EditorLabeledField(label: L("mom.field.timezone", table: .editor)) {
                Text(EditorText.plain(info.timezone))
                    .font(Typography.body)
                    .foregroundStyle(Palette.textPrimary)
                    .accessibilityLabel(Text(L("mom.field.timezone", table: .editor)))
                    .accessibilityValue(Text(EditorText.plain(info.timezone)))
            }
            EditorLabeledField(label: L("mom.field.participants", table: .editor)) {
                VStack(alignment: .leading, spacing: Spacing.sm) {
                    ForEach(Array(info.participants.enumerated()), id: \.offset) { index, name in
                        HStack(spacing: Spacing.xs) {
                            TextField(text: Binding(get: { name }, set: { model.setText(.participant(index), $0) }),
                                      prompt: Text(L("mom.field.participants", table: .editor)).foregroundStyle(Palette.textSecondary)) {
                                Text(L("mom.field.participants", table: .editor))
                            }
                            .focused(focus, equals: .participant(index))
                            .onSubmit { if let t = model.addParticipant() { Task { focus.wrappedValue = t } } }
                            .editorField()
                            Button { model.delete(.participant(index)) } label: {
                                Image(systemName: "xmark.circle.fill")
                                    .foregroundStyle(Palette.textSecondary)
                                    .minTouchTarget()
                            }
                            .buttonStyle(.plain)
                            .accessibilityLabel(Text(L("mom.participant.remove", table: .editor, name)))
                        }
                    }
                    Button { if let t = model.addParticipant() { Task { focus.wrappedValue = t } } } label: {
                        Label { Text(L("mom.participant.add", table: .editor)) } icon: { Image(systemName: "plus") }
                            .font(Typography.callout)
                            .frame(minHeight: Spacing.minTouchTarget)
                    }
                    .buttonStyle(.borderless)
                    .tint(Palette.accent)
                }
            }
            EditorLabeledField(label: L("mom.field.agenda", table: .editor)) {
                VStack(alignment: .leading, spacing: Spacing.sm) {
                    ForEach(Array(info.agenda.enumerated()), id: \.element.id) { index, item in
                        HStack(alignment: .firstTextBaseline, spacing: Spacing.sm) {
                            Text(EditorText.plain("\(index + 1).")).foregroundStyle(Palette.textSecondary).accessibilityHidden(true)
                            VStack(alignment: .leading, spacing: Spacing.xs) {
                                EditorTextField(target: .agenda(item.id), text: item.text, prompt: L("mom.field.agenda", table: .editor),
                                             a11yLabel: EditorText.originA11y(item.origin, item.edited), model: model, focus: focus)
                                if item.origin == .ai || !item.sources.isEmpty {
                                    EditorItemMeta(origin: item.origin, edited: item.edited, sources: item.sources, model: model, isPlayable: isPlayable)
                                }
                            }
                        }
                        .contextMenu {
                            Button(role: .destructive) { model.delete(.agenda(item.id)) } label: {
                                Label { Text(L("item.delete", table: .editor)) } icon: { Image(systemName: "trash") }
                            }
                        }
                    }
                    Button { if let t = model.addAgenda() { Task { focus.wrappedValue = t } } } label: {
                        Label { Text(L("mom.agenda.add", table: .editor)) } icon: { Image(systemName: "plus") }
                            .font(Typography.callout)
                            .frame(minHeight: Spacing.minTouchTarget)
                    }
                    .buttonStyle(.borderless)
                    .tint(Palette.accent)
                }
            }
        }
        .sheet(isPresented: $pickingDate) {
            EditorDatePickSheet(title: L("mom.field.date", table: .editor), initial: info.startedAt ?? .now,
                          components: [.date, .hourAndMinute], timeZone: timeZone) { model.setStartedAt($0) }
        }
    }
}

enum EditorDateFormat {
    static func date(_ date: Date?, timeZone: TimeZone, localization: String? = nil) -> String {
        guard let date else { return LS("common.notDetermined", localization: localization) }
        var style = Date.FormatStyle(date: .abbreviated, time: .shortened)
        style.timeZone = timeZone
        let zone = timeZone.abbreviation(for: date) ?? timeZone.identifier
        return "\(date.formatted(style)) \(zone)"
    }
}
