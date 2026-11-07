import CryptoKit
import Foundation
import Security

struct PairingLink {
    let server: URL
    let pin: String
    let code: String

    init(_ text: String) throws {
        guard text.utf8.count < 4096,
              let link = URLComponents(string: text.trimmingCharacters(in: .whitespacesAndNewlines)),
              link.scheme == "buddy-pair", link.host == "connect", link.user == nil,
              let parts = link.queryItems,
              parts.count == 4, Set(parts.map(\.name)).count == 4,
              let version = parts.first(where: { $0.name == "v" })?.value, version == "1",
              let address = parts.first(where: { $0.name == "server" })?.value,
              let origin = URLComponents(string: address), origin.scheme == "https",
              let host = origin.host, !host.isEmpty,
              origin.user == nil, origin.password == nil, origin.query == nil, origin.fragment == nil,
              origin.path == "" || origin.path == "/",
              let server = origin.url,
              let pin = parts.first(where: { $0.name == "pin" })?.value,
              pin.utf8.count == 64, pin.utf8.allSatisfy({ (48...57).contains($0) || (97...102).contains($0) }),
              let code = parts.first(where: { $0.name == "code" })?.value,
              !code.isEmpty else {
            throw BuddyError("That isn’t a Buddy pairing link. Generate a QR code on your Mac.")
        }
        self.server = server
        self.pin = pin
        self.code = code
    }
}

struct BuddyCredentials: Codable, Equatable {
    let id: String
    let name: String
    let server: URL
    let pin: String
    let token: String
}

struct BuddyError: LocalizedError {
    let message: String
    let status: Int?

    init(_ message: String, status: Int? = nil) {
        self.message = message
        self.status = status
    }

    var errorDescription: String? { message }
}

private final class ServerTrust: NSObject, URLSessionTaskDelegate {
    let pin: String

    init(pin: String) {
        self.pin = pin
    }

    func urlSession(_ session: URLSession, didReceive challenge: URLAuthenticationChallenge,
                    completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
        guard challenge.protectionSpace.authenticationMethod == NSURLAuthenticationMethodServerTrust,
              let trust = challenge.protectionSpace.serverTrust,
              let leaf = (SecTrustCopyCertificateChain(trust) as? [SecCertificate])?.first else {
            completionHandler(.cancelAuthenticationChallenge, nil)
            return
        }
        let fingerprint = SHA256.hash(data: SecCertificateCopyData(leaf) as Data)
            .map { String(format: "%02x", $0) }.joined()
        // The desktop's QR code binds this exact certificate to the origin.
        // Buddy's private self-signed identity is also used by older homes.
        guard fingerprint == pin,
              SecTrustSetPolicies(trust, SecPolicyCreateBasicX509()) == errSecSuccess,
              SecTrustSetAnchorCertificates(trust, [leaf] as CFArray) == errSecSuccess,
              SecTrustSetAnchorCertificatesOnly(trust, true) == errSecSuccess,
              SecTrustSetNetworkFetchAllowed(trust, false) == errSecSuccess,
              SecTrustEvaluateWithError(trust, nil) else {
            completionHandler(.cancelAuthenticationChallenge, nil)
            return
        }
        completionHandler(.useCredential, URLCredential(trust: trust))
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse,
                    newRequest request: URLRequest, completionHandler: @escaping (URLRequest?) -> Void) {
        completionHandler(nil)
    }
}

final class BuddyAPI: Sendable {
    let credentials: BuddyCredentials?
    private let server: URL
    private let session: URLSession

    private init(server: URL, pin: String, credentials: BuddyCredentials? = nil) {
        self.credentials = credentials
        self.server = server
        // Users can pair with a private Buddy certificate at any DNS name.
        // Keep HTTPS, the TLS minimum and certificate pin in this transport.
        let config = URLSessionConfiguration.ephemeral
        config.timeoutIntervalForRequest = 10
        config.timeoutIntervalForResource = 15
        config.tlsMinimumSupportedProtocolVersion = .TLSv13
        session = URLSession(configuration: config, delegate: ServerTrust(pin: pin), delegateQueue: nil)
    }

    convenience init(credentials: BuddyCredentials) {
        self.init(server: credentials.server, pin: credentials.pin, credentials: credentials)
    }

    deinit { session.invalidateAndCancel() }

    static func pair(_ link: PairingLink, name: String) async throws -> BuddyCredentials {
        struct Receipt: Decodable {
            let id: String
            let token: String
            let name: String
        }
        let api = BuddyAPI(server: link.server, pin: link.pin)
        let receipt: Receipt = try await api.load("/v1/mobile/pair", method: "POST", body: ["code": link.code, "name": name])
        return BuddyCredentials(id: receipt.id, name: receipt.name, server: link.server, pin: link.pin, token: receipt.token)
    }

    func directory() async throws -> BuddyDirectory {
        try await load("/v1/workspace")
    }

    func chat(_ id: String, before: Int64? = nil, reactionsAfter: Int64? = nil) async throws -> Conversation {
        var query: [URLQueryItem] = []
        if let before { query.append(URLQueryItem(name: "before", value: String(before))) }
        if let reactionsAfter { query.append(URLQueryItem(name: "reactions_after", value: String(reactionsAfter))) }
        return try await load("/v1/conversations/\(id)", query: query)
    }

    func setReaction(chat: String, message: Int64, emoji: String) async throws -> ReactionUpdate {
        try await load("/v1/conversations/\(chat)/messages/\(message)/reaction", method: "PUT", body: ["emoji": emoji])
    }

    func generatedImage(task: String) async throws -> Data {
        struct ImagePayload: Decodable { let data: Data }
        let image: ImagePayload = try await load("/v1/tasks/\(task)/generated-image")
        return image.data
    }

    func send(chat: String, text: String, agent: String? = nil, job: String? = nil, key: String) async throws {
        var body = ["text": text]
        body["agent_id"] = agent
        body["job_id"] = job
        _ = try await data("/v1/conversations/\(chat)/messages", method: "POST", body: body, key: key)
    }

    func markRead(chat: String, through: Int64) async throws {
        _ = try await data("/v1/conversations/\(chat)/read", method: "POST", body: ["through": through])
    }

    func answerCard(chat: String, card: String, choice: Int) async throws {
        _ = try await data("/v1/conversations/\(chat)/cards/\(card)/answer", method: "POST", body: ["choice": choice])
    }

    func activateWatch(chat: String, task: String) async throws {
        _ = try await data("/v1/conversations/\(chat)/watch-drafts/\(task)/activate", method: "POST")
    }

    func inbox() async throws -> [InboxItem] {
        try await load("/v1/inbox") as [InboxItem]? ?? []
    }

    func inboxItem(_ id: String) async throws -> InboxItem {
        try await load("/v1/inbox/\(id)")
    }

    func decideInbox(id: String, decision: String) async throws {
        _ = try await data("/v1/inbox/\(id)", method: "PATCH", body: ["decision": decision])
    }

    func work() async throws -> [WorkItem] {
        try await load("/v1/work") as [WorkItem]? ?? []
    }

    func workHistory(before: String?) async throws -> WorkHistory {
        try await load("/v1/work/history", query: before.map { [URLQueryItem(name: "before", value: $0)] } ?? [])
    }

    func workDetails(chat: String, id: String) async throws -> WorkDetails {
        try await load("/v1/conversations/\(chat)/work/\(id)")
    }

    func answerWork(chat: String, input: String, text: String, key: String) async throws {
        _ = try await data("/v1/conversations/\(chat)/work-inputs/\(input)/answer", method: "POST", body: ["text": text], key: key)
    }

    func notificationAvailability() async throws -> Bool {
        struct Availability: Decodable { let available: Bool; let bundleId: String? }
        let status: Availability = try await load("/v1/mobile/notifications")
        if let configured = status.bundleId, let signed = Bundle.main.bundleIdentifier,
           configured.caseInsensitiveCompare(signed) != .orderedSame {
            throw BuddyError("This app’s bundle ID doesn’t match the Buddy server’s iPhone push configuration.")
        }
        return status.available
    }

    func setNotifications(token: String, work: Bool) async throws {
        struct Preference: Encodable { let token: String; let work: Bool }
        _ = try await data("/v1/mobile/notifications", method: "PUT", body: Preference(token: token, work: work))
    }

    func disconnect() async throws {
        _ = try await data("/v1/mobile/session", method: "DELETE")
    }

    private func load<Value: Decodable>(_ path: String, method: String = "GET", body: (any Encodable)? = nil,
                                        query: [URLQueryItem] = []) async throws -> Value {
        let data = try await data(path, method: method, body: body, query: query)
        let decoder = JSONDecoder()
        decoder.keyDecodingStrategy = .convertFromSnakeCase
        decoder.dateDecodingStrategy = .custom {
            let value = try $0.singleValueContainer().decode(String.self)
            let clock = ISO8601DateFormatter()
            clock.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
            if let date = clock.date(from: value) { return date }
            clock.formatOptions = [.withInternetDateTime]
            if let date = clock.date(from: value) { return date }
            throw BuddyError("Buddy returned an invalid timestamp.")
        }
        return try decoder.decode(Value.self, from: data)
    }

    private func data(_ path: String, method: String, body: (any Encodable)? = nil, key: String? = nil,
                      query: [URLQueryItem] = []) async throws -> Data {
        guard var endpoint = URLComponents(url: server, resolvingAgainstBaseURL: false),
              endpoint.scheme == "https", endpoint.host != nil, endpoint.user == nil,
              endpoint.password == nil, endpoint.fragment == nil else {
            throw BuddyError("Invalid Buddy server address.")
        }
        endpoint.path = path
        endpoint.queryItems = query.isEmpty ? nil : query
        guard let url = endpoint.url else { throw BuddyError("Invalid Buddy request.") }
        var request = URLRequest(url: url)
        request.httpMethod = method
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        if let token = credentials?.token {
            request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        }
        if let key { request.setValue(key, forHTTPHeaderField: "Idempotency-Key") }
        if let body {
            request.httpBody = try JSONEncoder().encode(body)
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        }
        let data: Data
        let response: URLResponse
        do {
            (data, response) = try await session.data(for: request)
        } catch let error as URLError where [
            .serverCertificateUntrusted, .serverCertificateHasUnknownRoot, .secureConnectionFailed, .userCancelledAuthentication,
            .cancelled
        ].contains(error.code) && !Task.isCancelled {
            throw BuddyError("Buddy couldn’t verify this server. Check its address or generate a new pairing code.")
        }
        guard let http = response as? HTTPURLResponse else { throw BuddyError("Invalid response from Buddy.") }
        if http.statusCode == 401 && credentials != nil {
            throw BuddyError("This iPhone is no longer paired. Open Settings to reconnect.", status: 401)
        }
        guard (200..<300).contains(http.statusCode) else {
            struct Problem: Decodable { let error: String }
            let message = (try? JSONDecoder().decode(Problem.self, from: data).error) ?? "Buddy returned HTTP \(http.statusCode)."
            throw BuddyError(message, status: http.statusCode)
        }
        return data
    }
}

enum BuddyKeychain {
    private static let query: [String: Any] = [
        kSecClass as String: kSecClassGenericPassword,
        kSecAttrService as String: "dev.buddy.ios.connection",
        kSecAttrAccount as String: "buddy-home",
    ]

    static func load() -> BuddyCredentials? {
        var request = query
        request[kSecReturnData as String] = true
        request[kSecMatchLimit as String] = kSecMatchLimitOne
        var result: CFTypeRef?
        guard SecItemCopyMatching(request as CFDictionary, &result) == errSecSuccess,
              let data = result as? Data else { return nil }
        return try? JSONDecoder().decode(BuddyCredentials.self, from: data)
    }

    static func save(_ credentials: BuddyCredentials) throws {
        let data = try JSONEncoder().encode(credentials)
        var item = query
        item[kSecValueData as String] = data
        item[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        var status = SecItemAdd(item as CFDictionary, nil)
        if status == errSecDuplicateItem {
            status = SecItemUpdate(query as CFDictionary, [kSecValueData as String: data] as CFDictionary)
        }
        guard status == errSecSuccess else { throw BuddyError("Buddy couldn’t save access in the Keychain.") }
    }

    static func clear() {
        SecItemDelete(query as CFDictionary)
    }
}
