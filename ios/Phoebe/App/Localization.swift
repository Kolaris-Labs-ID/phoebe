import Foundation

enum StringTable: String, CaseIterable, Sendable {
    case common = "Common"
    case onboarding = "Onboarding"
    case library = "Library"
    case settings = "Settings"
    case recording = "Recording"
    case meetingDetail = "MeetingDetail"
    case editor = "Editor"
    case export = "Export"
    case audio = "Audio"
    case sync = "Sync"
}

func L(_ key: String, table: StringTable = .common) -> LocalizedStringResource {
    LocalizedStringResource(String.LocalizationValue(key), table: table.rawValue)
}

// Catalog keys are dotted ids without format specifiers, so the value is formatted eagerly
// and wrapped in a resource whose lookup (missing table) falls back to the formatted text.
func L(_ key: String, table: StringTable = .common, _ args: any CVarArg...) -> LocalizedStringResource {
    let text = localizedString(key, table: table, localization: nil, args: args)
    return LocalizedStringResource(String.LocalizationValue(text), table: "Verbatim")
}

func LS(_ key: String, table: StringTable = .common, localization: String? = nil, _ args: any CVarArg...) -> String {
    localizedString(key, table: table, localization: localization, args: args)
}

func localizedString(_ key: String, table: StringTable, localization: String?, args: [any CVarArg]) -> String {
    let bundle = localization
        .flatMap { Bundle.main.path(forResource: $0, ofType: "lproj") }
        .flatMap(Bundle.init(path:)) ?? .main
    let format = bundle.localizedString(forKey: key, value: nil, table: table.rawValue)
    guard !args.isEmpty else { return format }
    let locale = localization.map(Locale.init(identifier:)) ?? .current
    return String(format: format, locale: locale, arguments: args)
}
