import SwiftUI

struct SourceReferenceButton: View {
    let ref: SourceRef
    var isVerified = true
    var isPlayable = true
    let onSeek: (Int) -> Void

    init(ref: SourceRef, isVerified: Bool = true, isPlayable: Bool = true, onSeek: @escaping (Int) -> Void) {
        self.ref = ref
        self.isVerified = isVerified
        self.isPlayable = isPlayable
        self.onSeek = onSeek
    }

    var body: some View {
        switch ref {
        case .transcript where !isVerified:
            Text(L("ref.unverified"))
                .font(Typography.caption)
                .foregroundStyle(Palette.textSecondary)
        case .transcript(_, let startMs, _):
            let stamp = Formatting.timestamp(ms: startMs)
            if isPlayable {
                Button { onSeek(startMs) } label: {
                    Label(stamp, systemImage: "clock")
                        .font(Typography.caption.monospacedDigit())
                        .foregroundStyle(Palette.accent)
                        .minTouchTarget()
                }
                .buttonStyle(.plain)
                .accessibilityLabel(Text(L("ref.playAt", stamp)))
            } else {
                Label(stamp, systemImage: "clock")
                    .font(Typography.caption.monospacedDigit())
                    .foregroundStyle(Palette.textSecondary)
            }
        case .userNotes:
            Label { Text(L("ref.fromNotes")) } icon: { Image(systemName: "note.text.badge.plus") }
                .font(Typography.caption)
                .foregroundStyle(Palette.textSecondary)
        }
    }
}

#Preview {
    VStack(alignment: .leading) {
        SourceReferenceButton(ref: .transcript(segmentId: "s1", startMs: 724_000, endMs: 730_000)) { _ in }
        SourceReferenceButton(ref: .transcript(segmentId: "s1", startMs: 0, endMs: 1), isVerified: false) { _ in }
        SourceReferenceButton(ref: .userNotes(noteId: "n1")) { _ in }
    }
    .padding()
}
