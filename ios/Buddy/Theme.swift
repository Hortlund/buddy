import SwiftUI

enum BuddyStyle {
    static let background = adaptive(0xFCFCFD, 0x0D1117)
    static let surface = adaptive(0xFFFFFF, 0x151B23)
    static let raised = adaptive(0xF4F4F5, 0x212830)
    static let border = adaptive(0xE5E5E8, 0x30363D)
    static let text = adaptive(0x19191B, 0xF0F6FC)
    static let muted = adaptive(0x68686F, 0x9198A1)
    static let accent = adaptive(0x4164CB, 0x4493F8)
    static let success = adaptive(0x276448, 0x3FB950)

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
