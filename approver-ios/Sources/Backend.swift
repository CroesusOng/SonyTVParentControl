import Foundation

/// Where the app keeps what it learned at enrolment.
///
/// The bearer token goes in the ordinary keychain, not the Secure Enclave: it is only
/// good for reading state and asking for a nonce. Everything that changes anything needs
/// a signature from the enclave key as well, so a token lifted off a jailbroken phone
/// buys the holder a read-only view and nothing else.
struct Credentials {

    var baseURL: URL
    var approverID: String
    var token: String

    private static let service = "dev.homelab.braviaguard.approver"
    private static let account = "backend"
    private static let baseURLKey = "approver.baseURL"
    private static let approverIDKey = "approver.id"

    static func load() -> Credentials? {
        let defaults = UserDefaults.standard
        guard
            let raw = defaults.string(forKey: baseURLKey),
            let url = URL(string: raw),
            let approverID = defaults.string(forKey: approverIDKey),
            let token = loadToken()
        else { return nil }
        return Credentials(baseURL: url, approverID: approverID, token: token)
    }

    func save() {
        let defaults = UserDefaults.standard
        defaults.set(baseURL.absoluteString, forKey: Self.baseURLKey)
        defaults.set(approverID, forKey: Self.approverIDKey)
        Self.saveToken(token)
    }

    static func forget() {
        let defaults = UserDefaults.standard
        defaults.removeObject(forKey: baseURLKey)
        defaults.removeObject(forKey: approverIDKey)
        SecItemDelete(query() as CFDictionary)
    }

    private static func query() -> [String: Any] {
        [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
        ]
    }

    private static func loadToken() -> String? {
        var request = query()
        request[kSecReturnData as String] = true
        var item: CFTypeRef?
        guard SecItemCopyMatching(request as CFDictionary, &item) == errSecSuccess,
              let data = item as? Data
        else { return nil }
        return String(data: data, encoding: .utf8)
    }

    private static func saveToken(_ token: String) {
        SecItemDelete(query() as CFDictionary)
        var request = query()
        request[kSecValueData as String] = Data(token.utf8)
        request[kSecAttrAccessible as String] = kSecAttrAccessibleWhenUnlockedThisDeviceOnly
        SecItemAdd(request as CFDictionary, nil)
    }
}

enum BackendError: Error, LocalizedError {
    case badAddress
    case http(Int, String)
    case malformed(String)

    var errorDescription: String? {
        switch self {
        case .badAddress: "地址看不懂"
        case .http(let status, let code):
            switch code {
            case "bad_code": "注册码不对或者已经用过了"
            case "approval_required": "这一步要 Face ID 签名"
            case "unknown_approver": "后端不认这台手机,可能被移除了"
            default: "后端返回 \(status) \(code)"
            }
        case .malformed(let what): "后端回了看不懂的东西: \(what)"
        }
    }
}

/// Talks to the household backend. One `approve` method covers every gated action,
/// because the server checks all of them the same way.
struct Backend {

    var credentials: Credentials

    // MARK: - enrolling

    /// Trades the six-digit code from the parent app for a reading token.
    ///
    /// The key signs its own enrolment code, which is what proves to the server that this
    /// app holds the private half of the key it is registering -- a code read over
    /// someone's shoulder is no use without it.
    static func enrol(
        address: String, code: String, name: String
    ) async throws -> Credentials {
        guard let baseURL = Self.normalise(address) else { throw BackendError.badAddress }

        let publicKey = try ApproverKey.create()
        let path = "/api/approver/enroll"
        let signature = try ApproverKey.sign(
            Approval.payload(nonce: code, method: "POST", path: path, body: Data()),
            reason: "注册这台手机为审批手机")

        let body: [String: Any] = [
            "code": code,
            "name": name,
            "publicKey": publicKey.base64EncodedString(),
            "signature": signature.base64EncodedString(),
        ]
        let json = try await Self.send(
            url: baseURL.appendingPathComponent(path), method: "POST",
            body: try JSONSerialization.data(withJSONObject: body), headers: [:])

        guard
            let approverID = json["approverId"] as? String,
            let token = json["token"] as? String
        else {
            // The key is useless without the server's half of the deal, so do not leave
            // a dangling one behind to confuse the next attempt.
            ApproverKey.deleteExisting()
            throw BackendError.malformed("注册响应里没有 approverId/token")
        }

        let credentials = Credentials(
            baseURL: baseURL, approverID: approverID, token: token)
        credentials.save()
        return credentials
    }

    // MARK: - reading

    func state() async throws -> [String: Any] {
        try await Self.send(
            url: credentials.baseURL.appendingPathComponent("/api/approver/state"),
            method: "GET", body: nil, headers: authHeaders)
    }

    // MARK: - approving

    /// Hands out television time. The one action this app exists for.
    func unlock(minutes: Int, note: String) async throws {
        let body = try JSONSerialization.data(withJSONObject: [
            "minutes": minutes,
            "note": note,
        ])
        _ = try await approve(
            method: "POST", path: "/api/parent/grants", body: body,
            reason: "解锁电视 \(minutes) 分钟")
    }

    func setEnforcement(_ enabled: Bool) async throws {
        let body = try JSONSerialization.data(withJSONObject: ["enabled": enabled])
        _ = try await approve(
            method: "POST", path: "/api/parent/enforcement", body: body,
            reason: enabled ? "重新打开拦截" : "关闭拦截")
    }

    /// Fetch a nonce, sign the exact request, send it. Every gated endpoint goes through
    /// here, so there is one place where the signing can be got wrong.
    ///
    /// The nonce is spent whether the server accepts the signature or not, so a failure
    /// means starting again from the challenge -- which is also one more Face ID prompt,
    /// deliberately.
    @discardableResult
    func approve(
        method: String, path: String, body: Data?, reason: String
    ) async throws -> [String: Any] {
        let challenge = try await Self.send(
            url: credentials.baseURL.appendingPathComponent("/api/approver/challenge"),
            method: "POST", body: nil, headers: authHeaders)
        guard
            let challengeID = challenge["challengeId"] as? String,
            let nonce = challenge["nonce"] as? String
        else {
            throw BackendError.malformed("挑战响应里没有 challengeId/nonce")
        }

        let signature = try ApproverKey.sign(
            Approval.payload(
                nonce: nonce, method: method, path: path, body: body ?? Data()),
            reason: reason)

        var headers = authHeaders
        headers["X-Approver-Id"] = credentials.approverID
        headers["X-Approver-Challenge"] = challengeID
        headers["X-Approver-Signature"] = signature.base64EncodedString()

        return try await Self.send(
            url: credentials.baseURL.appendingPathComponent(path),
            method: method, body: body, headers: headers)
    }

    private var authHeaders: [String: String] {
        ["Authorization": "Bearer \(credentials.token)"]
    }

    // MARK: - plumbing

    /// Accepts "tv.example.com" as readily as a full URL, because the alternative is
    /// typing a scheme on a phone keyboard.
    static func normalise(_ address: String) -> URL? {
        let trimmed = address.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return nil }
        let withScheme = trimmed.contains("://") ? trimmed : "https://\(trimmed)"
        guard let url = URL(string: withScheme), url.host != nil else { return nil }
        return url
    }

    private static func send(
        url: URL, method: String, body: Data?, headers: [String: String]
    ) async throws -> [String: Any] {
        var request = URLRequest(url: url)
        request.httpMethod = method
        request.httpBody = body
        if body != nil {
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        }
        for (name, value) in headers {
            request.setValue(value, forHTTPHeaderField: name)
        }
        // Short: every one of these is a tap the parent is waiting on.
        request.timeoutInterval = 15

        let (data, response) = try await URLSession.shared.data(for: request)
        let status = (response as? HTTPURLResponse)?.statusCode ?? 0
        let json = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any] ?? [:]

        guard (200..<300).contains(status) else {
            throw BackendError.http(status, json["error"] as? String ?? "")
        }
        return json
    }
}
