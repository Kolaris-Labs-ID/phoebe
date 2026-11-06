import CryptoKit
import Foundation
import Testing
@testable import Phoebe

struct ChecksumTests {
    @Test func streamingDigestEqualsOneShotOnTwentyMegabytes() throws {
        let count = 20 * 1024 * 1024 + 123
        var data = Data(count: count)
        data.withUnsafeMutableBytes { arc4random_buf($0.baseAddress, count) }
        let url = FileManager.default.temporaryDirectory.appending(path: "checksum-\(UUID().uuidString).bin")
        try data.write(to: url)
        defer { try? FileManager.default.removeItem(at: url) }

        let oneShot = SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined()
        #expect(try Checksum.sha256Hex(fileURL: url) == oneShot)
        #expect(try Checksum.sha256Hex(fileURL: url, chunkSize: 4096 + 7) == oneShot)
        #expect(Checksum.sha256Hex(data) == oneShot)
        #expect(oneShot.count == 64)
    }

    @Test func incrementalUpdatesMatch() {
        var s = Checksum.Streaming()
        s.update(Data("hello ".utf8))
        s.update(Data("world".utf8))
        #expect(s.bytes == 11)
        #expect(s.finalizeHex() == Checksum.sha256Hex("hello world"))
        #expect(Checksum.sha256Hex("hello world") == "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9")
    }
}
