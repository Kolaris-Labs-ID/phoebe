import Foundation

enum Formatting {
    // Fixed ":" separators (not Duration.formatted, which yields "01.05" in id) so the UI matches
    // the server's `[mm:ss]` export refs and the recording timer wireframe.
    static func timer(ms: Int) -> String {
        let total = max(0, ms) / 1000
        let h = total / 3600, m = (total % 3600) / 60, s = total % 60
        return h > 0 ? String(format: "%d:%02d:%02d", h, m, s) : String(format: "%02d:%02d", m, s)
    }

    // Chunk-timed transcripts carry chunk bounds, not word timestamps: "~mm:ss", spoken "sekitar …" / "about …".
    static func timestamp(ms: Int, approximate: Bool = false) -> String { (approximate ? "~" : "") + timer(ms: ms) }

    static func reference(ms: Int, approximate: Bool = false) -> String { "[\(timestamp(ms: ms, approximate: approximate))]" }

    static func spokenTimestamp(ms: Int, approximate: Bool, locale: Locale = .current, localization: String? = nil) -> String {
        let spoken = spokenDuration(ms: ms, locale: locale)
        return approximate ? LS("time.approximate.a11y", localization: localization, spoken) : spoken
    }

    static func spokenDuration(ms: Int, locale: Locale = .current, includeSeconds: Bool = true) -> String {
        let units: Set<Duration.UnitsFormatStyle.Unit> = includeSeconds ? [.hours, .minutes, .seconds] : [.hours, .minutes]
        return Duration.milliseconds(max(0, ms)).formatted(.units(allowed: units, width: .wide).locale(locale))
    }

    static func quotaMinutes(seconds: Int) -> Int { max(0, seconds) / 60 }

    static func deadline(date: String?, phrase: String?, locale: Locale = .current, localization: String? = nil) -> String {
        let phrase = phrase?.trimmingCharacters(in: .whitespacesAndNewlines).nilIfEmpty
        guard let date, let parsed = parseDay(date) else {
            return phrase ?? LS("common.notDetermined", localization: localization)
        }
        let formatted = parsed.formatted(dayStyle(locale: locale))
        return phrase.map { "\(formatted) (\($0))" } ?? formatted
    }

    static func notDetermined(_ value: String?, localization: String? = nil) -> String {
        value?.trimmingCharacters(in: .whitespacesAndNewlines).nilIfEmpty ?? LS("common.notDetermined", localization: localization)
    }

    static func parseDay(_ value: String) -> Date? {
        let parts = value.split(separator: "-", omittingEmptySubsequences: false)
        guard parts.map(\.count) == [4, 2, 2], let y = Int(parts[0]), let m = Int(parts[1]), let d = Int(parts[2]),
              (1...12).contains(m), (1...31).contains(d) else { return nil }
        return utcCalendar.date(from: DateComponents(year: y, month: m, day: d))
    }

    static func dayString(_ date: Date) -> String {
        let c = utcCalendar.dateComponents([.year, .month, .day], from: date)
        return String(format: "%04d-%02d-%02d", c.year ?? 0, c.month ?? 0, c.day ?? 0)
    }

    private static var utcCalendar: Calendar {
        var cal = Calendar(identifier: .gregorian)
        cal.timeZone = TimeZone(identifier: "UTC")!
        return cal
    }

    private static func dayStyle(locale: Locale) -> Date.FormatStyle {
        Date.FormatStyle(date: .abbreviated, time: .omitted, locale: locale, calendar: utcCalendar, timeZone: TimeZone(identifier: "UTC")!)
    }
}

extension String {
    var nilIfEmpty: String? { isEmpty ? nil : self }
}
