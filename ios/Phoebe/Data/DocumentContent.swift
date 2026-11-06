import Foundation

enum DocumentKind: String, Codable, CaseIterable, Sendable, Hashable {
    case manualNotes = "manual_notes"
    case aiNotes = "ai_notes"
    case mom
}

enum ClientID {
    static func make() -> String { "h_" + UUID().uuidString.lowercased() }
}

// MARK: - Manual notes (api §3.1)

struct ManualNotesContent: Codable, Equatable, Sendable {
    var schemaVersion = 1
    var items: [NoteItem]

    init(schemaVersion: Int = 1, items: [NoteItem] = []) {
        self.schemaVersion = schemaVersion
        self.items = items
    }

    func wireContent() -> ManualNotesContent {
        var copy = self
        copy.items = items.filter { !$0.text.isBlank }
        return copy
    }
}

struct NoteItem: Codable, Equatable, Identifiable, Sendable {
    var id: String
    var text: String
    var atMs: Int?

    init(id: String, text: String, atMs: Int?) {
        self.id = id
        self.text = text
        self.atMs = atMs
    }

    static func new(text: String = "", atMs: Int? = nil) -> NoteItem {
        NoteItem(id: ClientID.make(), text: text, atMs: atMs)
    }

    enum CodingKeys: String, CodingKey { case id, text, atMs }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(id, forKey: .id)
        try c.encode(text, forKey: .text)
        try c.encodeNullable(atMs, forKey: .atMs)
    }
}

// MARK: - AI notes (api §3.2)

struct AINotesContent: Codable, Equatable, Sendable {
    var schemaVersion = 1
    var language: String
    var sections: [NotesSection]

    init(schemaVersion: Int = 1, language: String, sections: [NotesSection]) {
        self.schemaVersion = schemaVersion
        self.language = language
        self.sections = sections
    }

    func wireContent() -> AINotesContent {
        var copy = self
        copy.sections = sections.map { section in
            var s = section
            s.items = section.items.filter { !$0.text.isBlank }
            return s
        }
        return copy
    }
}

struct NotesSection: Codable, Equatable, Identifiable, Sendable {
    var id: String
    var heading: String
    var style: ListStyle
    var items: [TextItem]
}

// MARK: - MoM (api §3.3)

struct MoMContent: Codable, Equatable, Sendable {
    var schemaVersion = 1
    var language: String
    var meetingInfo: MeetingInfo
    var sections: [MoMSection]

    init(schemaVersion: Int = 1, language: String, meetingInfo: MeetingInfo, sections: [MoMSection]) {
        self.schemaVersion = schemaVersion
        self.language = language
        self.meetingInfo = meetingInfo
        self.sections = sections
    }

    func wireContent() -> MoMContent {
        var copy = self
        copy.meetingInfo.agenda = meetingInfo.agenda.filter { !$0.text.isBlank }
        copy.meetingInfo.participants = meetingInfo.participants
            .map { $0.trimmingCharacters(in: .whitespacesAndNewlines) }
            .filter { !$0.isEmpty }
        copy.sections = sections.map { section in
            var s = section
            s.items = section.items.compactMap { $0.wireItem() }
            return s
        }
        return copy
    }

    static func emptyTemplate(language: String, title: String, startedAt: Date?, timezone: String, participants: [String]) -> MoMContent {
        let info = MeetingInfo(title: title, startedAt: startedAt, timezone: timezone, participants: participants, agenda: [])
        let types: [SectionType] = [.summary, .discussion, .decisions, .actionItems, .openIssues, .followUps]
        let sections = types.map { type in
            MoMSection(id: type.rawValue, type: type, heading: type.defaultHeading(language: language), style: type.defaultStyle, items: [])
        }
        return MoMContent(language: language, meetingInfo: info, sections: sections)
    }
}

struct MeetingInfo: Codable, Equatable, Sendable {
    var title: String
    var startedAt: Date?
    var timezone: String
    var participants: [String]
    var agenda: [TextItem]

    enum CodingKeys: String, CodingKey { case title, startedAt, timezone, participants, agenda }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(title, forKey: .title)
        try c.encodeNullable(startedAt, forKey: .startedAt)
        try c.encode(timezone, forKey: .timezone)
        try c.encode(participants, forKey: .participants)
        try c.encode(agenda, forKey: .agenda)
    }
}

enum SectionType: String, Codable, Sendable, CaseIterable {
    case summary, discussion, decisions
    case actionItems = "action_items"
    case openIssues = "open_issues"
    case followUps = "follow_ups"
    case custom

    var defaultStyle: ListStyle? {
        switch self {
        case .summary: .paragraph
        case .decisions: .numbered
        case .actionItems: nil
        case .discussion, .openIssues, .followUps, .custom: .bullet
        }
    }

    func defaultHeading(language: String) -> String {
        let id = language == "id"
        switch self {
        case .summary: return id ? "Ringkasan" : "Summary"
        case .discussion: return id ? "Pembahasan" : "Discussion"
        case .decisions: return id ? "Keputusan" : "Decisions"
        case .actionItems: return "Action items"
        case .openIssues: return id ? "Hal terbuka" : "Open issues"
        case .followUps: return id ? "Tindak lanjut" : "Follow-ups"
        case .custom: return id ? "Catatan lain" : "Other notes"
        }
    }
}

enum ListStyle: String, Codable, Sendable, CaseIterable {
    case bullet, numbered, paragraph
}

enum MoMItem: Equatable, Sendable {
    case text(TextItem)
    case topic(TopicItem)
    case action(ActionItem)

    var id: String {
        switch self {
        case .text(let i): i.id
        case .topic(let i): i.id
        case .action(let i): i.id
        }
    }

    func wireItem() -> MoMItem? {
        switch self {
        case .text(let i):
            return i.text.isBlank ? nil : self
        case .topic(var t):
            t.points = t.points.filter { !$0.text.isBlank }
            return t.title.isBlank && t.points.isEmpty ? nil : .topic(t)
        case .action(let a):
            return a.description.isBlank ? nil : self
        }
    }
}

struct MoMSection: Codable, Equatable, Identifiable, Sendable {
    var id: String
    var type: SectionType
    var heading: String
    var style: ListStyle?
    var items: [MoMItem]

    enum CodingKeys: String, CodingKey { case id, type, heading, style, items }

    init(id: String, type: SectionType, heading: String, style: ListStyle?, items: [MoMItem]) {
        self.id = id
        self.type = type
        self.heading = heading
        self.style = style
        self.items = items
    }

    static func newCustom(language: String) -> MoMSection {
        MoMSection(id: ClientID.make(), type: .custom, heading: SectionType.custom.defaultHeading(language: language), style: .bullet, items: [])
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        type = try c.decode(SectionType.self, forKey: .type)
        heading = try c.decode(String.self, forKey: .heading)
        style = try c.decodeIfPresent(ListStyle.self, forKey: .style)
        switch type {
        case .discussion: items = try c.decode([TopicItem].self, forKey: .items).map(MoMItem.topic)
        case .actionItems: items = try c.decode([ActionItem].self, forKey: .items).map(MoMItem.action)
        default: items = try c.decode([TextItem].self, forKey: .items).map(MoMItem.text)
        }
    }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(id, forKey: .id)
        try c.encode(type, forKey: .type)
        try c.encode(heading, forKey: .heading)
        try c.encodeNullable(style, forKey: .style)
        var list = c.nestedUnkeyedContainer(forKey: .items)
        for item in items {
            switch item {
            case .text(let i): try list.encode(i)
            case .topic(let i): try list.encode(i)
            case .action(let i): try list.encode(i)
            }
        }
    }
}

enum Origin: String, Codable, Sendable {
    case ai, human
}

struct TextItem: Codable, Equatable, Identifiable, Sendable {
    var id: String
    var text: String
    var origin: Origin
    var edited: Bool
    var sources: [SourceRef]

    static func human(_ text: String = "") -> TextItem {
        TextItem(id: ClientID.make(), text: text, origin: .human, edited: false, sources: [])
    }

    mutating func setText(_ value: String) {
        guard value != text else { return }
        text = value
        markEdited()
    }

    mutating func markEdited() { if origin == .ai { edited = true } }
}

struct TopicItem: Codable, Equatable, Identifiable, Sendable {
    var id: String
    var title: String
    var origin: Origin
    var edited: Bool
    var sources: [SourceRef]
    var points: [TextItem]

    static func human(title: String = "") -> TopicItem {
        TopicItem(id: ClientID.make(), title: title, origin: .human, edited: false, sources: [], points: [])
    }

    mutating func setTitle(_ value: String) {
        guard value != title else { return }
        title = value
        if origin == .ai { edited = true }
    }
}

struct ActionItem: Codable, Equatable, Identifiable, Sendable {
    var id: String
    var description: String
    var pic: String?
    var deadline: String?
    var deadlinePhrase: String?
    var origin: Origin
    var edited: Bool
    var sources: [SourceRef]

    static func human(description: String = "") -> ActionItem {
        ActionItem(id: ClientID.make(), description: description, pic: nil, deadline: nil, deadlinePhrase: nil, origin: .human, edited: false, sources: [])
    }

    mutating func setDescription(_ value: String) {
        guard value != description else { return }
        description = value
        markEdited()
    }

    mutating func setPIC(_ value: String?) {
        let v = value?.nonBlank
        guard v != pic else { return }
        pic = v
        markEdited()
    }

    mutating func setDeadline(_ value: String?) {
        let v = value?.nonBlank
        guard v != deadline else { return }
        deadline = v
        markEdited()
    }

    mutating func setDeadlinePhrase(_ value: String?) {
        let v = value?.nonBlank
        guard v != deadlinePhrase else { return }
        deadlinePhrase = v
        markEdited()
    }

    private mutating func markEdited() { if origin == .ai { edited = true } }

    enum CodingKeys: String, CodingKey { case id, description, pic, deadline, deadlinePhrase, origin, edited, sources }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(id, forKey: .id)
        try c.encode(description, forKey: .description)
        try c.encodeNullable(pic, forKey: .pic)
        try c.encodeNullable(deadline, forKey: .deadline)
        try c.encodeNullable(deadlinePhrase, forKey: .deadlinePhrase)
        try c.encode(origin, forKey: .origin)
        try c.encode(edited, forKey: .edited)
        try c.encode(sources, forKey: .sources)
    }
}

enum SourceRef: Equatable, Hashable, Sendable {
    // `approximate` is set by the server from the transcript revision's timing; the client only round-trips it.
    case transcript(segmentId: String, startMs: Int, endMs: Int, approximate: Bool = false)
    case userNotes(noteId: String)
}

extension SourceRef: Codable {
    enum CodingKeys: String, CodingKey { case type, segmentId, startMs, endMs, approximate, noteId }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        switch try c.decode(String.self, forKey: .type) {
        case "transcript":
            self = .transcript(
                segmentId: try c.decode(String.self, forKey: .segmentId),
                startMs: try c.decode(Int.self, forKey: .startMs),
                endMs: try c.decode(Int.self, forKey: .endMs),
                approximate: try c.decodeIfPresent(Bool.self, forKey: .approximate) ?? false
            )
        case "user_notes":
            self = .userNotes(noteId: try c.decode(String.self, forKey: .noteId))
        case let other:
            throw DecodingError.dataCorruptedError(forKey: .type, in: c, debugDescription: "unknown source type \(other)")
        }
    }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        switch self {
        case let .transcript(segmentId, startMs, endMs, approximate):
            try c.encode("transcript", forKey: .type)
            try c.encode(segmentId, forKey: .segmentId)
            try c.encode(startMs, forKey: .startMs)
            try c.encode(endMs, forKey: .endMs)
            try c.encode(approximate, forKey: .approximate)
        case let .userNotes(noteId):
            try c.encode("user_notes", forKey: .type)
            try c.encode(noteId, forKey: .noteId)
        }
    }
}

// MARK: - Kind-tagged content

enum DocumentContentValue: Equatable, Sendable {
    case manualNotes(ManualNotesContent)
    case aiNotes(AINotesContent)
    case mom(MoMContent)

    var kind: DocumentKind {
        switch self {
        case .manualNotes: .manualNotes
        case .aiNotes: .aiNotes
        case .mom: .mom
        }
    }

    static func decode(kind: DocumentKind, from data: Data) throws -> DocumentContentValue {
        let decoder = APICoding.decoder()
        switch kind {
        case .manualNotes: return .manualNotes(try decoder.decode(ManualNotesContent.self, from: data))
        case .aiNotes: return .aiNotes(try decoder.decode(AINotesContent.self, from: data))
        case .mom: return .mom(try decoder.decode(MoMContent.self, from: data))
        }
    }

    static func decode(kind: DocumentKind, from decoder: Decoder) throws -> DocumentContentValue {
        switch kind {
        case .manualNotes: return .manualNotes(try ManualNotesContent(from: decoder))
        case .aiNotes: return .aiNotes(try AINotesContent(from: decoder))
        case .mom: return .mom(try MoMContent(from: decoder))
        }
    }

    func wireContent() -> DocumentContentValue {
        switch self {
        case .manualNotes(let c): .manualNotes(c.wireContent())
        case .aiNotes(let c): .aiNotes(c.wireContent())
        case .mom(let c): .mom(c.wireContent())
        }
    }

    func encoded() throws -> Data {
        let encoder = APICoding.encoder()
        switch self {
        case .manualNotes(let c): return try encoder.encode(c)
        case .aiNotes(let c): return try encoder.encode(c)
        case .mom(let c): return try encoder.encode(c)
        }
    }

    func wireData() throws -> Data { try wireContent().encoded() }
}

extension KeyedEncodingContainer {
    mutating func encodeNullable<T: Encodable>(_ value: T?, forKey key: Key) throws {
        if let value { try encode(value, forKey: key) } else { try encodeNil(forKey: key) }
    }
}

extension String {
    var isBlank: Bool { trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }
    var nonBlank: String? { isBlank ? nil : self }
}
