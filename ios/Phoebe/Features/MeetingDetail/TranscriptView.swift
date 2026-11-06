import SwiftUI
import UIKit

struct TranscriptTab: View {
    @Environment(AppModel.self) private var app
    let data: DetailData
    @Bindable var model: MeetingDetailModel
    let player: PlayerModel
    @AccessibilityFocusState private var reviewFocus: String?

    var body: some View {
        VStack(alignment: .leading, spacing: Spacing.md) {
            if data.transcript != nil {
                transcriptContent
            } else if data.isNoSpeech {
                ContentUnavailableView {
                    Label { Text(L("status.noSpeech")) } icon: { Image(systemName: "waveform.slash") }
                } description: {
                    Text(L("detail.noSpeech", table: .meetingDetail))
                }
            } else if DetailStatus.transcript(data.stage) == .failed {
                failureBanner
            } else if DetailStatus.transcript(data.stage) == .active(percent: nil) {
                VStack(alignment: .leading, spacing: Spacing.md) {
                    Label { Text(L("detail.transcribing", table: .meetingDetail)) } icon: { ProgressView() }
                        .font(Typography.callout)
                        .foregroundStyle(Palette.textSecondary)
                    ForEach(0..<5, id: \.self) { _ in
                        Text(verbatim: "00:00  Lorem ipsum dolor sit amet, consectetur adipiscing elit.")
                            .readingTextStyle()
                            .redacted(reason: .placeholder)
                            .accessibilityHidden(true)
                    }
                }
            } else {
                ContentUnavailableView {
                    Label { Text(L("detail.transcript.pending.title", table: .meetingDetail)) } icon: { Image(systemName: "text.quote") }
                } description: {
                    Text(L("detail.transcript.pending", table: .meetingDetail))
                }
            }
        }
    }

    // MARK: Failure

    private var failureBanner: some View {
        let category = data.job(.transcribe)?.errorCategory ?? data.meeting.transcriptionErrorCategory
        let copy = FailureCopy.make(category: category)
        var actions: [BannerAction] = []
        if copy.retryAllowed && data.isOnline {
            actions.append(BannerAction(L("common.retry"), isLoading: model.generating.contains(.transcribe)) {
                Task { await model.startTranscription(language: data.meeting.transcriptionLanguage, app: app) }
            })
        }
        if copy.offerDelete {
            actions.append(BannerAction(L("meeting.delete", table: .meetingDetail)) { model.confirmDeleteMeeting = true })
        }
        return VStack(alignment: .leading, spacing: Spacing.sm) {
            Banner(
                tone: .danger,
                systemImage: "exclamationmark.triangle",
                title: L("detail.transcription.failed", table: .meetingDetail),
                message: copy.showsRequestCode ? L(copy.key, table: .meetingDetail, data.jobs[.transcribe]?.id.lower ?? "-") : L(copy.key, table: .meetingDetail),
                actions: actions
            )
            if let key = model.actionErrors[.transcribe] {
                ActionErrorText(key: key)
            }
        }
    }

    // MARK: Rows

    @ViewBuilder private var transcriptContent: some View {
        let showOriginal = model.showOriginal && data.originalTranscript != nil
        let all = data.transcriptSegments(original: showOriginal)
        let uncertainIDs = showOriginal ? [] : TranscriptReview.uncertainIDs(all)
        let segments = model.uncertainOnly && !uncertainIDs.isEmpty ? all.filter(\.uncertain) : all
        let corrected = data.transcript?.kind == .correction
            ? TranscriptDiff.correctedIDs(current: data.transcriptSegments(original: false), original: data.transcriptSegments(original: true))
            : []
        let playable = data.playback.isPlayable
        let playingID = player.isPlaying ? TranscriptDiff.playingSegmentID(segments, atMs: player.currentMs) : nil

        if data.transcript?.kind == .correction {
            HStack {
                Spacer()
                Button {
                    model.showOriginal.toggle()
                } label: {
                    Label {
                        Text(L(showOriginal ? "transcript.showCurrent" : "transcript.showOriginal", table: .meetingDetail))
                    } icon: {
                        Image(systemName: "clock.arrow.circlepath")
                    }
                    .font(Typography.subheadline)
                    .minTouchTarget()
                }
                .buttonStyle(.borderless)
                .tint(Palette.accent)
                .disabled(data.originalTranscript == nil)
            }
        }
        if all.contains(where: \.isApproximate) {
            Label { Text(L("transcript.timing.chunk", table: .meetingDetail)) } icon: { Image(systemName: "info.circle") }
                .font(Typography.footnote)
                .foregroundStyle(Palette.textSecondary)
                .accessibilityElement(children: .combine)
        }
        if !data.isOnline {
            Text(L("transcript.edit.offline", table: .meetingDetail))
                .font(Typography.footnote)
                .foregroundStyle(Palette.textSecondary)
        }
        if !uncertainIDs.isEmpty {
            UncertainReviewBar(
                count: uncertainIDs.count,
                onlyUncertain: $model.uncertainOnly,
                onPrevious: { model.stepReview(uncertainIDs, forward: false) },
                onNext: { model.stepReview(uncertainIDs, forward: true) }
            )
        }
        ForEach(segments) { segment in
            TranscriptRow(
                segment: segment,
                isCorrected: !showOriginal && corrected.contains(segment.id),
                isPlaying: playingID == segment.id,
                isPlayable: playable,
                canEdit: data.isOnline && !showOriginal,
                isReviewFocus: !showOriginal && model.reviewCursor == segment.id,
                showsUncertain: !showOriginal && segment.uncertain,
                onSeek: {
                    Task { await player.play(from: segment.startMs, meetingID: data.meeting.id, availability: data.playback, app: app) }
                },
                onEdit: { model.correctionTarget = segment }
            )
            .id(segment.id)
            .accessibilityFocused($reviewFocus, equals: segment.id)
        }
        .onChange(of: model.reviewCursor) { _, id in reviewFocus = id }
    }
}

private struct UncertainReviewBar: View {
    let count: Int
    @Binding var onlyUncertain: Bool
    let onPrevious: () -> Void
    let onNext: () -> Void
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize

    var body: some View {
        VStack(alignment: .leading, spacing: Spacing.sm) {
            let layout = dynamicTypeSize.isAccessibilitySize
                ? AnyLayout(VStackLayout(alignment: .leading, spacing: Spacing.xs))
                : AnyLayout(HStackLayout(spacing: Spacing.sm))
            layout {
                Label { Text(L("transcript.review.count", table: .meetingDetail, Int64(count))) } icon: { Image(systemName: UncertainMarker.symbol) }
                    .font(Typography.subheadline.weight(.semibold))
                    .foregroundStyle(Palette.textPrimary)
                    .frame(maxWidth: .infinity, alignment: .leading)
                HStack(spacing: Spacing.xs) {
                    Button(action: onPrevious) { Image(systemName: "chevron.up").minTouchTarget() }
                        .accessibilityLabel(Text(L("transcript.review.previous", table: .meetingDetail)))
                    Button(action: onNext) { Image(systemName: "chevron.down").minTouchTarget() }
                        .accessibilityLabel(Text(L("transcript.review.next", table: .meetingDetail)))
                }
                .buttonStyle(.borderless)
                .tint(Palette.accent)
            }
            Toggle(isOn: $onlyUncertain) {
                Text(L("transcript.review.filter", table: .meetingDetail))
                    .font(Typography.subheadline)
                    .foregroundStyle(Palette.textPrimary)
            }
            .tint(Palette.accent)
            .frame(minHeight: Spacing.minTouchTarget)
        }
        .padding(Spacing.md)
        .background(Palette.surface, in: RoundedRectangle(cornerRadius: Radius.md, style: .continuous))
        .accessibilityElement(children: .contain)
    }
}

enum UncertainMarker {
    static let symbol = "exclamationmark.bubble"
}

private struct TranscriptRow: View {
    let segment: TranscriptSegment
    let isCorrected: Bool
    let isPlaying: Bool
    let isPlayable: Bool
    let canEdit: Bool
    let isReviewFocus: Bool
    let showsUncertain: Bool
    let onSeek: () -> Void
    let onEdit: () -> Void

    var body: some View {
        let stamp = Formatting.timestamp(ms: segment.startMs, approximate: segment.isApproximate)
        let spokenStamp = Formatting.spokenTimestamp(ms: segment.startMs, approximate: segment.isApproximate)
        HStack(alignment: .firstTextBaseline, spacing: Spacing.md) {
            if isPlayable {
                Button(action: onSeek) {
                    Text(verbatim: stamp)
                        .font(Typography.subheadline.monospacedDigit())
                        .foregroundStyle(Palette.accent)
                        .frame(minWidth: Spacing.minTouchTarget, minHeight: Spacing.minTouchTarget, alignment: .leading)
                        .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
            } else {
                Text(verbatim: stamp)
                    .font(Typography.subheadline.monospacedDigit())
                    .foregroundStyle(Palette.textSecondary)
                    .frame(minWidth: Spacing.minTouchTarget, alignment: .leading)
            }
            VStack(alignment: .leading, spacing: Spacing.xs) {
                Text(verbatim: segment.text)
                    .readingTextStyle()
                    .foregroundStyle(Palette.textPrimary)
                    .frame(maxWidth: .infinity, alignment: .leading)
                if isCorrected {
                    Label { Text(L("transcript.corrected", table: .meetingDetail)) } icon: { Image(systemName: "pencil") }
                        .font(Typography.caption)
                        .foregroundStyle(Palette.textSecondary)
                }
                if showsUncertain {
                    Label { Text(L("transcript.uncertain", table: .meetingDetail)) } icon: { Image(systemName: UncertainMarker.symbol) }
                        .font(Typography.caption.weight(.semibold))
                        .foregroundStyle(Palette.warning)
                }
            }
            if isPlaying {
                Image(systemName: "speaker.wave.2")
                    .foregroundStyle(Palette.accent)
                    .accessibilityHidden(true)
            }
        }
        .padding(.vertical, Spacing.xs)
        .padding(.horizontal, Spacing.sm)
        .background(isPlaying ? Palette.accentSoft : .clear, in: RoundedRectangle(cornerRadius: Radius.sm, style: .continuous))
        .overlay {
            if isReviewFocus {
                RoundedRectangle(cornerRadius: Radius.sm, style: .continuous).strokeBorder(Palette.warning, lineWidth: 2)
            }
        }
        .contextMenu {
            Button(action: onEdit) { Label { Text(L("transcript.edit", table: .meetingDetail)) } icon: { Image(systemName: "pencil") } }
                .disabled(!canEdit)
            Button {
                UIPasteboard.general.string = segment.text
            } label: {
                Label { Text(L("transcript.copy", table: .meetingDetail)) } icon: { Image(systemName: "doc.on.doc") }
            }
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(Text(verbatim: "\(spokenStamp), \(segment.text)"
            + (isCorrected ? ", \(LS("transcript.corrected", table: .meetingDetail))" : "")
            + (showsUncertain ? ", \(LS("transcript.uncertain", table: .meetingDetail))" : "")))
        .accessibilityValue(isPlaying ? Text(L("transcript.playing", table: .meetingDetail)) : Text(verbatim: ""))
        .modifier(SeekAction(enabled: isPlayable, stamp: segment.isApproximate ? spokenStamp : stamp, onSeek: onSeek))
        .accessibilityAction(named: Text(L("transcript.copy", table: .meetingDetail))) { UIPasteboard.general.string = segment.text }
        .modifier(EditAction(enabled: canEdit, onEdit: onEdit))
    }
}

private struct SeekAction: ViewModifier {
    let enabled: Bool
    let stamp: String
    let onSeek: () -> Void

    func body(content: Content) -> some View {
        if enabled {
            content
                .accessibilityAddTraits(.isButton)
                .accessibilityHint(Text(L("ref.playAt", stamp)))
                .accessibilityAction(.default, onSeek)
        } else {
            content.accessibilityAddTraits(.isStaticText)
        }
    }
}

private struct EditAction: ViewModifier {
    let enabled: Bool
    let onEdit: () -> Void

    func body(content: Content) -> some View {
        if enabled {
            content.accessibilityAction(named: Text(L("transcript.edit", table: .meetingDetail)), onEdit)
        } else {
            content
        }
    }
}

struct ActionErrorText: View {
    @Environment(AppModel.self) private var app
    let key: String
    var date: Date?
    var isError = true

    var body: some View {
        Label {
            if key == AILimitCopy.key {
                Text(verbatim: Self.aiLimitText(usage: app.usage, fallbackNextSlot: date))
            } else {
                Text(L(key, table: .meetingDetail))
            }
        } icon: {
            Image(systemName: isError ? "exclamationmark.circle" : "info.circle")
        }
        .font(Typography.footnote)
        .foregroundStyle(isError ? Palette.danger : Palette.textSecondary)
    }

    static func aiLimitText(usage: UsageDTO?, fallbackNextSlot: Date?, now: Date = .now, localization: String? = nil) -> String {
        if let usage {
            let allowance = AIAllowance(usage.aiGenerations)
            if allowance.isExhausted, allowance.nextSlotAt == nil, let fallbackNextSlot {
                return "\(allowance.label(localization: localization)) · \(allowance.remainingText(localization: localization)). \(AIAllowance.nextSlotText(fallbackNextSlot, now: now, localization: localization))."
            }
            return allowance.summary(now: now, localization: localization)
        }
        let reached = LS(AILimitCopy.key, table: .meetingDetail, localization: localization)
        return fallbackNextSlot.map { "\(reached) \(AIAllowance.nextSlotText($0, now: now, localization: localization))." } ?? reached
    }
}

// MARK: - Correction sheet

struct TranscriptCorrectionSheet: View {
    @Environment(AppModel.self) private var app
    @Environment(\.dismiss) private var dismiss
    let segment: TranscriptSegment
    let expectedRevision: Int
    let model: MeetingDetailModel

    @State private var text = ""
    @State private var saving = false
    @State private var errorKey: String?
    @State private var conflictRevision: Int?
    @State private var attempt: (id: UUID, text: String, expected: Int)?

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    TextEditor(text: $text)
                        .frame(minHeight: 160)
                        .font(Typography.body)
                        .accessibilityLabel(Text(L("transcript.edit", table: .meetingDetail)))
                } header: {
                    Text(verbatim: Formatting.timestamp(ms: segment.startMs, approximate: segment.isApproximate))
                        .accessibilityLabel(Text(verbatim: Formatting.spokenTimestamp(ms: segment.startMs, approximate: segment.isApproximate)))
                } footer: {
                    VStack(alignment: .leading, spacing: Spacing.sm) {
                        Text(L("transcript.edit.footnote", table: .meetingDetail))
                        if text.count > 2000 {
                            ActionErrorText(key: "transcript.edit.tooLong")
                        }
                        if !app.isOnline {
                            Text(L("transcript.edit.offline", table: .meetingDetail))
                        }
                        if let errorKey { ActionErrorText(key: errorKey) }
                    }
                }
                if segment.uncertain {
                    Section {
                        Label { Text(L("transcript.uncertain", table: .meetingDetail)) } icon: { Image(systemName: UncertainMarker.symbol) }
                            .font(Typography.headline)
                            .foregroundStyle(Palette.warning)
                        Text(L("transcript.review.body", table: .meetingDetail))
                            .font(Typography.callout)
                            .foregroundStyle(Palette.textPrimary)
                        if text == segment.text {
                            Button(L("transcript.review.confirm", table: .meetingDetail)) { save(expected: attempt?.expected ?? expectedRevision) }
                                .disabled(saving || !app.isOnline || conflictRevision != nil)
                                .frame(minHeight: Spacing.minTouchTarget)
                        }
                    }
                }
                if let conflictRevision {
                    Section {
                        Label { Text(L("transcript.edit.conflict", table: .meetingDetail)) } icon: { Image(systemName: "arrow.triangle.branch") }
                            .foregroundStyle(Palette.warning)
                        Button(L("transcript.edit.conflict.apply", table: .meetingDetail)) {
                            attempt = nil
                            self.conflictRevision = nil
                            save(expected: conflictRevision)
                        }
                        .disabled(saving || !app.isOnline)
                    }
                }
            }
            .navigationTitle(Text(L("transcript.edit", table: .meetingDetail)))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button(L("common.cancel")) { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    if saving {
                        ProgressView()
                    } else {
                        Button(L("common.save")) { save(expected: attempt?.expected ?? expectedRevision) }
                            .disabled(!app.isOnline || !TranscriptReview.canSave(segment: segment, text: text) || conflictRevision != nil)
                    }
                }
            }
        }
        .interactiveDismissDisabled(text != segment.text)
        .onAppear { text = segment.text }
    }

    private func save(expected: Int) {
        // Same text and revision as a failed attempt → reuse its mutation id so a lost response replays.
        let mutation = attempt.flatMap { $0.text == text && $0.expected == expected ? $0.id : nil } ?? UUID()
        attempt = (mutation, text, expected)
        saving = true
        errorKey = nil
        Task {
            let outcome = await model.submitCorrection(
                segmentID: segment.id, text: text, resolvedUncertain: TranscriptReview.resolvedIDs(segment: segment),
                expectedRevision: expected, mutationID: mutation, app: app
            )
            saving = false
            switch outcome {
            case .saved: dismiss()
            case .conflict(let latest): conflictRevision = latest
            case .failed(let key): errorKey = key
            }
        }
    }
}
