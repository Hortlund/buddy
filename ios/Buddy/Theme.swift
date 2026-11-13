import SwiftUI

enum BuddyStyle {
    static let background = adaptive(0xFAF9FC, 0x121214)
    static let surface = adaptive(0xFFFFFF, 0x19191D)
    static let raised = adaptive(0xF3F1F7, 0x2A2931)
    static let border = adaptive(0xE5E1EB, 0x303036)
    static let text = adaptive(0x201D27, 0xF3F1F5)
    static let muted = adaptive(0x696471, 0xA4A0AC)
    static let accent = adaptive(0x603DAE, 0xC0AAFF)
    static let accentText = adaptive(0xFFFFFF, 0x201637)
    static let success = adaptive(0x276448, 0x83D8AE)

    private static func adaptive(_ light: UInt32, _ dark: UInt32) -> Color {
        Color(uiColor: UIColor { traits in
            let hex = traits.userInterfaceStyle == .dark ? dark : light
            return UIColor(red: CGFloat(hex >> 16 & 255) / 255,
                           green: CGFloat(hex >> 8 & 255) / 255,
                           blue: CGFloat(hex & 255) / 255, alpha: 1)
        })
    }
}

extension View {
    func buddyPage() -> some View {
        scrollContentBackground(.hidden)
            .background(BuddyStyle.background)
            .toolbarBackground(BuddyStyle.background, for: .navigationBar)
    }
}
