import SwiftUI
import UIKit

struct RecordingView: View {
    @Environment(AppModel.self) private var app
    @Environment(\.accessibilityVoiceOverEnabled) private var voiceOver
    @State private var model = RecordingModel()

    private var recorder: any RecordingEngineAPI { app.recorder }
    private var phase: RecordingPhase { recorder.phase }

    var body: some View {
        NavigationStack {
            content
                .background(Palette.bg.ignoresSafeArea())
                .toolbar {
                    if model.summary == nil && !phase.hasLiveSession {
                        ToolbarItem(placement: .cancellationAction) {
                            Button {
                                app.isRecordingPresented = false
                            } label: {
                                Image(systemName: "xmark").minTouchTarget()
                            }
                            .accessibilityLabel(Text(L("rec.close", table: .recording)))
                        }
                    }
                }
                .navigationBarTitleDisplayMode(.inline)
        }
        .interactiveDismissDisabled(true)
        .task {
            model.load(app: app)
            recorder.refreshPermission()
            if !phase.hasLiveSession { await recorder.prepare() }
        }
        .onChange(of: phase) { old, new in phaseChanged(from: old, to: new) }
        .sensoryFeedback(trigger: phase) { old, new in Self.haptic(from: old, to: new) }
        .sheet(isPresented: $model.showPermissionSheet) {
            PermissionExplanationSheet { Task { await model.permissionContinue(app: app) } }
        }
        .sheet(isPresented: $model.showRename) {
            RenameSheet(initial: model.title, placeholder: model.defaultTitle) { model.rename(to: $0, app: app) }
        }
        .confirmationDialog(Text(L("rec.stop.title", table: .recording)), isPresented: $model.showStopConfirm, titleVisibility: .visible) {
            Button(L("rec.stop.confirm", table: .recording)) { Task { await model.confirmStop(app: app) } }
            Button(L("rec.stop.cancel", table: .recording), role: .cancel) {}
        }
    }

    @ViewBuilder private var content: some View {
        if let summary = model.summary {
            StoppedSummaryView(summary: summary, next: model.nextStep(app: app), banner: recorder.banner) {
                app.openMeeting(summary.meetingID)
            }
            .task(id: summary.meetingID) {
                guard !voiceOver else { return }
                try? await Task.sleep(for: .seconds(4))
                if !Task.isCancelled, app.isRecordingPresented { app.openMeeting(summary.meetingID) }
            }
        } else if phase.hasLiveSession {
            LiveRecordingView(model: model)
        } else {
            ReadyRecordingView(model: model)
        }
    }

    private func phaseChanged(from old: RecordingPhase, to new: RecordingPhase) {
        if new == .interrupted {
            var text = AttributedString(LS("rec.interrupt.title", table: .recording))
            text.accessibilitySpeechAnnouncementPriority = .high
            AccessibilityNotification.Announcement(text).post()
        }
        if new == .stopped, old.hasLiveSession { model.finish(app: app) }
    }

    static func haptic(from old: RecordingPhase, to new: RecordingPhase) -> SensoryFeedback? {
        switch (old, new) {
        case (.starting, .recording): .start
        case (.paused, .recording), (.interrupted, .recording): .impact(weight: .light)
        case (.recording, .paused): .impact(weight: .light)
        case (_, .interrupted): .warning
        case (_, .stopped) where old.hasLiveSession: .stop
        default: nil
        }
    }
}

// MARK: - Ready (pre-record)

private struct ReadyRecordingView: View {
    @Environment(AppModel.self) private var app
    @Bindable var model: RecordingModel

    private var recorder: any RecordingEngineAPI { app.recorder }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: Spacing.lg) {
                VStack(alignment: .leading, spacing: Spacing.xs) {
                    Text(L("rec.title.label", table: .recording))
                        .font(Typography.subheadline)
                        .foregroundStyle(Palette.textSecondary)
                    TextField(text: $model.title, prompt: Text(verbatim: model.defaultTitle)) {
                        Text(L("rec.title.label", table: .recording))
                    }
                    .font(Typography.body)
                    .submitLabel(.done)
                    .padding(.horizontal, Spacing.md)
                    .frame(minHeight: Spacing.minTouchTarget)
                    .background(Palette.surface2, in: RoundedRectangle(cornerRadius: Radius.md, style: .continuous))
                    .overlay(RoundedRectangle(cornerRadius: Radius.md, style: .continuous).stroke(Palette.borderStrong, lineWidth: 1))
                    .onChange(of: model.title) { _, new in
                        if new.count > 200 { model.title = String(new.prefix(200)) }
                    }
                }

                infoCard

                LabeledMenuPicker(title: L("rec.ready.language", table: .recording), selection: $model.language) {
                    ForEach(TranscriptionLanguage.allCases, id: \.self) { lang in
                        Text(L("rec.language.\(lang.rawValue)", table: .recording)).tag(lang)
                    }
                }
                .font(Typography.body)
                .foregroundStyle(Palette.textPrimary)

                if let notice = ReadyNotice.make(usage: app.usage) {
                    Banner(
                        tone: .warning,
                        systemImage: notice == .plan ? "calendar.badge.exclamationmark" : "gauge.with.dots.needle.100percent",
                        message: L(notice == .plan ? "rec.ready.plan" : "rec.ready.quota", table: .recording)
                    )
                }
                if !app.isOnline && !model.offlineBannerDismissed {
                    Banner(tone: .neutral, systemImage: "wifi.slash", message: L("rec.offline", table: .recording)) {
                        model.offlineBannerDismissed = true
                    }
                }
                if let banner = recorder.banner {
                    RecordingBannerView(banner: banner) { Task { await model.start(app: app) } }
                }
            }
            .padding(.horizontal, Spacing.screenInsetCompact)
            .padding(.vertical, Spacing.lg)
            .readingWidth()
        }
        .scrollDismissesKeyboard(.interactively)
        .safeAreaInset(edge: .bottom) {
            VStack(spacing: Spacing.sm) {
                RecordButton(
                    state: buttonState,
                    disabledReason: recorder.micPermission == .denied ? L("rec.perm.denied.body", table: .recording) : nil
                ) {
                    Task { await model.recordTapped(app: app) }
                }
                if recorder.phase == .starting {
                    Text(L("rec.starting", table: .recording))
                        .font(Typography.footnote)
                        .foregroundStyle(Palette.textSecondary)
                }
                if recorder.micPermission == .denied {
                    SecondaryButton(L("rec.perm.denied.cta", table: .recording), systemImage: "gear") {
                        if let url = URL(string: UIApplication.openSettingsURLString) { UIApplication.shared.open(url) }
                    }
                }
            }
            .padding(.vertical, Spacing.lg)
            .frame(maxWidth: .infinity)
            .background(Palette.bg)
        }
    }

    private var buttonState: RecordButtonState {
        if recorder.micPermission == .denied { return .disabled }
        return recorder.phase == .starting ? .starting : .ready
    }

    private var infoCard: some View {
        VStack(alignment: .leading, spacing: Spacing.md) {
            InfoRow(systemImage: "person.2", text: L("rec.ready.consent", table: .recording))
            InfoRow(systemImage: "lock.shield", text: cloudText)
            InfoRow(systemImage: "iphone", text: L("rec.ready.calls", table: .recording))
        }
        .padding(Spacing.lg)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Palette.surface, in: RoundedRectangle(cornerRadius: Radius.lg, style: .continuous))
    }

    private var cloudText: LocalizedStringResource {
        if let days = app.serverConfig?.audioRetentionDays {
            return L("rec.ready.cloud", table: .recording, Int64(days))
        }
        return L("rec.ready.cloud.unknown", table: .recording)
    }
}

private struct InfoRow: View {
    let systemImage: String
    let text: LocalizedStringResource
    @ScaledMetric(relativeTo: .callout) private var iconWidth: CGFloat = 24

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: Spacing.md) {
            Image(systemName: systemImage)
                .foregroundStyle(Palette.accent)
                .frame(width: iconWidth)
                .accessibilityHidden(true)
            Text(text)
                .font(Typography.callout)
                .foregroundStyle(Palette.textPrimary)
                .fixedSize(horizontal: false, vertical: true)
        }
    }
}

// MARK: - Live (recording / paused / interrupted / stopping)

private struct LiveRecordingView: View {
    @Environment(AppModel.self) private var app
    @Bindable var model: RecordingModel
    @FocusState private var focusedPoint: String?
    @FocusState private var newPointFocused: Bool

    private var recorder: any RecordingEngineAPI { app.recorder }
    private var phase: RecordingPhase { recorder.phase }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: Spacing.lg) {
                statusRow
                if let banner = recorder.banner {
                    RecordingBannerView(banner: banner, onRetry: nil)
                }
                if !app.isOnline && !model.offlineBannerDismissed {
                    Banner(tone: .neutral, systemImage: "wifi.slash", message: L("rec.offline", table: .recording)) {
                        model.offlineBannerDismissed = true
                    }
                }
                timerBlock
                notesCard
            }
            .padding(.horizontal, Spacing.screenInsetCompact)
            .padding(.vertical, Spacing.lg)
            .readingWidth()
        }
        .scrollDismissesKeyboard(.interactively)
        .safeAreaInset(edge: .bottom) { controls }
        .accessibilityAction(.magicTap) { Task { await model.mainTapped(app: app) } }
    }

    private var statusRow: some View {
        HStack(spacing: Spacing.md) {
            switch phase {
            case .recording, .starting: LiveIndicator()
            case .interrupted: StatusChip(.interrupted)
            case .stopping: StatusChip(L("rec.stopping", table: .recording), systemImage: "square.and.arrow.down", tone: .neutral)
            default: StatusChip(L("rec.state.paused", table: .recording), systemImage: "pause.fill", tone: .neutral)
            }
            Button {
                model.showRename = true
            } label: {
                HStack(spacing: Spacing.xs) {
                    Text(verbatim: model.displayTitle)
                        .font(Typography.headline)
                        .foregroundStyle(Palette.textPrimary)
                        .lineLimit(2)
                        .multilineTextAlignment(.leading)
                    Image(systemName: "pencil")
                        .imageScale(.small)
                        .foregroundStyle(Palette.textSecondary)
                        .accessibilityHidden(true)
                }
                .frame(minHeight: Spacing.minTouchTarget, alignment: .leading)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityHint(Text(L("rec.rename.hint", table: .recording)))
            Spacer(minLength: 0)
        }
    }

    private var timerBlock: some View {
        TimelineView(.periodic(from: .now, by: 0.5)) { _ in
            VStack(spacing: Spacing.sm) {
                Text(verbatim: Formatting.timer(ms: recorder.activeDurationMs))
                    .timerTextStyle()
                    .foregroundStyle(phase == .recording ? Palette.record : Palette.textPrimary)
                    .contentTransition(.numericText())
                    .accessibilityLabel(Text(L("rec.timer.a11y", table: .recording)))
                    .accessibilityValue(Text(verbatim: Formatting.spokenDuration(ms: recorder.activeDurationMs)))
                    .accessibilityAddTraits(.updatesFrequently)
                LevelMeter(level: phase == .recording ? recorder.inputLevel : 0)
                Text(L("rec.saved.segments", table: .recording, Int64(recorder.segmentCount)))
                    .font(Typography.footnote)
                    .foregroundStyle(Palette.textSecondary)
            }
            .frame(maxWidth: .infinity)
        }
        .padding(.vertical, Spacing.md)
    }

    private var notesCard: some View {
        VStack(alignment: .leading, spacing: Spacing.md) {
            HStack {
                Text(L("rec.notes.label", table: .recording))
                    .font(Typography.headline)
                    .foregroundStyle(Palette.textPrimary)
                    .accessibilityAddTraits(.isHeader)
                Spacer()
                SaveIndicator(state: model.notesSaving ? .saving : .local)
            }
            ForEach(model.notes) { note in
                HStack(alignment: .firstTextBaseline, spacing: Spacing.sm) {
                    Text(verbatim: Formatting.timestamp(ms: note.atMs ?? 0))
                        .font(Typography.footnote.monospacedDigit())
                        .foregroundStyle(Palette.textSecondary)
                        .accessibilityHidden(true)
                    TextField(text: Binding(
                        get: { note.text },
                        set: { model.updatePoint(note.id, text: $0, app: app) }
                    ), axis: .vertical) {
                        Text(L("rec.notes.placeholder", table: .recording))
                    }
                    .font(Typography.body)
                    .focused($focusedPoint, equals: note.id)
                    .accessibilityLabel(Text(L("rec.notes.point.a11y", table: .recording, Formatting.timestamp(ms: note.atMs ?? 0), note.text)))
                }
                .frame(minHeight: Spacing.minTouchTarget)
            }
            TextField(text: $model.draftPoint, prompt: Text(L("rec.notes.placeholder", table: .recording))) {
                Text(L("rec.notes.placeholder", table: .recording))
            }
            .font(Typography.body)
            .submitLabel(.return)
            .focused($newPointFocused)
            .onSubmit {
                model.addPoint(atMs: recorder.activeDurationMs, app: app)
                newPointFocused = true
            }
            .padding(.horizontal, Spacing.md)
            .frame(minHeight: Spacing.minTouchTarget)
            .background(Palette.surface2, in: RoundedRectangle(cornerRadius: Radius.md, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: Radius.md, style: .continuous).stroke(Palette.borderStrong, lineWidth: 1))
            Text(L("rec.notes.hint", table: .recording))
                .font(Typography.footnote)
                .foregroundStyle(Palette.textSecondary)
        }
        .padding(Spacing.lg)
        .background(Palette.surface, in: RoundedRectangle(cornerRadius: Radius.lg, style: .continuous))
    }

    private var resumeBlocked: Bool { phase == .interrupted && !recorder.canResume }

    private var controls: some View {
        VStack(spacing: Spacing.sm) {
            RecordControls(
                state: phase.buttonState,
                durationMs: recorder.activeDurationMs,
                showsStop: true,
                onMain: { Task { await model.mainTapped(app: app) } },
                onStop: { model.showStopConfirm = true }
            )
            .disabled(resumeBlocked)
            .opacity(resumeBlocked ? 0.4 : 1)
            if resumeBlocked {
                Text(L("rec.interrupt.waiting", table: .recording))
                    .font(Typography.footnote)
                    .foregroundStyle(Palette.textSecondary)
            }
        }
        .padding(.vertical, Spacing.lg)
        .frame(maxWidth: .infinity)
        .background(Palette.bg)
    }
}

private struct LiveIndicator: View {
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var dim = false

    var body: some View {
        HStack(spacing: Spacing.xs) {
            Image(systemName: "circle.fill")
                .imageScale(.small)
                .opacity(dim ? Motion.pulseMinOpacity : 1)
            Text(L("rec.state.rec", table: .recording)).fontWeight(.bold)
            Text(L("rec.state.recording", table: .recording))
        }
        .font(Typography.caption)
        .foregroundStyle(Tone.record.foreground)
        .padding(.vertical, Spacing.xs)
        .padding(.horizontal, Spacing.sm)
        .background(Tone.record.fill, in: RoundedRectangle(cornerRadius: Radius.sm, style: .continuous))
        .accessibilityElement(children: .combine)
        .onAppear {
            guard !reduceMotion else { return }
            withAnimation(Motion.recordPulse) { dim = true }
        }
    }
}

private struct LevelMeter: View {
    let level: Float
    private static let weights: [CGFloat] = [0.35, 0.6, 0.85, 1, 0.75, 0.55, 0.3]

    var body: some View {
        HStack(alignment: .center, spacing: Spacing.xs) {
            ForEach(Array(Self.weights.enumerated()), id: \.offset) { _, weight in
                Capsule()
                    .fill(Palette.textTertiary)
                    .frame(width: 6, height: 4 + 28 * weight * CGFloat(min(max(level, 0), 1)))
            }
        }
        .frame(height: 32)
        .animation(Motion.fast, value: level)
        .accessibilityHidden(true)
    }
}

struct RecordingBannerView: View {
    @Environment(AppModel.self) private var app
    let banner: RecordingBanner
    let onRetry: (() -> Void)?

    var body: some View {
        let content = RecordingBannerContent.make(banner)
        Banner(
            tone: content.tone,
            systemImage: content.systemImage,
            title: content.titleKey.map { L($0, table: .recording) },
            message: RecordingBannerContent.message(banner),
            actions: content.retriesStart && onRetry != nil ? [BannerAction(L("common.retry")) { onRetry?() }] : [],
            onClose: content.closable ? { app.recorder.dismissBanner() } : nil
        )
        .task(id: banner) {
            guard let delay = content.autoDismissAfter else { return }
            try? await Task.sleep(for: delay)
            if !Task.isCancelled, app.recorder.banner == banner { app.recorder.dismissBanner() }
        }
    }
}

// MARK: - Stopped summary

private struct StoppedSummaryView: View {
    let summary: RecordingSummary
    let next: RecordingNextStep
    let banner: RecordingBanner?
    let onOpen: () -> Void

    var body: some View {
        ScrollView {
            VStack(spacing: Spacing.lg) {
                if let banner, case .storageStopped = banner {
                    RecordingBannerView(banner: banner, onRetry: nil)
                } else if let banner, case .limitReached = banner {
                    RecordingBannerView(banner: banner, onRetry: nil)
                }
                Image(systemName: "checkmark.circle.fill")
                    .font(.system(size: 48))
                    .foregroundStyle(Palette.success)
                    .accessibilityHidden(true)
                Text(L("rec.stopped.title", table: .recording))
                    .font(Typography.title)
                    .foregroundStyle(Palette.textPrimary)
                    .accessibilityAddTraits(.isHeader)
                Text(L("rec.stopped.duration", table: .recording, Formatting.timer(ms: summary.durationMs)))
                    .font(Typography.body.monospacedDigit())
                    .foregroundStyle(Palette.textPrimary)
                    .accessibilityLabel(Text(L("rec.stopped.duration", table: .recording, Formatting.spokenDuration(ms: summary.durationMs))))
                Label { Text(L("save.local")) } icon: { Image(systemName: "iphone") }
                    .font(Typography.callout)
                    .foregroundStyle(Palette.textSecondary)
                Text(L(next.key, table: .recording))
                    .font(Typography.callout)
                    .foregroundStyle(Palette.textSecondary)
                    .multilineTextAlignment(.center)
            }
            .padding(Spacing.screenInsetCompact)
            .padding(.top, Spacing.xxl)
            .frame(maxWidth: .infinity)
            .readingWidth()
        }
        .safeAreaInset(edge: .bottom) {
            PrimaryButton(L("rec.stopped.open", table: .recording), action: onOpen)
                .padding(Spacing.screenInsetCompact)
                .background(Palette.bg)
        }
    }
}

// MARK: - Sheets

private struct PermissionExplanationSheet: View {
    let onContinue: () -> Void

    var body: some View {
        VStack(spacing: Spacing.lg) {
            Image(systemName: "mic")
                .font(.system(size: 44, weight: .semibold))
                .foregroundStyle(Palette.accent)
                .accessibilityHidden(true)
            Text(L("rec.perm.explain.title", table: .recording))
                .font(Typography.title)
                .foregroundStyle(Palette.textPrimary)
                .accessibilityAddTraits(.isHeader)
            Text(L("rec.perm.explain.body", table: .recording))
                .font(Typography.body)
                .foregroundStyle(Palette.textSecondary)
                .multilineTextAlignment(.center)
            Spacer(minLength: 0)
            PrimaryButton(L("rec.perm.explain.cta", table: .recording), action: onContinue)
        }
        .padding(Spacing.xl)
        .padding(.top, Spacing.xl)
        .presentationDetents([.medium, .large])
        .background(Palette.surface.ignoresSafeArea())
    }
}

private struct RenameSheet: View {
    @Environment(\.dismiss) private var dismiss
    let initial: String
    let placeholder: String
    let onSave: (String) -> Void
    @State private var text = ""
    @FocusState private var focused: Bool

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    TextField(text: $text, prompt: Text(verbatim: placeholder)) {
                        Text(L("rec.title.label", table: .recording))
                    }
                    .focused($focused)
                    .submitLabel(.done)
                    .onSubmit(save)
                } header: {
                    Text(L("rec.title.label", table: .recording))
                } footer: {
                    if text.count > 200 {
                        Label { Text(L("rec.title.error.long", table: .recording)) } icon: { Image(systemName: "exclamationmark.circle") }
                            .foregroundStyle(Palette.danger)
                    }
                }
            }
            .navigationTitle(Text(L("rec.rename.title", table: .recording)))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button(L("common.cancel")) { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button(L("common.save"), action: save).disabled(text.count > 200)
                }
            }
        }
        .presentationDetents([.medium])
        .onAppear {
            text = initial
            focused = true
        }
    }

    private func save() {
        guard text.count <= 200 else { return }
        onSave(text)
        dismiss()
    }
}
