import SwiftUI

enum Typography {
    static var display: Font { .largeTitle.weight(.bold) }
    static var title: Font { .title2.weight(.semibold) }
    static var headline: Font { .headline }
    static var body: Font { .body }
    static var callout: Font { .callout }
    static var subheadline: Font { .subheadline }
    static var footnote: Font { .footnote }
    static var caption: Font { .caption.weight(.medium) }

    static let readingLineSpacing: CGFloat = 4
}

struct TimerTextStyle: ViewModifier {
    @ScaledMetric(relativeTo: .largeTitle) private var size: CGFloat = 56
    static let maxSize: CGFloat = 88

    func body(content: Content) -> some View {
        content
            .font(.system(size: min(size, Self.maxSize), weight: .semibold, design: .rounded).monospacedDigit())
    }
}

struct ReadingTextStyle: ViewModifier {
    func body(content: Content) -> some View {
        content.font(Typography.body).lineSpacing(Typography.readingLineSpacing)
    }
}

extension View {
    func timerTextStyle() -> some View { modifier(TimerTextStyle()) }
    func readingTextStyle() -> some View { modifier(ReadingTextStyle()) }
}
