import CryptoKit
import Foundation
import LocalAuthentication
import Security

/// The one thing this whole app exists for: a key that only a face can use.
///
/// `.biometryCurrentSet` is doing the work. It means two things, and both matter here:
/// the private key cannot be used without a successful biometric match, with no passcode
/// fallback of any kind; and the key is destroyed the moment the enrolled set of faces
/// changes. A child who adds their own face to Face ID does not gain the ability to
/// approve anything -- they destroy the approver instead, which is loud and recoverable
/// rather than quiet and not.
///
/// Compare `.userPresence`, which is what most sample code reaches for: that one *does*
/// fall back to the passcode, and would have left us exactly where the passkey already
/// was.
enum ApproverKey {

    enum Failure: Error, LocalizedError {
        case accessControl(String)
        case generate(String)
        case notEnrolled
        case noPublicKey
        case export(String)
        case sign(String)
        case noSecureEnclave

        var errorDescription: String? {
            switch self {
            case .accessControl(let why): "无法建立访问控制: \(why)"
            case .generate(let why): "无法生成密钥: \(why)"
            case .notEnrolled: "这台手机还没注册成审批手机"
            case .noPublicKey: "拿不到公钥"
            case .export(let why): "导出公钥失败: \(why)"
            case .sign(let why): "签名失败: \(why)"
            case .noSecureEnclave: "这台设备没有 Secure Enclave,或者没有设置 Face ID"
            }
        }
    }

    private static let tag = Data("dev.homelab.braviaguard.approver".utf8)

    /// Whether biometrics are set up at all. Worth checking before offering to enrol,
    /// because key generation under `.biometryCurrentSet` fails on a phone with no face
    /// enrolled, and the error it fails with is not one a parent should have to read.
    static var biometricsAvailable: Bool {
        var error: NSError?
        let ready = LAContext().canEvaluatePolicy(
            .deviceOwnerAuthenticationWithBiometrics, error: &error)
        return ready
    }

    static var isEnrolled: Bool {
        (try? loadPrivateKey()) != nil
    }

    // MARK: - creating

    /// Generates the key inside the Secure Enclave. Replaces any previous one, so a
    /// re-enrolment after a lost phone or a changed face set is just this call again.
    @discardableResult
    static func create() throws -> Data {
        deleteExisting()

        var cfError: Unmanaged<CFError>?
        guard let access = SecAccessControlCreateWithFlags(
            nil,
            // ThisDeviceOnly: never goes near a backup or another device. The key is the
            // phone, which is the whole premise.
            kSecAttrAccessibleWhenUnlockedThisDeviceOnly,
            [.privateKeyUsage, .biometryCurrentSet],
            &cfError
        ) else {
            throw Failure.accessControl(describe(cfError))
        }

        let attributes: [String: Any] = [
            kSecAttrKeyType as String: kSecAttrKeyTypeECSECPrimeRandom,
            kSecAttrKeySizeInBits as String: 256,
            kSecAttrTokenID as String: kSecAttrTokenIDSecureEnclave,
            kSecPrivateKeyAttrs as String: [
                kSecAttrIsPermanent as String: true,
                kSecAttrApplicationTag as String: tag,
                kSecAttrAccessControl as String: access,
            ],
        ]

        guard let key = SecKeyCreateRandomKey(attributes as CFDictionary, &cfError) else {
            throw Failure.generate(describe(cfError))
        }
        return try publicKeyData(of: key)
    }

    static func deleteExisting() {
        SecItemDelete([
            kSecClass as String: kSecClassKey,
            kSecAttrApplicationTag as String: tag,
            kSecAttrKeyType as String: kSecAttrKeyTypeECSECPrimeRandom,
        ] as CFDictionary)
    }

    // MARK: - using

    /// The uncompressed ANSI X9.63 point, `04 || X || Y`, 65 bytes. The server stores it
    /// in exactly this form; see parseApproverKey in approver.go.
    static func publicKey() throws -> Data {
        try publicKeyData(of: try loadPrivateKey())
    }

    /// Signs, which is what triggers the Face ID prompt -- the access control on the key
    /// does that, not any call made here. There is nothing to suppress: with
    /// `.biometryCurrentSet` the system never offers a passcode alternative.
    ///
    /// `reason` is what the parent reads in the prompt, so it says what is being
    /// approved rather than something generic.
    static func sign(_ payload: Data, reason: String) throws -> Data {
        let context = LAContext()
        // What the parent reads in the prompt. Set on the context rather than passed as
        // kSecUseOperationPrompt, which has been deprecated since iOS 14.
        context.localizedReason = reason
        // Belt and braces: an empty fallback title removes the button entirely on the
        // off chance a future OS offers one for a key it should not.
        context.localizedFallbackTitle = ""

        let key = try loadPrivateKey(context: context)
        var cfError: Unmanaged<CFError>?
        guard let signature = SecKeyCreateSignature(
            key, .ecdsaSignatureMessageX962SHA256, payload as CFData, &cfError
        ) else {
            throw Failure.sign(describe(cfError))
        }
        // DER, which is what crypto/ecdsa.VerifyASN1 wants on the other end.
        return signature as Data
    }

    // MARK: - internals

    /// Without a context this does not prompt, which is what `isEnrolled` relies on: it
    /// needs to know whether the key exists without putting a Face ID sheet on screen.
    private static func loadPrivateKey(context: LAContext? = nil) throws -> SecKey {
        var query: [String: Any] = [
            kSecClass as String: kSecClassKey,
            kSecAttrApplicationTag as String: tag,
            kSecAttrKeyType as String: kSecAttrKeyTypeECSECPrimeRandom,
            kSecReturnRef as String: true,
        ]
        if let context {
            query[kSecUseAuthenticationContext as String] = context
        }

        var item: CFTypeRef?
        let status = SecItemCopyMatching(query as CFDictionary, &item)
        guard status == errSecSuccess, let key = item else {
            throw Failure.notEnrolled
        }
        // The query asked for a key ref, so this is one.
        return key as! SecKey
    }

    private static func publicKeyData(of key: SecKey) throws -> Data {
        guard let publicKey = SecKeyCopyPublicKey(key) else {
            throw Failure.noPublicKey
        }
        var cfError: Unmanaged<CFError>?
        guard let data = SecKeyCopyExternalRepresentation(publicKey, &cfError) else {
            throw Failure.export(describe(cfError))
        }
        return data as Data
    }

    private static func describe(_ error: Unmanaged<CFError>?) -> String {
        guard let error else { return "原因不明" }
        return (error.takeRetainedValue() as Error).localizedDescription
    }
}

/// Builds the byte string the server will rebuild and verify.
///
/// This has to agree with approvalPayload in server/internal/api/approver.go down to the
/// newlines. Five fields, joined by "\n": a domain string, the server's nonce, the HTTP
/// method, the path, and the body's SHA-256 in lower-case hex. The body is in there so an
/// approval cannot be moved onto a different request, and the method and path are in
/// there so one cannot be moved onto a different endpoint.
enum Approval {

    static let domain = "braviaguard/approval/v1"

    static func payload(nonce: String, method: String, path: String, body: Data) -> Data {
        let digest = SHA256.hash(data: body)
        let hex = digest.map { String(format: "%02x", $0) }.joined()
        return Data([domain, nonce, method, path, hex].joined(separator: "\n").utf8)
    }
}
