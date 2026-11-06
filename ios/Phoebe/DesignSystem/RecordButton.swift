import SwiftUI

enum RecordButtonState: Sendable, Equatable {
    case ready, starting, recording, paused, stopping, disabled
}

struct RecordButton: View {
    let state: RecordButtonState
    var durationMs: Int?
    var disabledReason: LocalizedStringResource?
    let action: () -> Void

    @ScaledMetric(relativeTo: .largeTitle) private var diameter: CGFloat = 76
    @ScaledMetric(relativeTo: .title) private var glyph: CGFloat = 28
    @Environment(\.dynamicTypeSize) private var typeSize

    init(state: RecordButtonState, durationMs: Int? = nil, disabledReason: LocalizedStringResource? = nil, action: @escaping () -> Void) {
        self.state = state
        self.durationMs = durationMs
        self.disabledReason = disabledReason
        self.action = action
    }

    var body: some View {
        VStack(spacing: Spacing.sm) {
            Button(action: action) {
                ZStack {
                    Circle().fill(fill)
                    if state == .starting || state == .stopping {
                        ProgressView().tint(glyphColor)
                    } else {
                        Image(systemName: systemImage)
                            .font(.system(size: min(glyph, 40), weight: .semibold))
                            .foregroundStyle(glyphColor)
                    }
                }
                .frame(width: size, height: size)
                .contentShape(Circle())
            }
            .buttonStyle(PressScaleButtonStyle())
            .disabled(!isEnabled)
            .accessibilityLabel(Text(label))
            .accessibilityValue(accessibilityValue)
            .accessibilityHint(state == .ready ? Text(L("recordButton.hint.start")) : Text(""))
            .accessibilityAddTraits(state == .ready ? .startsMediaSession : [])

            Text(label)
                .font(Typography.subheadline.weight(.semibold))
                .foregroundStyle(isEnabled ? Palette.textPrimary : Palette.textTertiary)
                .accessibilityHidden(true)
            if state == .disabled, let disabledReason {
                Text(disabledReason)
                    .font(Typography.footnote)
                    .foregroundStyle(Palette.textSecondary)
                    .multilineTextAlignment(.center)
            }
        }
    }

    private var size: CGFloat { min(max(diameter, 76), 96) }
    private var isEnabled: Bool { state == .ready || state == .recording || state == .paused }

    private var fill: Color {
        switch state {
        case .recording: Palette.record
        case .disabled: Palette.surface2
        case .stopping: Palette.record.opacity(0.6)
        default: Palette.accent
        }
    }

    private var glyphColor: Color {
        switch state {
        case .recording, .stopping: Palette.onRecord
        case .disabled: Palette.textTertiary
        default: Palette.onAccent
        }
    }

    private var systemImage: String {
        switch state {
        case .recording: "pause.fill"
        case .paused: "play.fill"
        case .disabled: "mic.slash"
        default: "record.circle"
        }
    }

    private var label: LocalizedStringResource {
        switch state {
        case .recording, .stopping: L("recordButton.pause")
        case .paused: L("recordButton.resume")
        default: L("recordButton.record")
        }
    }

    private var accessibilityValue: Text {
        let stateWord: String? = switch state {
        case .recording: LS("recordButton.state.recording")
        case .paused: LS("recordButton.state.paused")
        default: nil
        }
        let duration = durationMs.map { Formatting.spokenDuration(ms: $0) }
        return Text(verbatim: [stateWord, duration].compactMap { $0 }.joined(separator: ", "))
    }
}

struct StopRecordButton: View {
    var isEnabled = true
    let action: () -> Void

    @ScaledMetric(relativeTo: .title) private var diameter: CGFloat = 56

    var body: some View {
        VStack(spacing: Spacing.sm) {
            Button(action: action) {
                Image(systemName: "stop.fill")
                    .font(.title2.weight(.semibold))
                    .foregroundStyle(isEnabled ? Palette.textPrimary : Palette.textTertiary)
                    .frame(width: min(max(diameter, 56), 72), height: min(max(diameter, 56), 72))
                    .background(Palette.surface2, in: Circle())
                    .contentShape(Circle())
            }
            .buttonStyle(PressScaleButtonStyle())
            .disabled(!isEnabled)
            .accessibilityLabel(Text(L("recordButton.stop")))
            .accessibilityHint(Text(L("recordButton.hint.stop")))

            Text(L("recordButton.stop"))
                .font(Typography.subheadline.weight(.semibold))
                .foregroundStyle(isEnabled ? Palette.textPrimary : Palette.textTertiary)
                .accessibilityHidden(true)
        }
    }
}

struct RecordControls: View {
    let state: RecordButtonState
    var durationMs: Int?
    var disabledReason: LocalizedStringResource?
    var showsStop: Bool
    let onMain: () -> Void
    let onStop: () -> Void

    var body: some View {
        HStack(alignment: .top, spacing: Spacing.xl) {
            RecordButton(state: state, durationMs: durationMs, disabledReason: disabledReason, action: onMain)
            if showsStop {
                StopRecordButton(isEnabled: state == .recording || state == .paused, action: onStop)
                    .padding(.top, Spacing.sm)
            }
        }
    }
}

#Preview {
    VStack(spacing: Spacing.xl) {
        RecordControls(state: .ready, showsStop: false, onMain: {}, onStop: {})
        RecordControls(state: .recording, durationMs: 724_000, showsStop: true, onMain: {}, onStop: {})
        RecordControls(state: .paused, showsStop: true, onMain: {}, onStop: {})
        RecordButton(state: .disabled, disabledReason: L("recordButton.record")) {}
    }
}
