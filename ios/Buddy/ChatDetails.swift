import SwiftUI

struct ChatDetails: View {
    let api: BuddyAPI
    let id: String
    let name: String
    @Environment(\.scenePhase) private var phase
    @State private var conversation: Conversation?
    @State private var error: String?
    @State private var loadRevision = 0
    @State private var routineError: String?
    @State private var pendingRoutines: [String: Bool] = [:]
    @State private var selectedBud: String?
    @State private var primaryBudID: String?

    private var bud: Bud? {
        conversation?.members?.first { $0.id == selectedBud } ?? conversation?.agent
    }

    var body: some View {
        List {
            Section {
                VStack(spacing: 9) {
                    BudFace(appearance: bud?.avatar, size: 112,
                            mood: conversation?.active.map { .activity($0.activityKind) } ?? .idle)
                        .overlay(alignment: .bottomTrailing) {
                            if let bud, bud.id == primaryBudID {
                                PrimaryBudStar().padding(9)
                            }
                        }
                    VStack(spacing: 4) {
                        Text(conversation?.name?.nilIfEmpty ?? bud?.name ?? name)
                            .font(.title2.weight(.semibold))
                            .accessibilityAddTraits(.isHeader)
                        if let role = bud?.displayRole {
                            Text(role).font(.subheadline).foregroundStyle(BuddyStyle.muted)
                        }
                    }
                    .multilineTextAlignment(.center)
                }
                .frame(maxWidth: .infinity)
                .padding(.vertical, 12)
                .listRowBackground(Color.clear)
                .listRowSeparator(.hidden)
            }

            if let conversation, let bud {
                Section {
                    if let members = conversation.members, members.count > 1 {
                        Picker("Bud", selection: Binding(get: { selectedBud ?? conversation.agent.id }, set: { selectedBud = $0 })) {
                            ForEach(members) { Text($0.name).tag($0.id) }
                        }
                    }
                    NavigationLink(value: Destination.file(bud.id, ".", nil)) {
                        Label("Library", systemImage: "folder")
                    }
                    NavigationLink(value: Destination.chatWork(id)) {
                        HStack {
                            Label("Work runs", systemImage: "briefcase")
                            Spacer()
                            Text(workStatus(conversation.work ?? [])).foregroundStyle(BuddyStyle.muted)
                        }
                    }
                }
                .listRowBackground(BuddyStyle.surface)

                if let routines = conversation.routines, !routines.isEmpty {
                    Section("Routines") {
                        ForEach(routines) { routine in
                            let owner = ([conversation.agent] + (conversation.profiles ?? []) + (conversation.members ?? []))
                                .first { $0.id == routine.agentId }
                            let removed = owner?.removed == true
                            let pending = pendingRoutines[routine.id]
                            Toggle(isOn: Binding(get: { pending ?? routine.enabled }, set: { setRoutine(routine, enabled: $0) })) {
                                VStack(alignment: .leading, spacing: 4) {
                                    Text(routine.name)
                                    Text(routineSchedule(routine, removed: removed))
                                        .font(.footnote).foregroundStyle(BuddyStyle.muted)
                                    if routine.researchScopeMissing == true && (routine.deviceId ?? "").isEmpty {
                                        Text("To use web search, recreate this routine.")
                                            .font(.footnote).foregroundStyle(BuddyStyle.muted)
                                    }
                                }
                            }
                            .toggleStyle(.switch)
                            .tint(BuddyStyle.routine)
                            .disabled(conversation.archived == true || removed || routine.deviceDisconnected == true || pending != nil)
                        }
                    }
                    .listRowBackground(BuddyStyle.surface)
                }
            }
            if let message = routineError ?? error { ConnectionError(message: message).listRowBackground(BuddyStyle.surface) }
        }
        .buddyPage()
        .navigationTitle("Details")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(.hidden, for: .tabBar)
        .overlay { if conversation == nil && error == nil { ProgressView() } }
        .task(id: phase) { await refreshWhileActive(phase, every: .seconds(5), load: load) }
        .task(id: phase) {
            if phase == .active { await loadPrimaryBud() }
        }
        .refreshable {
            async let details: Void = load()
            await loadPrimaryBud()
            await details
        }
    }

    private func workStatus(_ items: [WorkItem]) -> String {
        let count = items.filter { ["queued", "running", "needs_input", "cancelling"].contains($0.status) }.count
        return count == 0 ? "Idle" : "\(count) active"
    }

    private func load() async {
        loadRevision += 1
        let revision = loadRevision
        do {
            let chat = try await api.chat(id)
            guard revision == loadRevision, !Task.isCancelled else { return }
            conversation = chat
            error = nil
        } catch where revision == loadRevision && !Task.isCancelled {
            self.error = error.localizedDescription
        } catch {}
    }

    private func loadPrimaryBud() async {
        if let directory = try? await api.directory(), !Task.isCancelled {
            primaryBudID = directory.primaryAgentId
        }
    }

    private func routineSchedule(_ routine: ChatRoutine, removed: Bool) -> String {
        let cadence: String
        switch routine.minutes {
        case 1: cadence = "Every minute"
        case 60: cadence = "Hourly"
        case 1440: cadence = "Daily"
        case 10080: cadence = "Weekly"
        case let minutes where minutes.isMultiple(of: 1440): cadence = "Every \(minutes / 1440) days"
        case let minutes where minutes.isMultiple(of: 60): cadence = "Every \(minutes / 60) hours"
        default: cadence = "Every \(routine.minutes) minutes"
        }

        let status: String
        if removed {
            status = "Bud removed"
        } else if routine.deviceDisconnected == true {
            status = "Device disconnected"
        } else if pendingRoutines[routine.id] != nil {
            status = "Updating…"
        } else if routine.enabled {
            status = routine.nextAt.formatted(.dateTime.weekday().hour().minute())
        } else {
            status = "Paused"
        }
        return "\(cadence) · \(status)"
    }

    private func setRoutine(_ routine: ChatRoutine, enabled: Bool) {
        guard pendingRoutines[routine.id] == nil else { return }
        pendingRoutines[routine.id] = enabled
        routineError = nil
        Task {
            defer { pendingRoutines.removeValue(forKey: routine.id) }
            do {
                try await api.setRoutine(chat: id, routine: routine.id, enabled: enabled)
                routineError = nil
                await load()
            } catch let failure as BuddyError where failure.status == 403 {
                routineError = "Update the Buddy server to change routines from iPhone."
            } catch { routineError = error.localizedDescription }
        }
    }
}

struct NewBudSheet: View {
    let api: BuddyAPI
    let refresh: () async -> Void
    @Environment(\.dismiss) private var dismiss
    @State private var name = ""
    @State private var role = ""
    @State private var instructions = ""
    @State private var saving = false
    @State private var error: String?

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    BudFace(appearance: nil, size: 92, mood: .idle)
                        .frame(maxWidth: .infinity)
                        .listRowBackground(Color.clear)
                        .listRowSeparator(.hidden)
                }
                Section {
                    TextField("Name", text: $name)
                    TextField("Role (optional)", text: $role)
                    TextField("Instructions (optional)", text: $instructions, axis: .vertical)
                        .lineLimit(3...7)
                }
                .listRowBackground(BuddyStyle.surface)
                if let error { ConnectionError(message: error) }
            }
            .buddyPage()
            .navigationTitle("New Bud")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }.disabled(saving)
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button(saving ? "Creating…" : "Create", action: create)
                        .disabled(saving || name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                }
            }
            .interactiveDismissDisabled(saving)
        }
    }

    private func create() {
        saving = true
        error = nil
        Task {
            defer { saving = false }
            do {
                _ = try await api.createBud(name: name, role: role, instructions: instructions)
                await refresh()
                dismiss()
            } catch { self.error = error.localizedDescription }
        }
    }
}
