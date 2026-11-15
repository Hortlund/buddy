import SwiftUI

struct ChatCodeRunCard: View {
    let run: ChatCodeRun
    let api: BuddyAPI
    let chat: String
    let task: String
    var stopping = false
    let onViewOutput: () -> Void
    @State private var outputError: String?
    @State private var copying = false

    private var running: Bool { run.status == "running" }

    private var status: (String, Color) {
        run.displayStatus(stopping: stopping)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            header
            if let command = run.command {
                Text(command).font(.system(.footnote, design: .monospaced)).textSelection(.enabled)
            }
            if let error = run.error {
                Text(error).font(.footnote).foregroundStyle(.red).textSelection(.enabled)
            }
            if run.stdout != nil || run.stderr != nil || run.outputPreview == true {
                VStack(alignment: .leading, spacing: 8) {
                    if let output = run.stdout { preview("Output", output) }
                    if let output = run.stderr { preview("Standard error", output) }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(8)
                .background(BuddyStyle.background, in: RoundedRectangle(cornerRadius: 8))
                outputControls
                if let outputError {
                    Text(outputError).font(.caption).foregroundStyle(.red)
                }
            } else if running {
                Text("Waiting for output…").font(.caption).foregroundStyle(BuddyStyle.muted)
            }
        }
        .padding(11)
        .background(BuddyStyle.raised, in: RoundedRectangle(cornerRadius: 12))
        .overlay { RoundedRectangle(cornerRadius: 12).strokeBorder(BuddyStyle.border) }
    }

    private var outputControls: some View {
        HStack {
            if run.truncated == true {
                Text("Output truncated").font(.caption).foregroundStyle(BuddyStyle.muted)
            } else if run.outputPreview == true {
                Text("Output preview").font(.caption).foregroundStyle(BuddyStyle.muted)
            }
            Spacer()
            Button("View output", action: onViewOutput)
                .font(.caption.weight(.medium))
                .buttonStyle(.borderless)
            Button {
                copyOutput()
            } label: {
                Image(systemName: "document.on.document")
                    .font(.caption.weight(.medium))
                    .accessibilityLabel("Copy output")
            }
            .buttonStyle(.borderless)
            .disabled(copying)
        }
    }

    private var header: some View {
        HStack(spacing: 6) {
            Label(run.workCommand == true ? "Command" : "Code run", systemImage: "terminal")
                .font(.footnote.weight(.semibold))
                .layoutPriority(1)
            Spacer(minLength: 4)
            if running { ProgressView().controlSize(.small).accessibilityHidden(true) }
            VStack(alignment: .trailing, spacing: 2) {
                Text(status.0).font(.caption.weight(.semibold)).foregroundStyle(status.1)
                if let exit = run.exitCode {
                    Text("Exit \(exit)").font(.caption2).foregroundStyle(BuddyStyle.muted)
                }
            }
            if let end = run.finishedAt {
                elapsed(at: end)
            } else if running {
                TimelineView(.periodic(from: .now, by: 1)) { elapsed(at: $0.date) }
            }
        }
        .accessibilityElement(children: .combine)
    }

    private func elapsed(at date: Date) -> some View {
        Text("\(Int(max(0, date.timeIntervalSince(run.startedAt))))s")
            .font(.caption.monospacedDigit())
            .foregroundStyle(BuddyStyle.muted)
    }

    private func preview(_ title: String, _ output: String) -> some View {
        let lines = output.trimmingCharacters(in: .newlines).split(separator: "\n", omittingEmptySubsequences: false)
        return codeRunOutput(lines.count > 6 ? "\(title) · Last 6 lines" : title, lines.suffix(6).joined(separator: "\n"), lineLimit: 6)
    }

    private func copyOutput() {
        guard !copying else { return }
        copying = true
        outputError = nil
        Task {
            defer { copying = false }
            do {
                let log = run.outputPreview == true ? try await api.codeRunOutput(chat: chat, run: task) : run
                UIPasteboard.general.string = log.clipboardOutput
            } catch { outputError = error.localizedDescription }
        }
    }
}

struct ChatCodeOutputView: View {
    let api: BuddyAPI
    let chat: String
    let task: String
    @Environment(\.dismiss) private var dismiss
    @State private var log: ChatCodeRun?
    @State private var error: String?
    @State private var retry = 0

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 15) {
                    if let log {
                        if log.status == "running" {
                            ProgressView("Running").font(.footnote)
                        } else {
                            let (status, tint) = log.displayStatus()
                            Text(log.exitCode.map { "\(status) · Exit \($0)" } ?? status)
                                .font(.footnote.weight(.semibold)).foregroundStyle(tint)
                        }
                        if let failure = log.error {
                            Text(failure).font(.footnote).foregroundStyle(.red).textSelection(.enabled)
                        }
                        if let stdout = log.stdout { codeRunOutput("Output", stdout) }
                        if let stderr = log.stderr { codeRunOutput("Standard error", stderr) }
                        if log.truncated == true {
                            Text("Earlier output omitted").font(.caption).foregroundStyle(BuddyStyle.muted)
                        }
                    } else if error == nil {
                        ProgressView("Loading output…")
                    }
                    if let error {
                        Text(error).foregroundStyle(.red)
                        Button("Retry") { retry += 1 }
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding()
            }
            .navigationTitle("Run output")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    Button("Copy output") { UIPasteboard.general.string = log?.clipboardOutput }
                        .disabled(log == nil)
                }
                ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() } }
            }
            .task(id: retry) { await refreshOutput() }
        }
    }

    private func refreshOutput() async {
        error = nil
        do {
            repeat {
                let snapshot = try await api.codeRunOutput(chat: chat, run: task)
                try Task.checkCancellation()
                log = snapshot
                if snapshot.status != "running" { return }
                try await Task.sleep(for: .milliseconds(500))
            } while !Task.isCancelled
        } catch where !Task.isCancelled {
            self.error = error.localizedDescription
        } catch {}
    }
}

private func codeRunOutput(_ title: String, _ text: String, lineLimit: Int? = nil) -> some View {
    VStack(alignment: .leading, spacing: 4) {
        Text(title).font(.caption).foregroundStyle(BuddyStyle.muted)
        Text(verbatim: text)
            .font(.system(.caption, design: .monospaced))
            .lineLimit(lineLimit)
            .textSelection(.enabled)
    }
}

private extension ChatCodeRun {
    func displayStatus(stopping: Bool = false) -> (String, Color) {
        switch status {
        case "succeeded": ("Finished", BuddyStyle.success)
        case "failed": ("Failed", .red)
        case "cancelled": ("Stopped", BuddyStyle.muted)
        case "interrupted": ("Interrupted", BuddyStyle.accent)
        default: (stopping ? "Stopping" : "Running", BuddyStyle.accent)
        }
    }

    var clipboardOutput: String {
        [stdout, stderr.map { error in
            stdout == nil ? error : "Standard error\n" + error
        }].compactMap { $0 }.joined(separator: "\n\n")
    }
}
