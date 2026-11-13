import AVFoundation
import SwiftUI
import VisionKit

struct PairingView: View {
    @Environment(BuddyHome.self) private var home
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @ScaledMetric(relativeTo: .largeTitle) private var titleSize = 42
    @State private var deviceName = "iPhone"
    @State private var link = ""
    @State private var scanning = false
    @State private var pairing = false
    @State private var error: String?
    @State private var appeared = false
    @State private var floating = false
    @State private var showLink = !DataScannerViewController.isSupported

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(spacing: 26) {
                    hero
                    pairingForm
                        .opacity(appeared || reduceMotion ? 1 : 0)
                        .offset(y: appeared || reduceMotion ? 0 : 12)
                }
                .frame(maxWidth: 460)
                .padding(.horizontal, 24)
                .padding(.vertical, 28)
                .frame(maxWidth: .infinity)
            }
            .background {
                BuddyStyle.background.ignoresSafeArea()
                RadialGradient(colors: [BuddyStyle.accent.opacity(0.14), .clear],
                               center: .top, startRadius: 24, endRadius: 360).ignoresSafeArea()
            }
            .toolbar(.hidden, for: .navigationBar)
            .task(id: reduceMotion) {
                withAnimation(reduceMotion ? nil : .easeOut(duration: 0.38)) { appeared = true }
                if reduceMotion {
                    floating = false
                } else {
                    withAnimation(.easeInOut(duration: 2.6).repeatForever(autoreverses: true)) { floating = true }
                }
            }
            .sheet(isPresented: $scanning) {
                NavigationStack {
                    PairingScanner { value in
                        scanning = false
                        link = value
                        connect(value)
                    } onFailure: {
                        scanning = false
                        error = "The camera is unavailable. Paste the pairing link from Buddy on your Mac."
                        showLink = true
                    }
                    .navigationTitle("Scan pairing code")
                    .navigationBarTitleDisplayMode(.inline)
                    .toolbar { ToolbarItem(placement: .cancellationAction) { Button("Cancel") { scanning = false } } }
                }
            }
        }
    }

    private var hero: some View {
        VStack(spacing: 13) {
            ZStack {
                Circle().fill(.linearGradient(colors: [BuddyStyle.accent.opacity(0.24), BuddyStyle.accent.opacity(0.04)],
                                               startPoint: .topLeading, endPoint: .bottomTrailing))
                BudFace(appearance: BudAppearance(color: "plum", shape: "cloud"), size: 87)
                    .offset(y: floating && !reduceMotion ? -4 : 4)
            }
            .frame(width: 124, height: 124)
            .accessibilityHidden(true)

            Text("Connect to Buddy")
                .font(.system(size: titleSize, weight: .bold))
                .tracking(-1.8)
                .accessibilityAddTraits(.isHeader)
        }
        .multilineTextAlignment(.center)
        .padding(.top, 16)
    }

    private var pairingForm: some View {
        VStack(alignment: .leading, spacing: 13) {
            Text("On your Mac, open Connections → Home & devices → Pair iPhone.")
                .font(.footnote)
                .foregroundStyle(BuddyStyle.muted)
            TextField("Device name", text: $deviceName)
                .textFieldStyle(.plain)
                .accessibilityLabel("Device name")
                .padding(12)
                .background(BuddyStyle.raised, in: RoundedRectangle(cornerRadius: 11))
                .accessibilityIdentifier("device-name")
                .disabled(pairing)

            if DataScannerViewController.isSupported {
                Button(action: requestScanner) {
                    Label("Scan pairing QR code", systemImage: "qrcode.viewfinder")
                        .frame(maxWidth: .infinity)
                }
                .buttonStyle(.borderedProminent)
                .foregroundStyle(BuddyStyle.accentText)
                .controlSize(.large)
                .disabled(pairing || deviceName.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
            }
            if showLink {
                HStack(spacing: 8) {
                    TextField("Pairing link", text: $link)
                        .textFieldStyle(.plain)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .keyboardType(.URL)
                        .padding(12)
                        .frame(minHeight: 44)
                        .background(BuddyStyle.raised, in: RoundedRectangle(cornerRadius: 11))
                        .accessibilityIdentifier("pairing-link")
                    PasteButton(payloadType: String.self) { strings in
                        if let value = strings.first { link = value }
                    }
                    .labelStyle(.iconOnly)
                    .accessibilityLabel("Paste pairing link")
                }
                .disabled(pairing)
                Button { connect(link) } label: {
                    Group {
                        if pairing { ProgressView().accessibilityLabel("Connecting") }
                        else { Text("Connect") }
                    }
                    .frame(maxWidth: .infinity)
                }
                .buttonStyle(.bordered)
                .controlSize(.large)
                .disabled(pairing || link.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ||
                          deviceName.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
            } else {
                Button("Use a pairing link") {
                    withAnimation(reduceMotion ? nil : .easeInOut(duration: 0.24)) { showLink = true }
                }
                .frame(maxWidth: .infinity, minHeight: 44)
                .disabled(pairing)
            }
            if let error {
                Label(error, systemImage: "exclamationmark.circle").foregroundStyle(.red).font(.callout)
                    .accessibilityIdentifier("pairing-error")
            }
        }
        .padding(18)
        .background(BuddyStyle.surface, in: RoundedRectangle(cornerRadius: 22))
        .overlay(RoundedRectangle(cornerRadius: 22).strokeBorder(BuddyStyle.border))
    }

    private func requestScanner() {
        Task {
            let allowed = await AVCaptureDevice.requestAccess(for: .video)
            if allowed && DataScannerViewController.isAvailable {
                scanning = true
            } else {
                error = "Allow camera access in iPhone Settings, or paste the pairing link from your Mac."
                showLink = true
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
