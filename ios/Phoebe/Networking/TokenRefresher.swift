import Foundation

// Refresh tokens are single-use, so concurrent callers share one exchange: a second would revoke the whole session.
actor TokenRefresher {
    // A lost reply leaves only the old refresh token, accepted again for REFRESH_REUSE_GRACE (30 s); ambiguous failures retry within it.
    static let lostResponseRetryWindow: TimeInterval = 20
    static let maxAttempts = 3

    private let store: any TokenStore
    private let now: @Sendable () -> Date
    private let exchange: @Sendable (String) async throws -> TokenPairDTO
    private let keepAlive: @Sendable () async -> @Sendable () -> Void
    private let onSessionEnded: @Sendable () -> Void
    private var inFlight: (refreshToken: String, task: Task<TokenPairDTO, Error>)?

    init(
        store: any TokenStore,
        now: @escaping @Sendable () -> Date,
        exchange: @escaping @Sendable (String) async throws -> TokenPairDTO,
        keepAlive: @escaping @Sendable () async -> @Sendable () -> Void = { {} },
        onSessionEnded: @escaping @Sendable () -> Void
    ) {
        self.store = store
        self.now = now
        self.exchange = exchange
        self.keepAlive = keepAlive
        self.onSessionEnded = onSessionEnded
    }

    func accessToken(minValidity: TimeInterval) async throws -> String? {
        guard let tokens = store.tokens else { return nil }
        if tokens.accessTokenExpiresAt.timeIntervalSince(now()) > minValidity { return tokens.accessToken }
        return try await refresh(rejecting: tokens.accessToken)
    }

    // Network and 5xx failures throw without ending the session; any 401 ends it.
    func refresh(rejecting rejected: String) async throws -> String {
        guard let tokens = store.tokens else { throw APIError.unauthenticated }
        // An exchange started for an earlier session (signed out or replaced since) is not joined.
        if let inFlight, inFlight.refreshToken == tokens.refreshToken { return try await inFlight.task.value.accessToken }
        if tokens.accessToken != rejected { return tokens.accessToken }
        let task = Task { try await self.rotate(tokens.refreshToken) }
        inFlight = (tokens.refreshToken, task)
        defer { if inFlight?.task == task { inFlight = nil } }
        return try await task.value.accessToken
    }

    private func rotate(_ refreshToken: String) async throws -> TokenPairDTO {
        // Finish and persist the rotation even if the app is suspended mid-exchange.
        let end = await keepAlive()
        defer { end() }
        do {
            let fresh = try await exchangeRetryingLostResponse(refreshToken)
            // Signed out or signed in again meanwhile: the result belongs to a session that is gone.
            guard isCurrent(refreshToken) else { throw APIError.unauthenticated }
            store.saveTokens(fresh)
            return fresh
        } catch APIError.unauthenticated {
            if isCurrent(refreshToken) { onSessionEnded() }
            throw APIError.unauthenticated
        }
    }

    private func exchangeRetryingLostResponse(_ refreshToken: String) async throws -> TokenPairDTO {
        let start = now()
        var attempt = 1
        while true {
            do {
                return try await exchange(refreshToken)
            } catch let error as APIError where error.isAmbiguous && attempt < Self.maxAttempts
                && now().timeIntervalSince(start) < Self.lostResponseRetryWindow {
                attempt += 1
            }
        }
    }

    private func isCurrent(_ refreshToken: String) -> Bool { store.tokens?.refreshToken == refreshToken }
}

extension TokenPairDTO {
    // Expiries are on the server clock; re-anchoring via the Date header keeps a skewed device clock from refreshing early or sending expired tokens.
    func anchored(serverDate: Date?, receivedAt: Date) -> TokenPairDTO {
        guard let serverDate else { return self }
        return TokenPairDTO(
            accessToken: accessToken,
            accessTokenExpiresAt: receivedAt.addingTimeInterval(accessTokenExpiresAt.timeIntervalSince(serverDate)),
            refreshToken: refreshToken, refreshTokenExpiresAt: refreshTokenExpiresAt, sessionExpiresAt: sessionExpiresAt)
    }

    static func httpDate(_ raw: String?) -> Date? {
        guard let raw else { return nil }
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.timeZone = TimeZone(identifier: "GMT")
        f.dateFormat = "EEE, dd MMM yyyy HH:mm:ss zzz"
        return f.date(from: raw)
    }
}
