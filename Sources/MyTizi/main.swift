import AppKit
import MytiziCore
import MytiziKit

// 同一个可执行文件既是 App，也是命令行诊断工具：
//   MyTizi --diagnose [--full] [--no-selftest]   在终端打印诊断报告（给没有图形界面的 AI / 同事用）
//   MyTizi --check-subscription <链接>           用和界面完全相同的代码下载并解析订阅，打印节点（不保存任何东西）
#if DEBUG
if let index = CommandLine.arguments.firstIndex(of: "--snapshot"), CommandLine.arguments.count > index + 1 {
    MainActor.assumeIsolated { Snapshot.runSnapshots(directory: CommandLine.arguments[index + 1]) }
    exit(0)
}
if let index = CommandLine.arguments.firstIndex(of: "--screenshot"), CommandLine.arguments.count > index + 1 {
    MainActor.assumeIsolated { Snapshot.runScreenshot(directory: CommandLine.arguments[index + 1]) }
    exit(0)
}
#endif
if CommandLine.arguments.contains("--diagnose") {
    CLI.runDiagnose(arguments: CommandLine.arguments)
} else if let index = CommandLine.arguments.firstIndex(of: "--check-subscription"), CommandLine.arguments.count > index + 1 {
    CLI.runCheckSubscription(link: CommandLine.arguments[index + 1])
} else {
    MyTiziApp.main()
}

enum CLI {
    static func runCheckSubscription(link: String) -> Never {
        let done = DispatchSemaphore(value: 0)
        var exitCode: Int32 = 0
        Task {
            do {
                let url = try SubscriptionService.normalizedURL(link)
                let fetched = try await SubscriptionService().fetch(url: url)
                print("下载成功：\(fetched.body.count) 字节，标题 \(fetched.title ?? "-")，流量 \(fetched.usage.map { "\($0.used)/\($0.total)" } ?? "-")")
                let work = FileManager.default.temporaryDirectory.appendingPathComponent("mytizi-check-\(UUID().uuidString.prefix(6))")
                try FileManager.default.createDirectory(at: work, withIntermediateDirectories: true)
                defer { try? FileManager.default.removeItem(at: work) }
                let core = BundleComponents.main().core
                let nodes = try await CoreProbe.nodes(in: fetched.body, core: FileManager.default.isExecutableFile(atPath: core.path) ? core : URL(fileURLWithPath: SystemPaths.coreBinary), workDirectory: work)
                print("解析出 \(nodes.count) 条线路：")
                for node in nodes.prefix(50) { print("  - \(node.name)  [\(node.type)]\(node.udp ? " udp" : "")") }
                if nodes.isEmpty { exitCode = 2 }
            } catch {
                print("失败：\(error.localizedDescription)")
                exitCode = 1
            }
            done.signal()
        }
        done.wait()
        exit(exitCode)
    }

    static func runDiagnose(arguments: [String]) -> Never {
        let full = arguments.contains("--full")
        let selfTest = !arguments.contains("--no-selftest")
        let done = DispatchSemaphore(value: 0)
        Task {
            var base = DiagnosticsInput()
            base.appVersion = Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "dev"
            base.appPhase = "（命令行，未运行界面）"
            let store = SettingsStore()
            base.settings = store.load()
            if let rules = try? store.loadCustomRules() { base.customRuleCount = rules.count }
            if let subscription = base.settings.subscription {
                base.nodes = subscription.nodes.map { NodeDelay(name: $0.name, type: $0.type, delay: nil) }
            }
            let text = await DiagnosticsService().report(base: base, full: full, runSelfTest: selfTest)
            print(text)
            done.signal()
        }
        done.wait()
        exit(0)
    }
}
