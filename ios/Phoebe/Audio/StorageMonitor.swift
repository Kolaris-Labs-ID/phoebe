import Foundation

protocol StorageProviding: Sendable {
    func freeBytes() -> Int64
}

struct StorageMonitor: StorageProviding {
    var volume = URL(filePath: NSHomeDirectory())

    func freeBytes() -> Int64 {
        let values = try? volume.resourceValues(forKeys: [.volumeAvailableCapacityForImportantUsageKey])
        return values?.volumeAvailableCapacityForImportantUsage ?? .max
    }
}
