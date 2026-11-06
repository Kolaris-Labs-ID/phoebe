import SwiftUI
import UIKit

struct RGBA: Sendable, Equatable {
    let r: Double, g: Double, b: Double, a: Double

    init(hex: UInt32, alpha: Double = 1) {
        r = Double((hex >> 16) & 0xFF) / 255
        g = Double((hex >> 8) & 0xFF) / 255
        b = Double(hex & 0xFF) / 255
        a = alpha
    }

    var uiColor: UIColor { UIColor(red: r, green: g, blue: b, alpha: a) }
}

enum ColorToken: String, CaseIterable, Sendable {
    case bg, surface, surface2, textPrimary, textSecondary, textTertiary, separator, borderStrong
    case accent, onAccent, accentSoft
    case record, onRecord, recordSoft
    case warning, warningSoft, success, successSoft, danger, dangerSoft, info, infoSoft
    case aiOrigin, aiSoft, humanOrigin, scrim

    var light: RGBA {
        switch self {
        case .bg: RGBA(hex: 0xF5F6F8)
        case .surface: RGBA(hex: 0xFFFFFF)
        case .surface2: RGBA(hex: 0xEEF0F3)
        case .textPrimary: RGBA(hex: 0x111827)
        case .textSecondary: RGBA(hex: 0x4B5563)
        case .textTertiary: RGBA(hex: 0x6B7280)
        case .separator: RGBA(hex: 0xD1D5DB)
        case .borderStrong: RGBA(hex: 0x8A929E)
        case .accent: RGBA(hex: 0x0F766E)
        case .onAccent: RGBA(hex: 0xFFFFFF)
        case .accentSoft: RGBA(hex: 0xE6F4F2)
        case .record: RGBA(hex: 0xC4161C)
        case .onRecord: RGBA(hex: 0xFFFFFF)
        case .recordSoft: RGBA(hex: 0xFDECEC)
        case .warning: RGBA(hex: 0x92400E)
        case .warningSoft: RGBA(hex: 0xFEF3C7)
        case .success: RGBA(hex: 0x166534)
        case .successSoft: RGBA(hex: 0xE8F5EC)
        case .danger: RGBA(hex: 0xB91C1C)
        case .dangerSoft: RGBA(hex: 0xFDECEC)
        case .info: RGBA(hex: 0x1D4ED8)
        case .infoSoft: RGBA(hex: 0xE8EFFD)
        case .aiOrigin: RGBA(hex: 0x4338CA)
        case .aiSoft: RGBA(hex: 0xEEF0FF)
        case .humanOrigin: RGBA(hex: 0x374151)
        case .scrim: RGBA(hex: 0x000000, alpha: 0.45)
        }
    }

    var dark: RGBA {
        switch self {
        case .bg: RGBA(hex: 0x0B0D10)
        case .surface: RGBA(hex: 0x16191E)
        case .surface2: RGBA(hex: 0x1F242B)
        case .textPrimary: RGBA(hex: 0xF3F4F6)
        case .textSecondary: RGBA(hex: 0xB4BAC4)
        case .textTertiary: RGBA(hex: 0x8E96A3)
        case .separator: RGBA(hex: 0x3A414B)
        case .borderStrong: RGBA(hex: 0x6E7682)
        case .accent: RGBA(hex: 0x2DD4BF)
        case .onAccent: RGBA(hex: 0x042F2E)
        case .accentSoft: RGBA(hex: 0x0F2F2C)
        case .record: RGBA(hex: 0xFF6B6B)
        case .onRecord: RGBA(hex: 0x2A0405)
        case .recordSoft: RGBA(hex: 0x3A1416)
        case .warning: RGBA(hex: 0xFBBF24)
        case .warningSoft: RGBA(hex: 0x3A2A08)
        case .success: RGBA(hex: 0x4ADE80)
        case .successSoft: RGBA(hex: 0x0F2E1B)
        case .danger: RGBA(hex: 0xF87171)
        case .dangerSoft: RGBA(hex: 0x3A1416)
        case .info: RGBA(hex: 0x93C5FD)
        case .infoSoft: RGBA(hex: 0x132544)
        case .aiOrigin: RGBA(hex: 0xA5B4FC)
        case .aiSoft: RGBA(hex: 0x1E2147)
        case .humanOrigin: RGBA(hex: 0xD1D5DB)
        case .scrim: RGBA(hex: 0x000000, alpha: 0.60)
        }
    }

    var uiColor: UIColor {
        let light = light.uiColor, dark = dark.uiColor
        return UIColor { $0.userInterfaceStyle == .dark ? dark : light }
    }

    var color: Color { Color(uiColor: uiColor) }
}

enum Palette {
    static var bg: Color { ColorToken.bg.color }
    static var surface: Color { ColorToken.surface.color }
    static var surface2: Color { ColorToken.surface2.color }
    static var textPrimary: Color { ColorToken.textPrimary.color }
    static var textSecondary: Color { ColorToken.textSecondary.color }
    static var textTertiary: Color { ColorToken.textTertiary.color }
    static var separator: Color { ColorToken.separator.color }
    static var borderStrong: Color { ColorToken.borderStrong.color }
    static var accent: Color { ColorToken.accent.color }
    static var onAccent: Color { ColorToken.onAccent.color }
    static var accentSoft: Color { ColorToken.accentSoft.color }
    static var record: Color { ColorToken.record.color }
    static var onRecord: Color { ColorToken.onRecord.color }
    static var recordSoft: Color { ColorToken.recordSoft.color }
    static var warning: Color { ColorToken.warning.color }
    static var warningSoft: Color { ColorToken.warningSoft.color }
    static var success: Color { ColorToken.success.color }
    static var successSoft: Color { ColorToken.successSoft.color }
    static var danger: Color { ColorToken.danger.color }
    static var dangerSoft: Color { ColorToken.dangerSoft.color }
    static var info: Color { ColorToken.info.color }
    static var infoSoft: Color { ColorToken.infoSoft.color }
    static var aiOrigin: Color { ColorToken.aiOrigin.color }
    static var aiSoft: Color { ColorToken.aiSoft.color }
    static var humanOrigin: Color { ColorToken.humanOrigin.color }
    static var scrim: Color { ColorToken.scrim.color }
}

enum Tone: Sendable, CaseIterable {
    case neutral, info, success, warning, danger, record, ai

    var foreground: Color {
        switch self {
        case .neutral: Palette.textSecondary
        case .info: Palette.info
        case .success: Palette.success
        case .warning: Palette.warning
        case .danger: Palette.danger
        case .record: Palette.record
        case .ai: Palette.aiOrigin
        }
    }

    var fill: Color {
        switch self {
        case .neutral: Palette.surface2
        case .info: Palette.infoSoft
        case .success: Palette.successSoft
        case .warning: Palette.warningSoft
        case .danger: Palette.dangerSoft
        case .record: Palette.recordSoft
        case .ai: Palette.aiSoft
        }
    }
}

enum Spacing {
    static let xxs: CGFloat = 2
    static let xs: CGFloat = 4
    static let sm: CGFloat = 8
    static let md: CGFloat = 12
    static let lg: CGFloat = 16
    static let xl: CGFloat = 24
    static let xxl: CGFloat = 32
    static let xxxl: CGFloat = 48

    static let screenInsetCompact: CGFloat = 16
    static let screenInsetRegular: CGFloat = 20
    static let rowVertical: CGFloat = 12
    static let section: CGFloat = 24
    static let minTouchTarget: CGFloat = 44
    static let readingMaxWidth: CGFloat = 680
}

enum Radius {
    static let sm: CGFloat = 6
    static let md: CGFloat = 10
    static let lg: CGFloat = 14
}

enum ZLayer {
    static let content: Double = 0
    static let banner: Double = 10
    static let bottomBar: Double = 20
    static let toast: Double = 30
}

enum Motion {
    static var fast: Animation { .easeOut(duration: 0.15) }
    static var standard: Animation { .snappy(duration: 0.25) }
    static var exit: Animation { .easeIn(duration: 0.17) }
    static var recordPulse: Animation { .easeInOut(duration: 1.2).repeatForever(autoreverses: true) }
    static let pulseMinOpacity: Double = 0.35
    static let pressScale: CGFloat = 0.97

    static func standard(reduceMotion: Bool) -> Animation {
        reduceMotion ? .easeInOut(duration: 0.15) : standard
    }

    static func exit(reduceMotion: Bool) -> Animation {
        reduceMotion ? .easeInOut(duration: 0.15) : exit
    }

    static func transition(reduceMotion: Bool, edge: Edge = .bottom) -> AnyTransition {
        reduceMotion ? .opacity : .move(edge: edge).combined(with: .opacity)
    }
}

struct ElevationModifier: ViewModifier {
    let level: Int
    @Environment(\.colorScheme) private var scheme

    func body(content: Content) -> some View {
        switch level {
        case 2 where scheme == .dark:
            content.background(Palette.surface2)
        case 2:
            content.background(Palette.surface).shadow(color: .black.opacity(0.08), radius: 8, x: 0, y: 2)
        case 1:
            content.background(Palette.surface)
        default:
            content.background(Palette.bg)
        }
    }
}

struct PressScaleButtonStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .scaleEffect(configuration.isPressed ? Motion.pressScale : 1)
            .animation(Motion.fast, value: configuration.isPressed)
    }
}

extension View {
    func elevation(_ level: Int) -> some View { modifier(ElevationModifier(level: level)) }

    func readingWidth() -> some View {
        frame(maxWidth: Spacing.readingMaxWidth).frame(maxWidth: .infinity)
    }

    func minTouchTarget() -> some View {
        frame(minWidth: Spacing.minTouchTarget, minHeight: Spacing.minTouchTarget).contentShape(Rectangle())
    }
}
