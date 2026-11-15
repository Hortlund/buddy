import SwiftUI

enum BuddyStyle {
    static let background = adaptive(0xFAF9F7, 0x141414)
    static let surface = adaptive(0xFFFFFF, 0x1E1E1E)
    static let raised = adaptive(0xF3F1EE, 0x2C2C2C)
    static let border = adaptive(0xE5E0DB, 0x333333)
    static let text = adaptive(0x242120, 0xF3F1EF)
    static let muted = adaptive(0x6B625C, 0xA5A3A0)
    static let accent = adaptive(0xA84513, 0xFFA269)
    static let accentText = adaptive(0xFFFFFF, 0x281509)
    static let routine = adaptive(0xB84E22, 0xCC5C29)
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
