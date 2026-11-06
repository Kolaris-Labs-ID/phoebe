import AuthenticationServices
import SwiftUI

struct OnboardingView: View {
    @Environment(AppModel.self) private var app

    var body: some View {
        switch app.session {
        case .onboarding(.language):
            NavigationStack { OnboardingLanguageStep() }
        case .onboarding:
            NavigationStack(path: path) {
                OnboardingWelcomeStep()
                    .navigationDestination(for: OnboardingStep.self) { step in
                        if step == .privacy { OnboardingPrivacyStep() } else { OnboardingSignInStep(stepNumber: 3) }
                    }
            }
        default:
            NavigationStack { OnboardingSignInStep(stepNumber: nil) }
        }
    }

    private var path: Binding<[OnboardingStep]> {
        Binding {
            guard case .onboarding(let step) = app.session else { return [] }
            return [OnboardingStep.privacy, .signIn].filter { $0.rawValue <= step.rawValue }
        } set: { newPath in
            app.advanceOnboarding(to: newPath.last ?? .welcome)
        }
    }
}

// MARK: - Shared pieces

struct OnboardingHeader: View {
    let systemImage: String
    let title: LocalizedStringResource
    var alignment: HorizontalAlignment = .center
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    @ScaledMetric(relativeTo: .largeTitle) private var iconSize: CGFloat = 64
    @AccessibilityFocusState private var titleFocused: Bool

    var body: some View {
        VStack(alignment: alignment, spacing: Spacing.lg) {
            if dynamicTypeSize < .accessibility1 {
                Image(systemName: systemImage)
                    .font(.system(size: iconSize))
                    .symbolRenderingMode(.hierarchical)
                    .foregroundStyle(Palette.accent)
                    .accessibilityHidden(true)
            }
            Text(title)
                // Long Indonesian words break mid-word at display size once text is accessibility-sized.
                .font(dynamicTypeSize.isAccessibilitySize ? Typography.title : Typography.display)
                .foregroundStyle(Palette.textPrimary)
                .multilineTextAlignment(alignment == .center ? .center : .leading)
                .fixedSize(horizontal: false, vertical: true)
                .accessibilityAddTraits(.isHeader)
                .accessibilityFocused($titleFocused)
        }
        .frame(maxWidth: .infinity, alignment: alignment == .center ? .center : .leading)
        .task {
            try? await Task.sleep(for: .milliseconds(350))
            titleFocused = true
        }
    }
}

struct OnboardingStepIndicator: View {
    let step: Int
    static let total = 3

    var body: some View {
        Text(L("onb.step", table: .onboarding, Int64(step), Int64(Self.total)))
            .font(Typography.footnote)
            .foregroundStyle(Palette.textSecondary)
            .monospacedDigit()
            .accessibilityLabel(Text(L("onb.step.a11y", table: .onboarding, Int64(step), Int64(Self.total))))
    }
}

struct OnboardingInfoRow: View {
    let systemImage: String
    let title: LocalizedStringResource
    let message: LocalizedStringResource
    var messageFont: Font = Typography.subheadline

    var body: some View {
        HStack(alignment: .top, spacing: Spacing.md) {
            Image(systemName: systemImage)
                .font(Typography.body)
                .symbolRenderingMode(.hierarchical)
                .foregroundStyle(Palette.accent)
                .frame(width: Spacing.xxl)
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: Spacing.xs) {
                Text(title).font(Typography.headline).foregroundStyle(Palette.textPrimary)
                Text(message)
                    .font(messageFont)
                    .foregroundStyle(Palette.textSecondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            Spacer(minLength: 0)
        }
        .accessibilityElement(children: .combine)
    }
}

struct OnboardingScaffold<Content: View, Footer: View>: View {
    @ViewBuilder let content: Content
    @ViewBuilder let footer: Footer

    var body: some View {
        ScrollView {
            VStack(spacing: Spacing.xl) { content }
                .padding(.horizontal, Spacing.screenInsetCompact)
                .padding(.vertical, Spacing.xl)
                .readingWidth()
        }
        .scrollDismissesKeyboard(.interactively)
        .background(Palette.bg)
        .safeAreaInset(edge: .bottom) {
            VStack(spacing: Spacing.md) { footer }
                .padding(.horizontal, Spacing.screenInsetCompact)
                .padding(.top, Spacing.md)
                .padding(.bottom, Spacing.sm)
                .readingWidth()
                .background(Palette.bg)
        }
    }
}

struct PrivacyCard: View {
    @Environment(AppModel.self) private var app

    var body: some View {
        VStack(alignment: .leading, spacing: Spacing.lg) {
            OnboardingInfoRow(systemImage: "mic", title: L("onb.privacy.mic.title", table: .onboarding), message: L("onb.privacy.mic.body", table: .onboarding), messageFont: Typography.callout)
            Divider().overlay(Palette.separator)
            OnboardingInfoRow(systemImage: "icloud", title: L("onb.privacy.cloud.title", table: .onboarding), message: L("onb.privacy.cloud.body", table: .onboarding), messageFont: Typography.callout)
            Divider().overlay(Palette.separator)
            OnboardingInfoRow(systemImage: "person.2", title: L("onb.privacy.consent.title", table: .onboarding), message: L("onb.privacy.consent.body", table: .onboarding), messageFont: Typography.callout)
            Divider().overlay(Palette.separator)
            OnboardingInfoRow(systemImage: "calendar.badge.clock", title: L("onb.privacy.retention.title", table: .onboarding), message: retentionMessage, messageFont: Typography.callout)
            Divider().overlay(Palette.separator)
            OnboardingInfoRow(systemImage: "sparkles", title: L("onb.privacy.ai.title", table: .onboarding), message: L("onb.privacy.ai.body", table: .onboarding), messageFont: Typography.callout)
        }
        .padding(Spacing.lg)
        .background(Palette.surface, in: RoundedRectangle(cornerRadius: Radius.lg, style: .continuous))
    }

    private var retentionMessage: LocalizedStringResource {
        if let days = app.serverConfig?.audioRetentionDays {
            return L("onb.privacy.retention.body", table: .onboarding, Int64(days))
        }
        return L("onb.privacy.retention.unknown", table: .onboarding)
    }
}

// MARK: - Steps

struct OnboardingWelcomeStep: View {
    @Environment(AppModel.self) private var app

    var body: some View {
        OnboardingScaffold {
            if app.signOutReason == .accountDeleted {
                Banner(tone: .success, systemImage: "checkmark.circle", message: L("settings.delete.done", table: .settings))
            }
            OnboardingHeader(systemImage: "waveform", title: L("onb.welcome.title", table: .onboarding))
            VStack(spacing: Spacing.lg) {
                OnboardingInfoRow(systemImage: "mic", title: L("onb.welcome.f1.title", table: .onboarding), message: L("onb.welcome.f1.body", table: .onboarding))
                OnboardingInfoRow(systemImage: "text.quote", title: L("onb.welcome.f2.title", table: .onboarding), message: L("onb.welcome.f2.body", table: .onboarding))
                OnboardingInfoRow(systemImage: "doc.text", title: L("onb.welcome.f3.title", table: .onboarding), message: L("onb.welcome.f3.body", table: .onboarding))
            }
        } footer: {
            PrimaryButton(L("onb.next", table: .onboarding)) { app.advanceOnboarding(to: .privacy) }
            OnboardingStepIndicator(step: 1)
        }
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                Button { app.advanceOnboarding(to: .privacy) } label: { Text(L("onb.skip", table: .onboarding)) }
                    .minTouchTarget()
            }
        }
        .navigationBarTitleDisplayMode(.inline)
    }
}

struct OnboardingPrivacyStep: View {
    @Environment(AppModel.self) private var app

    var body: some View {
        OnboardingScaffold {
            OnboardingHeader(systemImage: "lock.shield", title: L("onb.privacy.title", table: .onboarding), alignment: .leading)
            PrivacyCard()
        } footer: {
            PrimaryButton(L("onb.privacy.cta", table: .onboarding)) { app.advanceOnboarding(to: .signIn) }
            OnboardingStepIndicator(step: 2)
        }
        .navigationBarTitleDisplayMode(.inline)
        .task { if app.serverConfig == nil { await app.refreshConfig() } }
    }
}

struct OnboardingSignInStep: View {
    let stepNumber: Int?
    @Environment(AppModel.self) private var app
    @Environment(\.colorScheme) private var colorScheme
    @State private var model = SignInModel()

    var body: some View {
        OnboardingScaffold {
            banners
            OnboardingHeader(systemImage: "doc.text", title: L("onb.signin.title", table: .onboarding))
            Text(L("onb.signin.body", table: .onboarding))
                .font(Typography.footnote)
                .foregroundStyle(Palette.textSecondary)
                .multilineTextAlignment(.center)
                .frame(maxWidth: .infinity)
            if APIClient.devLoginEnabled { devLogin }
        } footer: {
            if model.phase == .failed {
                Banner(tone: .danger, systemImage: "exclamationmark.triangle", message: L("onb.signin.error", table: .onboarding))
            }
            SignInWithAppleButton(.signIn) { request in
                model.prepare(request)
            } onCompletion: { result in
                Task { await model.complete(result, app: app) }
            }
            .signInWithAppleButtonStyle(colorScheme == .dark ? .white : .black)
            .frame(height: 50)
            .clipShape(RoundedRectangle(cornerRadius: Radius.md, style: .continuous))
            .disabled(model.isSigningIn || !app.isOnline)
            .opacity(model.isSigningIn || !app.isOnline ? 0.4 : 1)
            if model.isSigningIn {
                HStack(spacing: Spacing.sm) {
                    ProgressView()
                    Text(L("onb.signin.progress", table: .onboarding))
                        .font(Typography.subheadline)
                        .foregroundStyle(Palette.textSecondary)
                }
                .accessibilityElement(children: .combine)
                .onAppear {
                    AccessibilityNotification.Announcement(String(localized: L("onb.signin.progress", table: .onboarding))).post()
                }
            }
            if let stepNumber { OnboardingStepIndicator(step: stepNumber) }
        }
        .navigationBarTitleDisplayMode(.inline)
    }

    @ViewBuilder private var banners: some View {
        switch app.signOutReason {
        case .revoked?:
            Banner(tone: .info, systemImage: "info.circle", message: L("onb.signin.revoked", table: .onboarding))
        case .unauthenticated?:
            Banner(tone: .info, systemImage: "info.circle", message: L("session.ended", table: .common))
        case .accountDeleted?:
            Banner(tone: .success, systemImage: "checkmark.circle", message: L("settings.delete.done", table: .settings))
        default:
            EmptyView()
        }
        if !app.isOnline {
            Banner(tone: .warning, systemImage: "wifi.slash", message: L("onb.signin.offline", table: .onboarding))
        }
    }

    private var devLogin: some View {
        @Bindable var model = model
        let subject = model.devSubject
        let valid = SignInModel.isValidDevSubject(subject)
        return VStack(alignment: .leading, spacing: Spacing.sm) {
            Text(L("onb.dev.title", table: .onboarding))
                .font(Typography.headline)
                .foregroundStyle(Palette.textPrimary)
            Text(L("onb.dev.field", table: .onboarding))
                .font(Typography.subheadline)
                .foregroundStyle(Palette.textSecondary)
            TextField(text: $model.devSubject) { Text(L("onb.dev.field", table: .onboarding)) }
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .padding(.horizontal, Spacing.md)
                .frame(minHeight: Spacing.minTouchTarget)
                .background(Palette.surface2, in: RoundedRectangle(cornerRadius: Radius.md, style: .continuous))
                .overlay(RoundedRectangle(cornerRadius: Radius.md, style: .continuous).stroke(Palette.borderStrong, lineWidth: 1))
                .submitLabel(.go)
                .onSubmit { Task { await model.devLogin(app: app) } }
            if !subject.isEmpty && !valid {
                Label { Text(L("onb.dev.invalid", table: .onboarding)) } icon: { Image(systemName: "exclamationmark.circle") }
                    .font(Typography.footnote)
                    .foregroundStyle(Palette.danger)
            }
            SecondaryButton(L("onb.dev.cta", table: .onboarding), isLoading: model.isSigningIn) {
                Task { await model.devLogin(app: app) }
            }
            .disabled(!valid || model.isSigningIn || !app.isOnline)
        }
        .padding(Spacing.lg)
        .background(Palette.surface, in: RoundedRectangle(cornerRadius: Radius.lg, style: .continuous))
    }
}

struct OnboardingLanguageStep: View {
    @Environment(AppModel.self) private var app
    @State private var transcript: TranscriptionLanguage = .auto
    @State private var output: OutputLanguage = ProfilePreferences.appOutputLanguage
    @State private var isSaving = false

    var body: some View {
        Form {
            Section {
                AppLanguageRow(title: L("onb.lang.app", table: .onboarding))
            } header: {
                Text(L("onb.lang.app", table: .onboarding))
            } footer: {
                Text(L("onb.lang.app.footer", table: .onboarding))
            }
            Section {
                LabeledMenuPicker(title: L("onb.lang.transcript", table: .onboarding), selection: $transcript) {
                    ForEach(TranscriptionLanguage.allCases, id: \.self) { Text(LanguageNames.transcription($0)).tag($0) }
                }
            } header: {
                Text(L("onb.lang.transcript", table: .onboarding))
            }
            Section {
                LabeledMenuPicker(title: L("onb.lang.output", table: .onboarding), selection: $output) {
                    ForEach(OutputLanguage.allCases, id: \.self) { Text(LanguageNames.output($0)).tag($0) }
                }
            } header: {
                Text(L("onb.lang.output", table: .onboarding))
            }
        }
        .scrollContentBackground(.hidden)
        .background(Palette.bg)
        .navigationTitle(Text(L("onb.lang.title", table: .onboarding)))
        .safeAreaInset(edge: .bottom) {
            PrimaryButton(L("onb.start", table: .onboarding), isLoading: isSaving) { start() }
                .padding(.horizontal, Spacing.screenInsetCompact)
                .padding(.vertical, Spacing.sm)
                .readingWidth()
                .background(Palette.bg)
        }
        .onAppear { transcript = ProfilePreferences.transcriptionLanguage(app: app) }
    }

    private func start() {
        isSaving = true
        ProfilePreferences.setTranscriptionLanguage(transcript, app: app)
        let chosen = output
        let needsPush = app.user?.language != chosen.rawValue
        Task {
            // Saved locally first; a failed PATCH stays pending and never blocks onboarding.
            if needsPush { await ProfilePreferences.setOutputLanguage(chosen, app: app) }
            isSaving = false
            app.completeOnboarding()
        }
    }
}

enum LanguageNames {
    static func transcription(_ language: TranscriptionLanguage) -> LocalizedStringResource {
        L("lang.\(language.rawValue)", table: .onboarding)
    }

    static func output(_ language: OutputLanguage) -> LocalizedStringResource {
        L("lang.\(language.rawValue)", table: .onboarding)
    }

    static var currentAppLanguage: LocalizedStringResource {
        L(Bundle.main.preferredLocalizations.first == "id" ? "lang.id" : "lang.en", table: .onboarding)
    }
}

struct AppLanguageRow: View {
    let title: LocalizedStringResource
    @Environment(\.openURL) private var openURL

    var body: some View {
        Button {
            if let url = URL(string: UIApplication.openSettingsURLString) { openURL(url) }
        } label: {
            LabeledContent {
                HStack(spacing: Spacing.xs) {
                    Text(LanguageNames.currentAppLanguage)
                    Image(systemName: "chevron.right").imageScale(.small).accessibilityHidden(true)
                }
                .foregroundStyle(Palette.textSecondary)
            } label: {
                Label { Text(title) } icon: { Image(systemName: "globe") }
                    .foregroundStyle(Palette.textPrimary)
            }
        }
        .frame(minHeight: Spacing.minTouchTarget)
    }
}
