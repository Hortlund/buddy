import AVFoundation
import Foundation
import OSLog

struct MicPacket: Sendable {
    let pcm: Data
    let level: Float
}

/// Owns the native audio graph. Network PCM is always mono, signed PCM16 at 24 kHz.
@MainActor
final class CallAudio {
    private let engine = AVAudioEngine()
    private let player = AVAudioPlayerNode()
    private let format = AVAudioFormat(standardFormatWithSampleRate: 24_000, channels: 1)!
    private var finishInput: (() -> Void)?
    private var tapped = false
    private var generation = UUID()
    private var item = ""
    private var pending = 0
    private var frames = 0
    private var completedFrames = 0
    private var ended = false
    private var playbackStart: AVAudioFramePosition = 0
    var onDrained: ((String) -> Void)?
    private let log = Logger(subsystem: "dev.buddy.ios", category: "CallAudio")

    static func permission() async -> Bool {
        await withCheckedContinuation { continuation in
            AVAudioApplication.requestRecordPermission { continuation.resume(returning: $0) }
        }
    }

    func start() throws -> AsyncStream<MicPacket> {
        let session = AVAudioSession.sharedInstance()
        try session.setCategory(.playAndRecord, mode: .voiceChat, options: [.defaultToSpeaker, .allowBluetoothHFP])
        try session.setActive(true)
        let input = engine.inputNode
        // Voice processing supplies echo cancellation on iPhone. Simulator audio
        // hardware may not implement it, so keep its regular input graph usable.
        #if !targetEnvironment(simulator)
        try input.setVoiceProcessingEnabled(true)
        #endif
        let source = input.outputFormat(forBus: 0)
        guard source.sampleRate > 0, source.channelCount > 0,
              let target = AVAudioFormat(commonFormat: .pcmFormatInt16, sampleRate: 24_000, channels: 1, interleaved: false),
              let converter = AVAudioConverter(from: source, to: target) else {
            throw BuddyError("No microphone input is available. Check your iPhone’s audio device.")
        }
        let (stream, continuation) = AsyncStream<MicPacket>.makeStream(bufferingPolicy: .bufferingOldest(32))
        finishInput = { continuation.finish() }
        input.installTap(onBus: 0, bufferSize: 1024, format: source) { buffer, _ in
            let capacity = AVAudioFrameCount(Double(buffer.frameLength) * 24_000 / source.sampleRate + 64)
            guard let output = AVAudioPCMBuffer(pcmFormat: target, frameCapacity: capacity) else { return }
            var supplied = false
            var failure: NSError?
            let status = converter.convert(to: output, error: &failure) { _, result in
                if supplied { result.pointee = .noDataNow; return nil }
                supplied = true
                result.pointee = .haveData
                return buffer
            }
            guard status != .error, failure == nil, output.frameLength > 0,
                  let samples = output.int16ChannelData?[0] else { return }
            let count = Int(output.frameLength)
            var square: Float = 0
            for index in 0..<count {
                let sample = Float(samples[index]) / 32768
                square += sample * sample
            }
            let packet = MicPacket(pcm: Data(bytes: samples, count: count * 2), level: min(1, sqrt(square / Float(count)) * 5))
            if case .dropped = continuation.yield(packet) {
                // A stalled upload must not accumulate seconds of stale speech.
                continuation.finish()
            }
        }
        tapped = true
        engine.attach(player)
        engine.connect(player, to: engine.mainMixerNode, format: format)
        engine.prepare()
        try engine.start()
        player.play()
        return stream
    }

    func begin(_ id: String) {
        _ = cut()
        item = id
        ended = false
        playbackStart = currentSample()
    }

    func enqueue(_ pcm: Data) throws {
        guard !item.isEmpty, pcm.count % 2 == 0 else { return }
        let count = pcm.count / 2
        guard count > 0, frames - completedFrames + count <= 24_000 * 45,
              let buffer = AVAudioPCMBuffer(pcmFormat: format, frameCapacity: AVAudioFrameCount(count)),
              let samples = buffer.floatChannelData?[0] else {
            throw BuddyError("Voice playback fell behind. Please reconnect the call.")
        }
        pcm.withUnsafeBytes { bytes in
            for index in 0..<count {
                let value = Int16(littleEndian: bytes.loadUnaligned(fromByteOffset: index * 2, as: Int16.self))
                samples[index] = Float(value) / 32768
            }
        }
        buffer.frameLength = AVAudioFrameCount(count)
        frames += count
        pending += 1
        let revision = generation
        player.scheduleBuffer(buffer, completionCallbackType: .dataPlayedBack) { [weak self] _ in
            Task { @MainActor in
                guard let self, self.generation == revision else { return }
                self.pending -= 1
                self.completedFrames += count
                self.drain()
            }
        }
        if !player.isPlaying { player.play() }
    }

    func end(_ id: String) {
        guard item == id else { return }
        log.info("Output ended frames=\(self.frames) pending=\(self.pending) completed=\(self.completedFrames)")
        ended = true
        drain()
    }

    private func drain() {
        guard ended, pending == 0, !item.isEmpty else { return }
        let finished = item
        item = ""
        log.info("Playback drained frames=\(self.completedFrames)")
        onDrained?(finished)
    }

    private func currentSample() -> AVAudioFramePosition {
        guard let render = player.lastRenderTime, let time = player.playerTime(forNodeTime: render) else { return 0 }
        return time.sampleTime
    }

    @discardableResult
    func cut() -> Int {
        let played = max(completedFrames, min(frames, Int(currentSample() - playbackStart)))
        generation = UUID()
        player.stop()
        item = ""
        pending = 0
        frames = 0
        completedFrames = 0
        ended = false
        if engine.isRunning { player.play() }
        return max(0, played / 24)
    }

    func stop() {
        finishInput?()
        finishInput = nil
        onDrained = nil
        _ = cut()
        if tapped { engine.inputNode.removeTap(onBus: 0); tapped = false }
        engine.stop()
        try? AVAudioSession.sharedInstance().setActive(false, options: .notifyOthersOnDeactivation)
    }
}
