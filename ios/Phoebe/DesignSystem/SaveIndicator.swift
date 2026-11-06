import SwiftUI

enum SaveState: Sendable, Equatable {
    case saving, local, syncing, synced, offline, notSynced

    var systemImage: String {
        switch self {
        case .saving: "arrow.down.doc"
        case .local: "checkmark"
        case .syncing: "arrow.triangle.2.circlepath"
        case .synced: "checkmark.icloud"
        case .offline: "wifi.slash"
        case .notSynced: "exclamationmark.icloud"
        }
    }

    var key: String {
        switch self {
        case .saving: "save.saving"
        case .local: "save.local"
        case .syncing: "save.syncing"
        case .synced: "save.synced"
        case .offline: "save.offline"
        case .notSynced: "save.notSynced"
        }
    }

    var tone: Color {
        switch self {
        case .offline: Palette.warning
        case .notSynced: Palette.danger
        default: Palette.textSecondary
        }
    }
}

struct SaveIndicator: View {
    let state: SaveState

    var body: some View {
        Label { Text(L(state.key)) } icon: { Image(systemName: state.systemImage) }
            .font(Typography.caption)
            .foregroundStyle(state.tone)
            .lineLimit(2)
            .accessibilityElement(children: .combine)
            .onChange(of: state) { _, new in
                guard new == .notSynced else { return }
                AccessibilityNotification.Announcement(LS(new.key)).post()
            }
    }
}

#Preview {
    VStack(alignment: .leading) {
        SaveIndicator(state: .local)
        SaveIndicator(state: .syncing)
        SaveIndicator(state: .synced)
        SaveIndicator(state: .offline)
        SaveIndicator(state: .notSynced)
    }
    .padding()
}
