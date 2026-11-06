import Foundation
import SwiftData

enum ModelContainerFactory {
    static var models: [any PersistentModel.Type] { [
        UserProfile.self, Folder.self, Tag.self, Meeting.self, TranscriptRevision.self,
        Document.self, DocumentRevision.self, ProcessingJob.self,
        LocalRecording.self, RecordingSegment.self, PendingUpload.self, PendingIntent.self,
        PendingDocumentEdit.self, PendingMutation.self, PendingEvent.self,
    ] }

    static var schema: Schema { Schema(models) }

    static func storeURL(userID: UUID) throws -> URL {
        let dir = URL.applicationSupportDirectory.appending(path: userID.lower, directoryHint: .isDirectory)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        return dir.appending(path: "Phoebe.store")
    }

    static func make(userID: UUID) throws -> ModelContainer {
        let config = ModelConfiguration(schema: schema, url: try storeURL(userID: userID))
        return try ModelContainer(for: schema, configurations: config)
    }

    static func inMemory() throws -> ModelContainer {
        try ModelContainer(for: schema, configurations: ModelConfiguration(schema: schema, isStoredInMemoryOnly: true))
    }
}
