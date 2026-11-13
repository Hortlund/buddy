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

struct ChatUpload: Encodable, Equatable, Sendable {
    let name: String
    let data: Data

    static func read(_ urls: [URL], remainingBytes: Int) throws -> [Self] {
        var remaining = remainingBytes
        return try urls.map { url in
            let scoped = url.startAccessingSecurityScopedResource()
            defer { if scoped { url.stopAccessingSecurityScopedResource() } }
            let file = try FileHandle(forReadingFrom: url)
            defer { try? file.close() }
            let data = try file.read(upToCount: remaining + 1) ?? Data()
            guard data.count <= remaining else { throw BuddyError("Attachments can total up to 8 MB.") }
            remaining -= data.count
            return ChatUpload(name: url.lastPathComponent, data: data)
        }
    }
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
    private let voiceSession: URLSession

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
        let voiceConfig = URLSessionConfiguration.ephemeral
        voiceConfig.timeoutIntervalForRequest = 60
        voiceConfig.timeoutIntervalForResource = 60 * 60
        voiceConfig.tlsMinimumSupportedProtocolVersion = .TLSv13
        voiceSession = URLSession(configuration: voiceConfig, delegate: ServerTrust(pin: pin), delegateQueue: nil)
    }

    convenience init(credentials: BuddyCredentials) {
        self.init(server: credentials.server, pin: credentials.pin, credentials: credentials)
    }

    deinit { session.invalidateAndCancel(); voiceSession.invalidateAndCancel() }

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

    func workspaceItem(bud: String, path: String, task: String? = nil) async throws -> WorkspacePreviewItem {
        var query = [URLQueryItem(name: "path", value: path)]
        if let task { query.append(URLQueryItem(name: "task", value: task)) }
        let item: WorkspacePreviewItem = try await load("/v1/agents/\(bud)/workspace/item", query: query)
        if let task, item.sourceTask != task { throw BuddyError("Update the Buddy server to open task-bound file references safely.") }
        return item
    }

    func voiceAvailable() async throws -> Bool {
        struct Status: Decodable { let source: String }
        do {
            let status: Status = try await load("/v1/runtime/voice/key")
            return !status.source.isEmpty
        } catch let error as BuddyError where error.status == 403 || error.status == 404 {
            throw BuddyError("Update the Buddy server to enable iPhone calls.")
        }
    }

    func openVoice(chat: String, agent: String) throws -> URLSessionWebSocketTask {
        guard let credentials, var url = URLComponents(url: server, resolvingAgainstBaseURL: false) else {
            throw BuddyError("Pair this iPhone with a Buddy server before calling.")
        }
        url.scheme = "wss"
        url.path = "/v1/voice/call"
        url.queryItems = [URLQueryItem(name: "chat", value: chat), URLQueryItem(name: "agent", value: agent)]
        guard let address = url.url else { throw BuddyError("The Buddy server address is invalid.") }
        var request = URLRequest(url: address)
        request.timeoutInterval = 30
        request.setValue("Bearer " + credentials.token, forHTTPHeaderField: "Authorization")
        let call = voiceSession.webSocketTask(with: request)
        call.maximumMessageSize = 256 << 10
        call.resume()
        return call
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

    func send(chat: String, text: String, agent: String? = nil, job: String? = nil,
              background: Bool = false, backgroundAnswer: String? = nil,
              attachments: [ChatUpload] = [], key: String) async throws {
        struct Message: Encodable {
            let text: String
            let agent_id: String?
            let job_id: String?
            let background: Bool?
            let background_answer: String?
            let attachments: [ChatUpload]?
        }
        let body = Message(text: text, agent_id: agent, job_id: job, background: background ? true : nil,
                           background_answer: backgroundAnswer, attachments: attachments.isEmpty ? nil : attachments)
        do {
            _ = try await data("/v1/conversations/\(chat)/messages", method: "POST", body: body, key: key)
        } catch let error as BuddyError where background && error.status == 400 && error.message == "Invalid request." {
            throw BuddyError("Update the Buddy server to run work directly from the composer.")
        }
    }

    enum BudApproval: String, Encodable {
        case createOnly = "create_only"
        case startWork = "start_work"
    }

    func createProposedBud(chat: String, task: String, approval: BudApproval?) async throws -> Bud {
        do {
            let path = "/v1/conversations/\(chat)/bud-proposals/\(task)"
            if let approval {
                return try await load(path + "/approval", method: "POST", body: ["approval": approval])
            }
            return try await load(path, method: "POST")
        } catch let error as BuddyError where error.status == 403 || error.status == 404 && error.message == "Buddy returned HTTP 404." {
            throw BuddyError("Update the Buddy server to approve this draft.")
        }
    }

    func stopWork(_ job: String) async throws {
        do {
            _ = try await data("/v1/work/\(job)/cancel", method: "POST")
        } catch let error as BuddyError where error.status == 403 || error.status == 405 {
            throw BuddyError("Update the Buddy server to stop jobs from iPhone.")
        }
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

    func workArtifact(job: String, file: String) async throws -> WorkArtifact { try await load("/v1/work/\(job)/artifacts/\(file)") }
    func executionFrame(job: String) async throws -> ExecutionFrame { try await load("/v1/work/\(job)/frame") }
    func pauseAutomation(job: String, paused: Bool) async throws { _ = try await data("/v1/work/\(job)/control", method: "POST", body: ["paused": paused]) }
    func hosts() async throws -> [ExecutionHost] { try await load("/v1/hosts") }

    func referencedWorkDetails(chat: String, id: String) async throws -> WorkDetails {
        do { return try await load("/v1/work/\(id)") }
        catch let error as BuddyError where error.status == 404 {
            return try await workDetails(chat: chat, id: id)
        }
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
