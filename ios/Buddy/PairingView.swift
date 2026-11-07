import AVFoundation
import SwiftUI
import VisionKit

struct PairingView: View {
    @Environment(BuddyHome.self) private var home
    @State private var deviceName = "iPhone"
    @State private var link = ""
    @State private var scanning = false
    @State private var pairing = false
    @State private var error: String?

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 22) {
                    BudFace(color: .indigo, size: 76)
                    VStack(alignment: .leading, spacing: 8) {
                        Text("Your Buds, anywhere.").font(.largeTitle.bold())
                        Text("Connect this iPhone to your Buddy home to chat, check work, and catch up on your inbox.")
                            .foregroundStyle(.secondary)
                    }
                    Label("On your Mac, open Connections → iPhone → Pair iPhone.", systemImage: "macbook")
                        .font(.callout)
                    VStack(alignment: .leading, spacing: 12) {
                        TextField("Device name", text: $deviceName)
                            .textFieldStyle(.roundedBorder)
                            .accessibilityIdentifier("device-name")
                        if DataScannerViewController.isSupported {
                            Button {
                                requestScanner()
                            } label: {
                                Label("Scan pairing QR code", systemImage: "qrcode.viewfinder").frame(maxWidth: .infinity)
                            }
                            .buttonStyle(.borderedProminent)
                        }
                        TextField("Paste a pairing link", text: $link)
                            .textFieldStyle(.roundedBorder)
                            .textInputAutocapitalization(.never)
                            .autocorrectionDisabled()
                            .keyboardType(.URL)
                            .accessibilityIdentifier("pairing-link")
                        Button {
                            connect(link)
                        } label: {
                            Group {
                                if pairing { ProgressView() } else { Text("Connect") }
                            }
                            .frame(maxWidth: .infinity)
                        }
                        .buttonStyle(.bordered)
                        .disabled(link.isEmpty || deviceName.trimmingCharacters(in: .whitespaces).isEmpty)
                    }
                    .disabled(pairing)
                    if let error { Label(error, systemImage: "exclamationmark.circle").foregroundStyle(.red).font(.callout) }
                }
                .frame(maxWidth: 460, alignment: .leading)
                .padding(24)
            }
            .navigationTitle("Buddy")
            .sheet(isPresented: $scanning) {
                NavigationStack {
                    PairingScanner { value in
                        scanning = false
                        link = value
                        connect(value)
                    } onFailure: {
                        scanning = false
                        error = "The camera is unavailable. Paste the pairing link from Buddy on your Mac."
                    }
                    .navigationTitle("Scan pairing code")
                    .navigationBarTitleDisplayMode(.inline)
                    .toolbar { ToolbarItem(placement: .cancellationAction) { Button("Cancel") { scanning = false } } }
                }
            }
        }
    }

    private func requestScanner() {
        Task {
            let allowed = await AVCaptureDevice.requestAccess(for: .video)
            if allowed && DataScannerViewController.isAvailable {
                scanning = true
            } else {
                error = "Allow camera access in iPhone Settings, or paste the pairing link from your Mac."
            }
        }
    }

    private func connect(_ value: String) {
        guard !pairing else { return }
        pairing = true
        error = nil
        Task {
            defer { pairing = false }
            do {
                try await home.pair(PairingLink(value), name: deviceName)
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}

private struct PairingScanner: UIViewControllerRepresentable {
    var onLink: (String) -> Void
    var onFailure: () -> Void

    func makeCoordinator() -> Coordinator { Coordinator(onLink: onLink, onFailure: onFailure) }

    func makeUIViewController(context: Context) -> DataScannerViewController {
        let scanner = DataScannerViewController(
            recognizedDataTypes: [.barcode(symbologies: [.qr])],
            qualityLevel: .balanced, recognizesMultipleItems: false,
            isGuidanceEnabled: true, isHighlightingEnabled: true
        )
        scanner.delegate = context.coordinator
        Task { @MainActor in
            do { try scanner.startScanning() }
            catch { context.coordinator.fail() }
        }
        return scanner
    }

    func updateUIViewController(_ controller: DataScannerViewController, context: Context) {}

    static func dismantleUIViewController(_ controller: DataScannerViewController, coordinator: Coordinator) {
        controller.stopScanning()
    }

    final class Coordinator: NSObject, DataScannerViewControllerDelegate {
        var handled = false
        let onLink: (String) -> Void
        let onFailure: () -> Void

        init(onLink: @escaping (String) -> Void, onFailure: @escaping () -> Void) {
            self.onLink = onLink
            self.onFailure = onFailure
        }

        func dataScanner(_ scanner: DataScannerViewController, didAdd items: [RecognizedItem], allItems: [RecognizedItem]) {
            for item in items {
                guard case let .barcode(code) = item, let value = code.payloadStringValue,
                      value.hasPrefix("buddy-pair://"), !handled else { continue }
                handled = true
                scanner.stopScanning()
                onLink(value)
            }
        }

        func dataScanner(_ scanner: DataScannerViewController, becameUnavailableWithError error: DataScannerViewController.ScanningUnavailable) {
            fail()
        }

        func fail() {
            guard !handled else { return }
            handled = true
            onFailure()
        }
    }
}
