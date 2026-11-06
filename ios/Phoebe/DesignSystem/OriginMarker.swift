import SwiftUI

struct OriginMarker: View {
    let origin: Origin
    let edited: Bool

    var body: some View {
        HStack(spacing: Spacing.xs) {
            Image(systemName: origin == .ai ? "sparkles" : "person.fill")
                .imageScale(.small)
            Text(L(Self.key(origin: origin, edited: edited)))
        }
        .font(Typography.caption)
        .foregroundStyle(origin == .ai ? Palette.aiOrigin : Palette.humanOrigin)
        .padding(.vertical, Spacing.xxs)
        .padding(.horizontal, Spacing.sm)
        .background(origin == .ai ? Palette.aiSoft : Palette.surface2, in: RoundedRectangle(cornerRadius: Radius.sm, style: .continuous))
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(Text(L(Self.key(origin: origin, edited: edited) + ".a11y")))
    }

    static func key(origin: Origin, edited: Bool) -> String {
        switch (origin, edited) {
        case (.human, _): "origin.human"
        case (.ai, true): "origin.aiEdited"
        case (.ai, false): "origin.ai"
        }
    }
}

#Preview {
    VStack(alignment: .leading) {
        OriginMarker(origin: .ai, edited: false)
        OriginMarker(origin: .ai, edited: true)
        OriginMarker(origin: .human, edited: false)
    }
    .padding()
}
