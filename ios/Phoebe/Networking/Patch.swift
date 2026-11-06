import Foundation

enum Patch<T: Encodable & Sendable>: Sendable {
    case unchanged
    case set(T)
    case clear

    var isUnchanged: Bool {
        if case .unchanged = self { return true }
        return false
    }
}

extension Patch: Equatable where T: Equatable {}

extension KeyedEncodingContainer {
    mutating func encode<T>(_ patch: Patch<T>, forKey key: Key) throws {
        switch patch {
        case .unchanged: break
        case .clear: try encodeNil(forKey: key)
        case .set(let value):
            switch value {
            case let uuid as UUID: try encode(uuid.lower, forKey: key)
            case let uuids as [UUID]: try encode(uuids.map(\.lower), forKey: key)
            default: try encode(value, forKey: key)
            }
        }
    }
}

struct MeetingPatchRequest: Encodable, Sendable, Equatable {
    var expectedMetadataRevision: Int?
    var title: Patch<String> = .unchanged
    var startedAt: Patch<Date> = .unchanged
    var timezone: Patch<String> = .unchanged
    var language: Patch<TranscriptionLanguage> = .unchanged
    var participants: Patch<[String]> = .unchanged
    var folderId: Patch<UUID> = .unchanged
    var tagIds: Patch<[UUID]> = .unchanged

    var hasMetadataChanges: Bool {
        !(title.isUnchanged && startedAt.isUnchanged && timezone.isUnchanged && language.isUnchanged && participants.isUnchanged)
    }

    var isEmpty: Bool { !hasMetadataChanges && folderId.isUnchanged && tagIds.isUnchanged }

    enum CodingKeys: String, CodingKey {
        case expectedMetadataRevision, title, startedAt, timezone, language, participants, folderId, tagIds
    }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encodeIfPresent(expectedMetadataRevision, forKey: .expectedMetadataRevision)
        try c.encode(title, forKey: .title)
        try c.encode(startedAt, forKey: .startedAt)
        try c.encode(timezone, forKey: .timezone)
        try c.encode(language, forKey: .language)
        try c.encode(participants, forKey: .participants)
        try c.encode(folderId, forKey: .folderId)
        try c.encode(tagIds, forKey: .tagIds)
    }
}
