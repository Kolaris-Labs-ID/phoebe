import Foundation

struct Backoff: Sendable, Equatable {
    let initial: TimeInterval
    let factor: Double
    let cap: TimeInterval
    let jitter: Double
    let steps: [TimeInterval]?
    private(set) var attempt = 0

    init(initial: TimeInterval, factor: Double, cap: TimeInterval, jitter: Double = 0) {
        self.initial = initial
        self.factor = factor
        self.cap = cap
        self.jitter = jitter
        self.steps = nil
    }

    init(steps: [TimeInterval], jitter: Double = 0) {
        self.initial = steps.first ?? 0
        self.factor = 1
        self.cap = steps.last ?? 0
        self.jitter = jitter
        self.steps = steps
    }

    static var poll: Backoff { Backoff(initial: 2, factor: 1.5, cap: 30) }
    static var upload: Backoff { Backoff(steps: [2, 4, 8, 16, 30, 60], jitter: 0.2) }

    func baseDelay(forAttempt n: Int) -> TimeInterval {
        if let steps { return steps[min(max(n, 0), steps.count - 1)] }
        return min(initial * pow(factor, Double(max(n, 0))), cap)
    }

    mutating func nextDelay() -> TimeInterval {
        var rng = SystemRandomNumberGenerator()
        return nextDelay(using: &rng)
    }

    mutating func nextDelay<G: RandomNumberGenerator>(using rng: inout G) -> TimeInterval {
        let base = baseDelay(forAttempt: attempt)
        attempt += 1
        guard jitter > 0 else { return base }
        return base * (1 + Double.random(in: -jitter...jitter, using: &rng))
    }

    mutating func reset() { attempt = 0 }
}
