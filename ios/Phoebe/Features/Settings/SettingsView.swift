import SwiftData
import SwiftUI

struct SettingsView: View {
    @Environment(AppModel.self) private var app
    @Environment(\.modelContext) private var context
    @State private var model = SettingsModel()
    @State private var output: OutputLanguage = ProfilePreferences.appOutputLanguage
    @Query private var profiles: [UserProfile]

    private var profile: UserProfile? { profiles.first { $0.id == app.user?.id } }

    var body: some View {
        @Bindable var model = model
        Form {
            usageSection
            planSection
            languageSection
            privacySection
            Section {
                NavigationLink {
                    PrivacyExplainerView()
                } label: {
                    Label { Text(L("settings.privacy.explainer", table: .settings)) } icon: { Image(systemName: "lock.shield") }
                }
            } footer: {
                Text(L("settings.privacy.noE2E", table: .settings))
            }
            accountSection
            Section {
                Button(role: .destructive) { model.showDeleteStep1 = true } label: {
                    Label { Text(L("settings.delete", table: .settings)) } icon: { Image(systemName: "trash") }
                }
                .foregroundStyle(Palette.danger)
                .frame(minHeight: Spacing.minTouchTarget)
            } footer: {
                Text(L("settings.delete.footer", table: .settings))
            }
            Section {} footer: {
                Text(L("settings.version", table: .settings, Self.version, Self.build))
                    .frame(maxWidth: .infinity, alignment: .center)
            }
        }
        .scrollContentBackground(.hidden)
        .background(Palette.bg)
        .navigationTitle(Text(L("settings.title", table: .settings)))
        .navigationBarTitleDisplayMode(.inline)
        .refreshable { await model.refreshUsage(app: app) }
        .task {
            output = ProfilePreferences.outputLanguage(app: app)
            model.refreshSyncPending(app: app)
            await model.refreshUsage(app: app)
        }
        .onChange(of: app.isOnline) { _, online in
            guard online else { return }
            Task { await model.retryPendingLanguage(app: app) }
        }
        .confirmationDialog(Text(L("settings.signout.confirm", table: .settings)), isPresented: $model.showSignOutConfirm, titleVisibility: .visible) {
            Button(role: .destructive) { Task { await model.signOut(app: app) } } label: { Text(L("settings.signout", table: .settings)) }
            Button(role: .cancel) {} label: { Text(L("common.cancel")) }
        } message: {
            let pending = model.pendingUploadCount(context: context)
            if pending > 0 { Text(L("settings.signout.pending", table: .settings, Int64(pending))) }
        }
        .confirmationDialog(Text(L("settings.delete.step1.title", table: .settings)), isPresented: $model.showDeleteStep1, titleVisibility: .visible) {
            Button(role: .destructive) {
                model.deleteConfirmText = ""
                model.showDeleteStep2 = true
            } label: { Text(L("settings.delete.step1.cta", table: .settings)) }
            Button(role: .cancel) {} label: { Text(L("common.cancel")) }
        } message: {
            Text(L("settings.delete.step1.body", table: .settings))
        }
        .alert(Text(L("settings.delete.step2.title", table: .settings)), isPresented: $model.showDeleteStep2) {
            TextField(text: $model.deleteConfirmText) { Text(L("settings.delete.step2.word", table: .settings)) }
                .autocorrectionDisabled()
                .textInputAutocapitalization(.characters)
            Button(role: .destructive) {
                guard model.deleteConfirmMatches(word: Self.confirmWord) else { return }
                Task { await model.deleteAccount(app: app) }
            } label: { Text(L("settings.delete.step2.cta", table: .settings)) }
            .disabled(!model.deleteConfirmMatches(word: Self.confirmWord))
            Button(role: .cancel) {} label: { Text(L("common.cancel")) }
        } message: {
            Text(L("settings.delete.step2.hint", table: .settings, Self.confirmWord))
        }
        .alert(Text(L("settings.delete.failed", table: .settings)), isPresented: $model.showDeleteFailed) {
            Button { Task { await model.deleteAccount(app: app) } } label: { Text(L("common.retry")) }
            Button(role: .cancel) {} label: { Text(L("common.cancel")) }
        }
        .confirmationDialog(Text(L("settings.localAudio.confirm", table: .settings, model.eligibleLocalAudioSize)), isPresented: $model.showLocalAudioConfirm, titleVisibility: .visible) {
            Button(role: .destructive) { model.deleteEligibleLocalAudio() } label: { Text(L("settings.localAudio.confirm.cta", table: .settings)) }
            Button(role: .cancel) {} label: { Text(L("common.cancel")) }
        }
        .overlay {
            if model.isDeletingAccount {
                ZStack {
                    Palette.scrim.ignoresSafeArea()
                    VStack(spacing: Spacing.md) {
                        ProgressView()
                        Text(L("settings.delete.progress", table: .settings))
                            .font(Typography.headline)
                            .foregroundStyle(Palette.textPrimary)
                    }
                    .padding(Spacing.xl)
                    .background(Palette.surface, in: RoundedRectangle(cornerRadius: Radius.lg, style: .continuous))
                    .accessibilityElement(children: .combine)
                    .onAppear {
                        AccessibilityNotification.Announcement(String(localized: L("settings.delete.progress", table: .settings))).post()
                    }
                }
            }
        }
    }

    // MARK: Usage

    @ViewBuilder private var usageSection: some View {
        Section {
            if model.usageState == .error {
                VStack(alignment: .leading, spacing: Spacing.sm) {
                    Label { Text(L("settings.usage.error", table: .settings)) } icon: { Image(systemName: "exclamationmark.triangle") }
                        .font(Typography.callout)
                        .foregroundStyle(Palette.danger)
                    SecondaryButton(L("common.retry"), systemImage: "arrow.clockwise") {
                        Task { await model.refreshUsage(app: app) }
                    }
                }
            }
            if let usage = app.usage {
                UsageCard(summary: UsageSummary(usage))
                if model.isStale(app: app), let updated = app.usageFetchedAt {
                    Text(L("settings.usage.stale", table: .settings, updated.formatted(date: .abbreviated, time: .shortened)))
                        .font(Typography.footnote)
                        .foregroundStyle(Palette.textSecondary)
                }
            } else if model.usageState != .error {
                UsageCard(summary: UsageSummary(.placeholder))
                    .redacted(reason: .placeholder)
                    .accessibilityHidden(true)
            }
        } header: {
            Text(L("settings.usage", table: .settings))
        }
    }

    @ViewBuilder private var planSection: some View {
        if let usage = app.usage {
            Section {
                LabeledContent {
                    Text(verbatim: "\(Self.planName(usage.entitlement.plan)) · \(LS("plan.status.\(usage.entitlement.status.rawValue)", table: .settings))")
                } label: {
                    Text(L("settings.plan.name", table: .settings))
                }
                if let end = usage.entitlement.periodEnd {
                    LabeledContent {
                        Text(end.formatted(date: .abbreviated, time: .omitted))
                    } label: {
                        Text(L("settings.plan.until", table: .settings))
                    }
                }
                AIAllowanceRow(allowance: AIAllowance(usage.aiGenerations))
            } header: {
                Text(L("settings.plan", table: .settings))
            } footer: {
                Text(L("settings.plan.footnote", table: .settings))
            }
        }
    }

    // MARK: Language

    private var languageSection: some View {
        Section {
            AppLanguageRow(title: L("settings.language.app", table: .settings))
            LabeledMenuPicker(title: L("settings.language.transcript", table: .settings), selection: transcriptBinding) {
                ForEach(TranscriptionLanguage.allCases, id: \.self) { Text(LanguageNames.transcription($0)).tag($0) }
            }
            LabeledMenuPicker(title: L("settings.language.output", table: .settings), selection: outputBinding) {
                ForEach(OutputLanguage.allCases, id: \.self) { Text(LanguageNames.output($0)).tag($0) }
            }
        } header: {
            Text(L("settings.language", table: .settings))
        } footer: {
            if model.outputSyncPending { Text(L("settings.syncPending", table: .settings)) }
        }
    }

    private var transcriptBinding: Binding<TranscriptionLanguage> {
        Binding {
            profile?.transcriptionLanguage ?? .auto
        } set: { value in
            ProfilePreferences.setTranscriptionLanguage(value, app: app)
        }
    }

    private var outputBinding: Binding<OutputLanguage> {
        Binding {
            output
        } set: { value in
            output = value
            Task { await model.setOutputLanguage(value, app: app) }
        }
    }

    // MARK: Data & privacy

    private var privacySection: some View {
        Section {
            LabeledContent {
                if let days = app.serverConfig?.audioRetentionDays {
                    Text(L("settings.retention.audio.value", table: .settings, Int64(days)))
                }
            } label: {
                Label { Text(L("settings.retention.audio", table: .settings)) } icon: { Image(systemName: "calendar.badge.clock") }
            }
            if app.serverConfig == nil {
                Text(L("onb.privacy.retention.unknown", table: .onboarding))
                    .font(Typography.footnote)
                    .foregroundStyle(Palette.textSecondary)
            }
            LabeledContent {
                Text(L("settings.retention.docs.value", table: .settings))
            } label: {
                Text(L("settings.retention.docs", table: .settings))
            }
            if let profile {
                Toggle(isOn: Binding {
                    profile.deleteLocalAudioAfterTranscript
                } set: { enabled in
                    model.setDeleteLocalAudio(enabled, profile: profile, context: context)
                }) {
                    Text(L("settings.localAudio", table: .settings))
                }
                .tint(Palette.accent)
            }
        } header: {
            Text(L("settings.privacy", table: .settings))
        } footer: {
            Text(L("settings.localAudio.footer", table: .settings))
        }
    }

    // MARK: Account

    private var accountSection: some View {
        Section {
            LabeledContent {
                if let name = app.user?.displayName, !name.isEmpty { Text(name) }
            } label: {
                Text(L(app.auth.signInMethod == .dev ? "settings.account.dev" : "settings.account.apple", table: .settings))
            }
            Button { model.showSignOutConfirm = true } label: {
                HStack(spacing: Spacing.sm) {
                    Label { Text(L("settings.signout", table: .settings)) } icon: { Image(systemName: "rectangle.portrait.and.arrow.right") }
                    if model.isSigningOut { ProgressView() }
                }
            }
            .foregroundStyle(Palette.accent)
            .disabled(model.isSigningOut)
            .frame(minHeight: Spacing.minTouchTarget)
        } header: {
            Text(L("settings.account", table: .settings))
        }
    }

    // MARK: Helpers

    private static var confirmWord: String { LS("settings.delete.step2.word", table: .settings) }
    private static var version: String { Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "" }
    private static var build: String { Bundle.main.object(forInfoDictionaryKey: "CFBundleVersion") as? String ?? "" }

    static func planName(_ plan: String) -> String {
        plan == "trial" ? LS("plan.trial", table: .settings) : plan
    }
}

struct UsageCard: View {
    let summary: UsageSummary

    var body: some View {
        VStack(alignment: .leading, spacing: Spacing.sm) {
            Label {
                Text(L("settings.usage.used", table: .settings, Int64(summary.usedMinutes), Int64(summary.quotaMinutes)))
                    .font(Typography.headline)
                    .monospacedDigit()
            } icon: {
                Image(systemName: icon).symbolRenderingMode(.hierarchical)
            }
            .foregroundStyle(Palette.textPrimary)
            ProgressView(value: summary.fraction)
                .tint(summary.condition == .normal ? Palette.accent : Palette.warning)
            if summary.condition != .planEnded {
                Text(verbatim: periodLine)
                    .font(Typography.subheadline)
                    .foregroundStyle(Palette.textSecondary)
                    .monospacedDigit()
            }
            if summary.reservedMinutes > 0 {
                Text(L("settings.usage.reserved", table: .settings, Int64(summary.reservedMinutes)))
                    .font(Typography.subheadline)
                    .foregroundStyle(Palette.textSecondary)
            }
            if let warning {
                Label { Text(warning.text) } icon: { Image(systemName: warning.icon) }
                    .font(Typography.callout)
                    .foregroundStyle(Palette.warning)
            }
        }
        .padding(.vertical, Spacing.xs)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(Text(L("settings.usage.a11y", table: .settings, Int64(summary.usedMinutes), Int64(summary.quotaMinutes), Int64(summary.condition == .planEnded ? 0 : summary.leftMinutes))))
        .accessibilityValue(warning.map { Text($0.text) } ?? Text(verbatim: ""))
    }

    private var icon: String {
        switch summary.condition {
        case .normal, .low: "gauge.with.dots.needle.33percent"
        case .exhausted: "gauge.with.dots.needle.100percent"
        case .planEnded: "calendar.badge.exclamationmark"
        }
    }

    private var periodLine: String {
        var parts = [LS("settings.usage.left", table: .settings, Int64(summary.leftMinutes))]
        if let end = summary.periodEnd {
            let date = end.formatted(date: .abbreviated, time: .omitted)
            parts.append(LS(summary.isTrial ? "settings.usage.ends" : "settings.usage.reset", table: .settings, date))
        }
        return parts.joined(separator: " · ")
    }

    private var warning: (text: LocalizedStringResource, icon: String)? {
        switch summary.condition {
        case .normal: nil
        case .low: (L("settings.usage.low", table: .settings), "exclamationmark.triangle")
        case .exhausted: (L(summary.isTrial ? "settings.usage.exhausted.trial" : "settings.usage.exhausted", table: .settings), "gauge.with.dots.needle.100percent")
        case .planEnded: (L("settings.plan.expired", table: .settings, summary.periodEnd?.formatted(date: .abbreviated, time: .omitted) ?? ""), "calendar.badge.exclamationmark")
        }
    }
}

struct PrivacyExplainerView: View {
    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: Spacing.xl) {
                OnboardingHeader(systemImage: "lock.shield", title: L("onb.privacy.title", table: .onboarding), alignment: .leading)
                PrivacyCard()
                Text(L("settings.privacy.noE2E", table: .settings))
                    .font(Typography.footnote)
                    .foregroundStyle(Palette.textSecondary)
            }
            .padding(.horizontal, Spacing.screenInsetCompact)
            .padding(.vertical, Spacing.xl)
            .readingWidth()
        }
        .background(Palette.bg)
        .navigationTitle(Text(L("settings.privacy.explainer", table: .settings)))
        .navigationBarTitleDisplayMode(.inline)
    }
}

private extension UsageDTO {
    static var placeholder: UsageDTO {
        UsageDTO(
            entitlement: EntitlementDTO(plan: "trial", status: .active, periodStart: nil, periodEnd: .now, quotaSeconds: 7200),
            consumedSeconds: 2520, reservedSeconds: 0, remainingSeconds: 4680,
            aiGenerations: AIGenerationsDTO(limit: 30, windowHours: 24, used: 0, reserved: 0, remaining: 30, nextSlotAt: nil)
        )
    }
}

private struct AIAllowanceRow: View {
    let allowance: AIAllowance

    var body: some View {
        VStack(alignment: .leading, spacing: Spacing.xs) {
            LabeledContent {
                Text(verbatim: allowance.remainingText())
                    .monospacedDigit()
            } label: {
                Label { Text(verbatim: allowance.label()) } icon: { Image(systemName: "sparkles") }
            }
            .labeledContentStyle(AdaptiveLabeledContentStyle())
            if let next = allowance.nextSlotText() {
                Label { Text(verbatim: next) } icon: { Image(systemName: "hourglass") }
                    .font(Typography.footnote)
                    .foregroundStyle(Palette.textSecondary)
            }
            Text(L("ai.allowance.footnote", table: .settings))
                .font(Typography.footnote)
                .foregroundStyle(Palette.textSecondary)
        }
        .accessibilityElement(children: .combine)
    }
}
