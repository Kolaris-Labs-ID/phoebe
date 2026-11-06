import Foundation
import Testing
@testable import Phoebe

struct EditorExportStringsTests {
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

    @Test(arguments: [StringTable.editor, .export])
    func everyKeyHasBothLanguagesWithMatchingSpecifiers(_ table: StringTable) throws {
        let entries = try Self.catalog(table)
        #expect(!entries.isEmpty)
        for (key, values) in entries {
            let en = values["en"] ?? "", id = values["id"] ?? ""
            #expect(!en.isEmpty && !id.isEmpty, "\(key)")
            #expect(Self.specifiers(en) == Self.specifiers(id), "\(key)")
        }
    }

    @Test func formattedLookups() {
        #expect(LS("version.restored", table: .editor, localization: "id", Int64(4)) == "Dipulihkan sebagai v4")
        #expect(LS("conflict.body", table: .editor, localization: "en", Int64(5), "10:40", Int64(4))
            == "The latest version (v5, 10:40) was made on another device or by Regenerate, after the version you edited (v4).")
        #expect(LS("export.revision", table: .export, localization: "id", Int64(3), "10.42") == "Dari revisi v3 · 10.42")
        #expect(LS("mom.unspecified", table: .editor, localization: "id") == "Belum ditentukan")
    }
}
