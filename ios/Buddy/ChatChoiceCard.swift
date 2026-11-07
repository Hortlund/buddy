import SwiftUI

struct ChatChoiceCard: View {
    let card: ChoiceCard
    let busy: Bool
    let onChoice: (Int) -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Label(card.choice == nil ? "Your choice" : "Answered",
                  systemImage: card.choice == nil ? "bubble.left" : "checkmark.circle")
                .font(.caption.weight(.semibold))
                .foregroundStyle(BuddyStyle.muted)
            Text(card.question)
                .font(.headline)
                .fixedSize(horizontal: false, vertical: true)
            if let detail = card.detail?.nilIfEmpty {
                Text(detail)
                    .font(.subheadline)
                    .foregroundStyle(BuddyStyle.muted)
                    .fixedSize(horizontal: false, vertical: true)
            }
            if let confirmation = card.confirmation, !confirmation.isEmpty, card.options.count == 2 {
                confirmationButtons(destructive: confirmation == "destructive")
            } else {
                VStack(spacing: 8) {
                    ForEach(card.options.indices, id: \.self, content: option)
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(14)
        .background(BuddyStyle.surface, in: RoundedRectangle(cornerRadius: 16))
        .overlay { RoundedRectangle(cornerRadius: 16).stroke(BuddyStyle.border, lineWidth: 1) }
    }

    private func option(_ index: Int) -> some View {
        let selected = card.choice == index
        return Button { onChoice(index) } label: {
            HStack(alignment: .top, spacing: 12) {
                VStack(alignment: .leading, spacing: 5) {
                    Text(card.options[index]).font(.subheadline.weight(.semibold))
                    if let notes = card.optionDetails, notes.indices.contains(index), !notes[index].isEmpty {
                        Text(notes[index]).font(.caption).foregroundStyle(BuddyStyle.muted)
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .fixedSize(horizontal: false, vertical: true)
                Image(systemName: selected ? "checkmark.circle.fill" : "chevron.right")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(selected ? BuddyStyle.success : BuddyStyle.muted)
                    .padding(.top, 3)
            }
            .foregroundStyle(BuddyStyle.text)
            .padding(12)
            .background(selected ? BuddyStyle.success.opacity(0.1) : BuddyStyle.raised,
                        in: RoundedRectangle(cornerRadius: 12))
            .overlay {
                RoundedRectangle(cornerRadius: 12)
                    .stroke(selected ? BuddyStyle.success : BuddyStyle.border, lineWidth: 1)
            }
            .contentShape(RoundedRectangle(cornerRadius: 12))
        }
        .buttonStyle(.plain)
        .accessibilityValue(selected ? "Selected" : "")
        .disabled(busy || card.choice != nil)
    }

    @ViewBuilder
    private func confirmationButtons(destructive: Bool) -> some View {
        if let choice = card.choice {
            Label(choice == 1 ? "Cancelled" : "Confirmed", systemImage: choice == 1 ? "xmark.circle" : "checkmark.circle")
                .font(.subheadline.weight(.medium))
                .foregroundStyle(BuddyStyle.muted)
        } else {
            HStack(spacing: 10) {
                Button(card.options[1]) { onChoice(1) }.buttonStyle(.bordered)
                Button(card.options[0], role: destructive ? .destructive : nil) { onChoice(0) }
                    .buttonStyle(.borderedProminent)
            }
            .frame(maxWidth: .infinity, alignment: .trailing)
            .disabled(busy)
        }
    }
}
