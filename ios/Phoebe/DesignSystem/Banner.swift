import SwiftUI

struct BannerAction {
    let title: LocalizedStringResource
    let isLoading: Bool
    let handler: () -> Void

    init(_ title: LocalizedStringResource, isLoading: Bool = false, handler: @escaping () -> Void) {
        self.title = title
        self.isLoading = isLoading
        self.handler = handler
    }
}

struct Banner: View {
    let tone: Tone
    let systemImage: String
    let title: LocalizedStringResource?
    let message: LocalizedStringResource
    var actions: [BannerAction] = []
    var onClose: (() -> Void)?

    init(
        tone: Tone,
        systemImage: String,
        title: LocalizedStringResource? = nil,
        message: LocalizedStringResource,
        actions: [BannerAction] = [],
        onClose: (() -> Void)? = nil
    ) {
        self.tone = tone
        self.systemImage = systemImage
        self.title = title
        self.message = message
        self.actions = actions
        self.onClose = onClose
    }

    var body: some View {
        HStack(alignment: .top, spacing: Spacing.md) {
            Image(systemName: systemImage)
                .symbolRenderingMode(.hierarchical)
                .font(Typography.body)
                .foregroundStyle(tone.foreground)
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: Spacing.xs) {
                VStack(alignment: .leading, spacing: Spacing.xs) {
                    if let title {
                        Text(title)
                            .font(Typography.headline)
                            .foregroundStyle(tone.foreground)
                    }
                    Text(message)
                        .font(Typography.callout)
                        .foregroundStyle(Palette.textPrimary)
                        .fixedSize(horizontal: false, vertical: true)
                }
                .accessibilityElement(children: .combine)
                .accessibilityAddTraits(.isStaticText)
                if !actions.isEmpty {
                    ViewThatFits(in: .horizontal) {
                        HStack(spacing: Spacing.sm) { actionButtons }
                        VStack(alignment: .leading, spacing: Spacing.sm) { actionButtons }
                    }
                    .padding(.top, Spacing.xs)
                }
            }
            Spacer(minLength: 0)
            if let onClose {
                Button(action: onClose) {
                    Image(systemName: "xmark")
                        .font(Typography.subheadline.weight(.semibold))
                        .foregroundStyle(Palette.textSecondary)
                        .minTouchTarget()
                }
                .buttonStyle(.plain)
                .padding(.top, -Spacing.md)
                .padding(.trailing, -Spacing.md)
                .accessibilityLabel(Text(L("common.close")))
            }
        }
        .padding(Spacing.md)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(tone.fill, in: RoundedRectangle(cornerRadius: Radius.md, style: .continuous))
        .zIndex(ZLayer.banner)
        .onAppear(perform: announce)
    }

    @ViewBuilder private var actionButtons: some View {
        ForEach(Array(actions.enumerated()), id: \.offset) { _, action in
            Button(action: action.handler) {
                ZStack {
                    Text(action.title).opacity(action.isLoading ? 0 : 1)
                    if action.isLoading { ProgressView() }
                }
                .font(Typography.subheadline.weight(.semibold))
                .frame(minHeight: Spacing.minTouchTarget - Spacing.sm)
            }
            .buttonStyle(.bordered)
            .tint(Palette.accent)
            .controlSize(.small)
            .disabled(action.isLoading)
            .accessibilityLabel(Text(action.title))
            .frame(minHeight: Spacing.minTouchTarget)
        }
    }

    private func announce() {
        var text = AttributedString(String(localized: message))
        if let title { text = AttributedString(String(localized: title) + ". ") + text }
        text.accessibilitySpeechAnnouncementPriority = .default
        AccessibilityNotification.Announcement(text).post()
    }
}

#Preview {
    VStack(spacing: Spacing.lg) {
        Banner(tone: .warning, systemImage: "arrow.counterclockwise.circle", title: L("status.interrupted"), message: L("common.retry"), actions: [BannerAction(L("common.retry")) {}], onClose: {})
        Banner(tone: .danger, systemImage: "exclamationmark.triangle", message: L("status.failed.upload"))
    }
    .padding()
}
