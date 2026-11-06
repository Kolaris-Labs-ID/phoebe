import CryptoKit
import Foundation

enum Checksum {
    static let chunkSize = 1 << 20

    struct Streaming {
        private var hasher = SHA256()
        private(set) var bytes = 0

        mutating func update(_ data: Data) {
            hasher.update(data: data)
            bytes += data.count
        }

        func finalizeHex() -> String { Checksum.hex(hasher.finalize()) }
    }

    static func sha256Hex(fileURL: URL, chunkSize: Int = chunkSize) throws -> String {
        let handle = try FileHandle(forReadingFrom: fileURL)
        defer { try? handle.close() }
        var stream = Streaming()
        while let data = try handle.read(upToCount: chunkSize), !data.isEmpty {
            stream.update(data)
        }
        return stream.finalizeHex()
    }

    static func sha256Hex(_ data: Data) -> String { hex(SHA256.hash(data: data)) }

    static func sha256Hex(_ string: String) -> String { sha256Hex(Data(string.utf8)) }

    static func hex(_ digest: SHA256.Digest) -> String {
        digest.map { String(format: "%02x", $0) }.joined()
    }
}
