import SwiftUI

struct BudFace: View {
    var appearance: BudAppearance?
    var size: CGFloat = 36

    var body: some View {
        let palette = BudPalette.named[appearance?.color ?? "ink"] ?? BudPalette.ink
        Canvas { context, canvasSize in
            let body = BudOutline(name: appearance?.shape).path(in: CGRect(origin: .zero, size: canvasSize))
            let bounds = body.boundingRect
            let gradientAngle = 145.0 * .pi / 180
            let dx = sin(gradientAngle), dy = -cos(gradientAngle)
            let half = (abs(bounds.width * dx) + abs(bounds.height * dy)) / 2
            context.fill(body, with: .linearGradient(
                palette.gradient,
                startPoint: CGPoint(x: bounds.midX - dx * half, y: bounds.midY - dy * half),
                endPoint: CGPoint(x: bounds.midX + dx * half, y: bounds.midY + dy * half)
            ))

            // Use the same normalized eye positions and round strokes as desktop.
            let eyeAngle = 77.0 * .pi / 180
            for eye in [(x: 53.0, y: 42.0, length: 9.0, width: 8.0),
                        (x: 68.0, y: 38.0, length: 8.0, width: 6.8)] {
                let dx = eye.length / 2 * cos(eyeAngle), dy = eye.length / 2 * sin(eyeAngle)
                var path = Path()
                path.move(to: CGPoint(x: (eye.x - dx) * canvasSize.width / 100,
                                     y: (eye.y - dy) * canvasSize.height / 100))
                path.addLine(to: CGPoint(x: (eye.x + dx) * canvasSize.width / 100,
                                        y: (eye.y + dy) * canvasSize.height / 100))
                context.stroke(path, with: .color(palette.eyes),
                               style: StrokeStyle(lineWidth: canvasSize.width * eye.width / 100, lineCap: .round))
            }
        }
        .frame(width: size, height: size)
        .accessibilityHidden(true)
    }
}

// Keep these normalized outlines in sync with blobOutline in cmd/buddy/avatar.go.
private struct BudOutline: Shape {
    let name: String?

    func path(in rect: CGRect) -> Path {
        var path = Path()
        switch name {
        case "bean":
            path.move(to: CGPoint(x: 56, y: 13))
            path.addCurve(to: CGPoint(x: 85, y: 50), control1: CGPoint(x: 78, y: 8), control2: CGPoint(x: 89, y: 28))
            path.addCurve(to: CGPoint(x: 34, y: 86), control1: CGPoint(x: 80, y: 73), control2: CGPoint(x: 56, y: 90))
            path.addCurve(to: CGPoint(x: 15, y: 48), control1: CGPoint(x: 13, y: 82), control2: CGPoint(x: 6, y: 64))
            path.addCurve(to: CGPoint(x: 56, y: 13), control1: CGPoint(x: 23, y: 34), control2: CGPoint(x: 31, y: 20))
        case "drop":
            path.move(to: CGPoint(x: 49, y: 11))
            path.addCurve(to: CGPoint(x: 89, y: 60), control1: CGPoint(x: 60, y: 11), control2: CGPoint(x: 89, y: 36))
            path.addCurve(to: CGPoint(x: 49, y: 89), control1: CGPoint(x: 89, y: 80), control2: CGPoint(x: 72, y: 89))
            path.addCurve(to: CGPoint(x: 9, y: 60), control1: CGPoint(x: 26, y: 89), control2: CGPoint(x: 9, y: 80))
            path.addCurve(to: CGPoint(x: 49, y: 11), control1: CGPoint(x: 9, y: 36), control2: CGPoint(x: 38, y: 11))
        case "cloud":
            path.move(to: CGPoint(x: 30, y: 30))
            path.addCurve(to: CGPoint(x: 69, y: 25), control1: CGPoint(x: 30, y: 8), control2: CGPoint(x: 62, y: 5))
            path.addCurve(to: CGPoint(x: 89, y: 51), control1: CGPoint(x: 86, y: 19), control2: CGPoint(x: 95, y: 34))
            path.addCurve(to: CGPoint(x: 66, y: 81), control1: CGPoint(x: 101, y: 71), control2: CGPoint(x: 83, y: 88))
            path.addCurve(to: CGPoint(x: 25, y: 81), control1: CGPoint(x: 57, y: 94), control2: CGPoint(x: 34, y: 94))
            path.addCurve(to: CGPoint(x: 12, y: 53), control1: CGPoint(x: 3, y: 84), control2: CGPoint(x: 3, y: 62))
            path.addCurve(to: CGPoint(x: 30, y: 30), control1: CGPoint(x: 7, y: 39), control2: CGPoint(x: 17, y: 26))
        case "capsule":
            path.move(to: CGPoint(x: 37, y: 22))
            path.addLine(to: CGPoint(x: 61, y: 22))
            path.addCurve(to: CGPoint(x: 61, y: 78), control1: CGPoint(x: 98.324, y: 22), control2: CGPoint(x: 98.324, y: 78))
            path.addLine(to: CGPoint(x: 37, y: 78))
            path.addCurve(to: CGPoint(x: 37, y: 22), control1: CGPoint(x: -0.324, y: 78), control2: CGPoint(x: -0.324, y: 22))
        case "pillow":
            path.move(to: CGPoint(x: 49, y: 17))
            path.addCurve(to: CGPoint(x: 83, y: 50), control1: CGPoint(x: 72, y: 17), control2: CGPoint(x: 92, y: 2))
            path.addCurve(to: CGPoint(x: 49, y: 83), control1: CGPoint(x: 92, y: 98), control2: CGPoint(x: 72, y: 83))
            path.addCurve(to: CGPoint(x: 15, y: 50), control1: CGPoint(x: 26, y: 83), control2: CGPoint(x: 6, y: 98))
            path.addCurve(to: CGPoint(x: 49, y: 17), control1: CGPoint(x: 6, y: 2), control2: CGPoint(x: 26, y: 17))
        default:
            let pebble = name == "pebble" || name == "squircle"
            let rx = pebble ? 39.0 : 40.0, ry = pebble ? 34.0 : 40.0
            let round = pebble ? 0.87 : 0.552285
            path.move(to: CGPoint(x: 49, y: 50 - ry))
            path.addCurve(to: CGPoint(x: 49 + rx, y: 50), control1: CGPoint(x: 49 + rx * round, y: 50 - ry), control2: CGPoint(x: 49 + rx, y: 50 - ry * round))
            path.addCurve(to: CGPoint(x: 49, y: 50 + ry), control1: CGPoint(x: 49 + rx, y: 50 + ry * round), control2: CGPoint(x: 49 + rx * round, y: 50 + ry))
            path.addCurve(to: CGPoint(x: 49 - rx, y: 50), control1: CGPoint(x: 49 - rx * round, y: 50 + ry), control2: CGPoint(x: 49 - rx, y: 50 + ry * round))
            path.addCurve(to: CGPoint(x: 49, y: 50 - ry), control1: CGPoint(x: 49 - rx, y: 50 - ry * round), control2: CGPoint(x: 49 - rx * round, y: 50 - ry))
        }
        path.closeSubpath()
        return path.applying(CGAffineTransform(a: rect.width / 100, b: 0, c: 0, d: rect.height / 100, tx: rect.minX, ty: rect.minY))
    }
}

private struct BudPalette {
    let gradient: Gradient
    let eyes: Color

    // The desktop palette uses Oklab interpolation. Sample that same curve so
    // native gradients match it on iOS 17 as well as newer versions.
    init(_ light: UInt32, _ base: UInt32, _ shade: UInt32, _ eyes: UInt32) {
        let colors = [light, base, shade].map(Self.oklab)
        let locations = [0.0, 0.47, 1.0]
        var stops: [Gradient.Stop] = []
        for segment in 0..<2 {
            for step in 0...16 where segment == 0 || step != 0 {
                let fraction = Double(step) / 16
                let lab = colors[segment] + (colors[segment + 1] - colors[segment]) * fraction
                let location = locations[segment] + (locations[segment + 1] - locations[segment]) * fraction
                stops.append(Gradient.Stop(color: Self.color(lab), location: location))
            }
        }
        gradient = Gradient(stops: stops)
        self.eyes = Color(.sRGB, red: Double(eyes >> 16 & 255) / 255,
                          green: Double(eyes >> 8 & 255) / 255, blue: Double(eyes & 255) / 255, opacity: 1)
    }

    static let ink = BudPalette(0x414147, 0x151518, 0x070709, 0xF5F5F6)
    static let named: [String: BudPalette] = {
        // Keep the palette values in sync with blobPalettes in cmd/buddy/avatar.go.
        var palettes = [
            "ink": ink,
            "pearl": BudPalette(0xFFFFFF, 0xE2E2E5, 0xA0A0A8, 0x171719),
            "lilac": BudPalette(0xECE2FF, 0xB6A1E2, 0x75629E, 0x252037),
            "peach": BudPalette(0xFFF0DB, 0xEEBD9D, 0xB67C61, 0x33221C),
            "mint": BudPalette(0xE5F7E9, 0xA6D1BB, 0x6B9E87, 0x1D3028),
            "sky": BudPalette(0xE5F1FF, 0xADC8E8, 0x6D8BB5, 0x202B3B),
            "rose": BudPalette(0xFFE0F0, 0xDE8EB7, 0xA94A7B, 0x3F1B30),
            "coral": BudPalette(0xFFD3C5, 0xE47D69, 0xA6443A, 0x431E1A),
            "honey": BudPalette(0xFFF2B7, 0xECC460, 0xAE772C, 0x3D2915),
            "moss": BudPalette(0xE2E9BC, 0xA4B56F, 0x607344, 0x25321A),
            "teal": BudPalette(0xA6E5D8, 0x398E85, 0x14565D, 0xF3FFF8),
            "cobalt": BudPalette(0x93AEFF, 0x4164CB, 0x24346E, 0xF5F7FF),
            "plum": BudPalette(0xBE98D2, 0x774F96, 0x452759, 0xFFF2FF),
            "slate": BudPalette(0xB4C3CF, 0x606F82, 0x323D50, 0xF3F7FF)
        ]
        for (alias, name) in ["blue": "cobalt", "green": "mint", "pink": "rose",
                              "orange": "peach", "amber": "peach", "purple": "lilac",
                              "violet": "lilac", "lavender": "lilac"] {
            palettes[alias] = palettes[name]
        }
        return palettes
    }()

    private static func oklab(_ hex: UInt32) -> SIMD3<Double> {
        let rgb = [Double(hex >> 16 & 255), Double(hex >> 8 & 255), Double(hex & 255)].map { channel in
            let value = channel / 255
            return value <= 0.04045 ? value / 12.92 : pow((value + 0.055) / 1.055, 2.4)
        }
        let l = cbrt(0.4122214708 * rgb[0] + 0.5363325363 * rgb[1] + 0.0514459929 * rgb[2])
        let m = cbrt(0.2119034982 * rgb[0] + 0.6806995451 * rgb[1] + 0.1073969566 * rgb[2])
        let s = cbrt(0.0883024619 * rgb[0] + 0.2817188376 * rgb[1] + 0.6299787005 * rgb[2])
        return SIMD3(0.2104542553 * l + 0.7936177850 * m - 0.0040720468 * s,
                     1.9779984951 * l - 2.4285922050 * m + 0.4505937099 * s,
                     0.0259040371 * l + 0.7827717662 * m - 0.8086757660 * s)
    }

    private static func color(_ lab: SIMD3<Double>) -> Color {
        let l = pow(lab.x + 0.3963377774 * lab.y + 0.2158037573 * lab.z, 3)
        let m = pow(lab.x - 0.1055613458 * lab.y - 0.0638541728 * lab.z, 3)
        let s = pow(lab.x - 0.0894841775 * lab.y - 1.2914855480 * lab.z, 3)
        let rgb = [4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s,
                   -1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s,
                   -0.0041960863 * l - 0.7034186147 * m + 1.7076147010 * s].map { value in
            let value = min(1, max(0, value))
            return value <= 0.0031308 ? 12.92 * value : 1.055 * pow(value, 1 / 2.4) - 0.055
        }
        return Color(.sRGB, red: rgb[0], green: rgb[1], blue: rgb[2], opacity: 1)
    }
}

#Preview("Bud shapes and colors") {
    LazyVGrid(columns: [GridItem(.adaptive(minimum: 96))], spacing: 18) {
        ForEach(["orb", "bean", "pebble", "drop", "cloud", "capsule", "pillow"], id: \.self) { shape in
            VStack {
                BudFace(appearance: BudAppearance(color: "cobalt", shape: shape), size: 80)
                Text(shape).font(.caption)
            }
        }
        ForEach(["ink", "pearl", "lilac", "peach", "mint", "sky", "rose", "coral", "honey", "moss", "teal", "cobalt", "plum", "slate"], id: \.self) { color in
            VStack {
                BudFace(appearance: BudAppearance(color: color, shape: "cloud"), size: 80)
                Text(color).font(.caption)
            }
        }
    }
    .padding()
}
