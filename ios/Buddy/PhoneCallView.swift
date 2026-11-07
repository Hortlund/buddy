import SwiftUI

struct PhoneCallView: View {
    let api: BuddyAPI
    @Environment(PhoneVoiceSession.self) private var voice
    @State private var showInvite = false
    @State private var showWork = false
    @State private var workPath: [Destination] = []
    private let accent = Color(red: 0.73, green: 0.68, blue: 1)

    var body: some View {
        NavigationStack {
            VStack(spacing: 0) {
                header
                ScrollView {
                    LazyVGrid(columns: [GridItem(.adaptive(minimum: 145), spacing: 12)], spacing: 12) {
                        ForEach(voice.members) { member in tile(member) }
                        microphone
                    }
                    .padding(20)
                }
                if !voice.caption.isEmpty {
                    VStack(alignment: .leading, spacing: 6) {
                        Text(voice.captionName).font(.caption.weight(.semibold)).foregroundStyle(accent)
                        Text(voice.caption).font(.subheadline).lineLimit(5)
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(14)
                    .background(Color.white.opacity(0.05), in: RoundedRectangle(cornerRadius: 16))
                    .padding(.horizontal, 20)
                    .accessibilityElement(children: .combine)
                }
                if let issue = voice.issue {
                    Label(issue, systemImage: "exclamationmark.circle")
                        .font(.footnote).foregroundStyle(.orange)
                        .padding(.horizontal, 20).padding(.top, 12)
                }
                controls.padding(20)
            }
            .background(Color(red: 0.055, green: 0.065, blue: 0.09))
            .foregroundStyle(.white)
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    Button { voice.presented = false } label: { Label("Chat", systemImage: "chevron.down") }
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Button { showWork = true } label: { Label("Work", systemImage: "briefcase") }
                }
            }
            .toolbarBackground(.hidden, for: .navigationBar)
            .navigationBarTitleDisplayMode(.inline)
            .sheet(isPresented: $showInvite) { inviteSheet }
            .sheet(isPresented: $showWork) {
                NavigationStack(path: $workPath) {
                    WorkList(api: api)
                        .navigationDestination(for: Destination.self) { destination in
                            switch destination {
                            case let .work(chat, id): WorkDetail(api: api, chat: chat, id: id)
                            case let .chat(id, name): ChatView(api: api, id: id, name: name)
                            case let .inbox(id): InboxDetail(api: api, id: id)
                            case let .file(bud, path): WorkspacePreview(api: api, bud: bud, path: path)
                            }
                        }
                        .toolbar { ToolbarItem(placement: .topBarTrailing) { Button("Done") { showWork = false } } }
                }
                .environment(\.openURL, OpenURLAction { url in
                    if let destination = referenceDestination(url) {
                        workPath.append(destination)
                        return .handled
                    }
                    return safeWebURL(url) == nil ? .discarded : .systemAction
                })
            }
        }
        .tint(accent)
        .preferredColorScheme(.dark)
        .interactiveDismissDisabled(voice.active)
    }

    private var header: some View {
        VStack(spacing: 6) {
            Text(voice.title).font(.title2.bold()).multilineTextAlignment(.center)
            HStack(spacing: 6) {
                if voice.active { Circle().fill(.mint).frame(width: 6, height: 6) }
                Text(voice.ready ? "Live · \(voice.members.count + 1) in the call" : voice.status)
                if let date = voice.joinedAt, voice.active { Text(date, style: .timer).monospacedDigit() }
            }
            .font(.caption).foregroundStyle(.white.opacity(0.6))
            if let leader = voice.members.first(where: { $0.id == voice.lead }), voice.members.count > 1 {
                Text("Lead · \(leader.name)").font(.caption).foregroundStyle(accent)
            }
        }
        .frame(maxWidth: .infinity).padding(.horizontal, 20).padding(.top, 12)
    }

    private func tile(_ member: CallMember) -> some View {
        let speaking = voice.speaker == member.id
        return Button { voice.focus(member.id) } label: {
            VStack(spacing: 12) {
                ZStack {
                    Circle().fill(accent.opacity(speaking ? 0.22 : 0.07))
                    BudFace(appearance: voice.appearance(member.id), size: 74)
                        .scaleEffect(speaking ? 1.06 : 1)
                        .rotationEffect(.degrees(speaking ? -4 : 0))
                }
                .frame(width: 98, height: 98)
                .animation(speaking ? .easeInOut(duration: 0.6).repeatForever(autoreverses: true) : .default, value: speaking)
                HStack(spacing: 4) {
                    Text(member.name).lineLimit(1)
                    if member.id == voice.lead { Image(systemName: "star.fill").font(.caption2).foregroundStyle(accent) }
                }
                .font(.subheadline.bold())
                Text(speaking ? "Speaking" : "Listening")
                    .font(.caption).foregroundStyle(speaking ? accent : .white.opacity(0.55))
            }
            .frame(maxWidth: .infinity).padding(.vertical, 16)
            .background(Color.white.opacity(0.055), in: RoundedRectangle(cornerRadius: 22))
            .overlay { RoundedRectangle(cornerRadius: 22).stroke(speaking ? accent : .white.opacity(0.08), lineWidth: 1.5) }
        }
        .buttonStyle(.plain)
        .accessibilityLabel("\(member.name), \(speaking ? "Speaking" : "Listening")")
        .disabled(!voice.ready)
    }

    private var microphone: some View {
        VStack(spacing: 12) {
            ZStack {
                Circle().fill(accent.opacity(0.12))
                Image(systemName: voice.muted ? "mic.slash.fill" : "mic.fill")
                    .font(.system(size: 36)).foregroundStyle(accent)
            }.frame(width: 98, height: 98)
            Text("You").font(.subheadline.bold())
            HStack(spacing: 3) {
                ForEach(0..<5) { index in
                    Capsule().fill(accent.opacity(voice.muted ? 0.2 : 0.8))
                        .frame(width: 3, height: max(3, CGFloat(voice.micLevel) * CGFloat([12, 20, 26, 20, 12][index])))
                }
                Text(voice.muted ? "Muted" : "Mic on").padding(.leading, 4)
            }
            .frame(height: 15).font(.caption).foregroundStyle(.white.opacity(0.55))
        }
        .frame(maxWidth: .infinity).padding(.vertical, 16)
        .background(Color.white.opacity(0.055), in: RoundedRectangle(cornerRadius: 22))
    }

    private var controls: some View {
        HStack(spacing: 0) {
            control("Invite", icon: "person.badge.plus") { showInvite = true }.disabled(!voice.ready)
            control(voice.muted ? "Unmute" : "Mute", icon: voice.muted ? "mic.slash" : "mic") { voice.toggleMute() }.disabled(!voice.active)
            control("Interrupt", icon: "stop.fill") { voice.interrupt() }.disabled(!voice.ready)
            control(voice.active ? "Hang up" : "Close", icon: "phone.down.fill", color: .red) { voice.hangup() }
        }
    }

    private func control(_ label: String, icon: String, color: Color = .white, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            VStack(spacing: 7) {
                Image(systemName: icon).font(.system(size: 19, weight: .medium))
                    .frame(width: 52, height: 52)
                    .background(color == .red ? Color.red.opacity(0.85) : .white.opacity(0.09), in: Circle())
                Text(label).font(.caption2)
            }.frame(maxWidth: .infinity)
        }
        .buttonStyle(.plain)
        .foregroundStyle(.white)
    }

    private var inviteSheet: some View {
        NavigationStack {
            List {
                ForEach(voice.directory.filter { bud in !voice.members.contains(where: { $0.id == bud.id }) }) { bud in
                    Button { voice.invite(bud.id) } label: {
                        HStack(spacing: 12) {
                            BudFace(appearance: bud.avatar)
                            VStack(alignment: .leading, spacing: 3) {
                                Text(bud.name).font(.headline)
                                Text(bud.role ?? "Bud").font(.caption).foregroundStyle(BuddyStyle.muted)
                            }
                            Spacer()
                            if voice.joining.contains(bud.id) { ProgressView() }
                            else { Image(systemName: "plus.circle.fill").foregroundStyle(BuddyStyle.accent) }
                        }
                        .foregroundStyle(BuddyStyle.text)
                    }
                    .disabled(voice.joining.contains(bud.id))
                    .listRowBackground(BuddyStyle.surface)
                }
                if voice.members.count > 1 {
                    Button { voice.continueChat() } label: {
                        Label(voice.continues ? "Buds will continue in group chat" : "Continue collaborating after the call", systemImage: "bubble.left.and.bubble.right")
                    }.disabled(voice.continues)
                }
            }
            .buddyPage()
            .navigationTitle("Invite Buds")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .topBarTrailing) { Button("Done") { showInvite = false } } }
        }
        .presentationDetents([.medium, .large])
    }
}
