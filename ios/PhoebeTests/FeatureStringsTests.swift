import Foundation
import Testing
@testable import Phoebe

struct FeatureStringsTests {
    static let tables: [StringTable] = [.onboarding, .library, .settings]

    private static func catalog(_ table: StringTable) throws -> [String: [String: String]] {
        let url = URL(filePath: #filePath).deletingLastPathComponent()
            .appending(path: "../Phoebe/Resources/\(table.rawValue).xcstrings").standardized
        let json = try JSONSerialization.jsonObject(with: Data(contentsOf: url)) as? [String: Any]
        let strings = json?["strings"] as? [String: [String: Any]] ?? [:]
        return strings.mapValues { entry in
            let locs = entry["localizations"] as? [String: [String: [String: String]]] ?? [:]
            return locs.compactMapValues { $0["stringUnit"]?["value"] }
        }
    }

    private static func specifiers(_ value: String) -> [String] {
        value.matches(of: /%(\d\$)?(lld|@|%)/).map { String($0.output.0) }.sorted()
    }

    @Test(arguments: tables)
    func everyKeyHasIndonesianAndEnglish(_ table: StringTable) throws {
        let entries = try Self.catalog(table)
        #expect(!entries.isEmpty)
        for (key, values) in entries {
            let en = values["en"] ?? "", id = values["id"] ?? ""
            #expect(!en.isEmpty, "\(table.rawValue): \(key) missing en")
            #expect(!id.isEmpty, "\(table.rawValue): \(key) missing id")
            #expect(Self.specifiers(en) == Self.specifiers(id), "\(table.rawValue): \(key) format mismatch")
        }
    }

    @Test(arguments: tables)
    func bundledLookupResolvesBothLanguages(_ table: StringTable) throws {
        for (key, values) in try Self.catalog(table) where Self.specifiers(values["en"] ?? "").isEmpty {
            #expect(LS(key, table: table, localization: "en") == values["en"], "\(key) en")
            #expect(LS(key, table: table, localization: "id") == values["id"], "\(key) id")
        }
    }

    @Test func formattedLookups() {
        #expect(LS("onb.step", table: .onboarding, localization: "id", Int64(2), Int64(3)) == "2 dari 3")
        #expect(LS("settings.usage.used", table: .settings, localization: "en", Int64(42), Int64(120)) == "42 of 120 minutes used")
        #expect(LS("import.copying", table: .library, localization: "id", Int64(45)) == "Menyalin file… 45%")
        #expect(LS("library.recovery.body", table: .library, localization: "en", "Sync", "12 minutes") == "\"Sync\" stopped unexpectedly. 12 minutes of saved audio can be recovered.")
    }
}
