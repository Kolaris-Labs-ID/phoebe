import Foundation
import Testing
@testable import Phoebe

struct BackoffTests {
    @Test func pollSequenceGrowsByOnePointFiveAndCapsAtThirty() {
        var b = Backoff.poll
        let delays = (0..<10).map { _ in b.nextDelay() }
        #expect(delays.prefix(4) == [2, 3, 4.5, 6.75])
        #expect(delays.allSatisfy { $0 <= 30 })
        #expect(delays.suffix(2) == [30, 30])
    }

    @Test func uploadScheduleIsTwoToSixtyThenStaysAtCap() {
        let b = Backoff.upload
        #expect((0..<8).map { b.baseDelay(forAttempt: $0) } == [2, 4, 8, 16, 30, 60, 60, 60])
    }

    @Test func uploadJitterStaysWithinTwentyPercent() {
        var b = Backoff.upload
        for attempt in 0..<200 {
            let base = b.baseDelay(forAttempt: attempt)
            let d = b.nextDelay()
            #expect(d >= base * 0.8 && d <= base * 1.2)
            #expect(d <= 72)
        }
    }

    @Test func resetStartsOver() {
        var b = Backoff.poll
        _ = b.nextDelay()
        _ = b.nextDelay()
        b.reset()
        #expect(b.attempt == 0)
        #expect(b.nextDelay() == 2)
    }
}
