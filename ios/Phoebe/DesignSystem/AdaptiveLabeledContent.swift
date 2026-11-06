import SwiftUI

// Side-by-side label/value rows break words mid-way at accessibility text sizes; stack them instead.
struct AdaptiveLabeledContentStyle: LabeledContentStyle {
    func makeBody(configuration: Configuration) -> some View {
        AdaptiveLabeledRow(configuration: configuration)
    }
}

private struct AdaptiveLabeledRow: View {
    let configuration: LabeledContentStyleConfiguration
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize

    var body: some View {
        if dynamicTypeSize.isAccessibilitySize {
            VStack(alignment: .leading, spacing: Spacing.xs) {
                configuration.label
                configuration.content.foregroundStyle(Palette.textSecondary)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        } else {
            LabeledContent(configuration)
        }
    }
}

struct LabeledMenuPicker<Value: Hashable, Options: View>: View {
    let title: LocalizedStringResource
    @Binding var selection: Value
    @ViewBuilder let options: Options

    var body: some View {
        LabeledContent {
            Picker(selection: $selection) { options } label: { Text(title) }
                .pickerStyle(.menu)
                .labelsHidden()
        } label: {
            Text(title)
        }
        .frame(minHeight: Spacing.minTouchTarget)
    }
}
