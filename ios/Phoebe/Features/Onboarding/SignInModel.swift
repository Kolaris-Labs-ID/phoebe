import AuthenticationServices
import Foundation
import Security

@MainActor @Observable
final class SignInModel {
    enum Phase: Equatable { case idle, signingIn, failed }

    private(set) var phase: Phase = .idle
    var devSubject = ""
    @ObservationIgnored private var rawNonce = ""

    var isSigningIn: Bool { phase == .signingIn }

    static func isValidDevSubject(_ subject: String) -> Bool {
        subject.wholeMatch(of: /[a-z0-9._-]{1,64}/) != nil
    }

    func prepare(_ request: ASAuthorizationAppleIDRequest) {
        rawNonce = Self.randomNonce()
        request.requestedScopes = [.fullName]
        request.nonce = Checksum.sha256Hex(rawNonce)
    }

    func complete(_ result: Result<ASAuthorization, any Error>, app: AppModel) async {
        switch result {
        case .failure(let error):
            if (error as? ASAuthorizationError)?.code == .canceled { phase = .idle } else { phase = .failed }
        case .success(let authorization):
            guard let credential = authorization.credential as? ASAuthorizationAppleIDCredential,
                  let tokenData = credential.identityToken,
                  let token = String(data: tokenData, encoding: .utf8) else {
                phase = .failed
                return
            }
            if let name = credential.fullName.map(Self.displayName), !name.isEmpty {
                app.auth.set(name, for: .pendingDisplayName)
            }
            let code = credential.authorizationCode.flatMap { String(data: $0, encoding: .utf8) }
            let body = AppleAuthRequest(identityToken: token, nonce: rawNonce, displayName: app.auth.pendingDisplayName, authorizationCode: code)
            await signIn(app: app, method: .apple, appleUserID: credential.user) {
                try await app.api.send(Endpoints.signInWithApple(body))
            }
        }
    }

    func devLogin(app: AppModel) async {
        let subject = devSubject
        guard Self.isValidDevSubject(subject) else { return }
        await signIn(app: app, method: .dev, appleUserID: nil) {
            try await app.api.send(Endpoints.devLogin(subject: subject))
        }
    }

    private func signIn(app: AppModel, method: SignInMethod, appleUserID: String?, request: () async throws -> AuthResponseDTO) async {
        phase = .signingIn
        do {
            let response = try await request()
            phase = .idle
            app.didSignIn(response, method: method, appleUserID: appleUserID)
        } catch {
            phase = .failed
        }
    }

    private static func displayName(_ components: PersonNameComponents) -> String {
        PersonNameComponentsFormatter().string(from: components).trimmingCharacters(in: .whitespacesAndNewlines)
    }

    private static func randomNonce() -> String {
        var bytes = [UInt8](repeating: 0, count: 32)
        _ = SecRandomCopyBytes(kSecRandomDefault, bytes.count, &bytes)
        return Data(bytes).base64EncodedString()
            .replacingOccurrences(of: "+", with: "-")
            .replacingOccurrences(of: "/", with: "_")
            .replacingOccurrences(of: "=", with: "")
    }
}
