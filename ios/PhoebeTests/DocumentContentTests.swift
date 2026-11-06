import Foundation
import Testing
@testable import Phoebe

enum Fixtures {
    static let manualNotes = """
    {"schema_version":1,"items":[{"id":"n1","text":"Bahas budget Q1","at_ms":125000},{"id":"n2","text":"Follow up vendor","at_ms":null}]}
    """

    static let aiNotes = """
    {"schema_version":1,"language":"id","sections":[{"id":"sec_1","heading":"Budget Q1","style":"bullet","items":[{"id":"it_1","text":"Budget marketing naik 10%.","origin":"ai","edited":false,"sources":[{"type":"transcript","segment_id":"s12","start_ms":61000,"end_ms":66000,"approximate":false}]}]}]}
    """

    static let mom = """
    {"schema_version":1,"language":"id","meeting_info":{"title":"Weekly sync","started_at":"2026-11-06T02:00:00.000Z","timezone":"Asia/Jakarta","participants":["Budi","Sari"],"agenda":[{"id":"it_1","text":"Budget","origin":"ai","edited":false,"sources":[]}]},"sections":[\
    {"id":"summary","type":"summary","heading":"Ringkasan","style":"paragraph","items":[{"id":"it_2","text":"Ringkas.","origin":"ai","edited":true,"sources":[{"type":"transcript","segment_id":"s1","start_ms":0,"end_ms":4200,"approximate":false}]}]},\
    {"id":"discussion","type":"discussion","heading":"Pembahasan","style":"bullet","items":[{"id":"it_4","title":"Rencana rilis","origin":"ai","edited":false,"sources":[],"points":[{"id":"it_5","text":"Rilis Desember","origin":"ai","edited":false,"sources":[{"type":"user_notes","note_id":"n3"}]}]}]},\
    {"id":"decisions","type":"decisions","heading":"Keputusan","style":"numbered","items":[]},\
    {"id":"action_items","type":"action_items","heading":"Action items","style":null,"items":[{"id":"it_9","description":"Siapkan laporan vendor","pic":"Budi","deadline":"2026-11-13","deadline_phrase":"minggu depan","origin":"ai","edited":false,"sources":[{"type":"transcript","segment_id":"s42","start_ms":734000,"end_ms":741500,"approximate":true}]},{"id":"h_1","description":"Cek kontrak","pic":null,"deadline":null,"deadline_phrase":null,"origin":"human","edited":false,"sources":[]}]},\
    {"id":"open_issues","type":"open_issues","heading":"Hal terbuka","style":"bullet","items":[]},\
    {"id":"follow_ups","type":"follow_ups","heading":"Tindak lanjut","style":"bullet","items":[]},\
    {"id":"h_2","type":"custom","heading":"Catatan lain","style":"bullet","items":[{"id":"h_3","text":"Lain","origin":"human","edited":false,"sources":[]}]}]}
    """

    static func canonical(_ json: String) throws -> Data {
        let object = try JSONSerialization.jsonObject(with: Data(json.utf8))
        return try JSONSerialization.data(withJSONObject: object, options: [.sortedKeys, .withoutEscapingSlashes])
    }

    static func string(_ data: Data) -> String { String(decoding: data, as: UTF8.self) }
}

@MainActor struct DocumentContentTests {
    func roundTrip<T: Codable & Equatable>(_ type: T.Type, _ json: String) throws {
        let decoded = try APICoding.decoder().decode(T.self, from: Data(json.utf8))
        let encoded = try APICoding.encoder().encode(decoded)
        #expect(Fixtures.string(encoded) == Fixtures.string(try Fixtures.canonical(json)))
        let again = try APICoding.encoder().encode(try APICoding.decoder().decode(T.self, from: encoded))
        #expect(again == encoded)
    }

    @Test func manualNotesByteStable() throws { try roundTrip(ManualNotesContent.self, Fixtures.manualNotes) }
    @Test func aiNotesByteStable() throws { try roundTrip(AINotesContent.self, Fixtures.aiNotes) }
    @Test func momByteStable() throws { try roundTrip(MoMContent.self, Fixtures.mom) }

    @Test func momDecodesItemKindsBySectionType() throws {
        let mom = try APICoding.decoder().decode(MoMContent.self, from: Data(Fixtures.mom.utf8))
        let byID = Dictionary(uniqueKeysWithValues: mom.sections.map { ($0.id, $0) })
        guard case .topic(let topic) = byID["discussion"]?.items.first else { Issue.record("discussion"); return }
        #expect(topic.points.first?.sources == [.userNotes(noteId: "n3")])
        guard case .action(let action) = byID["action_items"]?.items.first else { Issue.record("action"); return }
        #expect(action.deadlinePhrase == "minggu depan")
        #expect(byID["action_items"]?.style == nil)
        guard case .text(let summary) = byID["summary"]?.items.first else { Issue.record("summary"); return }
        #expect(summary.sources == [.transcript(segmentId: "s1", startMs: 0, endMs: 4200)])
        #expect(mom.meetingInfo.startedAt == APICoding.parseDate("2026-11-06T02:00:00.000Z"))
    }

    @Test func nilEncodedAsNull() throws {
        let action = ActionItem.human(description: "Cek")
        let a = Fixtures.string(try APICoding.encoder().encode(action))
        #expect(a.contains("\"pic\":null"))
        #expect(a.contains("\"deadline\":null"))
        #expect(a.contains("\"deadline_phrase\":null"))

        let note = Fixtures.string(try APICoding.encoder().encode(NoteItem(id: "n1", text: "x", atMs: nil)))
        #expect(note.contains("\"at_ms\":null"))

        let info = MeetingInfo(title: "", startedAt: nil, timezone: "Asia/Jakarta", participants: [], agenda: [])
        #expect(Fixtures.string(try APICoding.encoder().encode(info)).contains("\"started_at\":null"))

        let section = MoMSection(id: "action_items", type: .actionItems, heading: "Action items", style: nil, items: [])
        #expect(Fixtures.string(try APICoding.encoder().encode(section)).contains("\"style\":null"))
    }

    @Test func editingAIItemSetsEditedAndKeepsOrigin() {
        var item = TextItem(id: "it_1", text: "a", origin: .ai, edited: false, sources: [])
        item.setText("a")
        #expect(item.edited == false)
        item.setText("b")
        #expect(item.edited && item.origin == .ai)
        #expect(OriginMarker.key(origin: item.origin, edited: item.edited) == "origin.aiEdited")

        var action = ActionItem(id: "it_9", description: "d", pic: "Budi", deadline: nil, deadlinePhrase: nil, origin: .ai, edited: false, sources: [])
        action.setPIC("  ")
        #expect(action.pic == nil && action.edited)

        var topic = TopicItem(id: "it_4", title: "t", origin: .ai, edited: false, sources: [], points: [])
        topic.setTitle("t2")
        #expect(topic.edited)
    }

    @Test func editingHumanItemStaysUnedited() {
        var item = TextItem.human("x")
        item.setText("y")
        #expect(item.origin == .human && !item.edited)
    }

    @Test func newItemsAreHumanWithClientIDs() {
        let t = TextItem.human()
        let a = ActionItem.human()
        let topic = TopicItem.human()
        let note = NoteItem.new(text: "x", atMs: 1000)
        for id in [t.id, a.id, topic.id, note.id] {
            #expect(id.hasPrefix("h_"))
            #expect(id.count <= 64)
            #expect(id.allSatisfy { $0.isLetter || $0.isNumber || $0 == "_" || $0 == "-" })
        }
        #expect(t.origin == .human && t.sources.isEmpty && !t.edited)
        #expect(a.pic == nil && a.deadline == nil && a.deadlinePhrase == nil)
        #expect(t.id != TextItem.human().id)
    }

    @Test func wireContentStripsEmptyItems() {
        let notes = ManualNotesContent(items: [
            NoteItem(id: "n1", text: "keep", atMs: nil),
            NoteItem(id: "n2", text: "   ", atMs: 10),
        ]).wireContent()
        #expect(notes.items.map(\.id) == ["n1"])

        var mom = MoMContent.emptyTemplate(language: "id", title: "T", startedAt: nil, timezone: "Asia/Jakarta", participants: [" Budi ", "", "  "])
        mom.meetingInfo.agenda = [TextItem.human(""), TextItem.human("Agenda")]
        mom.sections[0].items = [.text(.human("")), .text(.human("Ringkas"))]
        var pointOnly = TopicItem.human(title: "")
        pointOnly.points = [.human("poin")]
        var emptyTopic = TopicItem.human(title: " ")
        emptyTopic.points = [.human(" ")]
        mom.sections[1].items = [.topic(emptyTopic), .topic(pointOnly)]
        mom.sections[3].items = [.action(.human(description: "")), .action(.human(description: "Tugas"))]

        let wire = mom.wireContent()
        #expect(wire.meetingInfo.participants == ["Budi"])
        #expect(wire.meetingInfo.agenda.map(\.text) == ["Agenda"])
        #expect(wire.sections[0].items.count == 1)
        #expect(wire.sections[1].items == [.topic(pointOnly)])
        #expect(wire.sections[3].items.count == 1)
        #expect(mom.sections[0].items.count == 2)

        let ai = AINotesContent(language: "id", sections: [NotesSection(id: "sec_1", heading: "H", style: .bullet, items: [.human(""), .human("x")])]).wireContent()
        #expect(ai.sections[0].items.map(\.text) == ["x"])
    }

    @Test func emptyTemplateHasSixStandardSections() {
        let mom = MoMContent.emptyTemplate(language: "en", title: "T", startedAt: nil, timezone: "UTC", participants: [])
        #expect(mom.sections.map(\.type) == [.summary, .discussion, .decisions, .actionItems, .openIssues, .followUps])
        #expect(mom.sections.map(\.heading) == ["Summary", "Discussion", "Decisions", "Action items", "Open issues", "Follow-ups"])
        #expect(mom.sections.first { $0.type == .actionItems }?.style == nil)
        #expect(mom.sections.allSatisfy { $0.items.isEmpty })
    }

    @Test func documentRevisionDecodesContentByKind() throws {
        let json = """
        {"id":"7d9f8e2a-0000-4000-8000-000000000001","document_id":"7d9f8e2a-0000-4000-8000-000000000002","kind":"ai_notes","revision_number":3,\
        "origin":"user_edit","restored_from_revision_number":null,"reviewed_at":null,"created_at":"2026-11-06T02:00:00.000Z",\
        "source":{"transcript_revision_id":null,"transcript_revision_number":null,"manual_notes_revision_id":null,"manual_notes_revision_number":null,"ai_notes_revision_id":null,"ai_notes_revision_number":null},\
        "content":\(Fixtures.aiNotes)}
        """
        let rev = try APICoding.decoder().decode(DocumentRevisionDTO.self, from: Data(json.utf8))
        guard case .aiNotes(let notes) = rev.content else { Issue.record("content"); return }
        #expect(notes.sections.first?.items.first?.sources.first == .transcript(segmentId: "s12", startMs: 61000, endMs: 66000))
        #expect(Fixtures.string(try rev.content!.encoded()) == Fixtures.string(try Fixtures.canonical(Fixtures.aiNotes)))

        let listing = json.replacingOccurrences(of: ",\"content\":\(Fixtures.aiNotes)", with: "")
        #expect(try APICoding.decoder().decode(DocumentRevisionDTO.self, from: Data(listing.utf8)).content == nil)
    }

    @Test func approximateRefRoundTripsAndAbsentMeansExact() throws {
        let mom = try APICoding.decoder().decode(MoMContent.self, from: Data(Fixtures.mom.utf8))
        guard case .action(let action) = mom.sections.first(where: { $0.id == "action_items" })?.items.first else { Issue.record("action"); return }
        #expect(action.sources == [.transcript(segmentId: "s42", startMs: 734000, endMs: 741500, approximate: true)])
        let legacy = try APICoding.decoder().decode(SourceRef.self, from: Data(#"{"type":"transcript","segment_id":"s1","start_ms":0,"end_ms":10}"#.utf8))
        #expect(legacy == .transcript(segmentId: "s1", startMs: 0, endMs: 10, approximate: false))
    }

    @Test func chunkTimingIsCopiedOntoSegmentsAndSurvivesLocalStorage() throws {
        let json = """
        {"id":"7d9f8e2a-0000-4000-8000-000000000003","revision_number":2,"kind":"correction","language":"id","timing":"chunk","created_at":"2026-11-06T02:00:00.000Z",        "segments":[{"id":"s1","start_ms":0,"end_ms":30000,"text":"Halo","uncertain":false,"uncertain_reason":null},        {"id":"s2","start_ms":30000,"end_ms":60000,"text":"Budget lima puluh","uncertain":true,"uncertain_reason":"low_confidence"}]}
        """
        let dto = try APICoding.decoder().decode(TranscriptRevisionDTO.self, from: Data(json.utf8))
        #expect(dto.timing == .chunk)
        let segments = try #require(dto.segments)
        #expect(segments.allSatisfy { $0.isApproximate })
        #expect(segments.map(\.uncertain) == [false, true])
        #expect(segments[1].uncertainReason == "low_confidence")

        let stored = try APICoding.encoder().encode(segments)
        #expect(try APICoding.decoder().decode([TranscriptSegment].self, from: stored) == segments)

        let segmentTimed = json.replacingOccurrences(of: #""timing":"chunk""#, with: #""timing":"segment""#)
        #expect(try APICoding.decoder().decode(TranscriptRevisionDTO.self, from: Data(segmentTimed.utf8)).segments?.contains(where: \.isApproximate) == false)
    }

    @Test func locallyCachedSegmentsWithoutNewFieldsDefaultToApproximateAndCertain() throws {
        let cached = try APICoding.decoder().decode([TranscriptSegment].self, from: Data(#"[{"id":"s1","start_ms":0,"end_ms":10,"text":"a"}]"#.utf8))
        #expect(cached.first?.timing == .chunk)
        #expect(cached.first?.uncertain == false)
    }

    @Test func transcriptionBodiesCarryNotesMode() throws {
        #expect(Fixtures.string(APICoding.encode(TranscriptionRequest(language: .id, notes: .skip))) == #"{"language":"id","notes":"skip"}"#)
        let legacy = try APICoding.decoder().decode(TranscriptionRequest.self, from: Data(#"{"language":"en"}"#.utf8))
        #expect(legacy == TranscriptionRequest(language: .en, notes: .auto))
        let key = UUID()
        let start = Fixtures.string(APICoding.encode(TranscriptionStart(idempotencyKey: key, language: .auto, notes: .skip)))
        #expect(start == #"{"idempotency_key":"\#(key.lower)","language":"auto","notes":"skip"}"#)
    }

    @Test func correctionSendsResolvedUncertainSegments() throws {
        let body = TranscriptCorrectionRequest(expectedRevision: 2, mutationId: UUID(), edits: [SegmentEdit(segmentId: "s2", text: "Budget 50")], resolvedUncertainSegmentIds: ["s2"])
        let json = try #require(JSONSerialization.jsonObject(with: APICoding.encode(body)) as? [String: Any])
        #expect(json["resolved_uncertain_segment_ids"] as? [String] == ["s2"])
    }

    @Test func uploadResultDecodesTranscriptionBlocked() throws {
        let session = #"{"id":"7d9f8e2a-0000-4000-8000-000000000004","meeting_id":"7d9f8e2a-0000-4000-8000-000000000005","status":"completed","content_type":"audio/mp4","size_bytes":10,"received_bytes":10,"sha256":"abc","duration_ms":60000,"chunk_max_bytes":10,"failure_reason":null,"auto_transcription":true,"expires_at":"2026-11-06T02:00:00.000Z","created_at":"2026-11-06T02:00:00.000Z"}"#
        let json = #"{"upload":\#(session),"audio":null,"job":null,"transcription_blocked":{"code":"ai_generation_limit","next_slot_at":"2026-11-06T05:00:00.000Z"}}"#
        let result = try APICoding.decoder().decode(UploadResultDTO.self, from: Data(json.utf8))
        #expect(result.job == nil)
        #expect(result.transcriptionBlocked == TranscriptionBlockedDTO(code: "ai_generation_limit", nextSlotAt: APICoding.parseDate("2026-11-06T05:00:00.000Z")))
    }
}
