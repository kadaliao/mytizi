#if DEBUG
import AppKit
import MytiziCore
import SwiftUI

/// 调试用：把各状态的界面渲染成 PNG（`MyTizi --snapshot <目录>`），不需要屏幕录制权限。
/// 正式构建（release）不包含这段代码。
@MainActor
enum Snapshot {
    static func run(directory: String) {
        _ = NSApplication.shared
        try? FileManager.default.createDirectory(atPath: directory, withIntermediateDirectories: true)

        let nodes = [
            NodeRow(name: "香港-01", type: "Shadowsocks", delay: 82),
            NodeRow(name: "日本-02", type: "Trojan", delay: 121),
            NodeRow(name: "美国-03", type: "Vmess", delay: nil),
        ]
        let subscription = SubscriptionRecord(
            url: "https://sub.example.com/x?token=abc", name: "公司外网代理", addedAt: Date(), updatedAt: Date().addingTimeInterval(-3 * 3600),
            nodes: nodes.map { ProbedNode(name: $0.name, type: $0.type) },
            usage: SubscriptionUsage(upload: 3_000_000_000, download: 40_000_000_000, total: 200_000_000_000, expire: Date().addingTimeInterval(90 * 86400)))

        let scenarios: [(String, AppModel)] = [
            ("1-onboarding", .preview(phase: .needsSubscription)),
            ("2-onboarding-error", .preview(phase: .needsSubscription, addError: "订阅地址返回的是一个网页，不是订阅内容。链接可能已过期，或需要先登录。")),
            ("3-disconnected", .preview(phase: .disconnected, subscription: subscription, nodes: nodes)),
            ("4-connecting", .preview(phase: .connecting("首次使用需要安装后台组件，请在弹出的窗口里授权…"), subscription: subscription, nodes: nodes)),
            ("5-connected", .preview(phase: .connected, subscription: subscription, nodes: nodes, activeNode: "香港-01", startedAt: Date().addingTimeInterval(-47 * 60))),
            ("6-failed", .preview(phase: .failed, subscription: subscription, nodes: nodes,
                                  errorMessage: "没能启动虚拟网卡。请先退出其他代理/VPN 软件再试；仍不行请复制诊断信息。")),
            ("7-notice", .preview(phase: .connected, subscription: subscription, nodes: nodes, activeNode: "香港-01",
                                  notice: "已复制诊断信息，直接粘贴给 AI 即可", startedAt: Date().addingTimeInterval(-90))),
        ]
        defer { previewDirectories.forEach { try? FileManager.default.removeItem(at: $0) } }
        for (name, model) in scenarios {
            for (suffix, scheme) in [("light", ColorScheme.light), ("dark", ColorScheme.dark)] {
                let view = RootView().environmentObject(model).environment(\.colorScheme, scheme)
                let renderer = ImageRenderer(content: view)
                renderer.scale = 2
                guard let image = renderer.nsImage, let tiff = image.tiffRepresentation,
                      let png = NSBitmapImageRep(data: tiff)?.representation(using: .png, properties: [:])
                else { print("渲染失败：\(name)"); continue }
                let path = "\(directory)/\(name)-\(suffix).png"
                try? png.write(to: URL(fileURLWithPath: path))
                print("已生成 \(path)")
            }
        }
    }
}
#endif
