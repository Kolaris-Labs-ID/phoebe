import Foundation
import SwiftData
import Testing
@testable import Phoebe

final class MemoryTokenStore: TokenStore, @unchecked Sendable {
    private let lock = NSLock()
    private var stored: TokenPairDTO?

    init(_ tokens: TokenPairDTO?) { stored = tokens }

    var tokens: TokenPairDTO? { lock.withLock { stored } }
    func saveTokens(_ tokens: TokenPairDTO) { lock.withLock { stored = tokens } }
    func clear() { lock.withLock { stored = nil } }
}

extension TokenPairDTO {
    static func pair(_ access: String, refresh: String = "rt_1", expiresIn: TimeInterval) -> TokenPairDTO {
        let session = Date.now.addingTimeInterval(180 * 86_400)
        return TokenPairDTO(accessToken: access, accessTokenExpiresAt: .now.addingTimeInterval(expiresIn),
                            refreshToken: refresh, refreshTokenExpiresAt: session, sessionExpiresAt: session)
    }
}

final class Recorder: @unchecked Sendable {
    private let lock = NSLock()
    private var log: [String] = []
    var items: [String] { lock.withLock { log } }
    func append(_ item: String) { lock.withLock { log.append(item) } }
}

final class AuthServer: @unchecked Sendable {
    struct Reply {
        var status = 200
        var body = ""
        var error: URLError?
    }

    struct Seen {
        let method: String
        let path: String
        let authorization: String?
        let body: Data
    }

    private static let registryLock = NSLock()
    nonisolated(unsafe) private static var registry: [String: AuthServer] = [:]

    let host = "a\(UUID().uuidString.prefix(12).lowercased()).test"
    var baseURL: URL { URL(string: "http://\(host)")! }
    private let lock = NSLock()
    private var accepted: String
    private var refreshes = 0
    private var log: [Seen] = []
    var refreshDelay: TimeInterval = 0
    var refreshFailure: Reply?
    // Consumed one per refresh, before refreshFailure. The rotation still happens (lost reply).
    var lostReplies: [Reply] = []
    var clockOffset: TimeInterval = 0
    var rejectRefreshedTokens = false

    init(accepted: String = "at_1") {
        self.accepted = accepted
        Self.registryLock.withLock { Self.registry[host] = self }
    }

    static func server(for host: String?) -> AuthServer? {
        guard let host else { return nil }
        return registryLock.withLock { registry[host] }
    }

    var seen: [Seen] { lock.withLock { log } }
    var refreshCount: Int { lock.withLock { refreshes } }
    func requests(_ path: String) -> [Seen] { seen.filter { $0.path == path } }

    static let userJSON = #"{"user":{"id":"0b6d1f0e-1111-4000-8000-000000000001","display_name":"Ari","language":"id","timezone":"Asia/Jakarta","created_at":"2026-11-06T02:00:00.000Z"}}"#

    static func error(_ status: Int, _ code: String) -> Reply {
        Reply(status: status, body: #"{"error":{"code":"\#(code)","message":"m","details":{},"request_id":"rid"}}"#)
    }

    static func pairJSON(_ n: Int) -> String {
        let at = APICoding.formatDate(.now.addingTimeInterval(900)), end = APICoding.formatDate(.now.addingTimeInterval(86_400))
        return #"{"access_token":"at_\#(n)","access_token_expires_at":"\#(at)","refresh_token":"rt_\#(n)","refresh_token_expires_at":"\#(end)","session_expires_at":"\#(end)"}"#
    }

    func respond(_ request: URLRequest, body: Data) -> Reply {
        let path = request.url?.path ?? ""
        let auth = request.value(forHTTPHeaderField: "Authorization")
        lock.withLock { log.append(Seen(method: request.httpMethod ?? "GET", path: path, authorization: auth, body: body)) }
        if path == "/v1/auth/refresh" {
            if refreshDelay > 0 { Thread.sleep(forTimeInterval: refreshDelay) }
            return lock.withLock {
                refreshes += 1
                if !lostReplies.isEmpty { return lostReplies.removeFirst() }
                if let refreshFailure { return refreshFailure }
                if !rejectRefreshedTokens { accepted = "at_\(refreshes + 1)" }
                return Reply(body: Self.pairJSON(refreshes + 1))
            }
        }
        let ok = lock.withLock { auth == "Bearer \(accepted)" }
        guard ok else { return Self.error(401, "access_token_expired") }
        return Reply(body: path == "/v1/usage" ? "{}" : Self.userJSON)
    }
}

final class AuthStubProtocol: URLProtocol, @unchecked Sendable {
    override class func canInit(with request: URLRequest) -> Bool { AuthServer.server(for: request.url?.host) != nil }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        guard let server = AuthServer.server(for: request.url?.host) else { return }
        let reply = server.respond(request, body: StubURLProtocol.readBody(request))
        if let error = reply.error {
            client?.urlProtocol(self, didFailWithError: error)
            return
        }
        let date = Date.now.addingTimeInterval(server.clockOffset)
        let response = HTTPURLResponse(url: request.url!, statusCode: reply.status, httpVersion: "HTTP/1.1",
                                       headerFields: ["Date": AuthStubProtocol.httpDate(date)])!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data(reply.body.utf8))
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}

    static func httpDate(_ date: Date) -> String {
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.timeZone = TimeZone(identifier: "GMT")
        f.dateFormat = "EEE, dd MMM yyyy HH:mm:ss zzz"
        return f.string(from: date)
    }

    static func session() -> URLSession {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [AuthStubProtocol.self]
        return URLSession(configuration: config)
    }
}

@Suite(.serialized)
struct AuthRefreshTests {
    let ended = Flag()

    func client(
        _ store: MemoryTokenStore, server: AuthServer = AuthServer(), now: @escaping @Sendable () -> Date = { Date() },
        keepAlive: @escaping @Sendable () async -> @Sendable () -> Void = { {} }
    ) -> (APIClient, AuthServer) {
        let flag = ended
        let api = APIClient(baseURL: server.baseURL, session: AuthStubProtocol.session(), tokens: store, now: now,
                            keepAlive: keepAlive, onUnauthenticated: { flag.value = true })
        return (api, server)
    }

    @Test func expiringTokenIsRefreshedBeforeTheRequest() async throws {
        let store = MemoryTokenStore(.pair("at_1", expiresIn: 30))
        let (api, server) = client(store)
        _ = try await api.send(Endpoints.me())

        #expect(server.seen.map(\.path) == ["/v1/auth/refresh", "/v1/me"])
        let refresh = try #require(server.requests("/v1/auth/refresh").first)
        #expect(String(decoding: refresh.body, as: UTF8.self) == #"{"refresh_token":"rt_1"}"#)
        #expect(refresh.authorization == nil)
        #expect(server.requests("/v1/me").first?.authorization == "Bearer at_2")
        #expect(store.tokens?.accessToken == "at_2")
        #expect(store.tokens?.refreshToken == "rt_2")
        #expect(!ended.value)
    }

    @Test func accessTokenExpiredRefreshesAndRetriesOnce() async throws {
        let store = MemoryTokenStore(.pair("at_1", expiresIn: 900))
        let (api, server) = client(store, server: AuthServer(accepted: "at_0"))
        let user = try await api.send(Endpoints.me()).user

        #expect(user.displayName == "Ari")
        #expect(server.seen.map(\.path) == ["/v1/me", "/v1/auth/refresh", "/v1/me"])
        #expect(server.requests("/v1/me").map(\.authorization) == ["Bearer at_1", "Bearer at_2"])
        #expect(!ended.value)
    }

    @Test func concurrentExpiredRequestsTriggerExactlyOneRefresh() async throws {
        let store = MemoryTokenStore(.pair("at_1", expiresIn: 900))
        let server = AuthServer(accepted: "at_0")
        server.refreshDelay = 0.2
        let (api, _) = client(store, server: server)
        try await withThrowingTaskGroup(of: Void.self) { group in
            for i in 0..<8 {
                group.addTask { _ = i.isMultiple(of: 2) ? try await api.send(Endpoints.me()).user : try await api.send(Endpoints.updateMe(UpdateMeRequest())).user }
            }
            try await group.waitForAll()
        }

        #expect(server.refreshCount == 1)
        #expect(server.seen.filter { $0.path == "/v1/me" && $0.authorization == "Bearer at_2" }.count == 8)
        #expect(store.tokens?.accessToken == "at_2")
        #expect(!ended.value)
    }

    @Test func concurrentExpiringRequestsTriggerExactlyOneRefresh() async throws {
        let store = MemoryTokenStore(.pair("at_1", expiresIn: 10))
        let server = AuthServer()
        server.refreshDelay = 0.2
        let (api, _) = client(store, server: server)
        try await withThrowingTaskGroup(of: Void.self) { group in
            for _ in 0..<8 { group.addTask { _ = try await api.send(Endpoints.me()) } }
            try await group.waitForAll()
        }

        #expect(server.refreshCount == 1)
        #expect(server.requests("/v1/me").allSatisfy { $0.authorization == "Bearer at_2" })
    }

    @Test(arguments: ["session_revoked", "session_expired", "unauthenticated"])
    func terminalRefreshFailureEndsTheSession(_ code: String) async {
        let store = MemoryTokenStore(.pair("at_1", expiresIn: 10))
        let server = AuthServer()
        server.refreshFailure = AuthServer.error(401, code)
        let (api, _) = client(store, server: server)

        await #expect(throws: APIError.unauthenticated) { try await api.send(Endpoints.me()) }
        #expect(ended.value)
        #expect(server.requests("/v1/me").isEmpty)
    }

    @Test(arguments: [
        (AuthServer.Reply(error: URLError(.networkConnectionLost)), APIError.transport("URLError \(URLError.networkConnectionLost.rawValue)")),
        (AuthServer.Reply(error: URLError(.notConnectedToInternet)), APIError.offline),
        (AuthServer.Reply(error: URLError(.timedOut)), APIError.timeout),
        (AuthServer.error(503, "unavailable"), APIError.unavailable),
    ])
    func refreshFailureWithoutA401KeepsTheSession(_ failure: AuthServer.Reply, _ expected: APIError) async throws {
        let store = MemoryTokenStore(.pair("at_1", expiresIn: 10))
        let server = AuthServer()
        server.refreshFailure = failure
        let (api, _) = client(store, server: server)

        await #expect(throws: expected) { try await api.send(Endpoints.me()) }
        #expect(!ended.value)
        #expect(store.tokens?.refreshToken == "rt_1")
        // Ambiguous failures may hide a committed rotation, so they are retried at once (within the grace).
        let attempts = expected.isAmbiguous ? TokenRefresher.maxAttempts : 1
        #expect(server.refreshCount == attempts)

        server.refreshFailure = nil
        _ = try await api.send(Endpoints.me())
        #expect(server.requests("/v1/auth/refresh").map { String(decoding: $0.body, as: UTF8.self) }
            == Array(repeating: #"{"refresh_token":"rt_1"}"#, count: attempts + 1))
        #expect(store.tokens?.accessToken == "at_\(attempts + 2)")
    }

    @Test func lostRefreshReplyIsRetriedWithTheSameTokenInsideTheGrace() async throws {
        let store = MemoryTokenStore(.pair("at_1", expiresIn: 10))
        let server = AuthServer()
        server.lostReplies = [AuthServer.Reply(error: URLError(.networkConnectionLost))]
        let (api, _) = client(store, server: server)
        _ = try await api.send(Endpoints.me())

        #expect(server.requests("/v1/auth/refresh").map { String(decoding: $0.body, as: UTF8.self) }
            == [#"{"refresh_token":"rt_1"}"#, #"{"refresh_token":"rt_1"}"#])
        #expect(store.tokens?.refreshToken == "rt_3")
        #expect(server.requests("/v1/me").last?.authorization == "Bearer at_3")
        #expect(!ended.value)
    }

    @Test func rotationRunsInsideABackgroundTask() async throws {
        let store = MemoryTokenStore(.pair("at_1", expiresIn: 10))
        let events = Recorder()
        let (api, server) = client(store, keepAlive: {
            events.append("begin:\(store.tokens?.refreshToken ?? "-")")
            return { events.append("end:\(store.tokens?.refreshToken ?? "-")") }
        })
        _ = try await api.send(Endpoints.me())

        #expect(server.refreshCount == 1)
        #expect(events.items == ["begin:rt_1", "end:rt_2"])
    }

    @Test func refreshLandingAfterSignOutIsDiscarded() async throws {
        let store = MemoryTokenStore(.pair("at_1", expiresIn: 10))
        let server = AuthServer()
        server.refreshDelay = 0.3
        let (api, _) = client(store, server: server)
        let call = Task { try await api.send(Endpoints.me()) }
        try await Task.sleep(for: .milliseconds(100))
        store.clear()

        await #expect(throws: APIError.unauthenticated) { try await call.value }
        #expect(server.refreshCount == 1)
        #expect(store.tokens == nil)
        #expect(!ended.value)
    }

    @Test func refreshLandingAfterANewSignInKeepsTheNewSession() async throws {
        let store = MemoryTokenStore(.pair("at_1", expiresIn: 10))
        let server = AuthServer()
        server.refreshDelay = 0.3
        server.lostReplies = [AuthServer.error(401, "session_revoked")]
        let (api, _) = client(store, server: server)
        let call = Task { try await api.send(Endpoints.me()) }
        try await Task.sleep(for: .milliseconds(100))
        store.saveTokens(.pair("at_new", refresh: "rt_new", expiresIn: 900))
        // The new session's own refresh must not join the old session's exchange.
        let renewed = try await api.authorizedRequest(Endpoints.me(), minValidity: 1000)

        await #expect(throws: APIError.unauthenticated) { try await call.value }
        #expect(renewed.value(forHTTPHeaderField: "Authorization") == "Bearer at_3")
        #expect(server.requests("/v1/auth/refresh").map { String(decoding: $0.body, as: UTF8.self) }
            == [#"{"refresh_token":"rt_1"}"#, #"{"refresh_token":"rt_new"}"#])
        #expect(store.tokens?.refreshToken == "rt_3")
        #expect(!ended.value)
    }

    @Test func fastDeviceClockDoesNotRefreshBeforeEveryRequest() async throws {
        let skew: TimeInterval = 20 * 60
        let store = MemoryTokenStore(.pair("at_1", expiresIn: 30))
        let server = AuthServer()
        let (api, _) = client(store, server: server, now: { Date().addingTimeInterval(skew) })
        for _ in 0..<3 { _ = try await api.send(Endpoints.me()) }

        #expect(server.refreshCount == 1)
        let expiry = try #require(store.tokens?.accessTokenExpiresAt)
        #expect(abs(expiry.timeIntervalSinceNow - (skew + 900)) < 5)
    }

    @Test func tokenExpiryIsAnchoredToTheDeviceClock() throws {
        let server = Date(timeIntervalSince1970: 1_800_000_000)
        let pair = TokenPairDTO(accessToken: "a", accessTokenExpiresAt: server.addingTimeInterval(900), refreshToken: "r",
                                refreshTokenExpiresAt: server, sessionExpiresAt: server)
        let device = server.addingTimeInterval(-3600)
        #expect(pair.anchored(serverDate: server, receivedAt: device).accessTokenExpiresAt == device.addingTimeInterval(900))
        #expect(pair.anchored(serverDate: nil, receivedAt: device) == pair)
        let header = try #require(TokenPairDTO.httpDate("Sun, 06 Nov 1994 08:49:37 GMT"))
        #expect(header == Date(timeIntervalSince1970: 784_111_777))
    }

    @Test func rejectedAgainAfterRefreshStopsWithoutSigningOut() async {
        let store = MemoryTokenStore(.pair("at_1", expiresIn: 900))
        let server = AuthServer(accepted: "at_0")
        server.rejectRefreshedTokens = true
        let (api, _) = client(store, server: server)

        await #expect(throws: APIError.unauthenticated) { try await api.send(Endpoints.me()) }
        #expect(server.refreshCount == 1)
        #expect(server.requests("/v1/me").count == 2)
        #expect(!ended.value)
    }

    @Test func signedOutRequestsCarryNoBearerAndNeverRefresh() async {
        let (api, server) = client(MemoryTokenStore(nil))
        await #expect(throws: APIError.unauthenticated) { try await api.send(Endpoints.me()) }
        #expect(server.refreshCount == 0)
        #expect(server.requests("/v1/me").first?.authorization == nil)
    }

    @Test func uploadRequestsGetATokenValidForFiveMinutes() async throws {
        let id = UUID()
        let fresh = MemoryTokenStore(.pair("at_1", expiresIn: 600))
        let (api, server) = client(fresh)
        let kept = try await api.authorizedRequest(Endpoints.uploadChunk(id, offset: 0))
        #expect(kept.value(forHTTPHeaderField: "Authorization") == "Bearer at_1")
        #expect(server.refreshCount == 0)

        let expiring = MemoryTokenStore(.pair("at_1", expiresIn: 120))
        let (api2, server2) = client(expiring)
        let renewed = try await api2.authorizedRequest(Endpoints.uploadChunk(id, offset: 0))
        #expect(renewed.value(forHTTPHeaderField: "Authorization") == "Bearer at_2")
        #expect(renewed.value(forHTTPHeaderField: "Upload-Offset") == "0")
        #expect(server2.refreshCount == 1)
    }

    @Test func signInResponseDecodesTheTokenPair() throws {
        let user = String(AuthServer.userJSON.dropFirst(8).dropLast())
        let json = #"{"user":"# + user + "," + String(AuthServer.pairJSON(1).dropFirst())
        let response = try APICoding.decoder().decode(AuthResponseDTO.self, from: Data(json.utf8))
        #expect(response.user.displayName == "Ari")
        #expect(response.tokens.accessToken == "at_1")
        #expect(response.tokens.refreshToken == "rt_1")
    }
}

@MainActor @Suite(.serialized)
struct AppSessionTests {
    func signedInApp(server: AuthServer) throws -> AppModel {
        let defaults = UserDefaults(suiteName: "phoebe.tests.\(UUID().uuidString)")!
        defaults.set(true, forKey: "phoebe.onboardingCompleted")
        let app = AppModel(auth: AuthStore(service: "com.kolarislabs.phoebe.tests.\(UUID().uuidString)"),
                           session: AuthStubProtocol.session(), baseURL: server.baseURL, defaults: defaults)
        let userID = UUID()
        let json = #"{"user":{"id":"\#(userID.lower)","display_name":"Ari","language":"id","timezone":"Asia/Jakarta","created_at":"2026-11-06T02:00:00.000Z"},"#
            + #""access_token":"at_1","access_token_expires_at":"\#(APICoding.formatDate(.now.addingTimeInterval(10)))","refresh_token":"rt_1","#
            + #""refresh_token_expires_at":"2099-01-01T00:00:00.000Z","session_expires_at":"2099-01-01T00:00:00.000Z"}"#
        app.didSignIn(try APICoding.decoder().decode(AuthResponseDTO.self, from: Data(json.utf8)), method: .dev)
        return app
    }

    func cleanUp(_ app: AppModel, _ userID: UUID?) async {
        await app.signOut(reason: .userInitiated, callServer: false)
        guard let userID else { return }
        try? FileManager.default.removeItem(at: URL.applicationSupportDirectory.appending(path: userID.lower))
    }

    @Test func sessionRevokedSignsOutToOnboarding() async throws {
        let server = AuthServer()
        server.refreshFailure = AuthServer.error(401, "session_revoked")
        let app = try signedInApp(server: server)
        let userID = app.user?.id
        #expect(app.auth.tokens?.accessToken == "at_1")
        if case .signedIn = app.session {} else { Issue.record("not signed in") }

        await #expect(throws: APIError.unauthenticated) { try await app.api.send(Endpoints.me()) }
        for _ in 0..<100 where app.session != .signedOut { try await Task.sleep(for: .milliseconds(20)) }

        #expect(app.session == .signedOut)
        #expect(app.signOutReason == .unauthenticated)
        #expect(app.auth.tokens == nil)
        #expect(app.user == nil)
        await cleanUp(app, userID)
    }

    @Test func networkFailureDuringRefreshKeepsTheSession() async throws {
        let server = AuthServer()
        server.refreshFailure = AuthServer.Reply(error: URLError(.notConnectedToInternet))
        let app = try signedInApp(server: server)
        let userID = app.user?.id

        await #expect(throws: APIError.offline) { try await app.api.send(Endpoints.me()) }
        try await Task.sleep(for: .milliseconds(200))

        if case .signedIn = app.session {} else { Issue.record("signed out after a network failure") }
        #expect(app.signOutReason == nil)
        #expect(app.auth.tokens?.refreshToken == "rt_1")
        await cleanUp(app, userID)
    }

    @Test func refreshedPairIsPersistedInTheKeychain() async throws {
        let app = try signedInApp(server: AuthServer())
        let userID = app.user?.id

        _ = try await app.api.send(Endpoints.me())
        let stored = try #require(app.auth.tokens)
        #expect(stored.accessToken == "at_2")
        #expect(stored.refreshToken == "rt_2")
        #expect(stored.sessionExpiresAt > .now)
        await cleanUp(app, userID)
    }
}
