import Foundation
import os

enum APICoding {
    static let fractional = Date.ISO8601FormatStyle(includingFractionalSeconds: true)
    static let whole = Date.ISO8601FormatStyle()

    static func encoder() -> JSONEncoder {
        let e = JSONEncoder()
        e.keyEncodingStrategy = .convertToSnakeCase
        e.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]
        e.dateEncodingStrategy = .custom { date, encoder in
            var c = encoder.singleValueContainer()
            try c.encode(formatDate(date))
        }
        return e
    }

    static func decoder() -> JSONDecoder {
        let d = JSONDecoder()
        d.keyDecodingStrategy = .convertFromSnakeCase
        d.dateDecodingStrategy = .custom { decoder in
            let c = try decoder.singleValueContainer()
            let raw = try c.decode(String.self)
            guard let date = parseDate(raw) else {
                throw DecodingError.dataCorruptedError(in: c, debugDescription: "invalid RFC3339 date")
            }
            return date
        }
        return d
    }

    static func formatDate(_ date: Date) -> String { date.formatted(fractional) }

    static func parseDate(_ raw: String) -> Date? {
        (try? Date(raw, strategy: fractional)) ?? (try? Date(raw, strategy: whole))
    }

    static func encode<T: Encodable>(_ value: T) -> Data {
        // Request DTOs are plain structs of strings/ints/dates; encoding cannot fail.
        try! encoder().encode(value)
    }
}

enum HTTPMethod: String, Sendable {
    case get = "GET", post = "POST", put = "PUT", patch = "PATCH", delete = "DELETE"
}

struct EmptyResponse: Decodable, Sendable {}

struct Endpoint<Response: Decodable & Sendable>: Sendable {
    var method: HTTPMethod
    var path: String
    var query: [URLQueryItem] = []
    var body: Data?
    var headers: [String: String] = [:]
    var requiresAuth = true
}

struct APIClient: Sendable {
    static let logger = Logger(subsystem: "com.kolarislabs.phoebe", category: "api")

    let baseURL: URL
    let session: URLSession
    let tokenProvider: @Sendable () -> String?
    let onUnauthenticated: @Sendable () -> Void

    init(
        baseURL: URL,
        session: URLSession = .shared,
        tokenProvider: @escaping @Sendable () -> String?,
        onUnauthenticated: @escaping @Sendable () -> Void = {}
    ) {
        self.baseURL = baseURL
        self.session = session
        self.tokenProvider = tokenProvider
        self.onUnauthenticated = onUnauthenticated
    }

    func send<R>(_ endpoint: Endpoint<R>) async throws -> R {
        let request = makeRequest(endpoint)
        let data: Data, response: URLResponse
        do {
            (data, response) = try await session.data(for: request)
        } catch {
            throw APIError.from(error)
        }
        return try handle(endpoint, data: data, response: response)
    }

    // 404 on a delete means it is already gone (api §4.2, ios §9).
    func sendDelete<R>(_ endpoint: Endpoint<R>) async throws -> R? {
        do {
            return try await send(endpoint)
        } catch APIError.notFound {
            return nil
        }
    }

    func makeRequest<R>(_ endpoint: Endpoint<R>, requestID: UUID = UUID()) -> URLRequest {
        var components = URLComponents(url: baseURL.appending(path: endpoint.path), resolvingAgainstBaseURL: false)!
        if !endpoint.query.isEmpty { components.queryItems = endpoint.query }
        var request = URLRequest(url: components.url!)
        request.httpMethod = endpoint.method.rawValue
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        request.setValue(requestID.uuidString.lowercased(), forHTTPHeaderField: "X-Request-ID")
        if let body = endpoint.body {
            request.httpBody = body
            request.setValue("application/json; charset=utf-8", forHTTPHeaderField: "Content-Type")
        }
        if endpoint.requiresAuth, let token = tokenProvider() {
            request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        }
        for (name, value) in endpoint.headers { request.setValue(value, forHTTPHeaderField: name) }
        return request
    }

    func handle<R>(_ endpoint: Endpoint<R>, data: Data, response: URLResponse) throws -> R {
        guard let http = response as? HTTPURLResponse else { throw APIError.transport(nil) }
        let requestID = http.value(forHTTPHeaderField: "X-Request-ID") ?? "-"
        Self.logger.debug("\(endpoint.method.rawValue, privacy: .public) \(endpoint.path, privacy: .private) → \(http.statusCode, privacy: .public) rid=\(requestID, privacy: .public)")
        guard (200..<300).contains(http.statusCode) else {
            let error = APIError.from(status: http.statusCode, data: data, retryAfter: http.value(forHTTPHeaderField: "Retry-After"))
            if error == .unauthenticated, endpoint.requiresAuth { onUnauthenticated() }
            throw error
        }
        return try Self.decode(R.self, from: data)
    }

    static func decode<R: Decodable>(_ type: R.Type, from data: Data) throws -> R {
        if let empty = EmptyResponse() as? R, data.isEmpty || type == EmptyResponse.self { return empty }
        do {
            return try APICoding.decoder().decode(R.self, from: data)
        } catch {
            throw APIError.decoding(String(describing: error))
        }
    }

    static func baseURLFromBundle(_ bundle: Bundle = .main) -> URL? {
        guard let raw = bundle.object(forInfoDictionaryKey: "PhoebeAPIBaseURL") as? String, !raw.isEmpty else { return nil }
        return URL(string: raw)
    }

    static var devLoginEnabled: Bool {
        #if DEBUG
        (Bundle.main.object(forInfoDictionaryKey: "PhoebeDevLogin") as? String) == "1"
        #else
        false
        #endif
    }
}

extension UUID {
    var lower: String { uuidString.lowercased() }
}
