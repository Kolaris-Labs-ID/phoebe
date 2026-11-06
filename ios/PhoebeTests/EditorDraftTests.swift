import Foundation
import Testing
@testable import Phoebe

struct EditorDraftTests {
    static let mom = """
    {"schema_version":1,"language":"id","meeting_info":{"title":"Weekly sync","started_at":null,"timezone":"Asia/Jakarta","participants":["Budi"],"agenda":[]},"sections":[\
    {"id":"summary","type":"summary","heading":"Ringkasan","style":"paragraph","items":[{"id":"it_1","text":"Ringkas.","origin":"ai","edited":false,"sources":[]}]},\
    {"id":"discussion","type":"discussion","heading":"Pembahasan","style":"bullet","items":[{"id":"it_2","title":"Vendor","origin":"ai","edited":false,"sources":[],"points":[{"id":"it_3","text":"Vendor A","origin":"ai","edited":false,"sources":[]}]}]},\
    {"id":"decisions","type":"decisions","heading":"Keputusan","style":"numbered","items":[]},\
    {"id":"action_items","type":"action_items","heading":"Action items","style":null,"items":[{"id":"it_9","description":"Kirim RFQ","pic":null,"deadline":null,"deadline_phrase":"minggu depan","origin":"ai","edited":false,"sources":[{"type":"transcript","segment_id":"s4","start_ms":1000,"end_ms":2000}]}]}]}
    """

    static func draft() throws -> EditorDraft {
        let value = try DocumentContentValue.decode(kind: .mom, from: Data(mom.utf8))
        return EditorDraft(try #require(EditorDocument(value)))
    }

    static func text(_ d: EditorDraft, _ section: String, _ index: Int) -> String? {
        let items = d.document.section(section)?.items ?? []
        guard items.indices.contains(index), case .text(let t) = items[index] else { return nil }
        return t.text
    }

    @Test func editingAIItemSetsEditedAndKeepsOrigin() throws {
        var d = try Self.draft()
        d.setText(.item("summary", "it_1"), "Ringkasan baru.")
        guard case .text(let t)? = d.document.section("summary")?.items.first else { Issue.record("missing"); return }
        #expect(t.origin == .ai)
        #expect(t.edited)
        #expect(t.text == "Ringkasan baru.")
    }

    @Test func typedNewlineSplitsIntoNewHumanItemBelow() throws {
        var d = try Self.draft()
        let focus = d.setText(.item("summary", "it_1"), "Ringkas.\nKalimat kedua")
        let items = try #require(d.document.section("summary")?.items)
        #expect(items.count == 2)
        guard case .text(let first) = items[0], case .text(let second) = items[1] else { Issue.record("types"); return }
        #expect(first.text == "Ringkas.")
        #expect(!first.edited)
        #expect(second.text == "Kalimat kedua")
        #expect(second.origin == .human)
        #expect(second.id.hasPrefix("h_"))
        #expect(focus == .item("summary", second.id))
    }

    @Test func newlineInPointSplitsPoint() throws {
        var d = try Self.draft()
        let focus = d.setText(.point("discussion", "it_2", "it_3"), "Vendor A\nVendor B")
        guard case .topic(let topic)? = d.document.section("discussion")?.items.first else { Issue.record("topic"); return }
        #expect(topic.points.map(\.text) == ["Vendor A", "Vendor B"])
        #expect(focus == .point("discussion", "it_2", topic.points[1].id))
    }

    @Test func picBlankBecomesNilAndDeadlineIsDay() throws {
        var d = try Self.draft()
        d.setText(.pic("action_items", "it_9"), "Sari")
        d.setText(.pic("action_items", "it_9"), "  ")
        d.setDeadline(section: "action_items", item: "it_9", day: "2026-11-13")
        guard case .action(let a)? = d.document.section("action_items")?.items.first else { Issue.record("action"); return }
        #expect(a.pic == nil)
        #expect(a.deadline == "2026-11-13")
        #expect(a.deadlinePhrase == "minggu depan")
        #expect(a.edited)
        let wire = String(decoding: try d.document.wireData(), as: UTF8.self)
        #expect(wire.contains("\"pic\":null"))
    }

    @Test func undoCoalescesTypingPerFieldWithinPause() throws {
        var d = try Self.draft()
        let t0 = Date(timeIntervalSince1970: 1000)
        d.setText(.item("summary", "it_1"), "R", now: t0)
        d.setText(.item("summary", "it_1"), "Ri", now: t0.addingTimeInterval(0.5))
        d.setText(.item("summary", "it_1"), "Rin", now: t0.addingTimeInterval(0.9))
        d.setText(.item("summary", "it_1"), "Ring", now: t0.addingTimeInterval(3))
        #expect(Self.text(d, "summary", 0) == "Ring")
        d.undo()
        #expect(Self.text(d, "summary", 0) == "Rin")
        d.undo()
        #expect(Self.text(d, "summary", 0) == "Ringkas.")
        #expect(!d.canUndo)
        d.redo()
        #expect(Self.text(d, "summary", 0) == "Rin")
    }

    @Test func structuralChangesAreUndoable() throws {
        var d = try Self.draft()
        let original = d.document
        d.deleteSection("discussion")
        #expect(d.document.section("discussion") == nil)
        #expect(d.document.missingStandardTypes.contains(.discussion))
        d.addSection(.custom, heading: "Bagian baru")
        d.moveSection("summary", by: 1)
        d.setStyle(section: "decisions", .bullet)
        d.addItem(section: "decisions")
        d.undo(); d.undo(); d.undo(); d.undo(); d.undo()
        #expect(d.document == original)
    }

    @Test func standardSectionAddedAtMostOnce() throws {
        var d = try Self.draft()
        let duplicate = d.addSection(.summary, heading: "Ringkasan")
        #expect(duplicate == nil)
        let target = d.addSection(.openIssues, heading: "Hal terbuka")
        #expect(target == .heading("open_issues"))
        #expect(d.document.sections.last?.style == .bullet)
        let again = d.addSection(.openIssues, heading: "Hal terbuka")
        #expect(again == nil)
    }

    @Test func emptyNewItemsAreStrippedOnWire() throws {
        var d = try Self.draft()
        d.addItem(section: "decisions")
        d.addItem(section: "action_items")
        d.addParticipant()
        let wire = try DocumentContentValue.decode(kind: .mom, from: try d.document.wireData())
        guard case .mom(let content) = wire else { Issue.record("kind"); return }
        #expect(content.sections.first { $0.id == "decisions" }?.items.isEmpty == true)
        #expect(content.sections.first { $0.id == "action_items" }?.items.count == 1)
        #expect(content.meetingInfo.participants == ["Budi"])
        #expect(d.document.section("decisions")?.items.count == 1)
    }

    @Test func deleteReturnsPreviousSiblingFocus() throws {
        var d = try Self.draft()
        let maybeAdded = d.addItem(section: "summary", after: "it_1")
        let added = try #require(maybeAdded)
        let focus = d.delete(added)
        #expect(focus == .item("summary", "it_1"))
        #expect(d.document.section("summary")?.items.count == 1)
    }

    @Test func aiNotesRoundTripThroughEditorDocument() throws {
        let value = try DocumentContentValue.decode(kind: .aiNotes, from: Data(Fixtures.aiNotes.utf8))
        var d = EditorDraft(try #require(EditorDocument(value)))
        #expect(d.document.value == value)
        d.setText(.item("sec_1", "it_1"), "Budget naik 12%.")
        guard case .aiNotes(let notes) = d.document.value else { Issue.record("kind"); return }
        #expect(notes.sections[0].items[0].text == "Budget naik 12%.")
        #expect(notes.sections[0].items[0].edited)
        #expect(notes.sections[0].style == .bullet)
    }

    @Test func deadlineDayUsesLocalCalendar() throws {
        var cal = Calendar(identifier: .gregorian)
        cal.timeZone = try #require(TimeZone(identifier: "Asia/Jakarta"))
        let date = try #require(cal.date(from: DateComponents(year: 2026, month: 11, day: 13)))
        #expect(DeadlineDay.string(from: date, calendar: cal) == "2026-11-13")
        let back = try #require(DeadlineDay.date(from: "2026-11-13", calendar: cal))
        #expect(cal.dateComponents([.year, .month, .day], from: back) == DateComponents(year: 2026, month: 11, day: 13))
    }

    @Test func saveStateResolution() {
        #expect(SaveStateResolver.resolve(debouncing: true, pending: nil, online: true, flushing: false, flushFailed: false) == .saving)
        #expect(SaveStateResolver.resolve(debouncing: false, pending: nil, online: true, flushing: false, flushFailed: false) == .synced)
        let pending = PendingEditSnapshot(hasPendingWork: true)
        #expect(SaveStateResolver.resolve(debouncing: false, pending: pending, online: false, flushing: false, flushFailed: false) == .offline)
        #expect(SaveStateResolver.resolve(debouncing: false, pending: pending, online: true, flushing: true, flushFailed: false) == .syncing)
        #expect(SaveStateResolver.resolve(debouncing: false, pending: pending, online: true, flushing: false, flushFailed: false) == .local)
        #expect(SaveStateResolver.resolve(debouncing: false, pending: pending, online: true, flushing: false, flushFailed: true) == .notSynced)
        let conflict = PendingEditSnapshot(hasPendingWork: true, conflictCurrentRevision: 5)
        #expect(SaveStateResolver.resolve(debouncing: false, pending: conflict, online: false, flushing: false, flushFailed: false) == .notSynced)
    }
}
