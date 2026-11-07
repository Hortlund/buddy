import Foundation

struct BuddyDirectory: Decodable {
    let agents: [Bud]?
    let groups: [GroupChat]?
    let primaryAgentId: String
    let autonomyUnread: Int?
}

struct Bud: Decodable, Identifiable {
    let id: String
    let name: String
    let role: String?
    let conversationId: String?
    let hasNewResult: Bool?
    let avatar: BudAppearance?
}

struct BudAppearance: Decodable {
    let color: String?
    let shape: String?
}

struct GroupChat: Decodable, Identifiable {
    let id: String
    let name: String
    let hasNewResult: Bool?
}

struct Conversation: Decodable {
    let archived: Bool?
    let transcriptRevision: Int64?
    let agent: Bud
    let name: String?
    let messages: [ChatMessage]?
    let active: ActiveReply?
    let before: Int64?
    let members: [Bud]?
    let profiles: [Bud]?
    let reactionOptions: [ReactionOption]?
    let reactionRevision: Int64?
    let reactionUpdates: [ReactionUpdate]?
}

struct ActiveReply: Decodable {
    let status: String
    let activity: String?
}

struct ChatMessage: Decodable, Identifiable {
    let id: Int64
    let taskId: String
    let role: String
    let text: String
    let agentId: String?
    let agentName: String?
    let origin: String?
    let card: ChoiceCard?
    let connectionCard: ConnectionCard?
    let github: GitHubCard?
    let jobId: String?
    let jobConversationId: String?
    let attachments: [ChatAttachment]?
    let generatedImage: Bool?
    let reactions: [MessageReaction]?
    let reactionRevision: Int64?
}

struct ReactionOption: Decodable, Identifiable {
    let emoji: String
    let label: String
    var id: String { emoji }
}

struct MessageReaction: Decodable, Identifiable {
    let emoji: String
    let agentId: String?
    let agentName: String?
    var id: String { agentId ?? "" }
    var mine: Bool { id.isEmpty }
}

struct ReactionUpdate: Decodable {
    let messageId: Int64
    let revision: Int64
    let reactions: [MessageReaction]
}

struct ChatAttachment: Decodable {
    let name: String
}

struct ChoiceCard: Decodable {
    let confirmation: String?
    let question: String
    let detail: String?
    let options: [String]
    let optionDetails: [String]?
    let choice: Int?
}

struct ConnectionCard: Decodable {
    let heading: String
    let detail: String?
    let body: String?
    let url: URL?
    let items: [ConnectionItem]?
    let watchDraft: Bool?
}

struct ConnectionItem: Decodable {
    let title: String
    let detail: String?
    let body: String?
    let badge: String?
    let value: String?
    let url: URL?
}

struct GitHubCard: Decodable {
    let heading: String?
    let detail: String?
    let patch: String?
    let url: URL?
    let items: [ConnectionItem]?
}

struct InboxItem: Decodable, Identifiable {
    let id: String
    let watch: String
    let conversationId: String
    let bud: String
    let signal: WatchSignal
    let state: String
    let taskId: String?
    let inputId: String?
    let result: String?
    let seen: Bool
    let at: Date
}

struct WatchSignal: Decodable {
    let title: String
    let detail: String?
    let url: URL?
}

struct WorkItem: Decodable, Identifiable {
    let projectId: String?
    let projectName: String?
    let workerRole: String?
    let requirements: [String]?
    let environment: String?
    let device: String?
    let id: String
    let agentId: String?
    let conversationId: String
    let conversationName: String
    let agentName: String
    let title: String
    let status: String
    let update: String?
    let inputId: String?
}

struct WorkDetails: Decodable {
    let item: WorkItem
    let sessions: [WorkerSession]?
    let artifacts: [WorkArtifact]?
    let dependencies: [WorkDependency]?
    let delegated: [WorkDependency]?
    let brief: String
    let result: String?
    let question: String?
    let events: [WorkEvent]?

    private enum CodingKeys: String, CodingKey { case brief, result, question, events, sessions, artifacts, dependencies, delegated }

    init(from decoder: Decoder) throws {
        item = try WorkItem(from: decoder)
        let data = try decoder.container(keyedBy: CodingKeys.self)
        sessions = try data.decodeIfPresent([WorkerSession].self, forKey: .sessions)
        artifacts = try data.decodeIfPresent([WorkArtifact].self, forKey: .artifacts)
        dependencies = try data.decodeIfPresent([WorkDependency].self, forKey: .dependencies)
        delegated = try data.decodeIfPresent([WorkDependency].self, forKey: .delegated)
        brief = try data.decode(String.self, forKey: .brief)
        result = try data.decodeIfPresent(String.self, forKey: .result)
        question = try data.decodeIfPresent(String.self, forKey: .question)
        events = try data.decodeIfPresent([WorkEvent].self, forKey: .events)
    }
}

struct WorkEvent: Decodable, Identifiable {
    let seq: Int64
    let at: Date
    let text: String
    var id: Int64 { seq }
}

struct WorkHistory: Decodable {
    let items: [WorkItem]?
    let next: String?
}

func statusLabel(_ status: String) -> String {
    switch status {
    case "needs_input": "Needs your input"
    case "succeeded": "Finished"
    case "running": "In progress"
    case "approval": "Needs approval"
    case "notified", "overflow", "digest": "Watch update"
    default: status.replacingOccurrences(of: "_", with: " ").capitalized
    }
}

func safeWebURL(_ url: URL?) -> URL? {
    guard let url, let scheme = url.scheme?.lowercased(),
          ["https", "http"].contains(scheme), url.host != nil, url.user == nil else { return nil }
    return url
}

struct WorkerSession: Decodable, Identifiable {
    let id: String
    let host: String
    let workspace: String
    let branch: String?
    let started: Date
    let ended: Date?
    let status: String
}
struct WorkArtifact: Decodable, Identifiable {
    let id: String
    let name: String
    let mediaType: String
    let bytes: Int
    let data: Data?
}
struct WorkDependency: Decodable, Identifiable {
    let id: String
    let title: String
    let budName: String
    let status: String
    let conversationId: String
}
struct ExecutionFrame: Decodable {
    let data: Data?
    let provider: String
    let at: Date
    let paused: Bool
}
struct HostCapability: Decodable, Identifiable {
    let id: String
    let installed: Bool
    let enabled: Bool
    let ready: Bool
    let detail: String?
}
struct HostProfile: Decodable {
    let os: String
    let architecture: String
    let environment: String
    let capabilities: [HostCapability]
}
struct ExecutionHost: Decodable, Identifiable {
    let id: String
    let name: String
    let online: Bool
    let profile: HostProfile?
}
