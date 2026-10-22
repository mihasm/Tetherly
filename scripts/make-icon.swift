// Draws Tetherly's app icon (line-icon style: a phone, a cable, a laptop) as a 1024 px PNG.
// usage: swift scripts/make-icon.swift out.png
import AppKit

let size: CGFloat = 1024
let rep = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: Int(size), pixelsHigh: Int(size), bitsPerSample: 8,
                           samplesPerPixel: 4, hasAlpha: true, isPlanar: false, colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)!
NSGraphicsContext.saveGraphicsState()
NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: rep)
let ctx = NSGraphicsContext.current!.cgContext

// background: rounded square, deep teal gradient
let inset: CGFloat = 100
let rect = CGRect(x: inset, y: inset, width: size - 2 * inset, height: size - 2 * inset)
let bg = NSBezierPath(roundedRect: rect, xRadius: 185, yRadius: 185)
NSGradient(colors: [NSColor(calibratedRed: 0.06, green: 0.36, blue: 0.42, alpha: 1),
                    NSColor(calibratedRed: 0.03, green: 0.17, blue: 0.24, alpha: 1)])!.draw(in: bg, angle: -90)

ctx.setStrokeColor(NSColor.white.cgColor)
ctx.setLineWidth(34)
ctx.setLineCap(.round)
ctx.setLineJoin(.round)

// phone (left)
let phone = CGPath(roundedRect: CGRect(x: 230, y: 360, width: 190, height: 330), cornerWidth: 38, cornerHeight: 38, transform: nil)
ctx.addPath(phone)
ctx.strokePath()
ctx.move(to: CGPoint(x: 300, y: 410)); ctx.addLine(to: CGPoint(x: 350, y: 410)); ctx.strokePath()

// laptop (right)
let screen = CGPath(roundedRect: CGRect(x: 560, y: 440, width: 240, height: 170), cornerWidth: 22, cornerHeight: 22, transform: nil)
ctx.addPath(screen); ctx.strokePath()
ctx.move(to: CGPoint(x: 520, y: 380)); ctx.addLine(to: CGPoint(x: 840, y: 380)); ctx.strokePath()

// cable from the phone's bottom to the laptop's side
ctx.move(to: CGPoint(x: 325, y: 360))
ctx.addCurve(to: CGPoint(x: 520, y: 380), control1: CGPoint(x: 325, y: 250), control2: CGPoint(x: 470, y: 250))
ctx.strokePath()

// signal arcs above the phone
ctx.setLineWidth(28)
for (i, r) in [60.0, 110.0].enumerated() {
    ctx.addArc(center: CGPoint(x: 325, y: 700), radius: CGFloat(r), startAngle: .pi * 0.25, endAngle: .pi * 0.75, clockwise: false)
    ctx.setAlpha(i == 0 ? 1 : 0.7)
    ctx.strokePath()
}
NSGraphicsContext.restoreGraphicsState()
try! rep.representation(using: .png, properties: [:])!.write(to: URL(fileURLWithPath: CommandLine.arguments[1]))
