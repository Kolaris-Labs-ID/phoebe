import SwiftUI

struct PrimaryButton: View {
    let title: LocalizedStringResource
    var systemImage: String?
    var isLoading = false
    let action: () -> Void

    init(_ title: LocalizedStringResource, systemImage: String? = nil, isLoading: Bool = false, action: @escaping () -> Void) {
        self.title = title
        self.systemImage = systemImage
        self.isLoading = isLoading
        self.action = action
    }

    var body: some View {
        Button(action: action) {
            ZStack {
                Group {
                    if let systemImage {
                        Label { Text(title) } icon: { Image(systemName: systemImage) }
                    } else {
                        Text(title)
                    }
                }
                .opacity(isLoading ? 0 : 1)
                if isLoading { ProgressView().tint(Palette.onAccent) }
            }
            .font(Typography.headline)
            .frame(maxWidth: .infinity, minHeight: 50)
        }
        .buttonStyle(.borderedProminent)
        .buttonBorderShape(.roundedRectangle(radius: Radius.md))
        .tint(Palette.accent)
        .foregroundStyle(Palette.onAccent)
        .disabled(isLoading)
        .accessibilityLabel(Text(title))
    }
}

struct SecondaryButton: View {
    let title: LocalizedStringResource
    var systemImage: String?
    var isLoading = false
    let action: () -> Void

    init(_ title: LocalizedStringResource, systemImage: String? = nil, isLoading: Bool = false, action: @escaping () -> Void) {
        self.title = title
        self.systemImage = systemImage
        self.isLoading = isLoading
        self.action = action
    }

    var body: some View {
        Button(action: action) {
            ZStack {
                Group {
                    if let systemImage {
                        Label { Text(title) } icon: { Image(systemName: systemImage) }
                    } else {
                        Text(title)
                    }
                }
                .opacity(isLoading ? 0 : 1)
                if isLoading { ProgressView() }
            }
            .frame(minHeight: Spacing.minTouchTarget - Spacing.sm)
        }
        .buttonStyle(.bordered)
        .tint(Palette.accent)
        .frame(minHeight: Spacing.minTouchTarget)
        .disabled(isLoading)
        .accessibilityLabel(Text(title))
    }
}
