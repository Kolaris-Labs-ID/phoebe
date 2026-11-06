import SwiftUI

func resourceVerbatim(_ text: String) -> LocalizedStringResource {
    LocalizedStringResource(String.LocalizationValue(text), table: "Verbatim")
}

extension MeetingRowData {
    @MainActor
    init(meeting: Meeting, app: AppModel) {
        let live = app.recorder.meetingID == meeting.id ? app.recorder.phase : nil
        let input = MeetingStatusInput(
            localPhase: meeting.localPhase,
            failedStage: meeting.failedStage,
            transcriptionErrorCategory: meeting.transcriptionErrorCategory,
            activeJobs: meeting.activeJobs,
            momReview: meeting.momReview,
            pipeline: meeting.pipeline,
            hasTranscript: meeting.hasTranscript,
            isServerCreated: meeting.isServerCreated,
            livePhase: live,
            uploadProgress: meeting.localPhase == .uploading ? app.uploads.progress(meetingID: meeting.id) : nil,
            isOnline: app.isOnline
        )
        self.init(
            id: meeting.id,
            title: meeting.title,
            date: meeting.sortDate,
            durationMs: meeting.durationMs,
            source: meeting.source,
            status: MeetingStatus.chip(for: input),
            folderName: meeting.folder?.name,
            tagNames: meeting.tags.map(\.name).sorted { $0.localizedStandardCompare($1) == .orderedAscending }
        )
    }

    init(item: MeetingListItemDTO, folders: [Folder], tags: [Tag], isOnline: Bool) {
        let input = MeetingStatusInput(
            failedStage: item.failedStage,
            transcriptionErrorCategory: item.transcriptionErrorCategory,
            activeJobs: item.activeJobTypes,
            momReview: item.momReviewStatus,
            pipeline: item.pipelineState,
            isOnline: isOnline
        )
        self.init(
            id: item.id,
            title: item.title,
            date: item.startedAt ?? item.createdAt,
            durationMs: item.durationMs,
            source: item.source,
            status: MeetingStatus.chip(for: input),
            folderName: folders.first { $0.id == item.folderId }?.name,
            tagNames: tags.filter { item.tagIds.contains($0.id) }.map(\.name)
        )
    }

    var displayTitle: String {
        let trimmed = title.trimmingCharacters(in: .whitespacesAndNewlines)
        guard trimmed.isEmpty else { return trimmed }
        return LS("meeting.untitled", table: .library, date.formatted(.dateTime.day().month(.abbreviated).hour().minute()))
    }

    var accessibilityLabel: String {
        var parts = [displayTitle, date.formatted(date: .abbreviated, time: .shortened)]
        if let durationMs {
            parts.append(LS("library.row.duration.a11y", table: .library, Formatting.spokenDuration(ms: durationMs, includeSeconds: false)))
        }
        if let status { parts.append(String(localized: status.text)) }
        if let folderName { parts.append(LS("library.row.folder.a11y", table: .library, folderName)) }
        if !tagNames.isEmpty { parts.append(LS("library.row.tags.a11y", table: .library, tagNames.joined(separator: ", "))) }
        return parts.joined(separator: ", ")
    }
}

struct MeetingRow: View {
    let data: MeetingRowData
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    @ScaledMetric(relativeTo: .body) private var iconCircle: CGFloat = 40

    var body: some View {
        HStack(alignment: .top, spacing: Spacing.md) {
            Image(systemName: data.source == .import ? "square.and.arrow.down" : "waveform")
                .font(Typography.body)
                .foregroundStyle(Palette.textSecondary)
                .frame(width: iconCircle, height: iconCircle)
                .background(Palette.surface2, in: Circle())
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: Spacing.xs) {
                Text(verbatim: data.displayTitle)
                    .font(Typography.headline)
                    .foregroundStyle(Palette.textPrimary)
                    .lineLimit(2)
                metadata
                chips
            }
            Spacer(minLength: 0)
        }
        .padding(.vertical, Spacing.xs)
        .frame(minHeight: 64, alignment: .top)
        .contentShape(Rectangle())
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(Text(verbatim: data.accessibilityLabel))
        .accessibilityHint(Text(L("library.row.hint", table: .library)))
        .accessibilityAddTraits(.isButton)
    }

    @ViewBuilder private var metadata: some View {
        let time = Text(data.date.formatted(date: .omitted, time: .shortened))
        let duration = data.durationMs.map { Text(verbatim: Formatting.timer(ms: $0)) }
        Group {
            if dynamicTypeSize.isAccessibilitySize {
                VStack(alignment: .leading, spacing: Spacing.xxs) {
                    time
                    if let duration { duration }
                }
            } else if let duration {
                Text("\(time) · \(duration)")
            } else {
                time
            }
        }
        .font(Typography.subheadline)
        .foregroundStyle(Palette.textSecondary)
        .monospacedDigit()
    }

    @ViewBuilder private var chips: some View {
        if data.status != nil || data.folderName != nil || !data.tagNames.isEmpty {
            ChipFlowLayout(spacing: Spacing.xs) {
                if let status = data.status { StatusChip(status) }
                if let folderName = data.folderName {
                    StatusChip(resourceVerbatim(folderName), systemImage: "folder", tone: .neutral)
                }
                ForEach(data.tagNames, id: \.self) { name in
                    Text(verbatim: "#\(name)")
                        .font(Typography.caption)
                        .foregroundStyle(Palette.accent)
                        .padding(.vertical, Spacing.xs)
                        .padding(.horizontal, Spacing.sm)
                        .background(Palette.accentSoft, in: RoundedRectangle(cornerRadius: Radius.sm, style: .continuous))
                }
            }
        }
    }
}

struct ChipFlowLayout: Layout {
    var spacing: CGFloat

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let rows = arrange(width: proposal.width ?? .infinity, subviews: subviews)
        let height = rows.reduce(0) { $0 + $1.height } + spacing * CGFloat(max(0, rows.count - 1))
        let width = rows.map(\.width).max() ?? 0
        return CGSize(width: proposal.width ?? width, height: height)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        var y = bounds.minY
        for row in arrange(width: bounds.width, subviews: subviews) {
            var x = bounds.minX
            for index in row.indices {
                let size = subviews[index].sizeThatFits(ProposedViewSize(width: bounds.width, height: nil))
                subviews[index].place(at: CGPoint(x: x, y: y), proposal: ProposedViewSize(width: min(size.width, bounds.width), height: size.height))
                x += min(size.width, bounds.width) + spacing
            }
            y += row.height + spacing
        }
    }

    private struct Row { var indices: [Int] = []; var width: CGFloat = 0; var height: CGFloat = 0 }

    private func arrange(width: CGFloat, subviews: Subviews) -> [Row] {
        var rows: [Row] = []
        var current = Row()
        for index in subviews.indices {
            let size = subviews[index].sizeThatFits(ProposedViewSize(width: width, height: nil))
            let itemWidth = min(size.width, width)
            let needed = current.indices.isEmpty ? itemWidth : current.width + spacing + itemWidth
            if needed > width, !current.indices.isEmpty {
                rows.append(current)
                current = Row()
            }
            current.width = current.indices.isEmpty ? itemWidth : current.width + spacing + itemWidth
            current.height = max(current.height, size.height)
            current.indices.append(index)
        }
        if !current.indices.isEmpty { rows.append(current) }
        return rows
    }
}
