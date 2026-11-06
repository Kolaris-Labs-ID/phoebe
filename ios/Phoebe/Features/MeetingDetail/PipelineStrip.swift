import SwiftUI

struct PipelineStrip: View {
    let stage: StageInputs
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize

    var body: some View {
        let layout = dynamicTypeSize.isAccessibilitySize
            ? AnyLayout(VStackLayout(alignment: .leading, spacing: Spacing.md))
            : AnyLayout(HStackLayout(alignment: .top, spacing: Spacing.sm))
        layout {
            StepView(step: .upload, state: DetailStatus.upload(stage), stacked: dynamicTypeSize.isAccessibilitySize)
            StepView(step: .transcript, state: DetailStatus.transcript(stage), stacked: dynamicTypeSize.isAccessibilitySize)
            StepView(step: .notes, state: DetailStatus.notes(stage), stacked: dynamicTypeSize.isAccessibilitySize)
        }
        .padding(Spacing.md)
        .frame(maxWidth: .infinity)
        .background(Palette.surface, in: RoundedRectangle(cornerRadius: Radius.lg, style: .continuous))
    }
}

enum PipelineStep {
    case upload, transcript, notes

    var labelKey: String {
        switch self {
        case .upload: "detail.step.upload"
        case .transcript: "detail.step.transcript"
        case .notes: "detail.step.notes"
        }
    }

    func icon(for state: StepState) -> String {
        switch (self, state) {
        case (_, .waitingConnection): "wifi.slash"
        case (_, .waitingQuota): "gauge.with.dots.needle.100percent"
        case (_, .planExpired): "calendar.badge.exclamationmark"
        case (_, .waitingAISlot): "hourglass"
        case (_, .notGenerated): "note.text"
        case (.upload, .pending): "icloud.and.arrow.up"
        case (.upload, .active): "arrow.up.circle"
        case (.upload, .verifying), (.upload, .done): "checkmark.icloud"
        case (.upload, .failed): "exclamationmark.icloud"
        case (.transcript, .pending): "text.quote"
        case (.transcript, .active): "waveform"
        case (.notes, .pending): "note.text"
        case (.notes, .active): "sparkles"
        case (_, .done): "checkmark.circle"
        case (_, .failed): "exclamationmark.triangle"
        case (_, .verifying): "checkmark.icloud"
        }
    }

    static func stateKey(_ step: PipelineStep, _ state: StepState) -> String {
        switch state {
        case .pending: "step.pending"
        case .waitingConnection: "step.waitingConnection"
        case .waitingQuota: "step.waitingQuota"
        case .planExpired: "step.planExpired"
        case .waitingAISlot: "step.waitingAISlot"
        case .notGenerated: "step.notGenerated"
        case .active: "step.processing"
        case .verifying: "step.verifying"
        case .done: step == .upload ? "step.done" : "step.ready"
        case .failed: "step.failed"
        }
    }
}

private struct StepView: View {
    let step: PipelineStep
    let state: StepState
    let stacked: Bool

    var body: some View {
        VStack(alignment: stacked ? .leading : .center, spacing: Spacing.xs) {
            HStack(spacing: Spacing.xs) {
                Image(systemName: step.icon(for: state))
                    .symbolRenderingMode(.hierarchical)
                    .foregroundStyle(color)
                Text(L(step.labelKey, table: .meetingDetail))
                    .font(Typography.subheadline.weight(.semibold))
                    .foregroundStyle(Palette.textPrimary)
                    .lineLimit(stacked ? nil : 1)
                    .minimumScaleFactor(0.8)
            }
            HStack(spacing: Spacing.xs) {
                if case .active(nil) = state { ProgressView().controlSize(.mini) }
                Text(stateText)
                    .font(Typography.caption.monospacedDigit())
                    .foregroundStyle(color)
            }
        }
        .frame(maxWidth: .infinity, alignment: stacked ? .leading : .center)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(Text(verbatim: "\(LS(step.labelKey, table: .meetingDetail)): \(String(localized: stateText))"))
    }

    private var stateText: LocalizedStringResource {
        if case .active(let percent?) = state { return L("step.percent", table: .meetingDetail, Int64(percent)) }
        return L(PipelineStep.stateKey(step, state), table: .meetingDetail)
    }

    private var color: Color {
        switch state {
        case .done: Palette.success
        case .failed: Palette.danger
        case .active, .verifying: Palette.info
        case .waitingConnection, .waitingQuota, .planExpired, .waitingAISlot: Palette.warning
        case .pending, .notGenerated: Palette.textSecondary
        }
    }
}

// MARK: - Player

struct PlayerCard: View {
    @Environment(AppModel.self) private var app
    let player: PlayerModel
    let availability: PlaybackAvailability
    let meetingID: UUID
    let durationMs: Int?

    var body: some View {
        switch availability {
        case .none:
            EmptyView()
        case .expired:
            disabledCard(icon: "speaker.slash", key: "detail.player.expired")
        case .offline:
            disabledCard(icon: "wifi.slash", key: "detail.player.offline")
        case .local, .cloud:
            controls
                .onAppear { player.configure(durationMs: durationMs) }
        }
    }

    private func disabledCard(icon: String, key: String) -> some View {
        Label { Text(L(key, table: .meetingDetail)) } icon: { Image(systemName: icon) }
            .font(Typography.callout)
            .foregroundStyle(Palette.textSecondary)
            .padding(Spacing.md)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Palette.surface, in: RoundedRectangle(cornerRadius: Radius.lg, style: .continuous))
            .accessibilityElement(children: .combine)
    }

    private var total: Int { max(player.durationMs, durationMs ?? 0) }

    private var controls: some View {
        VStack(spacing: Spacing.sm) {
            HStack(spacing: Spacing.lg) {
                Button { player.skip(by: -15_000) } label: { Image(systemName: "gobackward.15").font(.title3).minTouchTarget() }
                    .accessibilityLabel(Text(L("detail.player.back15", table: .meetingDetail)))
                Button {
                    Task { await player.togglePlay(meetingID: meetingID, availability: availability, app: app) }
                } label: {
                    ZStack {
                        if case .downloading = player.phase {
                            ProgressView()
                        } else {
                            Image(systemName: player.isPlaying ? "pause.fill" : "play.fill").font(.title2)
                        }
                    }
                    .minTouchTarget()
                }
                .disabled(isDownloading || app.recorder.phase.hasLiveSession)
                .accessibilityLabel(Text(L(player.isPlaying ? "detail.player.pause" : "detail.player.play", table: .meetingDetail)))
                Button { player.skip(by: 15_000) } label: { Image(systemName: "goforward.15").font(.title3).minTouchTarget() }
                    .accessibilityLabel(Text(L("detail.player.fwd15", table: .meetingDetail)))
                Spacer()
                Text(verbatim: "\(Formatting.timer(ms: player.currentMs)) / \(Formatting.timer(ms: total))")
                    .font(Typography.subheadline.monospacedDigit())
                    .foregroundStyle(Palette.textSecondary)
                    .accessibilityHidden(true)
            }
            .tint(Palette.accent)
            .foregroundStyle(Palette.accent)

            Slider(
                value: Binding(get: { Double(player.currentMs) }, set: { player.seek(to: Int($0)) }),
                in: 0...Double(max(total, 1))
            ) {
                Text(L("detail.player.position", table: .meetingDetail))
            }
            .tint(Palette.accent)
            .accessibilityValue(Text(L("detail.player.position.value", table: .meetingDetail, Formatting.spokenDuration(ms: player.currentMs), Formatting.spokenDuration(ms: total))))
            .accessibilityAdjustableAction { direction in
                switch direction {
                case .increment: player.skip(by: 15_000)
                case .decrement: player.skip(by: -15_000)
                @unknown default: break
                }
            }

            if case .downloading(let progress) = player.phase {
                ProgressView(value: progress) {
                    Text(L("detail.player.downloading", table: .meetingDetail, Int64((progress * 100).rounded())))
                        .font(Typography.footnote)
                        .foregroundStyle(Palette.textSecondary)
                }
            }
            if player.phase == .failed {
                HStack {
                    Text(L("detail.player.error", table: .meetingDetail))
                        .font(Typography.footnote)
                        .foregroundStyle(Palette.danger)
                    Spacer()
                    Button(L("common.retry")) {
                        player.stop()
                        Task { await player.play(from: nil, meetingID: meetingID, availability: availability, app: app) }
                    }
                    .buttonStyle(.bordered)
                    .tint(Palette.accent)
                }
            }
        }
        .padding(Spacing.md)
        .background(Palette.surface, in: RoundedRectangle(cornerRadius: Radius.lg, style: .continuous))
    }

    private var isDownloading: Bool {
        if case .downloading = player.phase { return true }
        return false
    }
}
