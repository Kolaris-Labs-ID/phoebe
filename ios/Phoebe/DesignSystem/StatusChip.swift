import SwiftUI

enum FailedStage: String, Sendable, CaseIterable {
    case upload, transcription, notes, mom
}

enum ChipStatus: Equatable, Sendable {
    case localOnly, waitingConnection, queued, uploading(percent: Int), verifying
    case waitingQuota, planExpired, waitingStorage, orphaned, importing
    case transcribing, transcriptReady, generatingNotes, notesReady, generatingMom
    case momDraft, reviewed, failed(FailedStage), noSpeech
    case recording, interrupted, outdated

    var systemImage: String {
        switch self {
        case .localOnly: "iphone"
        case .waitingConnection: "wifi.slash"
        case .queued: "icloud.and.arrow.up"
        case .uploading: "arrow.up.circle"
        case .verifying: "checkmark.icloud"
        case .waitingQuota: "gauge.with.dots.needle.100percent"
        case .planExpired: "calendar.badge.exclamationmark"
        case .waitingStorage: "externaldrive.badge.exclamationmark"
        case .orphaned: "iphone.slash"
        case .importing: "square.and.arrow.down"
        case .transcribing: "waveform"
        case .transcriptReady: "text.quote"
        case .generatingNotes, .generatingMom: "sparkles"
        case .notesReady: "note.text"
        case .momDraft: "pencil.circle"
        case .reviewed: "checkmark.seal.fill"
        case .failed: "exclamationmark.triangle"
        case .noSpeech: "waveform.slash"
        case .recording: "circle.fill"
        case .interrupted: "phone.down.circle"
        case .outdated: "clock.arrow.circlepath"
        }
    }

    var tone: Tone {
        switch self {
        case .localOnly, .queued, .momDraft, .noSpeech: .neutral
        case .uploading, .verifying, .transcribing, .generatingNotes, .generatingMom, .importing: .info
        case .transcriptReady, .notesReady, .reviewed: .success
        case .waitingConnection, .waitingQuota, .planExpired, .waitingStorage, .orphaned, .interrupted, .outdated: .warning
        case .failed: .danger
        case .recording: .record
        }
    }

    var text: LocalizedStringResource {
        switch self {
        case .uploading(let percent): L("status.uploading", Int64(percent))
        case .failed(let stage): L("status.failed.\(stage.rawValue)")
        default: L("status.\(keySuffix)")
        }
    }

    private var keySuffix: String {
        switch self {
        case .localOnly: "localOnly"
        case .waitingConnection: "waitingConnection"
        case .queued: "queued"
        case .uploading: "uploading"
        case .verifying: "verifying"
        case .waitingQuota: "waitingQuota"
        case .planExpired: "planExpired"
        case .waitingStorage: "waitingStorage"
        case .orphaned: "orphaned"
        case .importing: "importing"
        case .transcribing: "transcribing"
        case .transcriptReady: "transcriptReady"
        case .generatingNotes: "generatingNotes"
        case .notesReady: "notesReady"
        case .generatingMom: "generatingMom"
        case .momDraft: "momDraft"
        case .reviewed: "reviewed"
        case .failed(let stage): "failed.\(stage.rawValue)"
        case .noSpeech: "noSpeech"
        case .recording: "recording"
        case .interrupted: "interrupted"
        case .outdated: "outdated"
        }
    }
}

struct StatusChip: View {
    let text: LocalizedStringResource
    let systemImage: String
    let tone: Tone

    init(_ text: LocalizedStringResource, systemImage: String, tone: Tone) {
        self.text = text
        self.systemImage = systemImage
        self.tone = tone
    }

    init(_ status: ChipStatus) {
        self.init(status.text, systemImage: status.systemImage, tone: status.tone)
    }

    var body: some View {
        HStack(spacing: Spacing.xs) {
            Image(systemName: systemImage)
                .symbolRenderingMode(.hierarchical)
                .imageScale(.small)
            Text(text)
        }
        .font(Typography.caption)
        .foregroundStyle(tone.foreground)
        .padding(.vertical, Spacing.xs)
        .padding(.horizontal, Spacing.sm)
        .background(tone.fill, in: RoundedRectangle(cornerRadius: Radius.sm, style: .continuous))
        .accessibilityElement(children: .combine)
    }
}

#Preview {
    VStack(alignment: .leading) {
        StatusChip(.uploading(percent: 45))
        StatusChip(.notesReady)
        StatusChip(.failed(.transcription))
        StatusChip(.recording)
        StatusChip(.outdated)
    }
    .padding()
}
