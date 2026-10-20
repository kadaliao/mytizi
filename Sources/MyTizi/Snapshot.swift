#if DEBUG
import AppKit
import MytiziCore
import SwiftUI

/// 调试用：把各状态的界面渲染成 PNG，不需要屏幕录制权限。正式构建（release）不包含这段代码。
///
///   MyTizi --snapshot <目录>      各状态、亮/暗色各一张
///   MyTizi --screenshot <目录>    README 用的三联图（亮/暗）
///
/// 用 NSHostingView 在离屏窗口里真实绘制（菜单、输入框都是真控件），而不是 ImageRenderer
/// ——后者会把 AppKit 控件画成占位符。
@MainActor
enum Snapshot {
    static let windowSize = NSSize(width: 360, height: 580)

    // MARK: 示例数据（不含任何真实订阅信息）

    static let nodes = [
        NodeRow(name: "香港-01", type: "Shadowsocks", delay: 82),
        NodeRow(name: "日本-02", type: "Trojan", delay: 121),
        NodeRow(name: "美国-03", type: "Vmess", delay: nil),
    ]

    static var subscription: SubscriptionRecord {
        SubscriptionRecord(
            url: "https://sub.example.com/x?token=abc", name: "我的订阅", addedAt: Date(), updatedAt: Date().addingTimeInterval(-3 * 3600),
            nodes: nodes.map { ProbedNode(name: $0.name, type: $0.type) },
            usage: SubscriptionUsage(upload: 3_000_000_000, download: 40_000_000_000, total: 200_000_000_000, expire: Date().addingTimeInterval(90 * 86400)))
    }

    static func scenarios() -> [(String, AppModel)] {
        [
            ("1-onboarding", .preview(phase: .needsSubscription)),
            ("2-onboarding-error", .preview(phase: .needsSubscription, addError: "订阅地址返回的是一个网页，不是订阅内容。链接可能已过期，或需要先登录。")),
            ("3-disconnected", .preview(phase: .disconnected, subscription: subscription, nodes: nodes)),
            ("4-connecting", .preview(phase: .connecting("首次使用需要安装后台组件，请在弹出的窗口里授权…"), subscription: subscription, nodes: nodes)),
            ("5-connected", .preview(phase: .connected, subscription: subscription, nodes: nodes, activeNode: "香港-01", startedAt: Date().addingTimeInterval(-47 * 60))),
            ("6-failed", .preview(phase: .failed, subscription: subscription, nodes: nodes,
                                  errorMessage: "没能启动虚拟网卡。请先退出其他代理/VPN 软件再试；仍不行请复制诊断信息。")),
            ("7-notice", .preview(phase: .connected, subscription: subscription, nodes: nodes, activeNode: "香港-01",
                                  notice: "已复制诊断信息，直接粘贴给 AI 即可", startedAt: Date().addingTimeInterval(-90))),
            ("8-update-available", .preview(
                phase: .connected, subscription: subscription, nodes: nodes, activeNode: "香港-01",
                startedAt: Date().addingTimeInterval(-47 * 60),
                availableUpdate: UpdateInfo(
                    version: "0.2.0", tag: "v0.2.0",
                    releaseURL: URL(string: "https://github.com/kadaliao/mytizi/releases/tag/v0.2.0")!,
                    downloadURL: nil))),
            ("9-nodes-unreachable", .preview(
                phase: .connected, subscription: subscription, nodes: nodes.map { NodeRow(name: $0.name, type: $0.type, delay: nil) },
                activeNode: "香港-01", startedAt: Date().addingTimeInterval(-3600),
                connectivityIssue: "所有线路都连不上：节点服务器可能已经不可用，或者当前网络挡住了代理协议。")),
        ]
    }

    // MARK: 渲染

    /// 在离屏窗口里渲染一个界面状态，返回 2x 位图。
    static func render(_ model: AppModel, dark: Bool) -> NSBitmapImageRep? {
        let hosting = NSHostingView(rootView: RootView().environmentObject(model))
        hosting.frame = NSRect(origin: .zero, size: windowSize)
        let window = NSWindow(contentRect: hosting.frame, styleMask: [.borderless], backing: .buffered, defer: false)
        window.appearance = NSAppearance(named: dark ? .darkAqua : .aqua)
        window.contentView = hosting
        // 让 SwiftUI 完成布局与状态更新
        RunLoop.current.run(until: Date().addingTimeInterval(0.35))
        hosting.layoutSubtreeIfNeeded()

        let scale = 2
        guard let rep = NSBitmapImageRep(
            bitmapDataPlanes: nil, pixelsWide: Int(windowSize.width) * scale, pixelsHigh: Int(windowSize.height) * scale,
            bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false, colorSpaceName: .deviceRGB,
            bytesPerRow: 0, bitsPerPixel: 0)
        else { return nil }
        rep.size = windowSize
        hosting.cacheDisplay(in: hosting.bounds, to: rep)
        return rep
    }

    static func png(_ rep: NSBitmapImageRep) -> Data? { rep.representation(using: .png, properties: [:]) }

    static func runSnapshots(directory: String) {
        _ = NSApplication.shared
        try? FileManager.default.createDirectory(atPath: directory, withIntermediateDirectories: true)
        defer { previewDirectories.forEach { try? FileManager.default.removeItem(at: $0) } }
        for (name, model) in scenarios() {
            for dark in [false, true] {
                guard let rep = render(model, dark: dark), let data = png(rep) else { print("渲染失败：\(name)"); continue }
                let path = "\(directory)/\(name)-\(dark ? "dark" : "light").png"
                try? data.write(to: URL(fileURLWithPath: path))
                print("已生成 \(path)")
            }
        }
    }

    // MARK: README 三联图

    static func runScreenshot(directory: String) {
        _ = NSApplication.shared
        versionOverride = CommandLine.arguments.firstIndex(of: "--version").flatMap { CommandLine.arguments.indices.contains($0 + 1) ? CommandLine.arguments[$0 + 1] : nil } ?? "0.2.0"
        try? FileManager.default.createDirectory(atPath: directory, withIntermediateDirectories: true)
        defer { previewDirectories.forEach { try? FileManager.default.removeItem(at: $0) } }

        let shots: [(String, String, AppModel)] = [
            ("粘贴订阅链接", "1-onboarding", .preview(phase: .needsSubscription)),
            ("一键连接", "5-connected", .preview(phase: .connected, subscription: subscription, nodes: nodes, activeNode: "香港-01", startedAt: Date().addingTimeInterval(-47 * 60))),
            ("出问题，一键复制诊断", "6-failed", .preview(phase: .failed, subscription: subscription, nodes: nodes,
                                                         errorMessage: "没能启动虚拟网卡。请先退出其他代理/VPN 软件再试；仍不行请复制诊断信息。")),
        ]
        for dark in [false, true] {
            let reps = shots.compactMap { render($0.2, dark: dark) }
            guard reps.count == shots.count,
                  let data = composite(reps: reps, captions: shots.map(\.0), dark: dark)
            else { print("合成失败"); continue }
            let path = "\(directory)/screenshot-\(dark ? "dark" : "light").png"
            try? data.write(to: URL(fileURLWithPath: path))
            print("已生成 \(path)（\(data.count / 1024) KB）")
        }
    }

    /// 把几张窗口图排成一行：圆角 + 细边 + 阴影，渐变底色，下方一行说明。
    static func composite(reps: [NSBitmapImageRep], captions: [String], dark: Bool) -> Data? {
        let scale: CGFloat = 2
        let w = windowSize.width * scale, h = windowSize.height * scale
        let margin: CGFloat = 110, gap: CGFloat = 80, captionHeight: CGFloat = 130
        let width = margin * 2 + w * CGFloat(reps.count) + gap * CGFloat(reps.count - 1)
        let height = margin + h + captionHeight + margin * 0.6

        guard let canvas = NSBitmapImageRep(
            bitmapDataPlanes: nil, pixelsWide: Int(width), pixelsHigh: Int(height), bitsPerSample: 8, samplesPerPixel: 4,
            hasAlpha: true, isPlanar: false, colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0),
            let context = NSGraphicsContext(bitmapImageRep: canvas)
        else { return nil }
        NSGraphicsContext.saveGraphicsState()
        NSGraphicsContext.current = context
        defer { NSGraphicsContext.restoreGraphicsState() }

        // 背景
        let top = dark ? NSColor(srgbRed: 0.10, green: 0.14, blue: 0.21, alpha: 1) : NSColor(srgbRed: 0.90, green: 0.94, blue: 0.99, alpha: 1)
        let bottom = dark ? NSColor(srgbRed: 0.05, green: 0.07, blue: 0.12, alpha: 1) : NSColor(srgbRed: 0.80, green: 0.88, blue: 0.97, alpha: 1)
        NSGradient(starting: top, ending: bottom)?.draw(in: NSRect(x: 0, y: 0, width: width, height: height), angle: -90)

        for (index, rep) in reps.enumerated() {
            let x = margin + CGFloat(index) * (w + gap)
            let frame = NSRect(x: x, y: margin * 0.6 + captionHeight, width: w, height: h)
            let radius: CGFloat = 26
            let path = NSBezierPath(roundedRect: frame, xRadius: radius, yRadius: radius)

            NSGraphicsContext.saveGraphicsState()
            let shadow = NSShadow()
            shadow.shadowColor = NSColor.black.withAlphaComponent(dark ? 0.55 : 0.22)
            shadow.shadowBlurRadius = 46
            shadow.shadowOffset = NSSize(width: 0, height: -16)
            shadow.set()
            (dark ? NSColor(white: 0.12, alpha: 1) : NSColor.white).setFill()
            path.fill()
            NSGraphicsContext.restoreGraphicsState()

            NSGraphicsContext.saveGraphicsState()
            path.addClip()
            rep.draw(in: frame)
            NSGraphicsContext.restoreGraphicsState()

            NSColor.white.withAlphaComponent(dark ? 0.12 : 0.6).setStroke()
            path.lineWidth = 2
            path.stroke()

            let paragraph = NSMutableParagraphStyle()
            paragraph.alignment = .center
            let text = NSAttributedString(string: captions[index], attributes: [
                .font: NSFont(name: "PingFangSC-Medium", size: 46) ?? NSFont.systemFont(ofSize: 46, weight: .medium),
                .foregroundColor: dark ? NSColor(white: 0.85, alpha: 1) : NSColor(srgbRed: 0.15, green: 0.22, blue: 0.33, alpha: 1),
                .paragraphStyle: paragraph,
            ])
            text.draw(in: NSRect(x: x, y: margin * 0.6 + 30, width: w, height: 70))
        }
        return canvas.representation(using: .png, properties: [:])
    }
}
#endif
