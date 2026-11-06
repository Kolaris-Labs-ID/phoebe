import Foundation
import Testing
@testable import Phoebe

struct LocalizationTests {
    private static let appRoot = URL(filePath: #filePath).deletingLastPathComponent()
        .appending(path: "../Phoebe").standardized
    private static let resources = appRoot.appending(path: "Resources")

    private static func catalogNames() throws -> [String] {
        try FileManager.default.contentsOfDirectory(atPath: resources.path)
            .filter { $0.hasSuffix(".xcstrings") }
            .map { String($0.dropLast(".xcstrings".count)) }
            .sorted()
    }

    private static func catalog(_ name: String) throws -> [String: [String: String]] {
        let data = try Data(contentsOf: resources.appending(path: "\(name).xcstrings"))
        let json = try JSONSerialization.jsonObject(with: data) as? [String: Any]
        let strings = json?["strings"] as? [String: [String: Any]] ?? [:]
        return strings.mapValues { entry in
            let locs = entry["localizations"] as? [String: [String: [String: String]]] ?? [:]
            return locs.compactMapValues { $0["stringUnit"]?["value"] }
        }
    }

    private static func specifiers(_ value: String) -> [String] {
        value.matches(of: /%(\d\$)?(lld|ld|d|@|%)/).map { String($0.output.0) }.sorted()
    }

    @Test func everyStringTableHasACatalog() throws {
        let names = Set(try Self.catalogNames())
        for table in StringTable.allCases {
            #expect(names.contains(table.rawValue), "missing \(table.rawValue).xcstrings")
        }
    }

    @Test func everyKeyHasNonEmptyEnglishAndIndonesian() throws {
        for name in try Self.catalogNames() {
            let entries = try Self.catalog(name)
            #expect(!entries.isEmpty, "\(name) is empty")
            for (key, values) in entries {
                let en = values["en"] ?? "", id = values["id"] ?? ""
                #expect(!en.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty, "\(name): \(key) missing en")
                #expect(!id.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty, "\(name): \(key) missing id")
                #expect(Self.specifiers(en) == Self.specifiers(id), "\(name): \(key) format specifiers differ")
            }
        }
    }

    @Test func everyStaticKeyUsedInCodeExistsInItsTable() throws {
        var catalogs: [String: Set<String>] = [:]
        for table in StringTable.allCases { catalogs[table.rawValue] = Set(try Self.catalog(table.rawValue).keys) }
        let tableByCase = Dictionary(uniqueKeysWithValues: StringTable.allCases.map { ("\($0)", $0.rawValue) })
        let files = FileManager.default.enumerator(at: Self.appRoot, includingPropertiesForKeys: nil)?
            .compactMap { $0 as? URL }.filter { $0.pathExtension == "swift" } ?? []
        #expect(!files.isEmpty)
        var checked = 0
        for file in files {
            let source = try String(contentsOf: file, encoding: .utf8)
            for match in source.matches(of: /\bLS?\(\s*"([^"\\]+)"\s*(?:,\s*table:\s*\.(\w+))?/) {
                let key = String(match.output.1)
                let table = tableByCase[match.output.2.map(String.init) ?? "common"] ?? "?"
                checked += 1
                #expect(catalogs[table]?.contains(key) == true, "\(file.lastPathComponent): \"\(key)\" not in \(table).xcstrings")
            }
        }
        #expect(checked > 100)
    }

    @Test func runtimeLookupResolvesInBothLanguages() throws {
        for table in StringTable.allCases {
            for (key, values) in try Self.catalog(table.rawValue) where Self.specifiers(values["en"] ?? "").isEmpty {
                #expect(LS(key, table: table, localization: "en") == values["en"], "\(table.rawValue): \(key) en")
                #expect(LS(key, table: table, localization: "id") == values["id"], "\(table.rawValue): \(key) id")
            }
        }
    }
}
