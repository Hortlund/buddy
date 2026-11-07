import AVFoundation
import Observation
import OSLog
import SwiftUI

struct CallMember: Decodable, Identifiable {
    let id: String
    let name: String
}

struct CallEvent: Decodable {
    let type: String
    var agent: String?
    var lead: String?
    var speaker: String?
    var text: String?
    var item: String?
    var chat: String?
    var title: String?
    var participants: [CallMember]?
}

@MainActor
@Observable
final class PhoneVoiceSession {
    var presented = false
    private(set) var active = false
    private(set) var ready = false
    private(set) var muted = false
    private(set) var title = "Voice call"
    private(set) var chat = ""
    private(set) var lead = ""
    private(set) var speaker = ""
    private(set) var caption = ""
    private(set) var captionName = ""
    private(set) var status = "Connecting…"
    private(set) var issue: String?
    private(set) var members: [CallMember] = []
    private(set) var directory: [Bud] = []
    private(set) var micLevel: Float = 0
    private(set) var joinedAt: Date?
    private(set) var joining: Set<String> = []
    private(set) var continues = false
    private var socket: URLSessionWebSocketTask?
    private var audio: CallAudio?
    private var connection: Task<Void, Never>?
    private var upload: Task<Void, Never>?
    private var timeout: Task<Void, Never>?
    private var audioObserver: Task<Void, Never>?
    private var playbackTimeout: Task<Void, Never>?
    private var directoryLoad: Task<Void, Never>?
    private var invitationTimeouts: [String: Task<Void, Never>] = [:]
    private var revision = UUID()
    private var outputItem = ""
    private var micStarted = false
    private var audioAnnounced = false
    private let log = Logger(subsystem: "dev.buddy.ios", category: "Voice")

    func start(api: BuddyAPI, conversation: Conversation, chat id: String) {
        guard !active else { presented = true; return }
        revision = UUID()
        let attempt = revision
        active = true
        ready = false
        issue = nil
        muted = false
        micStarted = false
        audioAnnounced = false
        continues = false
        joinedAt = nil
        title = conversation.name?.nilIfEmpty ?? conversation.agent.name
        chat = id
        directory = conversation.profiles ?? [conversation.agent]
        members = []
        joining = []
        caption = ""
        captionName = ""
        status = "Connecting…"
        presented = true
        log.info("Starting phone voice call")
        connection = Task {
            do {
                guard try await api.voiceAvailable() else {
                    throw BuddyError("Add a voice API key in desktop Connections before calling.")
                }
                status = "Allow microphone access…"
                let allowed = await CallAudio.permission()
                try Task.checkCancellation()
                guard allowed else { throw BuddyError("Allow microphone access for Buddy in iPhone Settings, then call again.") }
                guard revision == attempt else { return }
                status = "Connecting…"
                let graph = CallAudio()
                audio = graph
                let packets = try graph.start()
                let call = try api.openVoice(chat: id, agent: conversation.agent.id)
                socket = call
                graph.onDrained = { [weak owner = self] item in
                    guard let owner, owner.outputItem == item else { return }
                    owner.playbackTimeout?.cancel()
                    owner.outputItem = ""
                    owner.speaker = ""
                    owner.status = "Listening"
                    owner.send(["type": "played", "item": item])
                }
                timeout = Task {
                    try? await Task.sleep(for: .seconds(30))
                    guard !Task.isCancelled, revision == attempt, !ready || !micStarted else { return }
                    fail("The call didn’t connect. Check your server’s voice configuration.")
                }
                upload = Task {
                    var meterTime = Date.distantPast
                    for await packet in packets {
                        guard !Task.isCancelled, revision == attempt else { return }
                        if !micStarted { micStarted = true; announceAudio() }
                        if Date().timeIntervalSince(meterTime) > 0.08 {
                            micLevel = muted ? 0 : packet.level
                            meterTime = Date()
                        }
                        guard ready, !muted else { continue }
                        do { try await call.send(.data(packet.pcm)) }
                        catch { if !Task.isCancelled { fail("Microphone audio couldn’t reach Buddy. Reconnect to try again.") }; return }
                    }
                    if !Task.isCancelled, revision == attempt, active {
                        fail("Microphone streaming stopped. Reconnect the call to resume.")
                    }
                }
                audioObserver = Task {
                    for await notification in NotificationCenter.default.notifications(named: AVAudioSession.interruptionNotification) {
                        guard !Task.isCancelled, revision == attempt else { return }
                        if let kind = notification.userInfo?[AVAudioSessionInterruptionTypeKey] as? UInt,
                           kind == AVAudioSession.InterruptionType.began.rawValue {
                            fail("Audio was interrupted by another call or app. Start a new Buddy call when you’re ready.")
                            return
                        }
                    }
                }
                directoryLoad = Task {
                    if let loaded = try? await api.directory(), revision == attempt, active {
                        directory = loaded.agents ?? directory
                    }
                }
                while !Task.isCancelled, revision == attempt {
                    let message = try await call.receive()
                    switch message {
                    case .data(let data): try audio?.enqueue(data)
                    case .string(let text):
                        let event = try JSONDecoder().decode(CallEvent.self, from: Data(text.utf8))
                        receive(event)
                    @unknown default: break
                    }
                }
            } catch where !Task.isCancelled && revision == attempt {
                log.error("Phone voice connection failed: \(String(describing: type(of: error)), privacy: .public)")
                fail((error as? BuddyError)?.message ?? "The call disconnected. Completed captions are saved in chat. Check that the Buddy server is reachable.")
            } catch {}
        }
    }

    private func receive(_ event: CallEvent) {
        switch event.type {
        case "ready", "roster":
            if let value = event.chat { chat = value }
            if let value = event.title { title = value }
            if let value = event.lead { lead = value }
            if let value = event.participants {
                members = value
                joining.subtract(value.map(\.id))
                for member in value { invitationTimeouts.removeValue(forKey: member.id)?.cancel() }
            }
            if event.type == "ready" {
                ready = true
                joinedAt = Date()
                status = "Listening"
                if muted { send(["type": "mute", "muted": true]) }
                announceAudio()
                log.info("Phone voice room ready")
            }
        case "joining":
            if let agent = event.agent { trackInvitation(agent) }
        case "audio_start":
            outputItem = event.item ?? ""
            audio?.begin(outputItem)
            speaker = event.agent ?? ""
            status = "Speaking"
        case "audio_end":
            let item = event.item ?? ""
            audio?.end(item)
            if outputItem == item, !item.isEmpty {
                playbackTimeout?.cancel()
                playbackTimeout = Task {
                    try? await Task.sleep(for: .seconds(50))
                    if !Task.isCancelled, outputItem == item { fail("Call audio stopped playing. Check your sound output and reconnect.") }
                }
            }
        case "interrupt":
            playbackTimeout?.cancel()
            let millis = audio?.cut() ?? 0
            send(["type": "cut", "item": event.item ?? outputItem, "millis": millis])
            speaker = ""
            outputItem = ""
            status = "Listening"
        case "caption", "final":
            if let text = event.text, !text.isEmpty {
                caption = text
                captionName = event.speaker ?? (event.agent == nil ? "You" : "Bud")
            }
        case "settled", "idle", "listening": speaker = ""; status = "Listening"
        case "responding", "routing": status = "Thinking…"
        case "team_queued": continues = true
        case "notice": issue = event.text
        case "error": fail(event.text ?? "The voice server reported a problem.")
        default: break
        }
    }

    func appearance(_ id: String) -> BudAppearance? { directory.first { $0.id == id }?.avatar }
    private func announceAudio() {
        guard ready, micStarted, !audioAnnounced else { return }
        audioAnnounced = true
        timeout?.cancel()
        send(["type": "audio_ready"])
    }
    func invite(_ id: String) {
        guard ready, !joining.contains(id), !members.contains(where: { $0.id == id }) else { return }
        trackInvitation(id)
        send(["type": "invite", "agent": id])
    }
    private func trackInvitation(_ id: String) {
        guard !joining.contains(id) else { return }
        joining.insert(id)
        let attempt = revision
        invitationTimeouts[id] = Task {
            try? await Task.sleep(for: .seconds(20))
            guard !Task.isCancelled, revision == attempt else { return }
            joining.remove(id)
            invitationTimeouts[id] = nil
            issue = "A Bud couldn’t join the call. You can retry from Invite."
        }
    }
    func focus(_ id: String) { send(["type": "focus", "agent": id]) }
    func toggleMute() { muted.toggle(); micLevel = 0; send(["type": "mute", "muted": muted]) }
    func interrupt() { send(["type": "interrupt"]) }
    func continueChat() { send(["type": "continue_chat"]) }

    private func send(_ value: [String: Any]) {
        guard let socket, let data = try? JSONSerialization.data(withJSONObject: value), let text = String(data: data, encoding: .utf8) else { return }
        let attempt = revision
        Task {
            do { try await socket.send(.string(text)) }
            catch where revision == attempt && active { fail("Couldn’t send a call control. Reconnect the call to try again.") }
            catch {}
        }
    }

    func hangup() {
        let call = socket
        cleanUp()
        issue = nil
        presented = false
        Task {
            try? await call?.send(.string("{\"type\":\"hangup\"}"))
            call?.cancel(with: .normalClosure, reason: nil)
        }
        log.info("Phone voice call ended")
    }

    private func fail(_ message: String) {
        let call = socket
        cleanUp()
        call?.cancel(with: .goingAway, reason: nil)
        issue = message
        status = "Call ended"
    }

    private func cleanUp() {
        revision = UUID()
        active = false
        ready = false
        speaker = ""
        micLevel = 0
        connection?.cancel()
        upload?.cancel()
        timeout?.cancel()
        audioObserver?.cancel()
        playbackTimeout?.cancel()
        directoryLoad?.cancel()
        for invitation in invitationTimeouts.values { invitation.cancel() }
        invitationTimeouts.removeAll()
        audio?.stop()
        audio = nil
        socket = nil
        joining = []
    }
}
