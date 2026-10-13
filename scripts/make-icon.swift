// 生成应用图标（1024×1024 PNG）：深青到蓝色渐变的圆角方块 + 白色梯子。
// 用法：swift scripts/make-icon.swift <输出.png>
import AppKit

let size: CGFloat = 1024
let output = CommandLine.arguments.count > 1 ? CommandLine.arguments[1] : "icon.png"

guard let context = CGContext(
    data: nil, width: Int(size), height: Int(size), bitsPerComponent: 8, bytesPerRow: 0,
    space: CGColorSpace(name: CGColorSpace.sRGB)!, bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue)
else { fatalError("无法创建画布") }

// macOS 图标网格：824×824 的圆角方块居中，四周留 100 的透明边距。
let tile = CGRect(x: 100, y: 100, width: 824, height: 824)
let path = CGPath(roundedRect: tile, cornerWidth: 185, cornerHeight: 185, transform: nil)
context.saveGState()
context.addPath(path)
context.clip()
let gradient = CGGradient(
    colorsSpace: CGColorSpace(name: CGColorSpace.sRGB)!,
    colors: [CGColor(red: 0.05, green: 0.55, blue: 0.62, alpha: 1), CGColor(red: 0.10, green: 0.30, blue: 0.80, alpha: 1)] as CFArray,
    locations: [0, 1])!
context.drawLinearGradient(gradient, start: CGPoint(x: 100, y: 924), end: CGPoint(x: 924, y: 100), options: [])
context.restoreGState()

// 梯子：两条竖杠 + 四级横档，整体略微后倾。
context.saveGState()
context.translateBy(x: size / 2, y: size / 2)
context.rotate(by: -0.22)
context.setStrokeColor(CGColor(red: 1, green: 1, blue: 1, alpha: 1))
context.setLineCap(.round)
let railHalfWidth: CGFloat = 120
let top: CGFloat = 270
let bottom: CGFloat = -270
context.setLineWidth(46)
for x in [-railHalfWidth, railHalfWidth] {
    context.move(to: CGPoint(x: x, y: bottom))
    context.addLine(to: CGPoint(x: x, y: top))
}
context.strokePath()
context.setLineWidth(34)
for index in 0..<4 {
    let y = bottom + 80 + CGFloat(index) * 125
    context.move(to: CGPoint(x: -railHalfWidth, y: y))
    context.addLine(to: CGPoint(x: railHalfWidth, y: y))
}
context.strokePath()
context.restoreGState()

guard let image = context.makeImage() else { fatalError("渲染失败") }
let rep = NSBitmapImageRep(cgImage: image)
try! rep.representation(using: .png, properties: [:])!.write(to: URL(fileURLWithPath: output))
