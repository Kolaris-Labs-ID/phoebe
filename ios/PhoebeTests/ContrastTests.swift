import Foundation
import Testing
import UIKit
@testable import Phoebe

struct ContrastTests {
    static let textPairs: [(ColorToken, ColorToken)] = [
        (.textPrimary, .bg), (.textPrimary, .surface), (.textPrimary, .surface2),
        (.textSecondary, .surface), (.textSecondary, .surface2), (.textTertiary, .surface),
        (.accent, .surface), (.accent, .bg), (.onAccent, .accent), (.accent, .accentSoft),
        (.record, .surface), (.onRecord, .record), (.record, .recordSoft),
        (.warning, .surface), (.warning, .warningSoft), (.warning, .bg), (.textSecondary, .bg),
        (.success, .surface), (.success, .successSoft),
        (.danger, .surface), (.danger, .dangerSoft),
        (.info, .surface), (.info, .infoSoft),
        (.aiOrigin, .surface), (.aiOrigin, .aiSoft),
        (.humanOrigin, .surface),
    ]

    static func luminance(_ color: UIColor) -> Double {
        var r: CGFloat = 0, g: CGFloat = 0, b: CGFloat = 0, a: CGFloat = 0
        color.getRed(&r, green: &g, blue: &b, alpha: &a)
        func lin(_ c: CGFloat) -> Double {
            let c = Double(c)
            return c <= 0.04045 ? c / 12.92 : pow((c + 0.055) / 1.055, 2.4)
        }
        return 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b)
    }

    static func ratio(_ fg: ColorToken, _ bg: ColorToken, style: UIUserInterfaceStyle) -> Double {
        let traits = UITraitCollection(userInterfaceStyle: style)
        let l1 = luminance(fg.uiColor.resolvedColor(with: traits))
        let l2 = luminance(bg.uiColor.resolvedColor(with: traits))
        return (max(l1, l2) + 0.05) / (min(l1, l2) + 0.05)
    }

    @Test(arguments: [UIUserInterfaceStyle.light, .dark])
    func textPairsMeetAA(style: UIUserInterfaceStyle) {
        for (fg, bg) in Self.textPairs {
            let r = Self.ratio(fg, bg, style: style)
            #expect(r >= 4.5, "\(fg) on \(bg) = \(r) in \(style == .dark ? "dark" : "light")")
        }
    }

    @Test(arguments: [UIUserInterfaceStyle.light, .dark])
    func toastInverseMeetsAA(style: UIUserInterfaceStyle) {
        let r = style == .dark ? Self.ratio(.textPrimary, .surface2, style: .dark) : Self.ratio(.surface, .textPrimary, style: .light)
        #expect(r >= 4.5)
    }

    @Test(arguments: [UIUserInterfaceStyle.light, .dark])
    func strongBorderMeetsNonTextThreshold(style: UIUserInterfaceStyle) {
        #expect(Self.ratio(.borderStrong, .surface, style: style) >= 3)
    }

    @Test func ratiosMatchMasterTable() {
        #expect(abs(Self.ratio(.textPrimary, .bg, style: .light) - 16.41) < 0.05)
        #expect(abs(Self.ratio(.textPrimary, .bg, style: .dark) - 17.68) < 0.05)
        #expect(abs(Self.ratio(.accent, .accentSoft, style: .light) - 4.84) < 0.05)
        #expect(abs(Self.ratio(.onRecord, .record, style: .dark) - 6.77) < 0.05)
    }
}
