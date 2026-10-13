import AppKit
import MytiziCore
import MytiziKit
import SwiftUI

struct MyTiziApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var delegate
    @StateObject private var model = AppModel()

    var body: some Scene {
        Window(AppInfo.displayName, id: "main") {
            RootView()
                .environmentObject(model)
                .onAppear {
                    delegate.model = model
                    model.start()
                }
        }
        .windowResizability(.contentSize)
        .commands {
            CommandGroup(replacing: .newItem) {}
            CommandMenu("诊断") {
                Button("复制诊断信息（给 AI 排查）") { Task { await model.copyDiagnostics(full: false) } }
                    .keyboardShortcut("c", modifiers: [.command, .shift])
                Button("复制完整诊断（含最近连接日志）") { Task { await model.copyDiagnostics(full: true) } }
            }
        }

        MenuBarExtra {
            MenuBarContent().environmentObject(model)
        } label: {
            Image(systemName: model.isConnected ? "network.badge.shield.half.filled" : "network")
        }
    }
}

final class AppDelegate: NSObject, NSApplicationDelegate {
    weak var model: AppModel?

    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool { false }

    /// 关闭窗口后（App 仍在菜单栏里运行），点 Dock 图标应当把窗口带回来。
    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        if !flag {
            sender.windows.first { $0.canBecomeMain }?.makeKeyAndOrderFront(nil)
            sender.activate(ignoringOtherApps: true)
        }
        return true
    }

    /// 退出时如果还连着：问一下是断开还是保持（后台组件会继续维持连接）。
    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        guard let model, model.isConnected else { return .terminateNow }
        let alert = NSAlert()
        alert.messageText = "还在连接中"
        alert.informativeText = "退出 \(AppInfo.displayName) 时要断开连接吗？选择\u{201C}保持连接\u{201D}，网络会继续走代理，之后重新打开 App 即可管理。"
        alert.addButton(withTitle: "断开并退出")
        alert.addButton(withTitle: "保持连接并退出")
        alert.addButton(withTitle: "取消")
        switch alert.runModal() {
        case .alertFirstButtonReturn:
            Task { @MainActor in
                await model.disconnect()
                NSApp.reply(toApplicationShouldTerminate: true)
            }
            return .terminateLater
        case .alertSecondButtonReturn:
            return .terminateNow
        default:
            return .terminateCancel
        }
    }
}
