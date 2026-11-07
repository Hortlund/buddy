import Observation
import SwiftUI
import UserNotifications

@main
struct BuddyApp: App {
    @State private var home = BuddyHome()
    @AppStorage("buddy-appearance") private var appearance = "system"
    @UIApplicationDelegateAdaptor(PushDelegate.self) private var push

    var body: some Scene {
        WindowGroup {
            Group {
                if let api = home.api {
                    BuddyTabs(api: api, push: push)
                        .id(api.credentials?.id)
                } else {
                    PairingView()
                }
            }
            .environment(home)
            .environment(\.openURL, OpenURLAction { url in
                safeWebURL(url) == nil ? .discarded : .systemAction
            })
            .tint(BuddyStyle.accent)
            .background(BuddyStyle.background)
            .foregroundStyle(BuddyStyle.text)
            .preferredColorScheme(appearance == "system" ? nil : appearance == "dark" ? .dark : .light)
        }
    }
}

@MainActor
@Observable
final class BuddyHome {
    var api = BuddyKeychain.load().map { BuddyAPI(credentials: $0) }

    func pair(_ invite: PairingLink, name: String) async throws {
        let credentials = try await BuddyAPI.pair(invite, name: name)
        let client = BuddyAPI(credentials: credentials)
        do {
            try BuddyKeychain.save(credentials)
        } catch {
            try? await client.disconnect()
            throw error
        }
        api = client
    }

    func forget() {
        BuddyKeychain.clear()
        api = nil
    }
}

enum Destination: Hashable {
    case chat(String, String)
    case inbox(String)
    case work(String, String)
    case file(String, String, String?)
 case artifact(String, String)
}

enum BuddyTab: Hashable { case chats, inbox, work, settings }

struct PushRoute: Equatable {
    let client: String
    let destination: Destination

    init?(userInfo: [AnyHashable: Any]) {
        guard let payload = userInfo["buddy"] as? [String: String],
              let client = payload["client"], let item = payload["item"],
              !client.isEmpty, !item.isEmpty else { return nil }
        self.client = client
        switch payload["destination"] {
        case "inbox": destination = .inbox(item)
        case "work":
            guard let conversation = payload["conversation"], !conversation.isEmpty else { return nil }
            destination = .work(conversation, item)
        default: return nil
        }
    }
}

private enum PhonePush {
    static let preference = "iphone-notifications"
    static let supported = Bundle.main.object(forInfoDictionaryKey: "BuddyPushEnabled") as? String == "YES"

    static var optedIn: Bool {
        supported && UserDefaults.standard.bool(forKey: preference)
    }
}

@MainActor
final class PushDelegate: NSObject, UIApplicationDelegate, ObservableObject, UNUserNotificationCenterDelegate {
    @Published var token: String?
    @Published var issue: String?
    @Published var route: PushRoute?

    func application(_ application: UIApplication, didFinishLaunchingWithOptions options: [UIApplication.LaunchOptionsKey: Any]? = nil) -> Bool {
        UNUserNotificationCenter.current().delegate = self
        return true
    }

    func application(_ application: UIApplication, didRegisterForRemoteNotificationsWithDeviceToken deviceToken: Data) {
        guard PhonePush.optedIn else { stop(); return }
        issue = nil
        token = deviceToken.map { String(format: "%02x", $0) }.joined()
    }

    func application(_ application: UIApplication, didFailToRegisterForRemoteNotificationsWithError error: Error) {
        guard PhonePush.optedIn else { return }
        issue = "iOS couldn’t register for notifications. Check the app’s signing and notification settings."
    }

    func stop() {
        if UIApplication.shared.isRegisteredForRemoteNotifications {
            UIApplication.shared.unregisterForRemoteNotifications()
        }
        token = nil
        issue = nil
        UNUserNotificationCenter.current().removeAllDeliveredNotifications()
    }

    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse) async {
        let destination = PushRoute(userInfo: response.notification.request.content.userInfo)
        await MainActor.run { route = destination }
    }

    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification) async -> UNNotificationPresentationOptions {
        guard PhonePush.optedIn,
              let route = PushRoute(userInfo: notification.request.content.userInfo),
              route.client == BuddyKeychain.load()?.id else { return [] }
        return [.banner, .sound]
    }
}

struct BuddyTabs: View {
    let api: BuddyAPI
    @ObservedObject var push: PushDelegate
    @Environment(\.scenePhase) private var phase
    @AppStorage(PhonePush.preference) private var alerts = false
    @AppStorage("iphone-work-notifications") private var workAlerts = false
    @State private var notificationIssue: String?
    @State private var selected: BuddyTab = .chats
    @State private var chats: [Destination] = []
    @State private var inbox: [Destination] = []
    @State private var work: [Destination] = []
    @State private var voice = PhoneVoiceSession()

    private struct PushSync: Equatable {
        var token: String?
        var enabled: Bool
        var work: Bool
        var phase: ScenePhase
    }

    var body: some View {
        VStack(spacing: 0) {
            if voice.active && !voice.presented {
                Button { voice.presented = true } label: {
                    HStack(spacing: 8) {
                        Image(systemName: "phone.fill")
                        Text(voice.title).lineLimit(1)
                        Spacer()
                        Text("Return to call").font(.caption.weight(.semibold))
                    }
                    .font(.subheadline)
                    .padding(.horizontal, 16).padding(.vertical, 10)
                    .background(BuddyStyle.success.opacity(0.14))
                }
                .buttonStyle(.plain)
                .foregroundStyle(BuddyStyle.success)
            }

            TabView(selection: $selected) {
                NavigationStack(path: $chats) {
                    ChatList(api: api).navigationDestination(for: Destination.self, destination: show)
                }
                .tabItem { Label("Chats", systemImage: "bubble.left.and.bubble.right") }
                .tag(BuddyTab.chats)

                NavigationStack(path: $inbox) {
                    InboxList(api: api).navigationDestination(for: Destination.self, destination: show)
                }
                .tabItem { Label("Inbox", systemImage: "tray") }
                .tag(BuddyTab.inbox)

                NavigationStack(path: $work) {
                    WorkList(api: api).navigationDestination(for: Destination.self, destination: show)
                }
                .tabItem { Label("Work", systemImage: "briefcase") }
                .tag(BuddyTab.work)

                NavigationStack {
                    PhoneSettings(api: api, issue: push.issue ?? notificationIssue, stopPush: push.stop)
                }
                .tabItem { Label("Settings", systemImage: "gearshape") }
                .tag(BuddyTab.settings)
            }
        }
        .environment(voice)
        .environment(\.openURL, OpenURLAction { url in
            if let destination = referenceDestination(url) {
                switch selected {
                case .chats: chats.append(destination)
                case .inbox: inbox.append(destination)
                case .work: work.append(destination)
                case .settings: selected = .chats; chats.append(destination)
                }
                return .handled
            }
            return safeWebURL(url) == nil ? .discarded : .systemAction
        })
        .fullScreenCover(isPresented: $voice.presented, onDismiss: {
            guard !voice.chat.isEmpty else { return }
            chats = [.chat(voice.chat, voice.title)]
            selected = .chats
        }) {
            PhoneCallView(api: api).environment(voice)
        }
        .onChange(of: push.route, initial: true) { _, route in
            guard let route else { return }
            push.route = nil
            guard route.client == api.credentials?.id else { return }
            switch route.destination {
            case .inbox: inbox = [route.destination]; selected = .inbox
            case .work: work = [route.destination]; selected = .work
            case .chat: chats = [route.destination]; selected = .chats
            case .file, .artifact: chats = [route.destination]; selected = .chats
            }
        }
        .task(id: PushSync(token: push.token, enabled: alerts, work: workAlerts, phase: phase)) {
            guard phase == .active else { return }
            do {
                if PhonePush.supported && alerts {
                    let settings = await UNUserNotificationCenter.current().notificationSettings()
                    try Task.checkCancellation()
                    if [.authorized, .provisional, .ephemeral].contains(settings.authorizationStatus),
                       try await api.notificationAvailability() {
                        try Task.checkCancellation()
                        guard PhonePush.optedIn else { return }
                        if let token = push.token {
                            try await api.setNotifications(token: token, work: workAlerts)
                        } else {
                            UIApplication.shared.registerForRemoteNotifications()
                        }
                        notificationIssue = nil
                        return
                    }
                }
                try Task.checkCancellation()
                push.stop()
                try await api.setNotifications(token: "", work: false)
                notificationIssue = nil
            } catch where !Task.isCancelled {
                notificationIssue = error.localizedDescription
            } catch {}
        }
    }

    @ViewBuilder
    private func show(_ destination: Destination) -> some View {
        switch destination {
        case let .chat(id, name): ChatView(api: api, id: id, name: name)
        case let .inbox(id): InboxDetail(api: api, id: id)
        case let .work(chat, id): WorkDetail(api: api, chat: chat, id: id)
        case let .file(bud, path, task): WorkspacePreview(api: api, bud: bud, path: path, sourceTask: task)
 case let .artifact(job,file): ArtifactPreview(api:api,job:job,file:file)
        }
    }
}

struct PhoneSettings: View {
    let api: BuddyAPI
    let issue: String?
    let stopPush: () -> Void
    @Environment(BuddyHome.self) private var home
    @Environment(PhoneVoiceSession.self) private var voice
    @AppStorage(PhonePush.preference) private var alerts = false
    @AppStorage("iphone-work-notifications") private var workAlerts = false
    @State private var available: Bool?
    @State private var error: String?
    @State private var disconnecting = false
    @State private var showDisconnect = false
    @State private var canForget = false
    @AppStorage("buddy-appearance") private var appearance = "system"

    var body: some View {
        Form {
            Section("Appearance") {
                Picker("Theme", selection: $appearance) {
                    Text("System").tag("system")
                    Text("Light").tag("light")
                    Text("GitHub Dark").tag("dark")
                }
                .accessibilityIdentifier("appearance-picker")
            }
            .listRowBackground(BuddyStyle.surface)
            Section("Execution") { NavigationLink("Hosts and capabilities") { HostCapabilities(api: api) } }.listRowBackground(BuddyStyle.surface)
 Section("Buddy home") {
                if let credentials = api.credentials {
                    LabeledContent("This iPhone", value: credentials.name)
                    LabeledContent("Server", value: credentials.server.host() ?? credentials.server.absoluteString)
                }
                Button("Disconnect iPhone", role: .destructive) { showDisconnect = true }
                    .disabled(disconnecting)
                if canForget {
                    Button("Forget this connection", role: .destructive) { forget() }
                    Text("You can remove the iPhone from Connections on your Mac to revoke its server access.")
                        .foregroundStyle(.secondary)
                }
            }
            .listRowBackground(BuddyStyle.surface)
            Section {
                Toggle("Allow notifications", isOn: Binding(get: { alerts }, set: changeAlerts))
                    .disabled(!PhonePush.supported || (!alerts && available != true))
                Toggle("Finished and failed jobs", isOn: $workAlerts)
                    .disabled(!PhonePush.supported || !alerts || available != true)
            } header: {
                Text("Notifications")
            } footer: {
                Text(notificationHelp)
            }
            .listRowBackground(BuddyStyle.surface)
            if let message = error ?? issue {
                Text(message).foregroundStyle(.secondary)
            }
        }
        .buddyPage()
        .navigationTitle("Settings")
        .task { await loadAvailability() }
        .refreshable { await loadAvailability() }
        .confirmationDialog("Disconnect this iPhone?", isPresented: $showDisconnect, titleVisibility: .visible) {
            Button("Disconnect", role: .destructive) {
                disconnecting = true
                Task { await disconnect() }
            }
        }
    }

    private var notificationHelp: String {
        if !PhonePush.supported { return "Build the Buddy Push scheme to enable alerts." }
        if available == false { return "This Buddy server hasn’t set up iPhone notifications." }
        return "Get notified when a Bud needs input or a watch has an update."
    }

    private func changeAlerts(_ enabled: Bool) {
        if !enabled {
            alerts = false
            stopPush()
            return
        }
        Task {
            do {
                if try await UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound, .badge]) {
                    error = nil
                    alerts = true
                } else {
                    error = "Allow Buddy notifications in iPhone Settings."
                }
            } catch {
                self.error = error.localizedDescription
            }
        }
    }

    private func loadAvailability() async {
        guard PhonePush.supported else { return }
        do {
            available = try await api.notificationAvailability()
            error = nil
        } catch where !Task.isCancelled {
            self.error = error.localizedDescription
        } catch {}
    }

    private func disconnect() async {
        voice.hangup()
        defer { disconnecting = false }
        do {
            try await api.disconnect()
            forget()
        } catch let failure as BuddyError where failure.status == 401 {
            forget()
        } catch {
            self.error = error.localizedDescription
            canForget = true
        }
    }

    private func forget() {
        alerts = false
        workAlerts = false
        stopPush()
        home.forget()
    }
}

@MainActor
func refreshWhileActive(_ phase: ScenePhase, every interval: Duration = .seconds(5),
                        load: () async -> Void) async {
    guard phase == .active else { return }
    while !Task.isCancelled {
        await load()
        do { try await Task.sleep(for: interval) } catch { return }
    }
}
