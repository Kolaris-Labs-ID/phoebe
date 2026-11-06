import AVFoundation
import Foundation

@MainActor @Observable
final class PlayerModel {
    enum Phase: Equatable { case idle, downloading(Double), ready, failed }

    private(set) var phase: Phase = .idle
    private(set) var isPlaying = false
    private(set) var currentMs = 0
    private(set) var durationMs = 0

    @ObservationIgnored private var player: AVAudioPlayer?
    @ObservationIgnored private var ticker: Task<Void, Never>?

    func configure(durationMs: Int?) {
        if player == nil, let durationMs { self.durationMs = durationMs }
    }

    func togglePlay(meetingID: UUID, availability: PlaybackAvailability, app: AppModel) async {
        if isPlaying { pause() } else { await play(from: nil, meetingID: meetingID, availability: availability, app: app) }
    }

    func play(from ms: Int?, meetingID: UUID, availability: PlaybackAvailability, app: AppModel) async {
        guard availability.isPlayable, !app.recorder.phase.hasLiveSession else { return }
        if player == nil || phase == .failed {
            guard await load(meetingID: meetingID, availability: availability, app: app) else { return }
        }
        guard let player else { return }
        if let ms { player.currentTime = TimeInterval(ms) / 1000 }
        do {
            try AVAudioSession.sharedInstance().setCategory(.playback, mode: .spokenAudio)
            try AVAudioSession.sharedInstance().setActive(true)
        } catch {
            phase = .failed
            return
        }
        if player.play() {
            isPlaying = true
            startTicker()
        } else {
            phase = .failed
        }
        sync()
    }

    func pause() {
        player?.pause()
        isPlaying = false
        ticker?.cancel()
        sync()
    }

    func seek(to ms: Int) {
        guard let player else {
            currentMs = ms
            return
        }
        player.currentTime = TimeInterval(min(max(0, ms), durationMs)) / 1000
        sync()
    }

    func skip(by deltaMs: Int) { seek(to: currentMs + deltaMs) }

    func stop() {
        player?.stop()
        player = nil
        isPlaying = false
        ticker?.cancel()
        phase = .idle
    }

    private func load(meetingID: UUID, availability: PlaybackAvailability, app: AppModel) async -> Bool {
        let url: URL
        switch availability {
        case .local(let local):
            url = local
        case .cloud:
            phase = .downloading(0)
            do {
                let signed = try await app.api.send(Endpoints.audioURL(meetingID: meetingID))
                url = try await Self.download(signed.url, meetingID: meetingID) { [weak self] p in
                    Task { @MainActor in
                        if case .downloading = self?.phase { self?.phase = .downloading(p) }
                    }
                }
            } catch {
                phase = .failed
                return false
            }
        default:
            return false
        }
        do {
            let p = try AVAudioPlayer(contentsOf: url)
            p.prepareToPlay()
            player = p
            durationMs = Int(p.duration * 1000)
            if currentMs > 0 { p.currentTime = TimeInterval(currentMs) / 1000 }
            phase = .ready
            return true
        } catch {
            if case .cloud = availability { try? FileManager.default.removeItem(at: url) }
            phase = .failed
            return false
        }
    }

    private func startTicker() {
        ticker?.cancel()
        ticker = Task { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(for: .milliseconds(250))
                guard let self else { return }
                self.sync()
                if self.player?.isPlaying != true {
                    self.isPlaying = false
                    return
                }
            }
        }
    }

    private func sync() {
        guard let player else { return }
        currentMs = Int(player.currentTime * 1000)
    }

    // MARK: Files

    static func cacheDirectory() -> URL {
        FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask)[0].appendingPathComponent("audio", isDirectory: true)
    }

    static func cachedFile(meetingID: UUID) -> URL? {
        let dir = cacheDirectory()
        let prefix = meetingID.lower + "."
        let files = (try? FileManager.default.contentsOfDirectory(atPath: dir.path)) ?? []
        return files.first { $0.hasPrefix(prefix) }.map { dir.appendingPathComponent($0) }
    }

    static func removeCache(meetingID: UUID) {
        if let file = cachedFile(meetingID: meetingID) { try? FileManager.default.removeItem(at: file) }
    }

    static func localFile(meetingID: UUID, uploadPath: String?) -> URL? {
        let fm = FileManager.default
        let support = fm.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
        let dir = support.appendingPathComponent("recordings", isDirectory: true).appendingPathComponent(meetingID.lower, isDirectory: true)
        var candidates: [URL] = []
        if let uploadPath {
            candidates.append(uploadPath.hasPrefix("/") ? URL(fileURLWithPath: uploadPath) : support.appendingPathComponent(uploadPath))
        }
        candidates.append(dir.appendingPathComponent("audio.m4a"))
        let upperDir = support.appendingPathComponent("recordings", isDirectory: true).appendingPathComponent(meetingID.uuidString, isDirectory: true)
        candidates.append(upperDir.appendingPathComponent("audio.m4a"))
        for folder in [dir, upperDir] {
            let names = (try? fm.contentsOfDirectory(atPath: folder.path)) ?? []
            candidates += names.filter { $0.hasPrefix("import.") }.map { folder.appendingPathComponent($0) }
        }
        if let cached = cachedFile(meetingID: meetingID) { candidates.append(cached) }
        return candidates.first { fm.fileExists(atPath: $0.path) }
    }

    private final class ObservationBox: @unchecked Sendable {
        var observation: NSKeyValueObservation?
    }

    nonisolated static func download(_ url: URL, meetingID: UUID, progress: @escaping @Sendable (Double) -> Void) async throws -> URL {
        let box = ObservationBox()
        defer { box.observation = nil }
        let dir = FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask)[0].appendingPathComponent("audio", isDirectory: true)
        return try await withCheckedThrowingContinuation { continuation in
            let task = URLSession.shared.downloadTask(with: url) { tmp, response, error in
                if let error {
                    continuation.resume(throwing: error)
                    return
                }
                guard let tmp, let http = response as? HTTPURLResponse, (200..<300).contains(http.statusCode) else {
                    continuation.resume(throwing: URLError(.badServerResponse))
                    return
                }
                let ext = switch http.mimeType {
                case "audio/mpeg": "mp3"
                case "audio/wav", "audio/x-wav", "audio/wave": "wav"
                default: "m4a"
                }
                do {
                    try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
                    let dest = dir.appendingPathComponent("\(meetingID.lower).\(ext)")
                    try? FileManager.default.removeItem(at: dest)
                    try FileManager.default.moveItem(at: tmp, to: dest)
                    var values = URLResourceValues()
                    values.isExcludedFromBackup = true
                    var mutable = dest
                    try? mutable.setResourceValues(values)
                    continuation.resume(returning: dest)
                } catch {
                    continuation.resume(throwing: error)
                }
            }
            box.observation = task.progress.observe(\.fractionCompleted) { p, _ in progress(p.fractionCompleted) }
            task.resume()
        }
    }
}
