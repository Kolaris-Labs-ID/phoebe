import SwiftUI

struct RefContext {
    let verifiedSegmentIDs: Set<String>?
    let isPlayable: Bool
    let onSeek: (Int) -> Void

    func isVerified(_ ref: SourceRef) -> Bool {
        guard case .transcript(let segmentId, _, _, _) = ref, let ids = verifiedSegmentIDs else { return true }
        return ids.contains(segmentId)
    }
}

struct AINotesReadView: View {
    let content: AINotesContent
    let refs: RefContext

    var body: some View {
        VStack(alignment: .leading, spacing: Spacing.lg) {
            ForEach(content.sections) { section in
                VStack(alignment: .leading, spacing: Spacing.sm) {
                    Text(verbatim: section.heading)
                        .font(Typography.headline)
                        .foregroundStyle(Palette.textPrimary)
                        .accessibilityAddTraits(.isHeader)
                    ForEach(Array(section.items.enumerated()), id: \.element.id) { index, item in
                        ReadItemRow(marker: section.style.marker(index), text: item.text, origin: item.origin, edited: item.edited, sources: item.sources, refs: refs)
                    }
                }
            }
        }
    }
}

struct MoMReadView: View {
    let content: MoMContent
    let refs: RefContext

    var body: some View {
        VStack(alignment: .leading, spacing: Spacing.lg) {
            VStack(alignment: .leading, spacing: Spacing.xs) {
                Text(verbatim: content.meetingInfo.title.nonBlank ?? LS("detail.untitled", table: .meetingDetail))
                    .font(Typography.title)
                    .foregroundStyle(Palette.textPrimary)
                    .accessibilityAddTraits(.isHeader)
                if !content.meetingInfo.participants.isEmpty {
                    Text(verbatim: content.meetingInfo.participants.joined(separator: ", "))
                        .font(Typography.subheadline)
                        .foregroundStyle(Palette.textSecondary)
                }
            }
            ForEach(content.sections) { section in
                VStack(alignment: .leading, spacing: Spacing.sm) {
                    Text(verbatim: section.heading)
                        .font(Typography.headline)
                        .foregroundStyle(Palette.textPrimary)
                        .accessibilityAddTraits(.isHeader)
                    if section.items.isEmpty {
                        Text(L("mom.section.empty", table: .meetingDetail))
                            .font(Typography.callout)
                            .foregroundStyle(Palette.textSecondary)
                    }
                    ForEach(Array(section.items.enumerated()), id: \.element.id) { index, item in
                        switch item {
                        case .text(let t):
                            ReadItemRow(marker: (section.style ?? .bullet).marker(index), text: t.text, origin: t.origin, edited: t.edited, sources: t.sources, refs: refs)
                        case .topic(let topic):
                            VStack(alignment: .leading, spacing: Spacing.xs) {
                                ReadItemRow(marker: nil, text: topic.title, origin: topic.origin, edited: topic.edited, sources: topic.sources, refs: refs, bold: true)
                                ForEach(Array(topic.points.enumerated()), id: \.element.id) { i, p in
                                    ReadItemRow(marker: ListStyle.bullet.marker(i), text: p.text, origin: p.origin, edited: p.edited, sources: p.sources, refs: refs)
                                        .padding(.leading, Spacing.lg)
                                }
                            }
                        case .action(let a):
                            VStack(alignment: .leading, spacing: Spacing.xxs) {
                                ReadItemRow(marker: "\(index + 1).", text: a.description, origin: a.origin, edited: a.edited, sources: a.sources, refs: refs)
                                Text(verbatim: "\(LS("mom.action.pic", table: .meetingDetail)): \(Formatting.notDetermined(a.pic)) · \(LS("mom.action.deadline", table: .meetingDetail)): \(Formatting.deadline(date: a.deadline, phrase: a.deadlinePhrase))")
                                    .font(Typography.footnote)
                                    .foregroundStyle(Palette.textSecondary)
                                    .padding(.leading, Spacing.xl)
                            }
                        }
                    }
                }
            }
        }
    }
}

private struct ReadItemRow: View {
    let marker: String?
    let text: String
    let origin: Origin
    let edited: Bool
    let sources: [SourceRef]
    let refs: RefContext
    var bold = false

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: Spacing.sm) {
            if let marker {
                Text(verbatim: marker)
                    .font(Typography.body.monospacedDigit())
                    .foregroundStyle(Palette.textSecondary)
                    .accessibilityHidden(true)
            }
            VStack(alignment: .leading, spacing: Spacing.xs) {
                Text(verbatim: text)
                    .readingTextStyle()
                    .fontWeight(bold ? .semibold : .regular)
                    .foregroundStyle(Palette.textPrimary)
                    .frame(maxWidth: .infinity, alignment: .leading)
                FlowRow {
                    OriginMarker(origin: origin, edited: edited)
                    ForEach(Array(sources.enumerated()), id: \.offset) { _, ref in
                        SourceReferenceButton(ref: ref, isVerified: refs.isVerified(ref), isPlayable: refs.isPlayable, onSeek: refs.onSeek)
                    }
                }
            }
        }
    }
}

private struct FlowRow<Content: View>: View {
    @ViewBuilder let content: Content

    var body: some View {
        ViewThatFits(in: .horizontal) {
            HStack(spacing: Spacing.sm) { content }
            VStack(alignment: .leading, spacing: Spacing.xs) { content }
        }
    }
}

extension ListStyle {
    func marker(_ index: Int) -> String? {
        switch self {
        case .bullet: "•"
        case .numbered: "\(index + 1)."
        case .paragraph: nil
        }
    }
}
