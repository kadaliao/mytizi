import Foundation
import MytiziKit

/// App 自己的日志：~/Library/Logs/MyTizi/app.log。
public final class AppLogger {
    private let log: RotatingLog

    public init(paths: UserPaths = UserPaths()) {
        try? paths.prepare()
        log = RotatingLog(path: paths.appLog.path, maxBytes: 1024 * 1024, keep: 2)
    }

    public func info(_ message: String) { log.line("INFO  \(message)") }
    public func error(_ message: String) { log.line("ERROR \(message)") }
}

/// 汇总诊断所需的一切：向后台组件问状态、读系统网络、读日志、跑实机自检，然后渲染并脱敏。
public struct DiagnosticsService {
    public var helper: HelperClient
    public var installer: HelperInstaller
    public var paths: UserPaths

    public init(helper: HelperClient = HelperClient(), installer: HelperInstaller = HelperInstaller(), paths: UserPaths = UserPaths()) {
        self.helper = helper
        self.installer = installer
        self.paths = paths
    }

    /// 一键生成报告文本。`base` 由 App 填入它自己知道的部分（版本、阶段、设置、最近错误）。
    public func report(base: DiagnosticsInput, full: Bool, runSelfTest: Bool = true) async -> String {
        let input = await gather(base, runSelfTest: runSelfTest)
        return DiagnosticsReport.render(input, redactor: Self.makeRedactor(for: input), includeFullLogs: full)
    }

    public func gather(_ base: DiagnosticsInput, runSelfTest: Bool) async -> DiagnosticsInput {
        var input = base
        input.userPaths = paths
        input.now = Date()
        input.facts = await Task.detached { SystemFactsCollector.collect() }.value

        do {
            input.helperHello = try await helper.hello()
            input.helperStatus = try await helper.status()
        } catch {
            input.helperError = error.localizedDescription
        }
        input.installedManifest = HelperInstaller.installedManifest()
        input.installState = await installer.state()

        input.appLog = RotatingLog.tail(ofFileAt: paths.appLog.path, lines: 200)
        input.helperLog = RotatingLog.tail(ofFileAt: SystemPaths.helperLog, lines: 200)
        input.coreLog = RotatingLog.tail(ofFileAt: SystemPaths.coreLog, lines: 400)

        if let status = input.helperStatus, status.phase == .running, let port = status.controllerPort, let secret = status.controllerSecret {
            let api = MihomoAPI(port: port, secret: secret)
            input.coreVersion = try? await api.version()
            if let provider = try? await api.provider(ConfigBuilder.providerName) {
                input.nodes = provider.proxies.map { NodeDelay(name: $0.name, type: $0.type, delay: $0.lastDelay) }
            }
            if runSelfTest {
                input.selfTest = await SelfTest.run(coreLog: { RotatingLog.tail(ofFileAt: SystemPaths.coreLog, lines: 400) })
            }
        } else if input.nodes.isEmpty, let subscription = input.settings.subscription {
            input.nodes = subscription.nodes.map { NodeDelay(name: $0.name, type: $0.type, delay: nil) }
        }
        return input
    }

    /// 必须抹掉的秘密：订阅链接（含 token）、控制接口密钥。
    public static func makeRedactor(for input: DiagnosticsInput) -> Redactor {
        var redactor = Redactor()
        redactor.addSubscriptionURL(input.settings.subscription?.url)
        redactor.add(input.helperStatus?.controllerSecret, label: "控制密钥")
        return redactor
    }
}
