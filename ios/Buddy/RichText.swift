import SwiftUI

struct RichText: View {
    private enum Block {
        case text(String)
        case code(String, String)
    }

    private let blocks: [Block]
    private let bud: String?
    private let basePath: String

    init(_ text: String, bud: String? = nil, basePath: String = ".") {
        self.bud = bud
        self.basePath = basePath
        var blocks: [Block] = []
        var lines: [String] = []
        var language: String?
        for line in text.components(separatedBy: .newlines) {
            if line.hasPrefix("```") {
                let content = lines.joined(separator: "\n")
                if let kind = language {
                    blocks.append(.code(kind, content))
                    language = nil
                } else {
                    if !content.isEmpty { blocks.append(.text(content)) }
                    language = String(line.dropFirst(3)).trimmingCharacters(in: .whitespaces)
                }
                lines.removeAll(keepingCapacity: true)
            } else {
                lines.append(line)
            }
        }
        let tail = lines.joined(separator: "\n")
        if let language { blocks.append(.code(language, tail)) }
        else if !tail.isEmpty { blocks.append(.text(tail)) }
        self.blocks = blocks
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            ForEach(blocks.indices, id: \.self) { index in
                switch blocks[index] {
                case let .text(text):
                    Text(linkedText(text))
                        .textSelection(.enabled)
                case let .code(language, code):
                    CodeBlock(language: language, code: code)
                }
            }
        }
    }

    private func linkedText(_ source: String) -> AttributedString {
        var result = (try? AttributedString(markdown: source,
                                           options: .init(interpretedSyntax: .inlineOnlyPreservingWhitespace))) ?? AttributedString(source)
        // Foundation's Markdown renderer doesn't autolink bare web URLs.
        let plain = String(result.characters)
        if let detector = try? NSDataDetector(types: NSTextCheckingResult.CheckingType.link.rawValue) {
            for match in detector.matches(in: plain, range: NSRange(plain.startIndex..., in: plain)) {
                guard let url = safeWebURL(match.url), let range = Range(match.range, in: plain),
                      let start = AttributedString.Index(range.lowerBound, within: result),
                      let end = AttributedString.Index(range.upperBound, within: result) else { continue }
                let target = start..<end
                if result[target].runs.allSatisfy({ $0.link == nil && $0.inlinePresentationIntent?.contains(.code) != true }) {
                    result[target].link = url
                }
            }
        }
        // A relative Markdown file link refers to its author's workspace.
        if let bud {
            for run in Array(result.runs) {
                guard let link = run.link, link.scheme == nil, link.host == nil, !link.path.isEmpty else { continue }
                let relative = ((basePath as NSString).appendingPathComponent(link.path) as NSString).standardizingPath
                guard validReferencePath(relative),
                      var target = URLComponents(string: "buddy://files/\(bud)") else { continue }
                target.queryItems = [URLQueryItem(name: "path", value: relative)]
                result[run.range].link = target.url
            }
        }
        return result
    }
}

struct CodeBlock: View {
    let language: String
    let code: String

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Text(language.nilIfEmpty ?? "Code").font(.caption.monospaced()).foregroundStyle(.secondary)
                Spacer()
                Button { UIPasteboard.general.string = code } label: {
                    Image(systemName: "document.on.document")
                }
                .font(.caption)
                .accessibilityLabel("Copy code")
            }
            ScrollView(.horizontal) {
                Text(formattedCode)
                    .font(.system(.footnote, design: .monospaced))
                    .textSelection(.enabled)
                    .fixedSize(horizontal: true, vertical: false)
                    .padding(.bottom, 3)
            }
        }
        .padding(10)
        .background(BuddyStyle.raised, in: RoundedRectangle(cornerRadius: 9))
    }

    private var formattedCode: AttributedString {
        guard language.lowercased() == "diff" else { return AttributedString(code) }
        var result = AttributedString()
        for (index, line) in code.components(separatedBy: "\n").enumerated() {
            if index > 0 { result += AttributedString("\n") }
            var text = AttributedString(line)
            if line.hasPrefix("+") { text.foregroundColor = .green }
            if line.hasPrefix("-") { text.foregroundColor = .red }
            if line.hasPrefix("@@") { text.foregroundColor = .secondary }
            result += text
        }
        return result
    }
}
