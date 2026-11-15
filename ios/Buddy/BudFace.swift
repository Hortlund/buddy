import SwiftUI

enum BudMood {
    case still, idle, thinking, working, reading, replying, listening, speaking

    var action: BudAction {
        switch self {
        case .still, .idle: .rest
        case .thinking: .think
        case .working: .investigate
        case .reading: .notebook
        case .replying: .idea
        case .listening: .nod
        case .speaking: .talk
        }
    }

    static func activity(_ kind: String?) -> Self {
        switch kind {
        case "reading", "editing": .reading
        case "searching", "running": .working
        case "replying": .replying
        default: .thinking
        }
    }
}

struct BudFace: View {
    var appearance: BudAppearance?
    var size: CGFloat = 36
    var mood: BudMood = .still
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @Environment(\.displayScale) private var displayScale
    @Environment(\.scenePhase) private var scenePhase
    @State private var frames = BudFrames()

    private var moves: Bool {
        mood != .still && scenePhase == .active && !reduceMotion && appearance?.animate != false
    }

    var body: some View {
        Group {
            if let image = appearance?.image, let picture = UIImage(data: image) {
                Image(uiImage: picture)
                    .resizable().scaledToFill()
                    .frame(width: size * 0.85, height: size * 0.85)
                    .clipShape(RoundedRectangle(cornerRadius: size * 0.28))
            } else {
                TimelineView(.animation(minimumInterval: 1 / 30, paused: !moves)) { timeline in
                    let palette = BudPalette.named[appearance?.color ?? "ink"] ?? .ink
                    let frame = frames.image(size: min(224, Int(size * displayScale)), shape: appearance?.shape,
                                             palette: palette, portrait: size < 32 && mood == .still,
                                             action: mood.action, time: timeline.date.timeIntervalSinceReferenceDate,
                                             moving: moves, small: size < 45)
                    if let frame {
                        Image(decorative: frame, scale: displayScale)
                            .resizable().scaledToFit().frame(width: size, height: size)
                    }
                }
            }
        }
        .frame(width: size, height: size)
        .accessibilityHidden(true)
    }
}

struct BudActivityDots: View {
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        TimelineView(.animation(minimumInterval: 1 / 24, paused: reduceMotion)) { timeline in
            let phase = timeline.date.timeIntervalSinceReferenceDate * 2 * .pi / 1.35
            HStack(spacing: 3) {
                ForEach(0..<3) { index in
                    let level = reduceMotion ? 0.6 : (1 + sin(phase - Double(index) * 1.3)) / 2
                    Circle()
                        .fill(BuddyStyle.accent.opacity(0.34 + 0.66 * level))
                        .frame(width: 6, height: 6)
                        .scaleEffect(0.6 + 0.4 * level)
                }
            }
        }
        .accessibilityHidden(true)
    }
}

struct BudPalette {
    let body: UInt32
    let eyes: UInt32

    init(_ body: UInt32, _ eyes: UInt32) {
        self.body = body
        self.eyes = eyes
    }

    static let ink = BudPalette(0x4C494F, 0xF6F1F0)
    static let named: [String: BudPalette] = {
        // Keep the palette values in sync with blobPalettes in cmd/buddy/avatar.go.
        var palettes = [
            "ink": ink,
            "pearl": BudPalette(0xEBE2D5, 0x2B2424),
            "lilac": BudPalette(0xC5AAFA, 0x30213F),
            "peach": BudPalette(0xFFBC91, 0x351F1B),
            "mint": BudPalette(0x98DDBD, 0x15332C),
            "sky": BudPalette(0x98D0F7, 0x1D2C47),
            "rose": BudPalette(0xF49DC5, 0x3A1E32),
            "coral": BudPalette(0xFF8856, 0x2D191A),
            "honey": BudPalette(0xF6C858, 0x362819),
            "moss": BudPalette(0xBDD07F, 0x29331F),
            "teal": BudPalette(0x57BDAC, 0x123331),
            "cobalt": BudPalette(0x779BFF, 0x1B2449),
            "plum": BudPalette(0xAE83DC, 0x29213D),
            "slate": BudPalette(0x768494, 0x18212D)
        ]
        for (alias, name) in ["blue": "cobalt", "green": "mint", "pink": "rose",
                              "orange": "peach", "amber": "peach", "purple": "lilac",
                              "violet": "lilac", "lavender": "lilac"] {
            palettes[alias] = palettes[name]
        }
        return palettes
    }()

}

#Preview("Bud shapes and colors") {
    LazyVGrid(columns: [GridItem(.adaptive(minimum: 96))], spacing: 18) {
        ForEach(["orb", "bean", "pebble", "drop", "cloud", "capsule", "pillow"], id: \.self) { shape in
            VStack {
                BudFace(appearance: BudAppearance(color: "cobalt", shape: shape), size: 80)
                Text(shape).font(.caption)
            }
        }
        ForEach(["ink", "pearl", "lilac", "peach", "mint", "sky", "rose", "coral", "honey", "moss", "teal", "cobalt", "plum", "slate"], id: \.self) { color in
            VStack {
                BudFace(appearance: BudAppearance(color: color, shape: "cloud"), size: 80)
                Text(color).font(.caption)
            }
        }
    }
    .padding()
}
