import SwiftUI

// Read-only rendering (old versions, server version in the conflict sheet): normal text, no field chrome.
struct ReadOnlyDocumentView: View {
    let document: EditorDocument

    var body: some View {
        VStack(alignment: .leading, spacing: Spacing.section) {
            if let info = document.meetingInfo { infoBlock(info) }
            ForEach(document.sections) { section in
                VStack(alignment: .leading, spacing: Spacing.sm) {
                    Text(EditorText.plain(section.heading))
                        .font(Typography.title)
                        .foregroundStyle(Palette.textPrimary)
                        .accessibilityAddTraits(.isHeader)
                    if section.items.isEmpty {
                        Text(L("mom.section.empty", table: .editor))
                            .font(Typography.callout)
                            .foregroundStyle(Palette.textSecondary)
                    }
                    ForEach(Array(section.items.enumerated()), id: \.element.id) { index, item in
                        itemView(item, marker: EditorText.marker(section.style, index: index))
                    }
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private func infoBlock(_ info: MeetingInfo) -> some View {
        let tz = TimeZone(identifier: info.timezone) ?? .current
        return VStack(alignment: .leading, spacing: Spacing.sm) {
            Text(L("mom.section.info", table: .editor))
                .font(Typography.title)
                .foregroundStyle(Palette.textPrimary)
                .accessibilityAddTraits(.isHeader)
            EditorLabeledField(label: L("mom.field.title", table: .editor)) { value(Formatting.notDetermined(info.title)) }
            EditorLabeledField(label: L("mom.field.date", table: .editor)) { value(EditorDateFormat.date(info.startedAt, timeZone: tz)) }
            EditorLabeledField(label: L("mom.field.participants", table: .editor)) {
                value(info.participants.isEmpty ? LS("mom.notMentioned", table: .editor) : info.participants.joined(separator: ", "))
            }
            if !info.agenda.isEmpty {
                EditorLabeledField(label: L("mom.field.agenda", table: .editor)) {
                    VStack(alignment: .leading, spacing: Spacing.xs) {
                        ForEach(Array(info.agenda.enumerated()), id: \.element.id) { index, item in
                            value("\(index + 1). \(item.text)")
                        }
                    }
                }
            }
        }
    }

    private func value(_ text: String) -> some View {
        Text(EditorText.plain(text))
            .readingTextStyle()
            .foregroundStyle(Palette.textPrimary)
            .accessibilityAddTraits(.isStaticText)
    }

    @ViewBuilder private func itemView(_ item: MoMItem, marker: String?) -> some View {
        switch item {
        case .text(let t):
            line(marker: marker, text: t.text, origin: t.origin, edited: t.edited, sources: t.sources)
        case .topic(let t):
            VStack(alignment: .leading, spacing: Spacing.xs) {
                line(marker: marker, text: t.title, origin: t.origin, edited: t.edited, sources: t.sources, font: Typography.headline)
                ForEach(t.points) { p in
                    line(marker: "•", text: p.text, origin: p.origin, edited: p.edited, sources: p.sources)
                        .padding(.leading, Spacing.lg)
                }
            }
        case .action(let a):
            VStack(alignment: .leading, spacing: Spacing.xs) {
                Text(EditorText.plain(a.description)).font(Typography.headline).foregroundStyle(Palette.textPrimary)
                EditorLabeledField(label: L("mom.action.pic", table: .editor)) { value(Formatting.notDetermined(a.pic)) }
                EditorLabeledField(label: L("mom.action.deadline", table: .editor)) {
                    value(Formatting.deadline(date: a.deadline, phrase: a.deadlinePhrase))
                }
                EditorItemMeta(origin: a.origin, edited: a.edited, sources: a.sources, model: nil, isPlayable: false)
            }
            .editorCard()
            .accessibilityElement(children: .combine)
        }
    }

    private func line(marker: String?, text: String, origin: Origin, edited: Bool, sources: [SourceRef], font: Font = Typography.body) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: Spacing.sm) {
            if let marker {
                Text(EditorText.plain(marker)).foregroundStyle(Palette.textSecondary).accessibilityHidden(true)
            }
            VStack(alignment: .leading, spacing: Spacing.xs) {
                Text(EditorText.plain(text)).font(font).lineSpacing(Typography.readingLineSpacing).foregroundStyle(Palette.textPrimary)
                EditorItemMeta(origin: origin, edited: edited, sources: sources, model: nil, isPlayable: false)
            }
        }
        .accessibilityElement(children: .combine)
    }
}
