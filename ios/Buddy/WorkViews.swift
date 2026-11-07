import SwiftUI

struct InboxList: View {
    let api: BuddyAPI
    @Environment(\.scenePhase) private var phase
    @State private var items: [InboxItem]?
    @State private var error: String?

    var body: some View {
        List {
            if let items {
                ForEach(items) { item in
                    NavigationLink(value: Destination.inbox(item.id)) {
                        VStack(alignment: .leading, spacing: 5) {
                            HStack {
                                Text(item.signal.title).font(.headline)
                                Spacer(minLength: 0)
                                if !item.seen { Circle().fill(BuddyStyle.accent).frame(width: 8, height: 8).accessibilityLabel("Unread") }
                            }
                            Text(item.watch).font(.subheadline).foregroundStyle(.secondary)
                            Label(statusLabel(item.state), systemImage: item.state == "approval" ? "questionmark.circle" : "tray")
                                .font(.caption).foregroundStyle(.secondary)
                        }
                        .padding(.vertical, 4)
                    }
                    .listRowBackground(BuddyStyle.surface)
                }
                if items.isEmpty {
                    ContentUnavailableView("Inbox is clear", systemImage: "tray",
                                           description: Text("Watch findings and investigation requests will appear here."))
                }
            }
            if let error { ConnectionError(message: error) }
        }
        .overlay { if items == nil && error == nil { ProgressView("Loading inbox…") } }
        .buddyPage()
        .navigationTitle("Inbox")
        .task(id: phase) { await refreshWhileActive(phase, load: load) }
        .refreshable { await load() }
    }

    private func load() async {
        do {
            items = try await api.inbox()
            error = nil
        } catch where !Task.isCancelled { self.error = error.localizedDescription } catch {}
    }
}

struct InboxDetail: View {
    let api: BuddyAPI
    let id: String
    @Environment(\.dismiss) private var dismiss
    @Environment(\.scenePhase) private var phase
    @State private var item: InboxItem?
    @State private var loadError: String?
    @State private var actionError: String?
    @State private var saving = false

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 18) {
                if let item {
                    VStack(alignment: .leading, spacing: 6) {
                        Text(item.watch).font(.caption.bold()).foregroundStyle(BuddyStyle.accent)
                        Text(item.signal.title).font(.title.bold())
                        Text(item.at, format: .dateTime.month().day().hour().minute())
                            .font(.subheadline).foregroundStyle(.secondary)
                        Text(statusLabel(item.state)).font(.subheadline.bold())
                    }
                    if let detail = item.signal.detail?.nilIfEmpty { RichText(detail) }
                    if let result = item.result?.nilIfEmpty { RichText(result) }
                    if let url = safeWebURL(item.signal.url) { Link("Open source", destination: url) }
                    if item.state == "approval" {
                        Button("Approve investigation") { decide("approve") }.buttonStyle(.borderedProminent)
                        Button("Dismiss", role: .destructive) { decide("dismiss") }.buttonStyle(.bordered)
                    }
                    if let task = item.taskId?.nilIfEmpty, !["notified", "overflow"].contains(item.state) {
                        NavigationLink(value: Destination.work(item.conversationId, task)) {
                            Label(item.inputId == nil ? "View investigation" : "Answer in Work", systemImage: "briefcase")
                        }
                    }
                    NavigationLink(value: Destination.chat(item.conversationId, item.bud)) {
                        Label("Open chat", systemImage: "bubble.left")
                    }
                }
                if let message = actionError ?? loadError { ConnectionError(message: message) }
            }
            .disabled(saving)
            .padding(20)
            .frame(maxWidth: 700, alignment: .leading)
        }
        .overlay { if item == nil && loadError == nil { ProgressView("Loading update…") } }
        .buddyPage()
        .navigationTitle("Inbox update")
        .navigationBarTitleDisplayMode(.inline)
        .task(id: phase) { await refreshWhileActive(phase, load: load) }
        .refreshable { await load() }
    }

    private func load() async {
        do {
            let updated = try await api.inboxItem(id)
            try Task.checkCancellation()
            item = updated
            loadError = nil
            if !updated.seen && updated.state != "approval" {
                try await api.decideInbox(id: id, decision: "read")
            }
        } catch where !Task.isCancelled { loadError = error.localizedDescription } catch {}
    }

    private func decide(_ decision: String) {
        guard !saving else { return }
        saving = true
        actionError = nil
        Task {
            defer { saving = false }
            do {
                try await api.decideInbox(id: id, decision: decision)
                if decision == "dismiss" { dismiss() } else { await load() }
            } catch { actionError = error.localizedDescription }
        }
    }
}

struct WorkList: View {
    let api: BuddyAPI
    @Environment(\.scenePhase) private var phase
    @State private var items: [WorkItem]?
    @State private var history: [WorkItem] = []
    @State private var next: String?
    @State private var loadedHistory = false
    @State private var loadingHistory = false
    @State private var error: String?

    private let activeStatuses = ["queued", "running", "needs_input", "cancelling"]

    var body: some View {
        List {
            if let items {
                let active = items.filter { activeStatuses.contains($0.status) }
                let recent = items.filter { !activeStatuses.contains($0.status) }
                let seen = Set(items.map(\.id))
                if !active.isEmpty {
                    Section("Active") { ForEach(active, content: row) }
                }
                Section("Recent") {
                    ForEach(recent + history.filter { !seen.contains($0.id) }, content: row)
                    if !loadedHistory || next != nil {
                        Button(loadingHistory ? "Loading…" : "Load older jobs") {
                            loadingHistory = true
                            Task { await loadHistory() }
                        }
                        .disabled(loadingHistory)
                        .listRowBackground(BuddyStyle.surface)
                    }
                }
                if items.isEmpty && history.isEmpty {
                    ContentUnavailableView("No jobs yet", systemImage: "briefcase",
                                           description: Text("Work your Buds take on will show up here."))
                }
            }
            if let error { ConnectionError(message: error) }
        }
        .overlay { if items == nil && error == nil { ProgressView("Loading work…") } }
        .buddyPage()
        .navigationTitle("Work")
        .task(id: phase) { await refreshWhileActive(phase, load: load) }
        .refreshable { await load() }
    }

    private func row(_ item: WorkItem) -> some View {
        NavigationLink(value: Destination.work(item.conversationId, item.id)) {
            VStack(alignment: .leading, spacing: 5) {
                Text(item.title).font(.headline)
                HStack(spacing: 7) {
                    if item.status == "running" { ProgressView().controlSize(.small) }
                    Text(statusLabel(item.status))
                    Text("·")
                    Text(item.agentName)
                }
                .font(.subheadline)
                .foregroundStyle(.secondary)
            }
            .padding(.vertical, 4)
        }
        .listRowBackground(BuddyStyle.surface)
    }

    private func load() async {
        do {
            items = try await api.work()
            error = nil
        } catch where !Task.isCancelled { self.error = error.localizedDescription } catch {}
    }

    private func loadHistory() async {
        defer { loadingHistory = false }
        do {
            var cursor = loadedHistory ? next : nil
            let known = Set((items ?? []).map(\.id) + history.map(\.id))
            repeat {
                let page = try await api.workHistory(before: cursor)
                let extra = (page.items ?? []).filter { !known.contains($0.id) }
                history += extra
                next = page.next
                loadedHistory = true
                if !extra.isEmpty || page.next == cursor { break }
                cursor = page.next
            } while cursor != nil && !Task.isCancelled
            error = nil
        } catch { self.error = error.localizedDescription }
    }
}

struct WorkDetail: View {
    let api: BuddyAPI
    let chat: String
    let id: String
    @Environment(\.scenePhase) private var phase
    @State private var detail: WorkDetails?
    @State private var answer = ""
    @State private var asking = ""
    @State private var request: ReplyRequest?
    @State private var loadError: String?
    @State private var actionError: String?
    @State private var busy = false

    private struct ReplyRequest {
        let text: String
        let input: String?
        let key: String
    }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 20) {
                if let detail {
                    VStack(alignment: .leading, spacing: 6) {
                        Text(detail.item.title).font(.title.bold())
                        Text("\(statusLabel(detail.item.status)) · \(detail.item.agentName)")
                            .foregroundStyle(.secondary)
                        NavigationLink(value: Destination.chat(chat, detail.item.conversationName)) {
                            Label("Open chat", systemImage: "bubble.left")
                        }
                        .font(.callout)
                    }
                    if let input = detail.item.inputId?.nilIfEmpty, detail.item.status == "needs_input" {
                        VStack(alignment: .leading, spacing: 9) {
                            Text("Your Bud needs input").font(.headline)
                            if let question = detail.question { RichText(question) }
                            replyField("Your answer", text: $answer, button: "Send answer", input: input)
                        }
                        .padding(14)
                        .background(.thinMaterial, in: RoundedRectangle(cornerRadius: 14))
                    }
                    if let result = detail.result?.nilIfEmpty { section("Result", result) }
                    if let update = detail.item.update?.nilIfEmpty, detail.item.status == "running" {
                        section("Latest update", update)
                    }
                    section("Assignment", detail.brief)
                    if let events = detail.events, !events.isEmpty {
                        DisclosureGroup("Activity") {
                            ForEach(events) { event in
                                VStack(alignment: .leading, spacing: 5) {
                                    Text(event.at, style: .time).font(.caption).foregroundStyle(.secondary)
                                    RichText(event.text)
                                }
                                .padding(.vertical, 6)
                            }
                        }
                    }
                    VStack(alignment: .leading, spacing: 8) {
                        Text("Ask about this job").font(.headline)
                        replyField("Question for your Bud", text: $asking, button: "Ask in chat", input: nil)
                    }
                }
                if let message = actionError ?? loadError { ConnectionError(message: message) }
            }
            .padding(20)
            .frame(maxWidth: 700, alignment: .leading)
        }
        .overlay { if detail == nil && loadError == nil { ProgressView("Loading job…") } }
        .buddyPage()
        .navigationTitle("Job details")
        .navigationBarTitleDisplayMode(.inline)
        .task(id: phase) { await refreshWhileActive(phase, load: load) }
        .refreshable { await load() }
    }

    private func section(_ title: String, _ body: String) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(title).font(.headline)
            RichText(body)
        }
    }

    private func replyField(_ prompt: String, text: Binding<String>, button: String, input: String?) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            TextField(prompt, text: text, axis: .vertical)
                .lineLimit(2...6)
                .textFieldStyle(.roundedBorder)
            Button(button) { send(text, input: input) }
                .buttonStyle(.borderedProminent)
                .disabled(text.wrappedValue.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
        }
        .disabled(busy)
    }

    private func load() async {
        do {
            detail = try await api.workDetails(chat: chat, id: id)
            loadError = nil
        } catch where !Task.isCancelled { loadError = error.localizedDescription } catch {}
    }

    private func send(_ field: Binding<String>, input: String?) {
        guard !busy else { return }
        let text = field.wrappedValue.trimmingCharacters(in: .whitespacesAndNewlines)
        let retry = request.flatMap { $0.text == text && $0.input == input ? $0 : nil }
        let reply = retry ?? ReplyRequest(text: text, input: input, key: UUID().uuidString)
        request = reply
        busy = true
        actionError = nil
        Task {
            defer { busy = false }
            do {
                if let input {
                    try await api.answerWork(chat: chat, input: input, text: text, key: reply.key)
                } else {
                    try await api.send(chat: chat, text: text, job: id, key: reply.key)
                }
                field.wrappedValue = ""
                request = nil
                await load()
            } catch { actionError = error.localizedDescription }
        }
    }
}
