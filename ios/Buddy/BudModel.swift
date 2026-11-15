import Foundation
import CoreGraphics
import simd

enum BudAction: Int, CaseIterable {
    case rest, look, hop, think, investigate, notebook, idea, nod, talk, twirl, doubleHop, tilt
    case tea, sketch, music, letter, telescope, sway, stretch, sleepy, peek, lookUp, settle
}

private struct BudPose {
    var values = [Float](repeating: 0, count: 13)
    var action = BudAction.rest
}

private struct BudMesh {
    let positions, normals: [SIMD3<Float>]
    struct Face {
        let a, b, c, material: Int
    }
    let faces: [Face]
}

private struct BudModel {
    struct Key {
        let time: Float
        let values: [Float]
    }
    static let shapes = ["orb", "bean", "pebble", "drop", "cloud", "capsule", "pillow"]
    static let shared = BudModel()

    let eyes: [[SIMD3<Float>]]
    let meshes: [BudMesh]
    let clips: [[Key]]

    private init() {
        guard let url = Bundle.main.url(forResource: "Bud", withExtension: "mesh"),
              let data = try? Data(contentsOf: url), data.starts(with: [66, 85, 68, 53]) else {
            preconditionFailure("The built app is missing its Bud mesh resource")
        }
        var reader = MeshReader(data: data)
        precondition(Int(reader.byte()) == Self.shapes.count, "Unexpected Bud shape count")
        let meshCount = Int(reader.byte())
        precondition(meshCount == 17 && Int(reader.byte()) == BudAction.allCases.count, "Unexpected Bud model contents")
        eyes = Self.shapes.map { _ in
            (0..<2).map { _ in SIMD3(reader.float(), reader.float(), reader.float()) }
        }
        meshes = (0..<meshCount).map { _ in
            let vertexCount = reader.word(), faceCount = reader.word()
            var positions: [SIMD3<Float>] = [], normals: [SIMD3<Float>] = []
            positions.reserveCapacity(vertexCount)
            normals.reserveCapacity(vertexCount)
            for _ in 0..<vertexCount {
                positions.append(SIMD3(reader.coordinate(), reader.coordinate(), reader.coordinate()))
                normals.append(SIMD3(reader.normal(), reader.normal(), reader.normal()))
            }
            let faces = (0..<faceCount).map { _ in
                BudMesh.Face(a: reader.word(), b: reader.word(), c: reader.word(), material: Int(reader.byte()))
            }
            return BudMesh(positions: positions, normals: normals, faces: faces)
        }
        clips = BudAction.allCases.map { action in
            guard action != .rest else { return [] }
            return (0..<Int(reader.byte())).map { _ in
                Key(time: reader.float(), values: (0..<13).map { _ in reader.float() })
            }
        }
        precondition(reader.offset == data.count, "Unexpected Bud mesh format")
    }

    private struct MeshReader {
        let data: Data
        var offset = 4

        mutating func byte() -> UInt8 {
            defer { offset += 1 }
            return data[offset]
        }

        mutating func word() -> Int { Int(byte()) | Int(byte()) << 8 }
        mutating func coordinate() -> Float { Float(Int16(bitPattern: UInt16(word()))) / 12000 }
        mutating func normal() -> Float { Float(Int16(bitPattern: UInt16(word()))) / 32767 }
        mutating func float() -> Float { Float(bitPattern: UInt32(word()) | UInt32(word()) << 16) }
    }

    func duration(_ action: BudAction) -> Double {
        Double(clips[action.rawValue].last?.time ?? 0)
    }

    func pose(_ action: BudAction, age: Double) -> BudPose {
        let keys = clips[action.rawValue]
        guard let index = keys.indices.dropFirst().first(where: { Double(keys[$0].time) > age }) else { return BudPose() }
        let before = keys[index - 1], after = keys[index]
        let t = max(0, (Float(age) - before.time) / (after.time - before.time))
        let mix = t * t * t * (10 + t * (-15 + 6 * t))
        return BudPose(values: zip(before.values, after.values).map { $0 + ($1 - $0) * mix }, action: action)
    }
}

private struct BudDirector {
    var started = 0.0, next = 0.0, blink = 0.0, tempo = 1.0
    var action = BudAction.rest, requested = BudAction.rest
    var recent = [BudAction](repeating: .rest, count: 3)
    var lastPlayful = 0.0
    var breathPhase = 0.0
    var intensity: Float = 1
    var requestChanged = false, mirror = false

    mutating func sample(time: Double, desired: BudAction, small: Bool, model: BudModel) -> BudPose {
        if next == 0 {
            next = time + Double.random(in: 7.5...14)
            blink = time + Double.random(in: 2.2...5)
            lastPlayful = time
            breathPhase = Double.random(in: 0...(2 * .pi))
        }
        if desired != requested {
            let wasRest = requested == .rest
            requested = desired
            requestChanged = true
            if desired != .rest && (started == 0 || wasRest) {
                var ready = time + Double.random(in: 0.65...1)
                if started != 0 { ready = max(ready, started + model.duration(action) / tempo + 2.5) }
                next = min(next, ready)
            }
        }
        if (time - started) * tempo >= model.duration(action) && time >= next {
            action = desired
            if desired == .rest || !requestChanged {
                action = choose(time: time, desired: desired)
            }
            recent = [action, recent[0], recent[1]]
            requestChanged = false
            started = time
            tempo = Double.random(in: 0.74...0.90)
            intensity = Float.random(in: 0.80...1)
            mirror = [.look, .hop, .think, .talk, .twirl, .tilt, .doubleHop,
                      .sway, .stretch, .sleepy, .peek, .lookUp, .settle].contains(action) && Bool.random()
            var delay = desired == .rest ? Double.random(in: 7.5...15) : Double.random(in: 3.8...8.5)
            if small { delay += Double.random(in: 2.5...6) }
            next = time + model.duration(action) / tempo + delay
        }
        var pose = model.pose(action, age: (time - started) * tempo)
        for i in 0..<5 where pose.action != .twirl || i != 0 { pose.values[i] *= intensity }
        if mirror {
            let v = pose.values
            pose.values[0] = -v[0]
            pose.values[2] = -v[2]
            pose.values[6] = -v[6]
        }
        let breath = Float(sin(time.truncatingRemainder(dividingBy: 8.5) * 2 * .pi / 8.5 + breathPhase))
        pose.values[3] += breath * 0.008
        pose.values[4] += breath * 0.005
        let blinkAge = time - blink
        if blinkAge >= 0.24 {
            blink = time + Double.random(in: 2.8...7.5)
        } else if blinkAge >= 0 {
            pose.values[5] = max(pose.values[5], Float(sin(blinkAge * .pi / 0.24)))
        }
        return pose
    }

    private mutating func choose(time: Double, desired: BudAction) -> BudAction {
        if desired == .rest && time - lastPlayful > 75 && Int.random(in: 0..<12) == 0 {
            lastPlayful = time
            return [.hop, .twirl, .doubleHop].randomElement()!
        }
        let pool: [BudAction]
        switch desired {
        case .rest: pool = [.look, .tilt, .sway, .stretch, .sleepy, .peek, .lookUp, .settle, .tea, .music, .notebook]
        case .think: pool = [.think, .tilt, .lookUp, .notebook, .sketch, .settle, .nod]
        case .investigate: pool = [.investigate, .telescope, .look, .peek, .think, .letter]
        case .notebook: pool = [.notebook, .sketch, .letter, .think, .lookUp, .settle]
        case .idea: pool = [.idea, .letter, .sketch, .think, .nod, .lookUp]
        default: pool = [desired, .tilt, .nod, .look, .sway, .settle]
        }
        return pool.filter { !recent.contains($0) }.randomElement()!
    }
}

final class BudFrames {
    private var director = BudDirector()
    private var renderer = BudRenderer()
    private var key: FrameKey?
    private var lastFrame: CGImage?
    private var lastTime = 0.0

    private struct FrameKey: Equatable {
        let size, shape: Int
        let body, eyes: UInt32
        let moving, portrait: Bool
    }

    func image(size: Int, shape: String?, palette: BudPalette, portrait: Bool,
               action: BudAction, time: Double, moving: Bool, small: Bool) -> CGImage? {
        guard size > 0 else { return nil }
        let model = BudModel.shared
        let index = BudModel.shapes.firstIndex(of: shape ?? "orb") ?? 0
        let key = FrameKey(size: size, shape: index, body: palette.body, eyes: palette.eyes, moving: moving, portrait: portrait)
        guard key != self.key || (moving && abs(time - lastTime) >= 1 / 34) else { return lastFrame }
        if !moving { director = BudDirector() }
        let pose = moving ? director.sample(time: time, desired: action, small: small, model: model) : BudPose()
        lastFrame = renderer.draw(size: size, index: index, colors: [palette.body, palette.eyes],
                                  pose: pose, portrait: portrait, model: model)
        lastTime = time
        self.key = key
        return lastFrame
    }
}

private struct BudRenderer {
    struct Vertex {
        let x, y, z, light, shine: Float
    }

    private var pixels: [UInt32] = []
    private var depth: [Float] = []
    private let colorSpace = CGColorSpace(name: CGColorSpace.sRGB)!
    private let propColors: [UInt32] = [0xFFECC4, 0x514D83, 0xE9AC53, 0x74435E, 0xFFD567, 0xA99AC3]

    mutating func draw(size: Int, index: Int, colors: [UInt32], pose: BudPose, portrait: Bool, model: BudModel) -> CGImage? {
        if pixels.count != size * size {
            pixels = [UInt32](repeating: 0, count: size * size)
            depth = [Float](repeating: -100, count: size * size)
        } else {
            pixels.withUnsafeMutableBufferPointer { $0.update(repeating: 0) }
            depth.withUnsafeMutableBufferPointer { $0.update(repeating: -100) }
        }
        let colors = (colors + propColors).map {
            SIMD3(Float($0 >> 16 & 255), Float($0 >> 8 & 255), Float($0 & 255))
        }
        let v = pose.values
        let root = rotation(0.145 + v[0], 0.025 + v[1], 0.12 + v[2])
        let lift = SIMD3<Float>(0, v[3] + 0.045, 0)
        let bodyScale = SIMD3<Float>(1 + v[4], 1 - v[4], 1 + v[4] * 0.6)
        let framing: Float = portrait ? 2.48 : 2.86
        var parts: [Part] = [
            Part(mesh: index, scale: bodyScale)
        ]
        if index == 0 {
            parts.append(Part(mesh: 7, position: SIMD3<Float>(-0.25,0.83,0.08) * bodyScale, scale: bodyScale, roll: 0.18))
        }
        for (i, anchor) in model.eyes[index].enumerated() {
            let position = (anchor + SIMD3(v[6], v[7], -0.30 * v[7])) * bodyScale
            let scale = SIMD3<Float>(1 - 0.09 * Float(i), 1 - 0.87 * v[5], 1) * bodyScale
            parts.append(Part(mesh: 8, position: position, scale: scale,
                              roll: 0.39 - 0.07 * Float(i)))
        }
        let pop = v[11]
        if !portrait && pop > 0.01 {
            let position = SIMD3(v[8], v[9], v[10])
            let scale = SIMD3(repeating: pop)
            switch pose.action {
            case .investigate: parts.append(Part(mesh: 9, position: position, scale: scale, roll: v[12]))
            case .notebook: parts.append(Part(mesh: 10, position: position, scale: scale, roll: v[12]))
            case .idea: parts.append(Part(mesh: 11, position: position, scale: scale, roll: v[12]))
            case .tea: parts.append(Part(mesh: 12, position: position, scale: scale, roll: v[12]))
            case .sketch: parts.append(Part(mesh: 13, position: position, scale: scale, roll: v[12]))
            case .music: parts.append(Part(mesh: 14, position: position, scale: scale, roll: v[12]))
            case .letter: parts.append(Part(mesh: 15, position: position, scale: scale, roll: v[12]))
            case .telescope: parts.append(Part(mesh: 16, position: position, scale: scale, roll: v[12]))
            default: break
            }
        }
        for part in parts {
            let mesh = model.meshes[part.mesh]
            let local = rotation(0, 0, part.roll)
            let half = Float(size) / 2, unit = Float(size) / framing
            let lightDirection = SIMD3<Float>(-0.42,0.66,0.62), halfVector = SIMD3<Float>(-0.22,0.35,0.91)
            let vertices = mesh.positions.indices.map { i in
                let point = root * (part.position + local * (mesh.positions[i] * part.scale)) + lift
                let normal = simd_normalize(root * (local * (mesh.normals[i] / part.scale)))
                let diffuse = max(0, simd_dot(normal, lightDirection))
                let specular = max(0, simd_dot(normal, halfVector))
                let square = specular * specular, fourth = square * square
                let eighth = fourth * fourth
                return Vertex(x: half + point.x * unit, y: half - point.y * unit, z: point.z,
                              light: part.mesh == 8 ? 1 : 0.54 + 0.42 * diffuse + 0.045 * max(0, -normal.y),
                              shine: part.mesh == 8 ? 0 : eighth * 30.6)
            }
            pixels.withUnsafeMutableBufferPointer { pixels in
                depth.withUnsafeMutableBufferPointer { depth in
                    for face in mesh.faces {
                        Self.fill(vertices[face.a],vertices[face.b],vertices[face.c],color: colors[face.material],
                                  size: size, pixels: pixels, depth: depth)
                    }
                }
            }
        }
        let data = pixels.withUnsafeBytes { Data($0) }
        guard let provider = CGDataProvider(data: data as CFData) else { return nil }
        return CGImage(width: size, height: size, bitsPerComponent: 8, bitsPerPixel: 32,
                       bytesPerRow: size * 4, space: colorSpace,
                       bitmapInfo: [.byteOrder32Big, CGBitmapInfo(rawValue: CGImageAlphaInfo.premultipliedLast.rawValue)],
                       provider: provider, decode: nil, shouldInterpolate: true, intent: .defaultIntent)
    }

    private struct Part {
        let mesh: Int
        var position = SIMD3<Float>.zero
        var scale = SIMD3<Float>(repeating: 1)
        var roll: Float = 0
    }

    private func rotation(_ yaw: Float, _ pitch: Float, _ roll: Float) -> simd_float3x3 {
        let rotation = simd_quatf(angle: roll, axis: [0, 0, 1]) *
                       simd_quatf(angle: pitch, axis: [1, 0, 0]) *
                       simd_quatf(angle: yaw, axis: [0, 1, 0])
        return simd_float3x3(rotation)
    }

    private static func fill(_ a: Vertex, _ b: Vertex, _ c: Vertex, color: SIMD3<Float>,
                             size: Int, pixels: UnsafeMutableBufferPointer<UInt32>, depth: UnsafeMutableBufferPointer<Float>) {
        let dx1 = b.x - a.x, dy1 = b.y - a.y, dx2 = c.x - a.x, dy2 = c.y - a.y
        let area = dx1 * dy2 - dy1 * dx2
        guard abs(area) >= 0.0001 else { return }
        let left = max(0, Int(floor(min(a.x,b.x,c.x)))), right = min(size - 1, Int(max(a.x,b.x,c.x)))
        let top = max(0, Int(floor(min(a.y,b.y,c.y)))), bottom = min(size - 1, Int(max(a.y,b.y,c.y)))
        guard left <= right, top <= bottom else { return }
        let dbx = dy2 / area, dby = -dx2 / area, dcx = -dy1 / area, dcy = dx1 / area
        for y in top...bottom {
            let px = Float(left) + 0.5 - a.x, py = Float(y) + 0.5 - a.y
            var u = px * dbx + py * dby, v = px * dcx + py * dcy
            var i = y * size + left
            for _ in left...right {
                let w = 1 - u - v
                if u >= -0.0001 && v >= -0.0001 && w >= -0.0001 {
                    let z = a.z * w + b.z * u + c.z * v
                    if z > depth[i] {
                        depth[i] = z
                        let light = a.light * w + b.light * u + c.light * v
                        let shine = a.shine * w + b.shine * u + c.shine * v
                        let rgb = simd_min(SIMD3(repeating: 255), color * light + SIMD3(repeating: shine))
                        pixels[i] = UInt32(rgb.x) | UInt32(rgb.y) << 8 | UInt32(rgb.z) << 16 | 0xff000000
                    }
                }
                i += 1
                u += dbx
                v += dcx
            }
        }
    }
}
