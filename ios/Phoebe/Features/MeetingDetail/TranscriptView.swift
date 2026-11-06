import SwiftUI
import UIKit

struct TranscriptTab: View {
    @Environment(AppModel.self) private var app
    let data: DetailData
    @Bindable var model: MeetingDetailModel
    let player: PlayerModel

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
                Task {
                    await model.generate(.transcribe, body: model.transcribeBody(language: data.meeting.transcriptionLanguage), flushKinds: [], pendingKinds: [], app: app)
                }
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
        let segments = data.transcriptSegments(original: showOriginal)
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
        if !data.isOnline {
            Text(L("transcript.edit.offline", table: .meetingDetail))
                .font(Typography.footnote)
                .foregroundStyle(Palette.textSecondary)
        }
        ForEach(segments) { segment in
            TranscriptRow(
                segment: segment,
                isCorrected: !showOriginal && corrected.contains(segment.id),
                isPlaying: playingID == segment.id,
                isPlayable: playable,
                canEdit: data.isOnline && !showOriginal,
                onSeek: {
                    Task { await player.play(from: segment.startMs, meetingID: data.meeting.id, availability: data.playback, app: app) }
                },
                onEdit: { model.correctionTarget = segment }
            )
        }
    }
}

private struct TranscriptRow: View {
    let segment: TranscriptSegment
    let isCorrected: Bool
    let isPlaying: Bool
    let isPlayable: Bool
    let canEdit: Bool
    let onSeek: () -> Void
    let onEdit: () -> Void

    var body: some View {
        let stamp = Formatting.timestamp(ms: segment.startMs)
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
        .accessibilityLabel(Text(verbatim: "\(Formatting.spokenDuration(ms: segment.startMs)), \(segment.text)" + (isCorrected ? ", \(LS("transcript.corrected", table: .meetingDetail))" : "")))
        .accessibilityValue(isPlaying ? Text(L("transcript.playing", table: .meetingDetail)) : Text(verbatim: ""))
        .modifier(SeekAction(enabled: isPlayable, stamp: stamp, onSeek: onSeek))
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
    let key: String
    var date: Date?
    var isError = true

    var body: some View {
        Label {
            if key == "doc.regen.limited" {
                Text(L(key, table: .meetingDetail, (date ?? .now).formatted(.relative(presentation: .named))))
            } else {
                Text(L(key, table: .meetingDetail))
            }
        } icon: {
            Image(systemName: isError ? "exclamationmark.circle" : "info.circle")
        }
        .font(Typography.footnote)
        .foregroundStyle(isError ? Palette.danger : Palette.textSecondary)
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
                    Text(verbatim: Formatting.timestamp(ms: segment.startMs))
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
                            .disabled(!app.isOnline || text == segment.text || text.count > 2000 || conflictRevision != nil)
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
            let outcome = await model.submitCorrection(segmentID: segment.id, text: text, expectedRevision: expected, mutationID: mutation, app: app)
            saving = false
            switch outcome {
            case .saved: dismiss()
            case .conflict(let latest): conflictRevision = latest
            case .failed(let key): errorKey = key
            }
        }
    }
}
