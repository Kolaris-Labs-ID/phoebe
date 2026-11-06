import Foundation
import Testing
@testable import Phoebe

struct FormattingTests {
    @Test func timerUsesMinutesSecondsUnderOneHour() {
        #expect(Formatting.timer(ms: 0) == "00:00")
        #expect(Formatting.timer(ms: 65_400) == "01:05")
        #expect(Formatting.timer(ms: 3_599_999) == "59:59")
    }

    @Test func timerUsesHoursFromOneHour() {
        #expect(Formatting.timer(ms: 3_600_000) == "1:00:00")
        #expect(Formatting.timer(ms: 3_725_000) == "1:02:05")
    }

    @Test func referenceBrackets() {
        #expect(Formatting.reference(ms: 724_000) == "[12:04]")
        #expect(Formatting.reference(ms: 3_725_000) == "[1:02:05]")
        #expect(Formatting.timestamp(ms: 724_000) == "12:04")
    }

    @Test func spokenDurationIndonesianAndEnglish() {
        #expect(Formatting.spokenDuration(ms: 724_000, locale: Locale(identifier: "id_ID")) == "12 menit, 4 detik")
        #expect(Formatting.spokenDuration(ms: 724_000, locale: Locale(identifier: "en_US")) == "12 minutes, 4 seconds")
        #expect(Formatting.spokenDuration(ms: 3_725_000, locale: Locale(identifier: "en_US")) == "1 hour, 2 minutes, 5 seconds")
        #expect(Formatting.spokenDuration(ms: 2_530_000, locale: Locale(identifier: "id_ID"), includeSeconds: false) == "42 menit")
    }

    @Test func deadlineBothNullIsNotDetermined() {
        #expect(Formatting.deadline(date: nil, phrase: nil, locale: Locale(identifier: "id_ID"), localization: "id") == "Belum ditentukan")
        #expect(Formatting.deadline(date: nil, phrase: nil, locale: Locale(identifier: "en_US"), localization: "en") == "Not determined")
    }

    @Test func deadlinePhraseOnlyShowsPhrase() {
        #expect(Formatting.deadline(date: nil, phrase: "minggu depan", locale: Locale(identifier: "id_ID"), localization: "id") == "minggu depan")
    }

    @Test func deadlineDateAndPhrase() {
        #expect(Formatting.deadline(date: "2026-11-13", phrase: "minggu depan", locale: Locale(identifier: "id_ID"), localization: "id") == "13 Nov 2026 (minggu depan)")
        #expect(Formatting.deadline(date: "2026-11-13", phrase: nil, locale: Locale(identifier: "id_ID"), localization: "id") == "13 Nov 2026")
        #expect(Formatting.deadline(date: "2026-11-13", phrase: nil, locale: Locale(identifier: "en_US"), localization: "en") == "Nov 13, 2026")
    }

    @Test func picNullIsNotDetermined() {
        #expect(Formatting.notDetermined(nil, localization: "id") == "Belum ditentukan")
        #expect(Formatting.notDetermined("Budi", localization: "id") == "Budi")
    }

    @Test func dayRoundTrip() throws {
        let d = try #require(Formatting.parseDay("2026-11-13"))
        #expect(Formatting.dayString(d) == "2026-11-13")
        #expect(Formatting.parseDay("13-11-2026") == nil)
    }

    @Test func localizedFormatHelpers() {
        #expect(LS("status.uploading", localization: "id", Int64(45)) == "Mengunggah 45%")
        #expect(LS("status.uploading", localization: "en", Int64(45)) == "Uploading 45%")
        #expect(LS("ref.playAt", localization: "id", "12:04") == "Putar dari 12:04")
        #expect(String(localized: L("status.uploading", Int64(45))) == LS("status.uploading", Int64(45)))
        #expect(String(localized: L("common.close")) == LS("common.close"))
    }
}
