import SwiftUI

struct ChatList: View {
    let api: BuddyAPI
    @Environment(\.scenePhase) private var phase
    @State private var directory: BuddyDirectory?
    @State private var error: String?

    var body: some View {
        List {
            if let directory {
                Section("Buds") {
                    ForEach(directory.agents ?? []) { bud in
                        if let chat = bud.conversationId {
                            NavigationLink(value: Destination.chat(chat, bud.name)) {
                                HStack(spacing: 12) {
                                    BudFace(appearance: bud.avatar)
                                    VStack(alignment: .leading, spacing: 3) {
                                        HStack {
                                            Text(bud.name).font(.headline)
                                            if bud.id == directory.primaryAgentId {
                                            Image(systemName: "star.fill").font(.caption).foregroundStyle(BuddyStyle.accent)
                                                    .accessibilityLabel("Primary Bud")
                                            }
                                        }
                                        if let role = bud.role?.nilIfEmpty {
                                            Text(role).font(.subheadline).foregroundStyle(.secondary)
                                        }
                                    }
                                    Spacer()
                                    if bud.hasNewResult == true { unreadDot }
                                }
                                .padding(.vertical, 4)
                            }
                            .listRowBackground(BuddyStyle.surface)
                        }
                    }
                }
                if let groups = directory.groups, !groups.isEmpty {
                    Section("Group chats") {
                        ForEach(groups) { chat in
                            NavigationLink(value: Destination.chat(chat.id, chat.name)) {
                                HStack {
                                    Label(chat.name, systemImage: "person.2")
                                    Spacer()
                                    if chat.hasNewResult == true { unreadDot }
                                }
                            }
                            .listRowBackground(BuddyStyle.surface)
                        }
                    }
                }
            }
            if let error { ConnectionError(message: error) }
        }
        .buddyPage()
        .overlay { if directory == nil && error == nil { ProgressView("Connecting to Buddy…") } }
        .navigationTitle("Chats")
        .task(id: phase) { await refreshWhileActive(phase, every: .seconds(12), load: load) }
        .refreshable { await load() }
    }

    private func load() async {
        do {
            directory = try await api.directory()
            error = nil
        } catch where !Task.isCancelled { self.error = error.localizedDescription } catch {}
    }
}

private var unreadDot: some View {
    Circle().fill(BuddyStyle.accent).frame(width: 8, height: 8).accessibilityLabel("Unread activity")
}

struct ConnectionError: View {
    let message: String

    var body: some View {
        Label(message, systemImage: "exclamationmark.circle")
            .font(.footnote)
            .foregroundStyle(.secondary)
            .accessibilityIdentifier("buddy-error")
    }
}

struct ChatView: View {
    let api: BuddyAPI
    let id: String
    let name: String
    @Environment(\.scenePhase) private var phase
    @Environment(PhoneVoiceSession.self) private var voice
    @State private var conversation: Conversation?
    @State private var older: [ChatMessage] = []
    @State private var cursor: Int64?
    @State private var loadingHistory = false
    @State private var loadingLatest = false
    @State private var lastRead: Int64 = 0
    @State private var draft = ""
    @State private var busy = false
    @State private var loadError: String?
    @State private var actionError: String?
    @State private var outgoing: Outgoing?
    @State private var reactionOverrides: [Int64: ReactionUpdate] = [:]
    @State private var reactionCursor: Int64?

    private struct Outgoing {
        let text: String
        let key: String
    }

    private var messages: [ChatMessage] {
        let recent = conversation?.messages ?? []
        if let first = recent.first { return older.filter { $0.id < first.id } + recent }
        return older
    }

    var body: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(spacing: 14) {
                    if let before = cursor, before > 0 {
                        Button(loadingHistory ? "Loading…" : "Load earlier messages") {
                            loadingHistory = true
                            Task { await loadEarlier(before: before) }
                        }
                        .buttonStyle(.bordered)
                        .disabled(loadingHistory || loadingLatest)
                    }
                    ForEach(messages) { message in
                        MessageBubble(message: message, chat: id, api: api, avatar: avatar(for: message), busy: busy || conversation?.archived == true,
                                      reactions: reactions(for: message),
                                      reactionOptions: conversation?.archived == true ? [] : conversation?.reactionOptions ?? [],
                                      onReaction: { emoji in
                                          perform {
                                              let update = try await api.setReaction(chat: id, message: message.id, emoji: emoji)
                                              recordReaction(update)
                                          }
                                      },
                                      onChoice: { index in perform { try await api.answerCard(chat: id, card: message.taskId, choice: index) } },
                                      onActivate: { perform { try await api.activateWatch(chat: id, task: message.taskId) } })
                            .id(message.id)
                    }
                    if let active = conversation?.active {
                        HStack(spacing: 9) {
                            ProgressView().controlSize(.small)
                            Text(active.activity?.nilIfEmpty ?? statusLabel(active.status)).font(.footnote).foregroundStyle(.secondary)
                            Spacer()
                        }
                    }
                    Color.clear.frame(height: 1).id("chat-bottom")
                }
                .padding(16)
            }
            .defaultScrollAnchor(.bottom)
            .onChange(of: messages.last?.id) { _, _ in
                withAnimation(.easeOut(duration: 0.18)) { proxy.scrollTo("chat-bottom", anchor: .bottom) }
            }
            .overlay {
                if conversation == nil && loadError == nil { ProgressView("Loading conversation…") }
                if let loadError, conversation == nil { ContentUnavailableView("Can’t open chat", systemImage: "wifi.slash", description: Text(loadError)) }
            }
            .safeAreaInset(edge: .bottom, spacing: 0) {
                if conversation?.archived == true {
                    Text("This group is archived.")
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                        .padding()
                } else {
                    composer
                }
            }
        }
        .buddyPage()
        .navigationTitle(conversation?.name?.nilIfEmpty ?? conversation?.agent.name ?? name)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                Button {
                    if let conversation { voice.start(api: api, conversation: conversation, chat: id) }
                } label: { Image(systemName: "phone") }
                .accessibilityLabel(voice.active ? "Return to voice call" : "Start voice call")
                .disabled(conversation == nil || conversation?.archived == true)
            }
        }
        .task(id: phase) { await refreshWhileActive(phase, every: .seconds(3), load: load) }
        .refreshable { await load() }
    }

    private var composer: some View {
        VStack(alignment: .leading, spacing: 7) {
            if let message = actionError ?? (conversation == nil ? nil : loadError) {
                ConnectionError(message: message)
            }
            HStack(alignment: .bottom, spacing: 10) {
                TextField(conversation?.name?.nilIfEmpty == nil ? "Message" : "Message the group", text: $draft, axis: .vertical)
                    .lineLimit(1...5)
                    .textFieldStyle(.plain)
                    .padding(11)
                    .background(BuddyStyle.raised, in: RoundedRectangle(cornerRadius: 16))
                    .accessibilityIdentifier("chat-composer")
                    .disabled(busy)
                Button(action: send) {
                    Image(systemName: "arrow.up.circle.fill").font(.system(size: 34))
                }
                .accessibilityLabel("Send")
                .disabled(busy || draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || conversation == nil)
            }
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 9)
        .background(BuddyStyle.background)
    }

    private func avatar(for message: ChatMessage) -> BudAppearance? {
        guard let conversation else { return nil }
        guard let sender = message.agentId, sender != conversation.agent.id else { return conversation.agent.avatar }
        return conversation.profiles?.first { $0.id == sender }?.avatar
            ?? conversation.members?.first { $0.id == sender }?.avatar
    }

    private func load() async {
        guard !loadingLatest, !loadingHistory else { return }
        loadingLatest = true
        defer { loadingLatest = false }
        do {
            let snapshot = try await api.chat(id, reactionsAfter: reactionCursor)
            let archiveChanged = (conversation?.transcriptRevision ?? 0) != (snapshot.transcriptRevision ?? 0)
            let recent = archiveChanged ? [] : (conversation?.messages ?? [])
            let bridge = try await messagesBetween(last: recent.last?.id, and: snapshot.before)
            try Task.checkCancellation()
            if conversation == nil || archiveChanged { cursor = snapshot.before }
            if archiveChanged {
                older = []
                reactionOverrides = [:]
            }
            if let first = snapshot.messages?.first {
                let known = older.last?.id ?? 0
                // Bridge pages are returned newest page first. Store the
                // contiguous history in display order before exposing the live page.
                older += (recent + bridge).filter { $0.id > known && $0.id < first.id }.sorted { $0.id < $1.id }
            }
            conversation = snapshot
            reactionCursor = snapshot.reactionRevision
            if let updates = snapshot.reactionUpdates, !updates.isEmpty {
                let loadedIDs = Set(older.map(\.id))
                for update in updates where loadedIDs.contains(update.messageId) {
                    recordReaction(update)
                }
            }
            for message in snapshot.messages ?? [] {
                if let update = reactionOverrides[message.id], update.revision <= (message.reactionRevision ?? 0) {
                    reactionOverrides.removeValue(forKey: message.id)
                }
            }
            loadError = nil
            if let through = snapshot.messages?.last?.id, through > lastRead {
                try await api.markRead(chat: id, through: through)
                lastRead = max(lastRead, through)
            }
        } catch where !Task.isCancelled { loadError = error.localizedDescription } catch {}
    }

    private func messagesBetween(last: Int64?, and newestCursor: Int64?) async throws -> [ChatMessage] {
        guard let last else { return [] }
        var cursor = newestCursor
        var bridge: [ChatMessage] = []
        while let before = cursor, before > last {
            let page = try await api.chat(id, before: before)
            try Task.checkCancellation()
            bridge += (page.messages ?? []).filter { $0.id > last }
            cursor = page.before
        }
        return bridge
    }

    private func loadEarlier(before: Int64) async {
        defer { loadingHistory = false }
        do {
            let page = try await api.chat(id, before: before)
            guard (page.transcriptRevision ?? 0) == (conversation?.transcriptRevision ?? 0) else { return }
            let first = messages.first?.id ?? Int64.max
            older.insert(contentsOf: (page.messages ?? []).filter { $0.id < first }, at: 0)
            cursor = page.before
            if let revision = page.reactionRevision, let reactionCursor, revision < reactionCursor {
                self.reactionCursor = revision
            }
            actionError = nil
        } catch { actionError = error.localizedDescription }
    }

    private func reactions(for message: ChatMessage) -> [MessageReaction] {
        if let update = reactionOverrides[message.id], update.revision > (message.reactionRevision ?? 0) {
            return update.reactions
        }
        return message.reactions ?? []
    }

    private func recordReaction(_ update: ReactionUpdate) {
        if update.revision > (reactionOverrides[update.messageId]?.revision ?? 0) {
            reactionOverrides[update.messageId] = update
        }
    }

    private func send() {
        let text = draft.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else { return }
        let retry = outgoing.flatMap { $0.text == text ? $0 : nil }
        let message = retry ?? Outgoing(text: text, key: UUID().uuidString)
        outgoing = message
        perform {
            try await api.send(chat: id, text: message.text, agent: nil, key: message.key)
            draft = ""
            outgoing = nil
        }
    }

    private func perform(_ action: @escaping @MainActor () async throws -> Void) {
        guard !busy else { return }
        busy = true
        actionError = nil
        Task {
            defer { busy = false }
            do {
                try await action()
                await load()
            } catch { actionError = error.localizedDescription }
        }
    }
}

private struct MessageBubble: View {
    let message: ChatMessage
    let chat: String
    let api: BuddyAPI
    let avatar: BudAppearance?
    let busy: Bool
    let reactions: [MessageReaction]
    let reactionOptions: [ReactionOption]
    let onReaction: (String) -> Void
    let onChoice: (Int) -> Void
    let onActivate: () -> Void

    private var outgoing: Bool { message.role == "user" }

    var body: some View {
        HStack(alignment: .top, spacing: 9) {
            if outgoing { Spacer(minLength: 24) }
            if !outgoing { BudFace(appearance: avatar, size: 29) }
            VStack(alignment: .leading, spacing: 8) {
                if let name = message.agentName?.nilIfEmpty, !outgoing {
                    Text(name).font(.caption.bold()).foregroundStyle(.secondary)
                }
                if !message.text.isEmpty { RichText(message.text) }
                if message.generatedImage == true {
                    GeneratedChatImage(api: api, task: message.taskId)
                }
                if let card = message.card { choice(card) }
                if let card = message.connectionCard {
                    evidence(title: card.heading, detail: card.detail, body: card.body, url: card.url, items: card.items)
                    if card.watchDraft == true {
                        Button("Activate watch", action: onActivate)
                            .buttonStyle(.borderedProminent)
                            .disabled(busy)
                    }
                }
                if let card = message.github {
                    evidence(title: card.heading ?? "GitHub", detail: card.detail, body: nil, url: card.url, items: card.items)
                    if let patch = card.patch?.nilIfEmpty { CodeBlock(language: "diff", code: patch) }
                }
                if let attachments = message.attachments {
                    ForEach(attachments.indices, id: \.self) { index in
                        Label(attachments[index].name, systemImage: "paperclip").font(.callout)
                    }
                }
                if let job = message.jobId {
                    NavigationLink(value: Destination.work(message.jobConversationId ?? chat, job)) {
                        Label("View job", systemImage: "briefcase")
                    }
                    .font(.footnote)
                }
                if !reactions.isEmpty { reactionChips }
            }
            .padding(12)
            .background(outgoing ? BuddyStyle.raised : BuddyStyle.surface,
                        in: RoundedRectangle(cornerRadius: 18))
            if !outgoing {
                if !reactionOptions.isEmpty { reactionPicker }
                Spacer(minLength: 8)
            }
        }
    }

    private var reactionPicker: some View {
        let current = reactions.first(where: \.mine)?.emoji
        return Menu {
            ForEach(reactionOptions) { option in
                Button {
                    onReaction(option.emoji == current ? "" : option.emoji)
                } label: {
                    if option.emoji == current {
                        Label("\(option.emoji)  \(option.label)", systemImage: "checkmark")
                    } else {
                        Text("\(option.emoji)  \(option.label)")
                    }
                }
            }
        } label: {
            Image(systemName: "face.smiling")
                .font(.callout)
                .foregroundStyle(.secondary)
                .frame(width: 32, height: 36)
                .accessibilityLabel("React to message")
        }
        .disabled(busy)
    }

    private var reactionChips: some View {
        HStack(spacing: 4) {
            ForEach(reactions) { reaction in
                if reaction.mine && !reactionOptions.isEmpty {
                    Button(reaction.emoji) { onReaction("") }
                        .buttonStyle(.bordered)
                        .controlSize(.mini)
                        .accessibilityLabel("Remove your reaction \(reaction.emoji)")
                        .disabled(busy)
                } else {
                    Text(reaction.emoji)
                        .font(.callout)
                        .padding(.horizontal, 6)
                        .padding(.vertical, 2)
                        .background(.thinMaterial, in: Capsule())
                        .accessibilityLabel("\(reaction.agentName?.nilIfEmpty ?? (reaction.mine ? "You" : "Bud")) reacted \(reaction.emoji)")
                }
            }
        }
    }

    private func choice(_ card: ChoiceCard) -> some View {
        ChatChoiceCard(card: card, busy: busy, onChoice: onChoice)
    }

    private func evidence(title: String, detail: String?, body: String?, url: URL?, items: [ConnectionItem]?) -> some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(title).font(.headline)
            if let detail { Text(detail).font(.subheadline).foregroundStyle(BuddyStyle.muted) }
            if let body { RichText(body) }
            if let items {
                ForEach(items.indices, id: \.self) { index in
                    let item = items[index]
                    Divider()
                    VStack(alignment: .leading, spacing: 6) {
                        HStack(alignment: .firstTextBaseline) {
                            if let link = safeWebURL(item.url) { Link(item.title, destination: link).font(.subheadline.bold()) }
                            else { Text(item.title).font(.subheadline.bold()) }
                            Spacer(minLength: 0)
                            if let badge = item.badge?.nilIfEmpty {
                                Text(badge).font(.caption2.weight(.medium)).foregroundStyle(BuddyStyle.muted)
                                    .padding(.horizontal, 7).padding(.vertical, 3)
                                    .background(BuddyStyle.raised, in: Capsule())
                            }
                        }
                        if let detail = item.detail { Text(detail).font(.caption).foregroundStyle(BuddyStyle.muted) }
                        if let body = item.body { RichText(body) }
                        if let value = item.value { Text(value).font(.system(.caption, design: .monospaced)) }
                    }
                    .fixedSize(horizontal: false, vertical: true)
                }
            }
            if let link = safeWebURL(url) { Link("Open source", destination: link).font(.footnote) }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(14)
        .background(BuddyStyle.surface, in: RoundedRectangle(cornerRadius: 16))
        .overlay { RoundedRectangle(cornerRadius: 16).stroke(BuddyStyle.border, lineWidth: 1) }
    }
}

private struct GeneratedChatImage: View {
    let api: BuddyAPI
    let task: String
    @State private var picture: UIImage?
    @State private var failed = false
    @State private var retry = 0

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            if let picture {
                let image = Image(uiImage: picture)
                image.resizable().scaledToFit().clipShape(.rect(cornerRadius: 10))
                    .accessibilityLabel("Generated image")
                ShareLink(item: image, preview: SharePreview("Buddy image", image: image)) {
                    Label("Share image", systemImage: "square.and.arrow.up").font(.footnote)
                }
            } else if failed {
                Button("Retry image preview") { retry += 1 }
            } else {
                ProgressView("Loading image…")
            }
        }
        .task(id: retry) {
            guard picture == nil else { return }
            failed = false
            do {
                let data = try await api.generatedImage(task: task)
                try Task.checkCancellation()
                picture = UIImage(data: data)
                failed = picture == nil
            } catch {
                if !Task.isCancelled { failed = true }
            }
        }
    }
}

extension String {
    var nilIfEmpty: String? { isEmpty ? nil : self }
}
