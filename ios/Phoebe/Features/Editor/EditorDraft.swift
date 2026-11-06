import Foundation

// AI notes sections are held as MoM `custom` sections of text items so one editor serves both kinds.
struct EditorDocument: Equatable, Sendable {
    let kind: DocumentKind
    var schemaVersion: Int
    var language: String
    var meetingInfo: MeetingInfo?
    var sections: [MoMSection]

    init?(_ value: DocumentContentValue) {
        switch value {
        case .mom(let c):
            kind = .mom
            schemaVersion = c.schemaVersion
            language = c.language
            meetingInfo = c.meetingInfo
            sections = c.sections
        case .aiNotes(let c):
            kind = .aiNotes
            schemaVersion = c.schemaVersion
            language = c.language
            meetingInfo = nil
            sections = c.sections.map {
                MoMSection(id: $0.id, type: .custom, heading: $0.heading, style: $0.style, items: $0.items.map(MoMItem.text))
            }
        case .manualNotes:
            return nil
        }
    }

    var value: DocumentContentValue {
        switch kind {
        case .aiNotes:
            let notes = sections.map { s in
                NotesSection(id: s.id, heading: s.heading, style: s.style ?? .bullet, items: s.items.compactMap {
                    if case .text(let t) = $0 { t } else { nil }
                })
            }
            return .aiNotes(AINotesContent(schemaVersion: schemaVersion, language: language, sections: notes))
        default:
            let info = meetingInfo ?? MeetingInfo(title: "", startedAt: nil, timezone: TimeZone.current.identifier, participants: [], agenda: [])
            return .mom(MoMContent(schemaVersion: schemaVersion, language: language, meetingInfo: info, sections: sections))
        }
    }

    func wireData() throws -> Data { try value.wireData() }

    func section(_ id: String) -> MoMSection? { sections.first { $0.id == id } }

    var missingStandardTypes: [SectionType] {
        guard kind == .mom else { return [] }
        let present = Set(sections.map(\.type))
        return SectionType.allCases.filter { $0 != .custom && !present.contains($0) }
    }
}

enum EditTarget: Hashable, Sendable {
    case title
    case participant(Int)
    case agenda(String)
    case heading(String)
    case item(String, String)
    case point(String, String, String)
    case pic(String, String)

    var sectionID: String? {
        switch self {
        case .heading(let s), .item(let s, _), .point(let s, _, _), .pic(let s, _): s
        default: nil
        }
    }
}

struct EditorDraft: Equatable, Sendable {
    static let coalesceWindow: TimeInterval = 1
    static let maxUndo = 200

    private(set) var document: EditorDocument
    private var undoStack: [EditorDocument] = []
    private var redoStack: [EditorDocument] = []
    private var coalesceKey: EditTarget?
    private var coalesceAt: Date?

    init(_ document: EditorDocument) { self.document = document }

    var canUndo: Bool { !undoStack.isEmpty }
    var canRedo: Bool { !redoStack.isEmpty }

    mutating func undo() {
        guard let previous = undoStack.popLast() else { return }
        redoStack.append(document)
        document = previous
        coalesceKey = nil
    }

    mutating func redo() {
        guard let next = redoStack.popLast() else { return }
        undoStack.append(document)
        document = next
        coalesceKey = nil
    }

    // Text edits to the same field within the pause window share one undo step.
    @discardableResult
    private mutating func change(_ key: EditTarget? = nil, now: Date = .now, _ body: (inout EditorDocument) -> Void) -> Bool {
        var next = document
        body(&next)
        guard next != document else { return false }
        let coalesce = key != nil && key == coalesceKey && coalesceAt.map { now.timeIntervalSince($0) < Self.coalesceWindow } == true
        if !coalesce {
            undoStack.append(document)
            if undoStack.count > Self.maxUndo { undoStack.removeFirst() }
        }
        redoStack.removeAll()
        coalesceKey = key
        coalesceAt = now
        document = next
        return true
    }

    // MARK: Meeting info

    mutating func setStartedAt(_ value: Date?) {
        change { $0.meetingInfo?.startedAt = value }
    }

    @discardableResult
    mutating func addParticipant() -> EditTarget {
        let index = document.meetingInfo?.participants.count ?? 0
        change { $0.meetingInfo?.participants.append("") }
        return .participant(index)
    }

    @discardableResult
    mutating func addAgenda(after id: String? = nil) -> EditTarget {
        let item = TextItem.human()
        change { doc in
            guard var agenda = doc.meetingInfo?.agenda else { return }
            let at = id.flatMap { id in agenda.firstIndex { $0.id == id } }.map { $0 + 1 } ?? agenda.count
            agenda.insert(item, at: at)
            doc.meetingInfo?.agenda = agenda
        }
        return .agenda(item.id)
    }

    // MARK: Text (typed newline splits the item; text after it becomes new items below)

    @discardableResult
    mutating func setText(_ target: EditTarget, _ value: String, now: Date = .now) -> EditTarget? {
        let lines = value.components(separatedBy: "\n")
        let head = lines[0]
        let tail = Array(lines.dropFirst())
        var focus: EditTarget?
        let key: EditTarget? = tail.isEmpty ? target : nil
        change(key, now: now) { doc in
            switch target {
            case .title:
                doc.meetingInfo?.title = lines.joined(separator: " ")
            case .participant(let i):
                guard let count = doc.meetingInfo?.participants.count, i < count else { return }
                doc.meetingInfo?.participants[i] = lines.joined(separator: " ")
            case .heading(let s):
                guard let si = doc.sections.firstIndex(where: { $0.id == s }) else { return }
                doc.sections[si].heading = lines.joined(separator: " ")
            case .pic(let s, let id):
                Self.updateAction(&doc, s, id) { $0.setPIC(lines.joined(separator: " ")) }
            case .agenda(let id):
                guard var agenda = doc.meetingInfo?.agenda, let i = agenda.firstIndex(where: { $0.id == id }) else { return }
                agenda[i].setText(head)
                let added = tail.map { TextItem.human($0) }
                agenda.insert(contentsOf: added, at: i + 1)
                doc.meetingInfo?.agenda = agenda
                focus = added.last.map { .agenda($0.id) }
            case .item(let s, let id):
                guard let si = doc.sections.firstIndex(where: { $0.id == s }),
                      let ii = doc.sections[si].items.firstIndex(where: { $0.id == id }) else { return }
                var item = doc.sections[si].items[ii]
                switch item {
                case .text(var t): t.setText(head); item = .text(t)
                case .topic(var t): t.setTitle(head); item = .topic(t)
                case .action(var a): a.setDescription(head); item = .action(a)
                }
                doc.sections[si].items[ii] = item
                let added = tail.map { Self.newItem(for: doc.sections[si].type, text: $0) }
                doc.sections[si].items.insert(contentsOf: added, at: ii + 1)
                focus = added.last.map { .item(s, $0.id) }
            case .point(let s, let topicID, let id):
                Self.updateTopic(&doc, s, topicID) { topic in
                    guard let pi = topic.points.firstIndex(where: { $0.id == id }) else { return }
                    topic.points[pi].setText(head)
                    let added = tail.map { TextItem.human($0) }
                    topic.points.insert(contentsOf: added, at: pi + 1)
                    focus = added.last.map { .point(s, topicID, $0.id) }
                }
            }
        }
        return focus
    }

    mutating func setDeadline(section: String, item: String, day: String?) {
        change { Self.updateAction(&$0, section, item) { $0.setDeadline(day) } }
    }

    // MARK: Items

    static func newItem(for type: SectionType, text: String = "") -> MoMItem {
        switch type {
        case .discussion: .topic(.human(title: text))
        case .actionItems: .action(.human(description: text))
        default: .text(.human(text))
        }
    }

    @discardableResult
    mutating func addItem(section: String, after id: String? = nil) -> EditTarget? {
        guard let s = document.section(section) else { return nil }
        let item = Self.newItem(for: s.type)
        change { doc in
            guard let si = doc.sections.firstIndex(where: { $0.id == section }) else { return }
            let at = id.flatMap { id in doc.sections[si].items.firstIndex { $0.id == id } }.map { $0 + 1 } ?? doc.sections[si].items.count
            doc.sections[si].items.insert(item, at: at)
        }
        return .item(section, item.id)
    }

    @discardableResult
    mutating func addPoint(section: String, topic: String) -> EditTarget {
        let point = TextItem.human()
        change { Self.updateTopic(&$0, section, topic) { $0.points.append(point) } }
        return .point(section, topic, point.id)
    }

    // Returns the field to focus next (the previous sibling), if any.
    @discardableResult
    mutating func delete(_ target: EditTarget) -> EditTarget? {
        var focus: EditTarget?
        change { doc in
            switch target {
            case .agenda(let id):
                guard var agenda = doc.meetingInfo?.agenda, let i = agenda.firstIndex(where: { $0.id == id }) else { return }
                agenda.remove(at: i)
                doc.meetingInfo?.agenda = agenda
                focus = i > 0 ? .agenda(agenda[i - 1].id) : nil
            case .item(let s, let id), .pic(let s, let id):
                guard let si = doc.sections.firstIndex(where: { $0.id == s }),
                      let ii = doc.sections[si].items.firstIndex(where: { $0.id == id }) else { return }
                doc.sections[si].items.remove(at: ii)
                focus = ii > 0 ? .item(s, doc.sections[si].items[ii - 1].id) : nil
            case .point(let s, let topicID, let id):
                Self.updateTopic(&doc, s, topicID) { topic in
                    guard let pi = topic.points.firstIndex(where: { $0.id == id }) else { return }
                    topic.points.remove(at: pi)
                    focus = pi > 0 ? .point(s, topicID, topic.points[pi - 1].id) : .item(s, topicID)
                }
            case .participant(let i):
                guard let count = doc.meetingInfo?.participants.count, i < count else { return }
                doc.meetingInfo?.participants.remove(at: i)
                focus = i > 0 ? .participant(i - 1) : nil
            case .title, .heading:
                return
            }
        }
        return focus
    }

    mutating func moveItem(section: String, item: String, by offset: Int) {
        change { doc in
            guard let si = doc.sections.firstIndex(where: { $0.id == section }),
                  let ii = doc.sections[si].items.firstIndex(where: { $0.id == item }) else { return }
            let to = ii + offset
            guard doc.sections[si].items.indices.contains(to) else { return }
            doc.sections[si].items.swapAt(ii, to)
        }
    }

    // MARK: Sections

    mutating func setStyle(section: String, _ style: ListStyle) {
        change { doc in
            guard let si = doc.sections.firstIndex(where: { $0.id == section }), doc.sections[si].type != .actionItems else { return }
            doc.sections[si].style = style
        }
    }

    mutating func moveSection(_ id: String, by offset: Int) {
        change { doc in
            guard let i = doc.sections.firstIndex(where: { $0.id == id }) else { return }
            let to = i + offset
            guard doc.sections.indices.contains(to) else { return }
            doc.sections.swapAt(i, to)
        }
    }

    mutating func deleteSection(_ id: String) {
        change { $0.sections.removeAll { $0.id == id } }
    }

    @discardableResult
    mutating func addSection(_ type: SectionType, heading: String) -> EditTarget? {
        guard type == .custom || document.sections.allSatisfy({ $0.type != type }) else { return nil }
        let id = type == .custom || document.kind == .aiNotes ? ClientID.make() : type.rawValue
        let section = MoMSection(id: id, type: type, heading: heading, style: type == .custom ? .bullet : type.defaultStyle, items: [])
        change { $0.sections.append(section) }
        return .heading(id)
    }

    // MARK: Helpers

    private static func updateAction(_ doc: inout EditorDocument, _ section: String, _ id: String, _ body: (inout ActionItem) -> Void) {
        guard let si = doc.sections.firstIndex(where: { $0.id == section }),
              let ii = doc.sections[si].items.firstIndex(where: { $0.id == id }),
              case .action(var a) = doc.sections[si].items[ii] else { return }
        body(&a)
        doc.sections[si].items[ii] = .action(a)
    }

    private static func updateTopic(_ doc: inout EditorDocument, _ section: String, _ id: String, _ body: (inout TopicItem) -> Void) {
        guard let si = doc.sections.firstIndex(where: { $0.id == section }),
              let ii = doc.sections[si].items.firstIndex(where: { $0.id == id }),
              case .topic(var t) = doc.sections[si].items[ii] else { return }
        body(&t)
        doc.sections[si].items[ii] = .topic(t)
    }
}

// Deadlines are calendar days (`YYYY-MM-DD`) picked in the user's calendar, not UTC instants.
enum DeadlineDay {
    static func string(from date: Date, calendar: Calendar = .current) -> String {
        let c = calendar.dateComponents([.year, .month, .day], from: date)
        return String(format: "%04d-%02d-%02d", c.year ?? 0, c.month ?? 0, c.day ?? 0)
    }

    static func date(from day: String, calendar: Calendar = .current) -> Date? {
        guard let utc = Formatting.parseDay(day) else { return nil }
        var utcCal = Calendar(identifier: .gregorian)
        utcCal.timeZone = TimeZone(identifier: "UTC")!
        let c = utcCal.dateComponents([.year, .month, .day], from: utc)
        return calendar.date(from: c)
    }
}

struct PendingEditSnapshot: Equatable, Sendable {
    var hasPendingWork: Bool
    var inFlight: Bool
    var baseRevision: Int
    var conflictCurrentRevision: Int?
    var failedCode: String?

    init(_ row: PendingDocumentEdit) {
        hasPendingWork = row.hasPendingWork
        inFlight = row.inFlightMutationID != nil
        baseRevision = row.baseRevision
        conflictCurrentRevision = row.conflictCurrentRevision
        failedCode = row.failedCode
    }

    init(hasPendingWork: Bool, inFlight: Bool = false, baseRevision: Int = 0, conflictCurrentRevision: Int? = nil, failedCode: String? = nil) {
        self.hasPendingWork = hasPendingWork
        self.inFlight = inFlight
        self.baseRevision = baseRevision
        self.conflictCurrentRevision = conflictCurrentRevision
        self.failedCode = failedCode
    }

    var isBlocked: Bool { conflictCurrentRevision != nil || failedCode != nil }
}

enum SaveStateResolver {
    static func resolve(debouncing: Bool, pending: PendingEditSnapshot?, online: Bool, flushing: Bool, flushFailed: Bool) -> SaveState {
        if debouncing { return .saving }
        guard let pending, pending.hasPendingWork || pending.isBlocked else { return .synced }
        if pending.isBlocked { return .notSynced }
        if !online { return .offline }
        if flushFailed { return .notSynced }
        if pending.inFlight || flushing { return .syncing }
        return .local
    }
}
