import AVFoundation
import SwiftUI

@MainActor @Observable
final class SourcePlayer {
    enum State: Equatable { case loading, ready, failed(String) }

    private(set) var state: State = .loading
    private(set) var isPlaying = false
    @ObservationIgnored private var player: AVPlayer?
    @ObservationIgnored private var observer: Any?

    func prepare(meetingID: UUID, api: APIClient, online: Bool, recording: Bool) async {
        guard !recording else {
            state = .failed("player.recording")
            return
        }
        if let url = EditorAudioFile.url(meetingID: meetingID) {
            setup(url)
            return
        }
        guard online else {
            state = .failed("player.offline")
            return
        }
        do {
            let signed = try await api.send(Endpoints.audioURL(meetingID: meetingID))
            let (tmp, response) = try await URLSession.shared.download(from: signed.url)
            guard let http = response as? HTTPURLResponse, (200..<300).contains(http.statusCode),
                  let dir = EditorAudioFile.cacheDirectory else {
                state = .failed("player.error")
                return
            }
            try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
            let dest = dir.appending(path: "\(meetingID.lower).\(Self.fileExtension(mime: http.mimeType))")
            try? FileManager.default.removeItem(at: dest)
            try FileManager.default.moveItem(at: tmp, to: dest)
            setup(dest)
        } catch APIError.audioUnavailable {
            state = .failed("player.expired")
        } catch {
            state = .failed("player.error")
        }
    }

    static func fileExtension(mime: String?) -> String {
        switch mime?.lowercased() {
        case "audio/mpeg", "audio/mp3": "mp3"
        case "audio/wav", "audio/x-wav", "audio/wave": "wav"
        default: "m4a"
        }
    }

    private func setup(_ url: URL) {
        let player = AVPlayer(url: url)
        observer = player.addPeriodicTimeObserver(forInterval: CMTime(seconds: 0.5, preferredTimescale: 600), queue: .main) { [weak self] _ in
            MainActor.assumeIsolated {
                guard let self, let player = self.player else { return }
                self.isPlaying = player.timeControlStatus != .paused
            }
        }
        self.player = player
        state = .ready
    }

    func play(from ms: Int) {
        guard let player else { return }
        try? AVAudioSession.sharedInstance().setCategory(.playback, mode: .spokenAudio)
        try? AVAudioSession.sharedInstance().setActive(true)
        player.seek(to: CMTime(value: CMTimeValue(ms), timescale: 1000), toleranceBefore: .zero, toleranceAfter: .zero)
        player.play()
        isPlaying = true
    }

    func toggle(from ms: Int) {
        if isPlaying {
            player?.pause()
            isPlaying = false
        } else {
            play(from: ms)
        }
    }

    func stop() {
        player?.pause()
        if let observer { player?.removeTimeObserver(observer) }
        observer = nil
        player = nil
        isPlaying = false
        try? AVAudioSession.sharedInstance().setActive(false, options: .notifyOthersOnDeactivation)
    }
}

struct SourcePlayerSheet: View {
    let request: SourcePlayRequest
    @Environment(AppModel.self) private var app
    @State private var player = SourcePlayer()

    var body: some View {
        HStack(alignment: .top, spacing: Spacing.md) {
            Button { player.toggle(from: request.startMs) } label: {
                Image(systemName: player.isPlaying ? "pause.circle.fill" : "play.circle.fill")
                    .font(.largeTitle)
                    .foregroundStyle(Palette.accent)
                    .minTouchTarget()
            }
            .buttonStyle(.plain)
            .disabled(player.state != .ready)
            .accessibilityLabel(Text(L(player.isPlaying ? "player.pause" : "player.play", table: .editor)))
            VStack(alignment: .leading, spacing: Spacing.xs) {
                Text(EditorText.plain("\(Formatting.timestamp(ms: request.startMs, approximate: request.approximate))–\(Formatting.timestamp(ms: request.endMs, approximate: request.approximate))"))
                    .font(Typography.caption.monospacedDigit())
                    .foregroundStyle(Palette.textSecondary)
                    .accessibilityLabel(Text(verbatim: request.approximate
                        ? "\(Formatting.spokenTimestamp(ms: request.startMs, approximate: true)) – \(Formatting.spokenTimestamp(ms: request.endMs, approximate: true))"
                        : "\(Formatting.timestamp(ms: request.startMs))–\(Formatting.timestamp(ms: request.endMs))"))
                if let text = request.text {
                    Text(EditorText.plain(text))
                        .font(Typography.callout)
                        .foregroundStyle(Palette.textPrimary)
                        .lineLimit(3)
                }
                switch player.state {
                case .loading:
                    ProgressView { Text(L("player.loading", table: .editor)).font(Typography.footnote) }
                case .failed(let key):
                    Label { Text(L(key, table: .editor)) } icon: { Image(systemName: "exclamationmark.circle") }
                        .font(Typography.footnote)
                        .foregroundStyle(Palette.danger)
                case .ready:
                    EmptyView()
                }
            }
            Spacer(minLength: 0)
        }
        .padding(Spacing.lg)
        .presentationDetents([.height(160)])
        .presentationDragIndicator(.visible)
        .task {
            await player.prepare(meetingID: request.meetingID, api: app.api, online: app.isOnline, recording: app.recorder.phase.hasLiveSession)
            player.play(from: request.startMs)
        }
        .onDisappear { player.stop() }
    }
}
