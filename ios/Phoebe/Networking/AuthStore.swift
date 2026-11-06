import Foundation
import Security

enum SignInMethod: String, Sendable {
    case apple, dev
}

struct AuthStore: Sendable {
    enum Key: String, CaseIterable, Sendable {
        case token, appleUserID, pendingDisplayName, signInMethod, userID
    }

    var service = "com.kolarislabs.phoebe.auth"

    var token: String? { value(for: .token) }
    var appleUserID: String? { value(for: .appleUserID) }
    var pendingDisplayName: String? { value(for: .pendingDisplayName) }
    var signInMethod: SignInMethod? { value(for: .signInMethod).flatMap(SignInMethod.init(rawValue:)) }
    var userID: UUID? { value(for: .userID).flatMap(UUID.init(uuidString:)) }

    func saveSession(token: String, userID: UUID, method: SignInMethod) {
        set(token, for: .token)
        set(userID.lower, for: .userID)
        set(method.rawValue, for: .signInMethod)
    }

    func clearSession() {
        for key in [Key.token, .userID, .signInMethod, .appleUserID] { set(nil, for: key) }
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
