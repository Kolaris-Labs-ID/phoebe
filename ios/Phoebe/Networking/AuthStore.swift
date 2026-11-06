import Foundation
import Security

enum SignInMethod: String, Sendable {
    case apple, dev
}

protocol TokenStore: Sendable {
    var tokens: TokenPairDTO? { get }
    func saveTokens(_ tokens: TokenPairDTO)
}

struct AuthStore: TokenStore {
    enum Key: String, CaseIterable, Sendable {
        case tokens, appleUserID, pendingDisplayName, signInMethod, userID
    }

    var service = "com.kolarislabs.phoebe.auth"

    // One item so access token, refresh token and expiries are always written together.
    var tokens: TokenPairDTO? {
        value(for: .tokens).flatMap { try? APICoding.decoder().decode(TokenPairDTO.self, from: Data($0.utf8)) }
    }
    var appleUserID: String? { value(for: .appleUserID) }
    var pendingDisplayName: String? { value(for: .pendingDisplayName) }
    var signInMethod: SignInMethod? { value(for: .signInMethod).flatMap(SignInMethod.init(rawValue:)) }
    var userID: UUID? { value(for: .userID).flatMap(UUID.init(uuidString:)) }

    func saveTokens(_ tokens: TokenPairDTO) {
        set(String(decoding: APICoding.encode(tokens), as: UTF8.self), for: .tokens)
    }

    func saveSession(tokens: TokenPairDTO, userID: UUID, method: SignInMethod) {
        saveTokens(tokens)
        set(userID.lower, for: .userID)
        set(method.rawValue, for: .signInMethod)
    }

    func clearSession() {
        for key in [Key.tokens, .userID, .signInMethod, .appleUserID] { set(nil, for: key) }
    }

    func value(for key: Key) -> String? {
        var query = baseQuery(key)
        query[kSecReturnData as String] = true
        query[kSecMatchLimit as String] = kSecMatchLimitOne
        var result: AnyObject?
        guard SecItemCopyMatching(query as CFDictionary, &result) == errSecSuccess, let data = result as? Data else { return nil }
        return String(data: data, encoding: .utf8)
    }

    func set(_ value: String?, for key: Key) {
        let query = baseQuery(key)
        guard let value else {
            SecItemDelete(query as CFDictionary)
            return
        }
        let attributes: [String: Any] = [
            kSecValueData as String: Data(value.utf8),
            kSecAttrAccessible as String: kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly,
        ]
        if SecItemUpdate(query as CFDictionary, attributes as CFDictionary) == errSecItemNotFound {
            SecItemAdd(query.merging(attributes) { $1 } as CFDictionary, nil)
        }
    }

    private func baseQuery(_ key: Key) -> [String: Any] {
        [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: key.rawValue,
        ]
    }
}
