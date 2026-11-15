import SwiftUI
import UniformTypeIdentifiers

struct ChatList: View {
    let api: BuddyAPI
    @Environment(\.scenePhase) private var phase
    @State private var directory: BuddyDirectory?
    @State private var error: String?

    @State private var search = ""
    @State private var creatingBud = false

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 26) {
                if let directory {
                    let buds = (directory.agents ?? []).filter { bud in
                        bud.conversationId != nil && matches(bud.name)
                    }
                    let groups = (directory.groups ?? []).filter { matches($0.name) }
                    LazyVGrid(columns: [GridItem(.adaptive(minimum: 101), spacing: 8)], spacing: 20) {
                        ForEach(buds) { bud in
                            if let chat = bud.conversationId {
                                let primary = bud.id == directory.primaryAgentId
                                NavigationLink(value: Destination.chat(chat, bud.name)) {
                                    VStack(spacing: 6) {
                                        BudFace(appearance: bud.avatar, size: 78, mood: primary ? .idle : .still)
                                            .overlay(alignment: .bottomTrailing) {
                                                if primary {
                                                    PrimaryBudStar().padding(5)
                                                }
                                            }
                                        VStack(spacing: 3) {
                                            HStack(spacing: 5) {
                                                Text(bud.name).lineLimit(2)
                                                if bud.hasNewResult == true { unreadDot }
                                            }
                                            .font(.subheadline.weight(.medium))
                                            Text(bud.displayRole ?? "")
                                                .font(.caption).foregroundStyle(BuddyStyle.muted)
                                                .lineLimit(2, reservesSpace: true)
                                                .accessibilityHidden(bud.displayRole == nil)
                                        }
                                        .multilineTextAlignment(.center)
                                    }
                                    .frame(maxWidth: .infinity)
                                    .padding(.vertical, 6)
                                    .contentShape(Rectangle())
                                }
                                .buttonStyle(.plain)
                            }
                        }
                    }
                    if !groups.isEmpty {
                        VStack(alignment: .leading, spacing: 7) {
                            Text("Groups").font(.subheadline.weight(.medium)).foregroundStyle(BuddyStyle.muted)
                                .accessibilityAddTraits(.isHeader)
                            VStack(spacing: 0) {
                                ForEach(groups) { chat in
                                    NavigationLink(value: Destination.chat(chat.id, chat.name)) {
                                        HStack {
                                            Label(chat.name, systemImage: "person.2")
                                            Spacer()
                                            if chat.hasNewResult == true { unreadDot }
                                            Image(systemName: "chevron.right").font(.caption).foregroundStyle(BuddyStyle.muted)
                                        }
                                        .padding(.horizontal, 14)
                                        .frame(minHeight: 48)
                                        .contentShape(Rectangle())
                                    }
                                    .buttonStyle(.plain)
                                    if chat.id != groups.last?.id { Divider().padding(.leading, 14) }
                                }
                            }
                            .background(BuddyStyle.surface, in: RoundedRectangle(cornerRadius: 18))
                        }
                    }
                    if buds.isEmpty && groups.isEmpty && !search.isEmpty {
                        ContentUnavailableView.search(text: search)
                    }
                }
                if let error { ConnectionError(message: error) }
            }
            .frame(maxWidth: 620)
            .padding(.horizontal, 18)
            .padding(.vertical, 24)
            .frame(maxWidth: .infinity)
        }
        .buddyPage()
        .overlay { if directory == nil && error == nil { ProgressView() } }
        .navigationTitle("Buddy")
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                Button { creatingBud = true } label: { Label("New Bud", systemImage: "plus") }
                    .disabled(directory == nil)
            }
        }
        .sheet(isPresented: $creatingBud) {
            NewBudSheet(api: api) { await load() }
        }
        .searchable(text: $search, placement: .navigationBarDrawer(displayMode: .automatic), prompt: "Find a chat")
        .task(id: phase) { await refreshWhileActive(phase, every: .seconds(12), load: load) }
        .refreshable { await load() }
    }

    private func matches(_ name: String) -> Bool {
        search.isEmpty || name.localizedCaseInsensitiveContains(search)
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

struct PrimaryBudStar: View {
    var body: some View {
        Image(systemName: "star.fill")
            .font(.system(size: 9, weight: .semibold))
            .foregroundStyle(BuddyStyle.accentText)
            .frame(width: 18, height: 18)
            .background(BuddyStyle.accent, in: Circle())
            .accessibilityLabel("Primary Bud")
    }
}

private struct ChatActivity: View {
    let active: ActiveReply
    let appearance: BudAppearance?
    let api: BuddyAPI
    let chat: String
    let onViewOutput: (String) -> Void

    private var progress: String? {
        guard active.status == "running" else { return nil }
        if let update = active.update?.trimmingCharacters(in: .whitespacesAndNewlines).nilIfEmpty { return update }
        switch active.activityKind {
        case "reading", "searching", "editing": return active.activity?.nilIfEmpty
        case "running" where active.activity?.split(separator: " · ", maxSplits: 1).count == 2: return active.activity
        default: return nil
        }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 5) {
            statusRow
            if let run = active.codeRun {
                let reference = run.runId.map { active.id + "/" + $0 } ?? active.id
                ChatCodeRunCard(run: run, api: api, chat: chat, task: reference, stopping: active.status == "cancelling") {
                    onViewOutput(reference)
                }
            }
        }
    }

    private var statusRow: some View {
        HStack(spacing: 8) {
            if active.status == "running" {
                BudFace(appearance: appearance, size: 37, mood: .activity(active.activityKind))
            } else {
                BudActivityDots()
            }
            if active.codeRun?.status != "running" {
                if let progress {
                    label(progress)
                } else {
                    switch (active.status, active.activityKind) {
                    case ("cancelling", _), (_, "stopping"):
                        label("Stopping…")
                    case ("queued", _):
                        label("Queued")
                    default:
                        RotatingReplyStatus(active: active)
                            .id(ReplyStage(id: active.id, status: active.status, kind: active.activityKind))
                    }
                }
            }
            Spacer(minLength: 0)
        }
        .padding(.horizontal, 5)
        .frame(minHeight: 32)
        .accessibilityElement(children: .combine)
    }

    private func label(_ text: String) -> some View {
        Text(text).font(.footnote).foregroundStyle(BuddyStyle.muted)
    }
}

private struct ReplyStage: Hashable {
    let id: String
    let status: String
    let kind: String?
}

private struct RotatingReplyStatus: View {
    let active: ActiveReply
    @State private var started = Date.now

    private static let catalog: [String: [String]] = {
        guard let url = Bundle.main.url(forResource: "status", withExtension: "json"),
              let data = try? Data(contentsOf: url),
              let copy = try? JSONDecoder().decode([String: [String]].self, from: data) else { return [:] }
        return copy
    }()

    var body: some View {
        TimelineView(.periodic(from: started, by: 5)) { timeline in
            let step = Int(max(0, timeline.date.timeIntervalSince(started)) / 5)
            Text(label(step: step))
                .font(.footnote)
                .foregroundStyle(BuddyStyle.muted)
        }
    }

    private func label(step: Int) -> String {
        let kind = active.activityKind ?? "thinking"
        let stage = Self.catalog[kind] == nil ? "thinking" : kind
        guard let labels = Self.catalog[stage], labels.count > 1 else { return "Working…" }
        let hash = (active.id + ":" + stage).utf8.reduce(UInt32(2166136261)) { ($0 ^ UInt32($1)) &* 16777619 }
        let value = Int(hash), count = labels.count
        var stride = value / count % (count - 1) + 1
        while gcd(stride, count) != 1 { stride += 1 }
        return labels[(value % count + step % count * stride) % count]
    }

    private func gcd(_ a: Int, _ b: Int) -> Int {
        b == 0 ? a : gcd(b, a % b)
    }
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
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var conversation: Conversation?
    @State private var older: [ChatMessage] = []
    @State private var cursor: Int64?
    @State private var loadingHistory = false
    @State private var loadingLatest = false
    @State private var nearHistory = false
    @State private var topMessage: Int64?
    @State private var historyError: String?
    @State private var lastRead: Int64 = 0
    @State private var draft = ""
    @State private var nearLatest = true
    @State private var busy = false
    @State private var stoppingCode: String?
    @State private var outputSelection: CodeOutputSelection?
    @State private var loadError: String?
    @State private var actionError: String?
    @State private var outgoing: Outgoing?
    @State private var reactionOverrides: [Int64: ReactionUpdate] = [:]
    @State private var reactionCursor: Int64?
    @State private var createdBuds: [String: String] = [:]
    @State private var backgroundAnswer: String?
    @State private var pickingFiles = false
    @State private var draftFiles: [ChatUpload] = []
    @FocusState private var composerFocused: Bool

    private struct Outgoing {
        let text: String
        let key: String
        let backgroundAnswer: String?
        let attachments: [ChatUpload]
    }

    private struct CodeOutputSelection: Identifiable {
        let id: String
    }

    private var messages: [ChatMessage] {
        let recent = conversation?.messages ?? []
        if let first = recent.first { return older.filter { $0.id < first.id } + recent }
        return older
    }

    private var historyPage: Int64? {
        if loadingHistory { return cursor }
        guard nearHistory, !nearLatest, !loadingLatest, historyError == nil else { return nil }
        return cursor
    }

    private var liveWork: [WorkItem] {
        (conversation?.work ?? []).filter {
            ["queued", "running", "cancelling", "needs_input"].contains($0.status) &&
            ($0.codeRun != nil || $0.projectId?.nilIfEmpty != nil)
        }
    }

    var body: some View {
        ScrollViewReader { proxy in
            ScrollView {
                VStack(spacing: 0) {
                    Color.clear
                        .frame(height: 1)
                        .onGeometryChange(for: Bool.self) { geometry in
                            geometry.frame(in: .scrollView).maxY >= -120
                        } action: { nearHistory = $0 }
                        .accessibilityHidden(true)
                    if loadingHistory {
                        ProgressView("Loading earlier messages…")
                            .font(.footnote)
                            .padding(.bottom, 10)
                    } else if let historyError, let before = cursor {
                        HStack {
                            ConnectionError(message: historyError)
                            Button("Retry") { Task { await loadEarlier(before: before) } }
                                .disabled(loadingLatest)
                        }
                        .padding(.bottom, 10)
                    }
                    LazyVStack(spacing: 14) {
                        ForEach(messages) { message in
                            MessageBubble(message: message, chat: id, api: api,
                                          showAuthor: showsAuthor(for: message), busy: busy || conversation?.archived == true,
                                          reactions: reactions(for: message),
                                          reactionOptions: conversation?.archived == true ? [] : conversation?.reactionOptions ?? [],
                                          onReaction: { emoji in
                                              perform {
                                                  let update = try await api.setReaction(chat: id, message: message.id, emoji: emoji)
                                                  recordReaction(update)
                                              }
                                          },
                                          onChoice: { index in perform { try await api.answerCard(chat: id, card: message.taskId, choice: index) } },
                                          onBackgroundReply: isOpenBackgroundQuestion(message) ? {
                                              backgroundAnswer = message.taskId
                                              composerFocused = true
                                          } : nil,
                                          onActivate: { perform { try await api.activateWatch(chat: id, task: message.taskId) } },
                                          onViewOutput: showCodeOutput,
                                          createdBud: createdBuds[message.taskId],
                                          onCreateBud: { approval in
                                              perform {
                                                  let bud = try await api.createProposedBud(chat: id, task: message.taskId, approval: approval)
                                                  createdBuds[message.taskId] = bud.conversationId
                                              }
                                          })
                                .id(message.id)
                        }
                    }
                    .scrollTargetLayout()
                    ForEach(liveWork) { item in
                        VStack(alignment: .leading, spacing: 5) {
                            Text(item.title).font(.subheadline.weight(.semibold))
                            Text([statusLabel(item.status), item.device?.nilIfEmpty ?? "Buddy server", item.projectName?.nilIfEmpty]
                                .compactMap { $0 }.joined(separator: " · "))
                                .font(.caption).foregroundStyle(BuddyStyle.muted)
                            if let update = item.update?.nilIfEmpty {
                                Text(update).font(.footnote).foregroundStyle(BuddyStyle.muted).lineLimit(2)
                            }
                            if let run = item.codeRun {
                                let reference = run.runId.map { item.id + "/" + $0 } ?? item.id
                                ChatCodeRunCard(run: run, api: api, chat: id, task: reference, stopping: item.status == "cancelling") {
                                    showCodeOutput(reference)
                                }
                            }
                        }
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(.top, 14)
                    }
                    if let active = conversation?.active {
                        ChatActivity(active: active, appearance: activityAppearance(active), api: api, chat: id,
                                     onViewOutput: showCodeOutput)
                            .padding(.top, 14)
                    }
                    Color.clear
                        .frame(height: 1)
                        .id("chat-bottom")
                        .onGeometryChange(for: Bool.self) { geometry in
                            guard let viewport = geometry.bounds(of: .scrollView) else { return true }
                            return geometry.frame(in: .scrollView).maxY <= viewport.height + 90
                        } action: { nearLatest = $0 }
                        .accessibilityHidden(true)
                }
                .frame(maxWidth: 660)
                .padding(.horizontal, 16)
                .padding(.vertical, 14)
                .frame(maxWidth: .infinity)
            }
            .scrollPosition(id: $topMessage, anchor: .top)
            .defaultScrollAnchor(.bottom)
            .task(id: historyPage) {
                if let before = historyPage { await loadEarlier(before: before) }
            }
            .onChange(of: messages.last?.id) { previous, _ in
                if previous == nil || nearLatest || messages.last?.role == "user" {
                    scrollToBottom(proxy)
                }
            }
            .onChange(of: conversation?.active?.id) { _, id in
                if id != nil && nearLatest { scrollToBottom(proxy) }
            }
            .onChange(of: conversation?.active?.codeRun) { _, run in
                if run != nil && nearLatest { proxy.scrollTo("chat-bottom", anchor: .bottom) }
            }
            .overlay {
                if messages.isEmpty, let conversation, conversation.active == nil, conversation.archived != true {
                    VStack(spacing: 12) {
                        BudFace(appearance: conversation.agent.avatar, size: 114, mood: .idle)
                        Text(chatTitle).font(.title2.weight(.semibold))
                    }
                    .padding(20)
                    .allowsHitTesting(false)
                }
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
                    VStack(spacing: 7) {
                        if !nearLatest && !messages.isEmpty {
                            Button { scrollToBottom(proxy) } label: {
                                Image(systemName: "chevron.down")
                                    .font(.system(size: 15, weight: .semibold))
                                    .frame(width: 44, height: 44)
                                    .background(.regularMaterial, in: Circle())
                            }
                            .buttonStyle(.plain)
                            .accessibilityLabel("Jump to latest")
                        }
                        composer
                    }
                }
            }
        }
        .buddyPage()
        .sheet(item: $outputSelection) { selection in
            ChatCodeOutputView(api: api, chat: id, task: selection.id)
        }
        .fileImporter(isPresented: $pickingFiles, allowedContentTypes: [.data], allowsMultipleSelection: true) {
            importFiles($0)
        }
        .navigationTitle(chatTitle)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(.hidden, for: .tabBar)
        .toolbar {
            ToolbarItem(placement: .principal) {
                NavigationLink(value: Destination.chatInfo(id, chatTitle)) {
                    HStack(spacing: 6) {
                        if isGroup {
                            Image(systemName: "person.2").font(.caption).foregroundStyle(BuddyStyle.muted)
                        } else {
                            BudFace(appearance: conversation?.agent.avatar, size: 25)
                        }
                        VStack(alignment: .leading, spacing: 1) {
                            Text(chatTitle).font(.subheadline.weight(.semibold)).lineLimit(1)
                            if let role = conversation?.agent.displayRole {
                                Text(role).font(.caption2).foregroundStyle(BuddyStyle.muted).lineLimit(1)
                            }
                        }
                    }
                    .padding(.horizontal, 11)
                    .frame(minHeight: 38)
                    .background(BuddyStyle.raised, in: Capsule())
                }
                .buttonStyle(.plain)
                .accessibilityLabel("\(chatTitle), conversation details")
                .accessibilityHint(conversation?.agent.displayRole ?? "")
                .disabled(conversation == nil)
            }
            ToolbarItem(placement: .topBarTrailing) {
                Menu {
                    Button {
                        if let conversation { voice.start(api: api, conversation: conversation, chat: id) }
                    } label: {
                        Label(voice.active ? "Return to call" : "Start voice call", systemImage: "waveform")
                    }
                    .disabled(conversation == nil || conversation?.archived == true)
                } label: {
                    Label("Chat actions", systemImage: "ellipsis")
                }
            }
        }
        .task(id: phase) {
            guard phase == .active else { return }
            while !Task.isCancelled {
                await load()
                let interval: Duration = if conversation?.active != nil {
                    .milliseconds(500)
                } else if conversation?.work?.contains(where: { ["queued", "running", "cancelling"].contains($0.status) }) == true {
                    .seconds(1)
                } else {
                    .seconds(3)
                }
                do { try await Task.sleep(for: interval) }
                catch { return }
            }
        }
        .refreshable { await load() }
    }

    private var composer: some View {
        VStack(alignment: .leading, spacing: 7) {
            if let message = actionError ?? (conversation == nil ? nil : loadError) {
                ConnectionError(message: message)
            }
            if backgroundAnswer != nil {
                HStack(spacing: 4) {
                    Text("Replying to background work").font(.footnote).foregroundStyle(.secondary)
                    Button {
                        backgroundAnswer = nil
                    } label: {
                        Image(systemName: "xmark").font(.caption.weight(.semibold)).frame(width: 44, height: 44)
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("Clear reply")
                    .disabled(busy)
                }
            }
            ForEach(draftFiles.indices, id: \.self) { index in
                HStack {
                    Label(draftFiles[index].name, systemImage: "doc").lineLimit(1)
                    Spacer(minLength: 0)
                    Button {
                        draftFiles.remove(at: index)
                    } label: {
                        Image(systemName: "xmark").frame(width: 44, height: 44)
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("Remove \(draftFiles[index].name)")
                    .disabled(busy)
                }
                .font(.footnote)
            }
            HStack(alignment: .bottom, spacing: 8) {
                Button { pickingFiles = true } label: {
                    Image(systemName: "plus").frame(width: 44, height: 44)
                }
                .buttonStyle(.plain)
                .foregroundStyle(BuddyStyle.muted)
                .accessibilityLabel("Attach files")
                .accessibilityIdentifier("chat-attach")
                .disabled(busy || conversation == nil || draftFiles.count >= 5)
                TextField(composerPlaceholder, text: $draft, axis: .vertical)
                    .lineLimit(1...5)
                    .textFieldStyle(.plain)
                    .padding(.horizontal, 10)
                    .padding(.vertical, 12)
                    .accessibilityIdentifier("chat-composer")
                    .focused($composerFocused)
                    .disabled(busy)
                if let active = conversation?.active, active.codeRun?.status == "running" {
                    let stopping = active.status == "cancelling" || stoppingCode == (active.codeRun?.runId ?? active.id)
                    Button {
                        stopCodeRun(active.id, attempt: active.codeRun?.runId)
                    } label: {
                        if showsVoiceAction {
                            composerAction("stop.fill")
                        } else {
                            Image(systemName: "stop.fill")
                                .font(.system(size: 14, weight: .semibold))
                                .foregroundStyle(BuddyStyle.muted)
                                .frame(width: 36, height: 44)
                                .contentShape(Rectangle())
                        }
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel(stopping ? "Stopping code run" : "Stop code run")
                    .disabled(stopping)
                } else if showsVoiceAction {
                    Button {
                        if let conversation { voice.start(api: api, conversation: conversation, chat: id) }
                    } label: {
                        composerAction("waveform")
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel(voice.active ? "Return to voice call" : "Start voice call")
                    .disabled(conversation == nil || conversation?.archived == true)
                }
                if !showsVoiceAction {
                    Button(action: send) { composerAction("arrow.up") }
                        .buttonStyle(.plain)
                        .accessibilityLabel("Send")
                        .disabled(!canSend)
                }
            }
            .padding(3)
            .background(BuddyStyle.surface, in: RoundedRectangle(cornerRadius: 26))
            .overlay(RoundedRectangle(cornerRadius: 26).strokeBorder(BuddyStyle.border))
        }
        .frame(maxWidth: 660)
        .padding(.horizontal, 16)
        .padding(.vertical, 8)
        .frame(maxWidth: .infinity)
        .background(.regularMaterial)
    }

    private func composerAction(_ symbol: String) -> some View {
        Image(systemName: symbol)
            .font(.system(size: 18, weight: .semibold))
            .foregroundStyle(BuddyStyle.accentText)
            .frame(width: 36, height: 36)
            .background(BuddyStyle.accent, in: Circle())
            .frame(width: 44, height: 44)
            .contentShape(Rectangle())
    }

    private var chatTitle: String { conversation?.name?.nilIfEmpty ?? conversation?.agent.name ?? name }
    private var isGroup: Bool { conversation?.name?.nilIfEmpty != nil }

    private var showsVoiceAction: Bool {
        backgroundAnswer == nil && draftFiles.isEmpty && draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    private func scrollToBottom(_ proxy: ScrollViewProxy) {
        withAnimation(reduceMotion ? nil : .easeOut(duration: 0.22)) { proxy.scrollTo("chat-bottom", anchor: .bottom) }
    }

    private func showCodeOutput(_ task: String) {
        outputSelection = CodeOutputSelection(id: task)
    }

    private func stopCodeRun(_ task: String, attempt: String?) {
        stoppingCode = attempt ?? task
        actionError = nil
        Task {
            do {
                try await api.stopCodeRun(chat: id, run: task, attempt: attempt)
                await load()
                stoppingCode = nil
            } catch {
                stoppingCode = nil
                actionError = error.localizedDescription
            }
        }
    }

    private var composerPlaceholder: String {
        if backgroundAnswer != nil { return "Reply…" }
        return isGroup ? "Message the group" : "Message \(chatTitle)"
    }

    private var canSend: Bool {
        !busy && conversation != nil &&
            (!draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || !draftFiles.isEmpty)
    }

    private func importFiles(_ selection: Result<[URL], Error>) {
        guard !busy, let conversation, conversation.archived != true else { return }
        do {
            let urls = try selection.get()
            guard !urls.isEmpty else { return }
            guard draftFiles.count + urls.count <= 5 else {
                throw BuddyError("Attach up to five files.")
            }
            let remaining = (8 << 20) - draftFiles.reduce(0) { $0 + $1.data.count }
            busy = true
            actionError = nil
            Task {
                defer { busy = false }
                do {
                    draftFiles += try await Task.detached {
                        try ChatUpload.read(urls, remainingBytes: remaining)
                    }.value
                } catch {
                    actionError = error.localizedDescription
                }
            }
        } catch {
            actionError = error.localizedDescription
        }
    }

    private func showsAuthor(for message: ChatMessage) -> Bool {
        isGroup || message.origin == "voice" || (message.agentId != nil && message.agentId != conversation?.agent.id)
    }

    private func activityAppearance(_ active: ActiveReply) -> BudAppearance? {
        guard let agent = active.agentId else { return conversation?.agent.avatar }
        return conversation?.profiles?.first(where: { $0.id == agent })?.avatar
            ?? conversation?.members?.first(where: { $0.id == agent })?.avatar
            ?? conversation?.agent.avatar
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
                historyError = nil
                reactionOverrides = [:]
            }
            if let first = snapshot.messages?.first {
                let known = older.last?.id ?? 0
                // Bridge pages are returned newest page first. Store the
                // contiguous history in display order before exposing the live page.
                older += (recent + bridge).filter { $0.id > known && $0.id < first.id }.sorted { $0.id < $1.id }
                if let updates = snapshot.earlierUpdates, !updates.isEmpty {
                    let byID = Dictionary(uniqueKeysWithValues: updates.map { ($0.id, $0) })
                    older = older.map { byID[$0.id] ?? $0 }
                }
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
        guard !loadingHistory, !loadingLatest, cursor == before else { return }
        loadingHistory = true
        defer { loadingHistory = false }
        do {
            let page = try await api.chat(id, before: before)
            try Task.checkCancellation()
            guard (page.transcriptRevision ?? 0) == (conversation?.transcriptRevision ?? 0) else { return }
            let first = messages.first?.id ?? Int64.max
            older.insert(contentsOf: (page.messages ?? []).filter { $0.id < first }, at: 0)
            cursor = page.before
            if let revision = page.reactionRevision, let reactionCursor, revision < reactionCursor {
                self.reactionCursor = revision
            }
            historyError = nil
        } catch where !Task.isCancelled { historyError = error.localizedDescription } catch {}
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
        guard canSend else { return }
        let retry = outgoing.flatMap {
            $0.text == text && $0.backgroundAnswer == backgroundAnswer &&
                $0.attachments == draftFiles ? $0 : nil
        }
        let message = retry ?? Outgoing(text: text, key: UUID().uuidString,
                                        backgroundAnswer: backgroundAnswer, attachments: draftFiles)
        outgoing = message
        perform {
            try await api.send(chat: id, text: message.text,
                               backgroundAnswer: message.backgroundAnswer, attachments: message.attachments, key: message.key)
            backgroundAnswer = nil
            draftFiles = []
            draft = ""
            outgoing = nil
        }
    }

    private func isOpenBackgroundQuestion(_ message: ChatMessage) -> Bool {
        guard message.role == "assistant" else { return false }
        return conversation?.openBackgroundQuestions?.contains(message.taskId) ?? (message.backgroundQuestion == true)
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
    let showAuthor: Bool
    let busy: Bool
    let reactions: [MessageReaction]
    let reactionOptions: [ReactionOption]
    let onReaction: (String) -> Void
    let onChoice: (Int) -> Void
    let onBackgroundReply: (() -> Void)?
    let onActivate: () -> Void
    let onViewOutput: (String) -> Void
    let createdBud: String?
    let onCreateBud: (BuddyAPI.BudApproval?) -> Void

    private var outgoing: Bool { message.role == "user" }

    var body: some View {
        HStack(alignment: .top, spacing: 9) {
            if outgoing { Spacer(minLength: 24) }
            VStack(alignment: .leading, spacing: 8) {
                if let name = message.agentName?.nilIfEmpty, !outgoing && showAuthor {
                    Text(name).font(.caption.bold()).foregroundStyle(.secondary)
                }
                let runs = message.codeRuns ?? message.codeRun.map { [$0] } ?? []
                ForEach(runs.indices, id: \.self) { index in
                    let run = runs[index]
                    let reference = run.runId.map { message.taskId + "/" + $0 } ?? message.taskId
                    ChatCodeRunCard(run: run, api: api, chat: chat, task: reference) {
                        onViewOutput(reference)
                    }
                }
                if !message.text.isEmpty { RichText(message.text, bud: message.agentId, task: message.taskId) }
                if message.generatedImage == true {
                    GeneratedChatImage(api: api, task: message.taskId)
                }
                if let card = message.card { choice(card) }
                if let bud = message.budProposal { budCard(bud) }
                if let onBackgroundReply {
                    Button("Reply", action: onBackgroundReply).buttonStyle(.bordered).disabled(busy)
                }
                ForEach([message.connectionCard, message.watchCard].compactMap { $0 }.indices, id: \.self) { index in
                    let card = [message.connectionCard, message.watchCard].compactMap { $0 }[index]
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
            .contextMenu {
                if !outgoing && !reactionOptions.isEmpty { reactionActions }
            }
            if !outgoing { Spacer(minLength: 18) }
        }
    }

    private func budCard(_ bud: BudProposal) -> some View {
        let destination = createdBud ?? bud.conversationId?.nilIfEmpty
        return VStack(alignment: .leading, spacing: 9) {
            Label(bud.name, systemImage: "person.crop.circle").font(.headline)
            Text(bud.role).font(.subheadline).foregroundStyle(.secondary)
            Text(bud.instructions).font(.callout)
            if let destination {
                NavigationLink(value: Destination.chat(destination, bud.name)) {
                    Label("Open chat", systemImage: "arrow.up.right")
                }
                .buttonStyle(.bordered)
            } else if !(bud.needsRevision ?? (message.card != nil)) {
                let work = bud.pendingWork ?? []
                ForEach(work.indices, id: \.self) { index in
                    Label(work[index], systemImage: "briefcase").font(.footnote).foregroundStyle(.secondary)
                }
                Button(work.isEmpty ? "Create Bud" : "Create Bud and start work") {
                    onCreateBud(work.isEmpty ? nil : .startWork)
                }
                    .buttonStyle(.borderedProminent)
                    .disabled(busy)
                if !work.isEmpty {
                    Button("Create Bud only") { onCreateBud(.createOnly) }
                        .buttonStyle(.bordered)
                        .disabled(busy)
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(13)
        .background(BuddyStyle.raised, in: RoundedRectangle(cornerRadius: 13))
    }

    @ViewBuilder
    private var reactionActions: some View {
        ForEach(reactionOptions) { option in
            let selected = reactions.contains { $0.mine && $0.emoji == option.emoji }
            Button { onReaction(selected ? "" : option.emoji) } label: {
                if selected {
                    Label("\(option.emoji) \(option.label)", systemImage: "checkmark")
                } else {
                    Text("\(option.emoji) \(option.label)")
                }
            }
            .disabled(busy)
        }
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
