import SwiftUI

// Both options carry equal weight; nothing is preselected or chosen on dismiss.
struct AISlotChoiceSheet: View {
    @Environment(AppModel.self) private var app
    @Environment(\.dismiss) private var dismiss
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    let durationMs: Int?
    let onTranscribeOnly: () -> Void

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: Spacing.lg) {
                    Text(L("ai.choice.title", table: .meetingDetail))
                        .font(Typography.title)
                        .foregroundStyle(Palette.textPrimary)
                        .accessibilityAddTraits(.isHeader)
                    if let usage = app.usage {
                        Label { Text(verbatim: AIAllowance(usage.aiGenerations).summary()) } icon: { Image(systemName: "sparkles") }
                            .font(Typography.subheadline)
                            .foregroundStyle(Palette.textPrimary)
                            .padding(Spacing.md)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .background(Palette.surface2, in: RoundedRectangle(cornerRadius: Radius.md, style: .continuous))
                            .accessibilityElement(children: .combine)
                    }
                    Text(L("ai.choice.body", table: .meetingDetail))
                        .font(Typography.body)
                        .foregroundStyle(Palette.textPrimary)
                        .fixedSize(horizontal: false, vertical: true)
                    option(
                        icon: "hourglass",
                        title: L("ai.choice.wait", table: .meetingDetail),
                        detail: L("ai.choice.wait.body", table: .meetingDetail)
                    ) { dismiss() }
                    option(
                        icon: "text.quote",
                        title: L("ai.choice.transcribeOnly", table: .meetingDetail),
                        detail: transcribeOnlyDetail
                    ) {
                        dismiss()
                        onTranscribeOnly()
                    }
                }
                .padding(Spacing.screenInsetCompact)
                .readingWidth()
            }
            .background(Palette.bg)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button(L("common.close")) { dismiss() }
                }
            }
        }
        // At accessibility sizes the medium detent would hide both options below the fold.
        .presentationDetents(dynamicTypeSize.isAccessibilitySize ? [.large] : [.medium, .large])
        .task { _ = await app.refreshUsage() }
    }

    private var transcribeOnlyDetail: LocalizedStringResource {
        guard let durationMs, durationMs > 0 else { return L("ai.choice.transcribeOnly.body", table: .meetingDetail) }
        let minutes = Int64((Double(durationMs) / 60_000).rounded(.up))
        return L("ai.choice.transcribeOnly.minutes", table: .meetingDetail, minutes)
    }

    private func option(icon: String, title: LocalizedStringResource, detail: LocalizedStringResource, action: @escaping () -> Void) -> some View {
        VStack(alignment: .leading, spacing: Spacing.sm) {
            Text(detail)
                .font(Typography.callout)
                .foregroundStyle(Palette.textSecondary)
                .fixedSize(horizontal: false, vertical: true)
            SecondaryButton(title, systemImage: icon, action: action)
        }
        .padding(Spacing.md)
        .background(Palette.surface, in: RoundedRectangle(cornerRadius: Radius.lg, style: .continuous))
    }
}
