import SwiftUI

struct ImportConfirmSheet: View {
    let candidate: ImportCandidate
    let onUpload: (TranscriptionLanguage) -> Void
    let onCancel: () -> Void
    @Environment(AppModel.self) private var app
    @State private var language: TranscriptionLanguage = .auto

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    LabeledContent {
                        Text(verbatim: Formatting.timer(ms: candidate.durationMs))
                            .monospacedDigit()
                            .accessibilityLabel(Text(verbatim: Formatting.spokenDuration(ms: candidate.durationMs)))
                    } label: {
                        Label { Text(verbatim: candidate.fileName).lineLimit(2) } icon: { Image(systemName: "waveform") }
                    }
                }
                Section {
                    Label {
                        Text(retentionText)
                            .font(Typography.callout)
                            .fixedSize(horizontal: false, vertical: true)
                    } icon: {
                        Image(systemName: "lock.shield")
                    }
                    .accessibilityElement(children: .combine)
                    LabeledMenuPicker(title: L("import.confirm.language", table: .library), selection: $language) {
                        ForEach(TranscriptionLanguage.allCases, id: \.self) { Text(LanguageNames.transcription($0)).tag($0) }
                    }
                } footer: {
                    if app.serverConfig == nil {
                        Text(L("import.confirm.waitConfig", table: .library))
                    }
                }
            }
            .scrollContentBackground(.hidden)
            .background(Palette.bg)
            .navigationTitle(Text(L("import.confirm.title", table: .library)))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button(action: onCancel) { Text(L("common.cancel")) }
                }
            }
            .safeAreaInset(edge: .bottom) {
                PrimaryButton(L("import.confirm.upload", table: .library), systemImage: "icloud.and.arrow.up") {
                    onUpload(language)
                }
                .disabled(app.serverConfig == nil)
                .padding(.horizontal, Spacing.screenInsetCompact)
                .padding(.vertical, Spacing.sm)
                .background(Palette.bg)
            }
        }
        .presentationDetents([.medium, .large])
        .interactiveDismissDisabled()
        .task {
            language = ProfilePreferences.transcriptionLanguage(app: app)
            while app.serverConfig == nil {
                await app.refreshConfig()
                if app.serverConfig == nil { guard (try? await Task.sleep(for: .seconds(5))) != nil else { return } }
            }
        }
    }

    private var retentionText: LocalizedStringResource {
        if let days = app.serverConfig?.audioRetentionDays {
            return L("import.confirm.cloud", table: .library, Int64(days))
        }
        return L("onb.privacy.retention.unknown", table: .onboarding)
    }
}
