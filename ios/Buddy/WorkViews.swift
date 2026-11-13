import SwiftUI
import UniformTypeIdentifiers

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

    @State private var search = ""
 @State private var owner = ""
 @State private var project = ""
 @State private var host = ""

    private let activeStatuses = ["queued", "running", "needs_input", "cancelling"]

    var body: some View {
        List {
            if let items {
                let filtered = items.filter(matches)
                let needs = filtered.filter { $0.status == "needs_input" }
                let active = filtered.filter { activeStatuses.contains($0.status) && $0.status != "needs_input" }
                Section {
                    HStack { summary("Needs you", needs.count); Spacer(); summary("Active", active.count); Spacer(); summary("Recent", filtered.count-needs.count-active.count) }
                    Menu("Filter team and projects") {
                        Picker("Bud", selection: $owner) { Text("All Buds").tag(""); ForEach(Array(Set(items.map(\.agentName))).sorted(), id: \.self) { Text($0).tag($0) } }
                        Picker("Project", selection: $project) { Text("All projects").tag(""); ForEach(Array(Set(items.compactMap(\.projectName))).sorted(), id: \.self) { Text($0).tag($0) } }
                        Picker("Host", selection: $host) { Text("All hosts").tag(""); ForEach(Array(Set(items.map { $0.device?.nilIfEmpty ?? "Buddy server" })).sorted(), id: \.self) { Text($0).tag($0) } }
                    }
                }.listRowBackground(BuddyStyle.surface)
                if !needs.isEmpty { Section("Needs you") { ForEach(needs, content: row) } }
                let recent = filtered.filter { !activeStatuses.contains($0.status) }
                let seen = Set(items.map(\.id))
                if !active.isEmpty {
                    Section("Active") { ForEach(active, content: row) }
                }
                Section("Recent") {
                    ForEach(recent + history.filter { !seen.contains($0.id) && matches($0) }, content: row)
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
 .searchable(text: $search, prompt: "Assignments, Buds, projects")
        .task(id: phase) { await refreshWhileActive(phase, load: load) }
        .refreshable { await load() }
    }

    private func matches(_ item: WorkItem) -> Bool {
        (owner.isEmpty || item.agentName == owner) && (project.isEmpty || item.projectName == project) &&
        (host.isEmpty || (item.device?.nilIfEmpty ?? "Buddy server") == host) &&
        (search.isEmpty || "\(item.title) \(item.agentName) \(item.projectName ?? "")".localizedCaseInsensitiveContains(search))
    }
    private func summary(_ label: String, _ count: Int) -> some View {
        VStack(alignment: .leading, spacing: 5) { Text(count, format: .number).font(.title2.bold()); Text(label).font(.caption).foregroundStyle(.secondary) }
    }

    private func row(_ item: WorkItem) -> some View {
        NavigationLink(value: Destination.work(item.conversationId, item.id)) {
            VStack(alignment: .leading, spacing: 5) {
                Text(item.title).font(.headline)
 Text("\(item.agentName) · \((item.workerRole ?? "general").capitalized) worker").font(.subheadline.weight(.medium))
 if let project = item.projectName?.nilIfEmpty { Label(project, systemImage: "folder").font(.caption).foregroundStyle(.secondary) }
                HStack(spacing: 7) {
                    if item.status == "running" { ProgressView().controlSize(.small) }
                    Text(statusLabel(item.status))
                    Text("·")
                    Text(item.device?.nilIfEmpty ?? "Buddy server")
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
    @State private var frame: ExecutionFrame?
    @State private var confirmStop = false
    private var currentChat: String { detail?.item.conversationId ?? chat }

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
                        NavigationLink(value: Destination.chat(currentChat, detail.item.conversationName)) {
                            Label("Open chat", systemImage: "bubble.left")
                        }
                        .font(.callout)
                        if ["queued", "running", "needs_input"].contains(detail.item.status) {
                            Button("Stop job", role: .destructive) { confirmStop = true }
                                .buttonStyle(.bordered)
                                .disabled(busy)
                        }
                    }
                    if let project = detail.item.projectName?.nilIfEmpty { Label(project, systemImage: "folder").font(.headline) }
                    if let sessions = detail.sessions, !sessions.isEmpty {
                        VStack(alignment: .leading, spacing: 10) {
                            Text("Worker sessions").font(.headline)
                            ForEach(sessions) { session in
                                VStack(alignment: .leading, spacing: 4) {
                                    Text("\((detail.item.workerRole ?? "general").capitalized) · \(session.host) · \(statusLabel(session.status))").font(.subheadline.bold())
                                    if let branch = session.branch { Text(branch).font(.caption.monospaced()).textSelection(.enabled) }
                                    Text(session.workspace).font(.caption).foregroundStyle(.secondary).textSelection(.enabled)
                                }
                            }
                        }
                    }
                    if let dependencies = detail.dependencies { ForEach(dependencies) { dependency in
                        NavigationLink(value: Destination.work(dependency.conversationId, dependency.id)) { Label("\(dependency.budName): \(dependency.title) · \(statusLabel(dependency.status))", systemImage: "arrow.triangle.branch") }
                    } }
                    if let children = detail.delegated, !children.isEmpty {
                        VStack(alignment: .leading, spacing: 9) {
                            Text("Delegated work").font(.headline)
                            ForEach(children) { child in
                                NavigationLink(value: Destination.work(child.conversationId, child.id)) {
                                    Label("\(child.budName): \(child.title) · \(statusLabel(child.status))", systemImage: "arrow.triangle.branch")
                                }
                            }
                        }
                    }
                    if let artifacts = detail.artifacts, !artifacts.isEmpty {
                        VStack(alignment: .leading, spacing: 9) { Text("Deliverables").font(.headline)
                            ForEach(artifacts) { artifact in NavigationLink(value: Destination.artifact(id, artifact.id)) { Label(artifact.name, systemImage: "doc") } }
                        }
                    }
                    if detail.item.requirements?.contains(where: { ["browser", "desktop"].contains($0) }) == true {
                        VStack(alignment: .leading, spacing: 9) {
                            Text("Automation view").font(.headline)
                            if let data=frame?.data, let image=UIImage(data:data) { Image(uiImage:image).resizable().scaledToFit().clipShape(RoundedRectangle(cornerRadius:10)) }
                            else { Text("Screenshots appear as the worker observes its environment.").foregroundStyle(.secondary) }
                            if detail.item.status == "running" { Button(frame?.paused == true ? "Resume automation" : "Take control") { Task { do {try await api.pauseAutomation(job:id,paused:frame?.paused != true); await load()} catch {actionError=error.localizedDescription} } }.buttonStyle(.bordered) }
                        }
                    }
                    if let input = detail.item.inputId?.nilIfEmpty, detail.item.status == "needs_input" {
                        VStack(alignment: .leading, spacing: 9) {
                            Text("Your Bud needs input").font(.headline)
                            if let question = detail.question { RichText(question, bud: detail.item.agentId, task: detail.item.id) }
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
                                    RichText(event.text, bud: detail.item.agentId, task: detail.item.id)
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
        .confirmationDialog("Stop this job?", isPresented: $confirmStop, titleVisibility: .visible) {
            Button("Stop job", role: .destructive, action: stop)
        }
    }

    private func section(_ title: String, _ body: String) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(title).font(.headline)
            RichText(body, bud: detail?.item.agentId, task: id)
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
            detail = try await api.referencedWorkDetails(chat: chat, id: id)
            if detail?.item.requirements?.contains(where: { ["browser", "desktop"].contains($0) }) == true { frame = try? await api.executionFrame(job:id) }
            loadError = nil
        } catch where !Task.isCancelled { loadError = error.localizedDescription } catch {}
    }

    private func stop() {
        guard !busy else { return }
        busy = true
        actionError = nil
        Task {
            defer { busy = false }
            do {
                try await api.stopWork(id)
                await load()
            } catch {
                actionError = error.localizedDescription
                await load()
            }
        }
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
                    try await api.answerWork(chat: currentChat, input: input, text: text, key: reply.key)
                } else {
                    try await api.send(chat: currentChat, text: text, job: id, key: reply.key)
                }
                field.wrappedValue = ""
                request = nil
                await load()
            } catch { actionError = error.localizedDescription }
        }
    }
}

struct ArtifactPreview: View {
    let api: BuddyAPI
    let job: String
    let file: String
    @State private var artifact: WorkArtifact?
    @State private var error: String?
    @State private var exporting = false
    var body: some View {
        ScrollView { VStack(alignment: .leading, spacing: 14) {
            if let artifact, let data=artifact.data {
                if let image=UIImage(data:data) { Image(uiImage:image).resizable().scaledToFit() }
                else if let text=String(data:data,encoding:.utf8) {
                    if ["md","markdown"].contains((artifact.name as NSString).pathExtension) { RichText(text) }
                    else { CodeBlock(language:(artifact.name as NSString).pathExtension,code:text) }
                } else { ContentUnavailableView("Preview unavailable",systemImage:"doc",description:Text("This file is available in the desktop Work view.")) }
            }
            if let error { ConnectionError(message:error) }
        }.padding(20) }
        .navigationTitle(artifact?.name ?? "Deliverable").navigationBarTitleDisplayMode(.inline).buddyPage()
        .toolbar {
            Button("Save a copy", systemImage: "square.and.arrow.up") { exporting = true }
                .disabled(artifact?.data == nil)
        }
        .fileExporter(isPresented: $exporting, document: ArtifactDocument(data: artifact?.data ?? Data()), contentType: .data, defaultFilename: artifact?.name) { result in
            if case .failure(let failure) = result { error = failure.localizedDescription }
        }
        .overlay { if artifact == nil && error == nil {ProgressView("Opening deliverable…")} }
        .task { do {artifact=try await api.workArtifact(job:job,file:file)} catch {self.error=error.localizedDescription} }
    }
}

struct ArtifactDocument: FileDocument {
    static var readableContentTypes: [UTType] { [.data] }
    let data: Data
    init(data: Data) { self.data = data }
    init(configuration: ReadConfiguration) throws { data = configuration.file.regularFileContents ?? Data() }
    func fileWrapper(configuration: WriteConfiguration) throws -> FileWrapper { FileWrapper(regularFileWithContents: data) }
}

struct HostCapabilities: View {
    let api: BuddyAPI
    @State private var hosts: [ExecutionHost]?
    @State private var error: String?
    var body: some View {
        List {
            ForEach(hosts ?? []) { host in
                Section { VStack(alignment: .leading, spacing: 8) {
                    HStack {Text(host.name).font(.headline);Spacer();Text(host.online ? "Online" : "Offline").foregroundStyle(.secondary)}
                    if let profile=host.profile {
                        Text("\(profile.os) · \(profile.architecture) · \(profile.environment)").font(.caption).foregroundStyle(.secondary)
                        ForEach(profile.capabilities.filter(\.installed)) { capability in
                            LabeledContent(capability.id, value: capability.ready ? "Ready" : capability.enabled ? "Needs setup" : "Disabled").font(.subheadline)
                        }
                    } else {Text("Update the worker to discover capabilities.").foregroundStyle(.secondary)}
                }.padding(.vertical,5) }.listRowBackground(BuddyStyle.surface)
            }
            if let error {ConnectionError(message:error)}
        }.navigationTitle("Hosts and tools").buddyPage()
        .task {await load()}.refreshable {await load()}
        .overlay {if hosts == nil && error == nil {ProgressView("Discovering hosts…")}}
    }
    private func load() async {do {hosts=try await api.hosts();error=nil} catch {self.error=error.localizedDescription}}
}
