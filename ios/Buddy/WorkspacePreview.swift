import SwiftUI

func validReferencePath(_ path: String) -> Bool {
    guard !path.isEmpty, path.utf8.count <= 2048, !path.contains("\\"), !path.contains(":"),
          !path.unicodeScalars.contains(where: { CharacterSet.controlCharacters.contains($0) }) else { return false }
    return path.split(separator: "/", omittingEmptySubsequences: false).allSatisfy { !$0.isEmpty && !$0.hasPrefix(".") }
}

func resolvedReferencePath(_ relative: String, base: String) -> String? {
    guard !relative.hasPrefix("/") else { return nil }
    var components: [Substring] = []
    for part in (base + "/" + relative).split(separator: "/") {
        if part == "." { continue }
        if part == ".." {
            guard !components.isEmpty else { return nil }
            components.removeLast()
        } else {
            components.append(part)
        }
    }
    let path = components.joined(separator: "/")
    return validReferencePath(path) ? path : nil
}

func referenceDestination(_ url: URL) -> Destination? {
    guard url.scheme == "buddy", url.user == nil, url.password == nil, url.fragment == nil,
          let parts = URLComponents(url: url, resolvingAgainstBaseURL: false) else { return nil }
    let id = String(url.path.dropFirst())
    let validID: (String) -> Bool = { value in
        !value.isEmpty && value.utf8.count <= 256 && value.unicodeScalars.allSatisfy {
            CharacterSet(charactersIn: "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-").contains($0)
        }
    }
    guard validID(id) else { return nil }
    // Accept Go's form-style queries without turning a literal %2B into a space.
    var queryParts = parts
    queryParts.percentEncodedQuery = parts.percentEncodedQuery?.replacingOccurrences(of: "+", with: "%20")
    let query = queryParts.queryItems ?? []
    switch url.host {
 case "artifacts":
 guard let file=query.first(where: {$0.name=="file"})?.value, validID(file) else {return nil}
 return .artifact(id,file)
    case "files":
        guard let path = query.first(where: { $0.name == "path" })?.value,
              path == "." || validReferencePath(path) else { return nil }
        let binding = query.first(where: { $0.name == "task" })
        let task = binding?.value
        guard binding == nil || task.map(validID) == true else { return nil }
        return .file(id, path, task)
    case "work":
        guard let job = query.first(where: { $0.name == "job" })?.value, validID(job) else { return nil }
        return .work(id, job)
    case "chat": return .chat(id, "Chat")
    default: return nil
    }
}

struct WorkspacePreviewItem: Decodable {
    let kind: String
    let sourceTask: String?
    let text: String?
    let image: Data?
    let entries: [Entry]?
    let truncated: Bool?

    struct Entry: Decodable, Identifiable {
        let name: String
        let folder: Bool
        var id: String { name }
    }
}

struct WorkspacePreview: View {
    let api: BuddyAPI
    let bud: String
    let path: String
    var sourceTask: String? = nil
    @State private var item: WorkspacePreviewItem?
    @State private var error: String?

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                if let item {
                    switch item.kind {
                    case "folder":
                        ForEach(item.entries ?? []) { entry in
                            NavigationLink(value: Destination.file(bud, path == "." ? entry.name : path + "/" + entry.name, sourceTask)) {
                                HStack {
                                    Label(entry.name, systemImage: entry.folder ? "folder" : "doc.text")
                                    Spacer()
                                    Image(systemName: "chevron.right").font(.caption).foregroundStyle(.secondary)
                                }
                                .padding(14)
                                .background(BuddyStyle.surface, in: RoundedRectangle(cornerRadius: 12))
                            }
                            .buttonStyle(.plain)
                        }
                        if item.entries?.isEmpty != false { ContentUnavailableView("No files yet", systemImage: "folder") }
                        if item.truncated == true { Text("Showing the first files in this folder.").font(.footnote).foregroundStyle(.secondary) }
                    case "text":
                        if ["md", "markdown"].contains((path as NSString).pathExtension.lowercased()) {
                            RichText(item.text ?? "", bud: bud, task: sourceTask, basePath: (path as NSString).deletingLastPathComponent)
                        } else {
                            CodeBlock(language: (path as NSString).pathExtension, code: item.text ?? "")
                        }
                    case "image":
                        if let data = item.image, let picture = UIImage(data: data) {
                            Image(uiImage: picture).resizable().scaledToFit().accessibilityLabel((path as NSString).lastPathComponent)
                        }
                    default:
                        ContentUnavailableView("Preview unavailable", systemImage: "doc",
                                               description: Text("This file’s type or size isn’t supported for preview."))
                    }
                }
                if let error { ConnectionError(message: error) }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(16)
        }
        .buddyPage()
        .navigationTitle(path == "." ? "Workspace" : (path as NSString).lastPathComponent)
        .navigationBarTitleDisplayMode(.inline)
        .overlay { if item == nil && error == nil { ProgressView("Opening file…") } }
        .task(id: bud + "/" + path + "/" + (sourceTask ?? "")) { await load() }
        .refreshable { await load() }
    }

    private func load() async {
        do {
            item = try await api.workspaceItem(bud: bud, path: path, task: sourceTask)
            error = nil
        } catch where !Task.isCancelled { self.error = error.localizedDescription } catch {}
    }
}
