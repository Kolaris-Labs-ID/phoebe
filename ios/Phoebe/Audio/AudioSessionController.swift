import AVFoundation

@MainActor
protocol AudioSessioning: AnyObject {
    var permission: MicPermission { get }
    func requestPermission() async -> Bool
    func activate() throws
    func deactivate()
    var currentInputName: String? { get }
}

@MainActor
final class AudioSessionController: AudioSessioning {
    private var session: AVAudioSession { .sharedInstance() }

    var permission: MicPermission {
        switch AVAudioApplication.shared.recordPermission {
        case .granted: .granted
        case .denied: .denied
        default: .undetermined
        }
    }

    func requestPermission() async -> Bool {
        await AVAudioApplication.requestRecordPermission()
    }

    // `.record` (not `.playAndRecord`): nothing the app plays can leak into the capture, and
    // Bluetooth HFP input is worse than the built-in mic for room meetings (ios.md §6).
    func activate() throws {
        try session.setCategory(.record, mode: .default, options: [])
        try session.setPrefersNoInterruptionsFromSystemAlerts(true)
        try session.setActive(true)
    }

    func deactivate() {
        try? session.setActive(false, options: .notifyOthersOnDeactivation)
    }

    var currentInputName: String? {
        guard session.isInputAvailable else { return nil }
        return session.currentRoute.inputs.first?.portName
    }
}
