import Foundation

// Counted only by the server (/v1/usage.ai_generations).
struct AIAllowance: Equatable {
    let limit: Int
    let windowHours: Int
    let remaining: Int
    let nextSlotAt: Date?

    init(_ dto: AIGenerationsDTO) {
        limit = dto.limit
        windowHours = dto.windowHours
        remaining = max(0, dto.remaining)
        nextSlotAt = dto.nextSlotAt
    }

    var isExhausted: Bool { remaining == 0 }

    // Automatic notes reserve a slot with the minutes, so with none left the user chooses first; unknown usage lets the server decide.
    static func needsChoice(usage: UsageDTO?, notes: NotesMode) -> Bool {
        notes == .auto && usage.map { AIAllowance($0.aiGenerations).isExhausted } == true
    }

    func label(localization: String? = nil) -> String {
        LS("ai.allowance.label", table: .settings, localization: localization, Int64(limit), Int64(windowHours))
    }

    func remainingText(localization: String? = nil) -> String {
        LS("ai.allowance.left", table: .settings, localization: localization, Int64(remaining))
    }

    func nextSlotText(now: Date = .now, locale: Locale = .current, timeZone: TimeZone = .current, localization: String? = nil) -> String? {
        guard isExhausted, let nextSlotAt else { return nil }
        return Self.nextSlotText(nextSlotAt, now: now, locale: locale, timeZone: timeZone, localization: localization)
    }

    func summary(now: Date = .now, locale: Locale = .current, timeZone: TimeZone = .current, localization: String? = nil) -> String {
        let head = "\(label(localization: localization)) · \(remainingText(localization: localization))"
        guard let next = nextSlotText(now: now, locale: locale, timeZone: timeZone, localization: localization) else { return head }
        return "\(head). \(next)."
    }

    static func nextSlotText(_ date: Date, now: Date = .now, locale: Locale = .current, timeZone: TimeZone = .current, localization: String? = nil) -> String {
        LS("ai.allowance.next", table: .settings, localization: localization, slotTime(date, now: now, locale: locale, timeZone: timeZone))
    }

    static func slotTime(_ date: Date, now: Date, locale: Locale, timeZone: TimeZone) -> String {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = timeZone
        let style = Date.FormatStyle(
            date: calendar.isDate(date, inSameDayAs: now) ? .omitted : .abbreviated,
            time: .shortened, locale: locale, calendar: calendar, timeZone: timeZone
        )
        let relative = RelativeDateTimeFormatter()
        relative.locale = locale
        relative.calendar = calendar
        relative.unitsStyle = .full
        return "\(date.formatted(style)) (\(relative.localizedString(for: max(date, now), relativeTo: now)))"
    }
}
