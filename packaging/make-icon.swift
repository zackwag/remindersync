// Draws ReminderSync's app icon: a checklist card inside a pair of sync
// arrows, on the macOS icon grid (an 824 pt rounded square in a 1024 pt
// canvas). Writes a 1024x1024 PNG to the path given as the only argument.
//
// Usage: swift packaging/make-icon.swift /tmp/icon.png
// scripts/make-icon.sh turns the PNG into packaging/AppIcon.icns.

import AppKit

let size: CGFloat = 1024
let out = CommandLine.arguments.count > 1 ? CommandLine.arguments[1] : "icon.png"

let rep = NSBitmapImageRep(
    bitmapDataPlanes: nil, pixelsWide: Int(size), pixelsHigh: Int(size),
    bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
    colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)!
NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: rep)

func color(_ hex: Int, _ alpha: CGFloat = 1) -> NSColor {
    NSColor(
        srgbRed: CGFloat((hex >> 16) & 0xff) / 255,
        green: CGFloat((hex >> 8) & 0xff) / 255,
        blue: CGFloat(hex & 0xff) / 255,
        alpha: alpha)
}

// Background: the standard rounded square, with a blue gradient and a soft
// drop shadow.
let body = NSRect(x: 100, y: 100, width: 824, height: 824)
let squircle = NSBezierPath(roundedRect: body, xRadius: 185, yRadius: 185)
NSGraphicsContext.saveGraphicsState()
let shadow = NSShadow()
shadow.shadowColor = color(0x000000, 0.3)
shadow.shadowOffset = NSSize(width: 0, height: -10)
shadow.shadowBlurRadius = 20
shadow.set()
color(0x1E6FD9).setFill()
squircle.fill()
NSGraphicsContext.restoreGraphicsState()
NSGradient(starting: color(0x4DA3FF), ending: color(0x1A5FC8))!.draw(in: squircle, angle: -90)

// Sync arrows: two arcs around the centre, each ending in an arrowhead.
let centre = NSPoint(x: 512, y: 512)
let radius: CGFloat = 300
func arc(from start: CGFloat, to end: CGFloat) {
    let path = NSBezierPath()
    // A square end runs into the arrowhead's base, and a dot rounds off the
    // tail.
    path.appendArc(withCenter: centre, radius: radius, startAngle: start, endAngle: end, clockwise: false)
    path.lineWidth = 46
    path.lineCapStyle = .butt
    color(0xF2F7FF).setStroke()
    path.stroke()
    let s = start * .pi / 180
    let tail = NSPoint(x: centre.x + radius * cos(s), y: centre.y + radius * sin(s))
    color(0xF2F7FF).setFill()
    NSBezierPath(ovalIn: NSRect(x: tail.x - 23, y: tail.y - 23, width: 46, height: 46)).fill()

    // Arrowhead at the end of the arc, pointing along it.
    let a = end * .pi / 180
    let tip = NSPoint(x: centre.x + radius * cos(a), y: centre.y + radius * sin(a))
    let along = NSPoint(x: -sin(a), y: cos(a))
    let out = NSPoint(x: cos(a), y: sin(a))
    let head = NSBezierPath()
    head.move(to: NSPoint(x: tip.x + along.x * 70, y: tip.y + along.y * 70))
    head.line(to: NSPoint(x: tip.x + out.x * 62 - along.x * 20, y: tip.y + out.y * 62 - along.y * 20))
    head.line(to: NSPoint(x: tip.x - out.x * 62 - along.x * 20, y: tip.y - out.y * 62 - along.y * 20))
    head.close()
    color(0xF2F7FF).setFill()
    head.fill()
}
arc(from: 20, to: 150)
arc(from: 200, to: 330)

// Checklist card in the middle.
let card = NSRect(x: 342, y: 352, width: 340, height: 320)
NSGraphicsContext.saveGraphicsState()
let cardShadow = NSShadow()
cardShadow.shadowColor = color(0x0A2F6B, 0.35)
cardShadow.shadowOffset = NSSize(width: 0, height: -8)
cardShadow.shadowBlurRadius = 18
cardShadow.set()
color(0xFFFFFF).setFill()
NSBezierPath(roundedRect: card, xRadius: 44, yRadius: 44).fill()
NSGraphicsContext.restoreGraphicsState()

// Three rows: a circle and a line each. The first two are checked off.
let rowColors = [0xFF3B30, 0xFF9500, 0x007AFF]
for (i, rowColor) in rowColors.enumerated() {
    let y = card.maxY - 82 - CGFloat(i) * 88
    let dot = NSRect(x: card.minX + 42, y: y - 26, width: 52, height: 52)
    let circle = NSBezierPath(ovalIn: dot)
    if i < 2 {
        color(rowColor).setFill()
        circle.fill()
        let check = NSBezierPath()
        check.move(to: NSPoint(x: dot.minX + 14, y: dot.midY))
        check.line(to: NSPoint(x: dot.minX + 23, y: dot.midY - 10))
        check.line(to: NSPoint(x: dot.maxX - 12, y: dot.midY + 11))
        check.lineWidth = 7
        check.lineCapStyle = .round
        check.lineJoinStyle = .round
        color(0xFFFFFF).setStroke()
        check.stroke()
    } else {
        circle.lineWidth = 7
        color(rowColor).setStroke()
        circle.stroke()
    }
    let line = NSBezierPath(roundedRect: NSRect(x: dot.maxX + 26, y: y - 10, width: card.maxX - dot.maxX - 68, height: 20), xRadius: 10, yRadius: 10)
    color(0xD1D5DB).setFill()
    line.fill()
}

NSGraphicsContext.current = nil
try! rep.representation(using: .png, properties: [:])!.write(to: URL(fileURLWithPath: out))
