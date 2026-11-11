// 画小尺寸图标（16–48 px）：铺满画布的圆角方块 + 白色梯子（和 App 图标同一个标志）。
// 用法：swift glyph.swift <输出.png> <边长> <brand|on|off>
//   brand  App 图标的青蓝渐变（ICO 里的小尺寸）
//   on     托盘·已连接：绿色（与主界面电源按钮同色）
//   off    托盘·未连接：中性灰，在浅色和深色任务栏上都看得清
import AppKit

let args = CommandLine.arguments
guard args.count == 4, let size = Int(args[2]) else {
    FileHandle.standardError.write("用法：swift glyph.swift <输出.png> <边长> <brand|on|off>\n".data(using: .utf8)!)
    exit(64)
}
let s = CGFloat(size)
let space = CGColorSpace(name: CGColorSpace.sRGB)!
guard let ctx = CGContext(data: nil, width: size, height: size, bitsPerComponent: 8, bytesPerRow: 0,
                          space: space, bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue)
else { fatalError("无法创建画布") }

func rgb(_ hex: UInt32) -> CGColor {
    CGColor(srgbRed: CGFloat((hex >> 16) & 0xff) / 255, green: CGFloat((hex >> 8) & 0xff) / 255,
            blue: CGFloat(hex & 0xff) / 255, alpha: 1)
}
let colors: [CGColor]
switch args[3] {
case "on": colors = [rgb(0x5FE07D), rgb(0x24B348)]
case "off": colors = [rgb(0x9A9AA0), rgb(0x6E6E74)]
default: colors = [rgb(0x0D8C9E), rgb(0x1A4DCC)]
}

// 方块铺满画布（Windows 的小图标不留 macOS 那样的大边距）。
let radius = (s * 0.22).rounded()
let tile = CGPath(roundedRect: CGRect(x: 0, y: 0, width: s, height: s), cornerWidth: radius, cornerHeight: radius, transform: nil)
ctx.saveGState()
ctx.addPath(tile)
ctx.clip()
let gradient = CGGradient(colorsSpace: space, colors: colors as CFArray, locations: [0, 1])!
ctx.drawLinearGradient(gradient, start: CGPoint(x: 0, y: s), end: CGPoint(x: s, y: 0), options: [])
ctx.restoreGState()

// 梯子：与 App 图标同样后倾、圆头，按方块铺满画布的比例放大一些。
// 小尺寸下线宽设了下限，16 px 时竖杠约 1.6 px、横档约 1.2 px，在任务栏上仍能认出是梯子
// （试过不倾斜、对齐像素的画法，16 px 时更像汉字"目"）。
ctx.setStrokeColor(CGColor(srgbRed: 1, green: 1, blue: 1, alpha: 1))
ctx.setLineCap(.round)
ctx.translateBy(x: s / 2, y: s / 2)
ctx.rotate(by: -0.22)
let k = s / 824 * 1.12
ctx.setLineWidth(max(1.6, 46 * k))
for x in [-120 * k, 120 * k] {
    ctx.move(to: CGPoint(x: x, y: -270 * k))
    ctx.addLine(to: CGPoint(x: x, y: 270 * k))
}
ctx.strokePath()
ctx.setLineWidth(max(1.2, 34 * k))
for i in 0..<4 {
    let y = (-270 + 80 + CGFloat(i) * 125) * k
    ctx.move(to: CGPoint(x: -120 * k, y: y))
    ctx.addLine(to: CGPoint(x: 120 * k, y: y))
}
ctx.strokePath()

guard let image = ctx.makeImage() else { fatalError("渲染失败") }
let rep = NSBitmapImageRep(cgImage: image)
try! rep.representation(using: .png, properties: [:])!.write(to: URL(fileURLWithPath: args[1]))
