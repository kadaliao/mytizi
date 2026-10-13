import AppKit
import Foundation
import MytiziCore
import MytiziKit
import SwiftUI

struct NodeRow: Identifiable, Equatable {
    var name: String
    var type: String
    var delay: Int?
    var id: String { name }
}

@MainActor
final class AppModel: ObservableObject {
    enum Phase: Equatable {
        case needsSubscription
        case disconnected
        case connecting(String)
        case connected
        case disconnecting
        case failed
    }

    nonisolated static let autoSelection = "AUTO"

    let paths: UserPaths
    private let store: SettingsStore
    private let logger: AppLogger
    private let helper = HelperClient()
    private let installer = HelperInstaller()
    private let subscriptionService = SubscriptionService()

    @Published private(set) var settings: AppSettings
    @Published private(set) var phase: Phase
    @Published private(set) var helperStatus: HelperStatus?
    @Published private(set) var nodes: [NodeRow] = []
    /// "AUTO" 或某个节点名
    @Published var selection: String = AppModel.autoSelection
    @Published private(set) var activeNode: String?
    @Published private(set) var errorMessage: String?
    @Published private(set) var notice: String?
    @Published private(set) var isAddingSubscription = false
    @Published private(set) var addProgress: String?
    @Published private(set) var addError: String?
    @Published private(set) var isRefreshingSubscription = false
    @Published private(set) var isTestingLatency = false
    @Published private(set) var isCopyingDiagnostics = false
    @Published private(set) var installState: InstallState = .upToDate

    private var api: MihomoAPI?
    private var pollTask: Task<Void, Never>?
    private var noticeTask: Task<Void, Never>?
    private var lastDelayRefresh = Date.distantPast
    private var lastSubscriptionCheck = Date()

    init(paths: UserPaths = UserPaths()) {
        self.paths = paths
        store = SettingsStore(paths: paths)
        logger = AppLogger(paths: paths)
        let loaded = store.load()
        settings = loaded
        phase = loaded.subscription == nil ? .needsSubscription : .disconnected
        nodes = loaded.subscription?.nodes.map { NodeRow(name: $0.name, type: $0.type, delay: nil) } ?? []
        log("App 启动 v\(Self.appVersion)")
    }

    /// 所有写入 app.log 的内容都先脱敏：URLError 的描述里可能带着完整的订阅链接（含 token）。
    private func log(_ message: String, level: LogLevel = .info, extraSecret: String? = nil) {
        var redactor = Redactor()
        redactor.addSubscriptionURL(settings.subscription?.url)
        redactor.addSubscriptionURL(extraSecret)
        let text = redactor.redact(message)
        switch level {
        case .info: logger.info(text)
        case .error: logger.error(text)
        }
    }

    private enum LogLevel { case info, error }

    static var appVersion: String {
        Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "dev"
    }

    // MARK: 生命周期

    func start() {
        guard pollTask == nil else { return }
        pollTask = Task { [weak self] in
            // 先接管一次现状：App 重新打开时，后台组件可能还保持着连接。
            await self?.poll()
            if let self, self.settings.autoConnectOnLaunch, self.phase == .disconnected { await self.connect() }
            while !Task.isCancelled {
                try? await Task.sleep(nanoseconds: 2_000_000_000)
                await self?.poll()
            }
        }
        NSWorkspace.shared.notificationCenter.addObserver(
            forName: NSWorkspace.didWakeNotification, object: nil, queue: .main
        ) { [weak self] _ in
            Task { @MainActor in await self?.handleWake() }
        }
    }

    var isConnected: Bool { phase == .connected }
    var isBusy: Bool {
        switch phase {
        case .connecting, .disconnecting: return true
        default: return false
        }
    }

    // MARK: 轮询

    private func poll() async {
        let status: HelperStatus
        do {
            status = try await helper.status()
        } catch {
            helperStatus = nil
            if phase == .connected {
                phase = .failed
                errorMessage = "与后台组件的连接中断了。"
            }
            return
        }
        helperStatus = status
        guard !isBusy else { return }

        switch status.phase {
        case .running:
            if phase != .connected {
                phase = .connected
                errorMessage = nil
                log("检测到后台组件已在运行，接管当前连接")
            }
            if let port = status.controllerPort, let secret = status.controllerSecret {
                api = MihomoAPI(port: port, secret: secret)
            }
            await refreshRuntimeInfo()
            await refreshSubscriptionIfDue()
        case .crashed:
            // 只在我们亲眼看着它从"已连接"掉下来时才报失败；启动时读到很久以前的崩溃记录不打扰用户
            //（诊断报告里仍然会写）。
            if phase == .connected {
                phase = .failed
                errorMessage = "连接意外中断，网络设置已自动恢复。可以重新连接，或点\u{201C}遇到问题\u{201D}复制诊断信息。"
                log("核心意外退出：\(String(describing: status.lastExit))", level: .error)
            }
            api = nil
        case .stopped:
            if phase == .connected {
                phase = settings.subscription == nil ? .needsSubscription : .disconnected
            }
            api = nil
            activeNode = nil
        case .starting, .stopping:
            break
        }
    }

    private func refreshRuntimeInfo() async {
        guard let api else { return }
        if let group = try? await api.group(ConfigBuilder.proxyGroup) {
            let chosen = group.now ?? Self.autoSelection
            if selection != chosen { selection = chosen }
            if chosen == Self.autoSelection, let auto = try? await api.group(ConfigBuilder.autoGroup) {
                activeNode = auto.now
            } else {
                activeNode = chosen
            }
        }
        // 延迟记录变化慢，10 秒取一次。
        if Date().timeIntervalSince(lastDelayRefresh) > 10, let provider = try? await api.provider(ConfigBuilder.providerName) {
            lastDelayRefresh = Date()
            nodes = provider.proxies.map { NodeRow(name: $0.name, type: $0.type, delay: $0.lastDelay) }
        }
    }

    private func handleWake() async {
        guard phase == .connected, let api else { return }
        // 睡眠唤醒后，旧网络上建立的连接都已失效：清掉让应用按新网络重连。
        try? await Task.sleep(nanoseconds: 3_000_000_000)
        try? await api.closeAllConnections()
        log("唤醒后已清理旧连接")
    }

    // MARK: 订阅

    func addSubscription(_ input: String) async {
        guard !isAddingSubscription else { return }
        isAddingSubscription = true
        addError = nil
        defer { isAddingSubscription = false; addProgress = nil }
        do {
            let url = try SubscriptionService.normalizedURL(input)
            addProgress = "正在下载订阅…"
            let fetched = try await subscriptionService.fetch(url: url)
            addProgress = "正在解析线路…"
            let nodes = try await probe(fetched.body)
            guard !nodes.isEmpty else { throw SubscriptionError.noNodes }
            try store.saveSubscriptionBody(fetched.body)

            var next = settings
            next.subscription = SubscriptionRecord(
                url: url.absoluteString, name: fetched.title ?? url.host ?? "订阅", addedAt: Date(), updatedAt: Date(),
                nodes: nodes, usage: fetched.usage, updateIntervalHours: fetched.updateIntervalHours)
            try store.save(next)
            settings = next
            self.nodes = nodes.map { NodeRow(name: $0.name, type: $0.type, delay: nil) }
            phase = .disconnected
            errorMessage = nil
            logger.info("已添加订阅 \(url.host ?? "")，\(nodes.count) 条线路")
        } catch {
            addError = Self.message(for: error)
            log("添加订阅失败：\(error)", level: .error, extraSecret: input)
        }
    }

    /// 刷新订阅。已连接时若直连抓不到，再改走本机代理端口试一次。
    func refreshSubscription(manual: Bool = true) async {
        guard !isRefreshingSubscription, var subscription = settings.subscription, let url = URL(string: subscription.url) else { return }
        isRefreshingSubscription = true
        defer { isRefreshingSubscription = false }
        do {
            // 订阅主机在规则里固定直连，所以这里不需要（也不应该）走代理：
            // 节点全挂时照样能刷新；订阅在内网时代理也到不了。
            let fetched = try await subscriptionService.fetch(url: url)
            let nodes = try await probe(fetched.body)
            guard !nodes.isEmpty else { throw SubscriptionError.noNodes }
            try store.saveSubscriptionBody(fetched.body)

            subscription.nodes = nodes
            subscription.updatedAt = Date()
            subscription.usage = fetched.usage ?? subscription.usage
            subscription.updateIntervalHours = fetched.updateIntervalHours ?? subscription.updateIntervalHours
            if let title = fetched.title { subscription.name = title }
            var next = settings
            next.subscription = subscription
            try store.save(next)
            settings = next
            self.nodes = nodes.map { NodeRow(name: $0.name, type: $0.type, delay: nil) }

            if phase == .connected {
                try await helper.updateFiles([HelperFile(path: "providers/sub.yaml", data: fetched.body)])
                try? await api?.updateProvider(ConfigBuilder.providerName)
            }
            lastSubscriptionCheck = Date()
            if manual { showNotice("订阅已更新：\(nodes.count) 条线路") }
            log("订阅已更新，\(nodes.count) 条线路")
        } catch {
            log("刷新订阅失败：\(error)", level: .error)
            if manual { showNotice("更新失败：\(Self.message(for: error))", seconds: 6) }
        }
    }

    private func refreshSubscriptionIfDue() async {
        guard let subscription = settings.subscription else { return }
        let interval = TimeInterval(max(subscription.updateIntervalHours ?? 24, 6)) * 3600
        guard Date().timeIntervalSince(subscription.updatedAt ?? .distantPast) > interval,
              Date().timeIntervalSince(lastSubscriptionCheck) > 1800 else { return }
        lastSubscriptionCheck = Date()
        await refreshSubscription(manual: false)
    }

    func removeSubscription() async {
        if isConnected || isBusy { await disconnect() }
        var next = settings
        next.subscription = nil
        try? store.save(next)
        settings = next
        nodes = []
        phase = .needsSubscription
        errorMessage = nil
    }

    private func probe(_ body: Data) async throws -> [ProbedNode] {
        let core = Self.coreForProbe()
        do {
            return try await CoreProbe.nodes(in: body, core: core, workDirectory: paths.caches)
        } catch let error as CoreProbe.ProbeError {
            throw SubscriptionError.notASubscription(error.localizedDescription)
        }
    }

    /// 探测用的核心：包内的通用二进制；开发运行时回退到 Vendor。
    static func coreForProbe() -> URL {
        let bundled = BundleComponents.main().core
        if FileManager.default.isExecutableFile(atPath: bundled.path) { return bundled }
        let installed = URL(fileURLWithPath: SystemPaths.coreBinary)
        if FileManager.default.isExecutableFile(atPath: installed.path) { return installed }
        return bundled
    }

    // MARK: 连接

    func toggle() async {
        switch phase {
        case .connected: await disconnect()
        case .disconnected, .failed: await connect()
        default: break
        }
    }

    func connect() async {
        guard phase == .disconnected || phase == .failed else { return }
        guard let subscription = settings.subscription, let body = store.loadSubscriptionBody() else {
            phase = .needsSubscription
            return
        }
        errorMessage = nil
        phase = .connecting("正在检查组件…")
        do {
            installState = await installer.state()
            if installState != .upToDate {
                phase = .connecting("首次使用需要安装后台组件，请在弹出的窗口里授权…")
                try await installer.install()
                installState = .upToDate
            }

            phase = .connecting("正在生成配置…")
            let secret = (0..<2).map { _ in UUID().uuidString.replacingOccurrences(of: "-", with: "") }.joined()
            let controllerPort = FreePort.find(preferred: [19097])
            var options = ConfigOptions(controllerPort: controllerPort, controllerSecret: secret, mixedPort: nil, tun: true)
            options.directHosts = [subscription.host].filter { !$0.isEmpty }
            options.healthCheckURL = settings.healthCheckURL
            options.localDNS = Self.currentSystemDNS()
            do {
                options.customRules = try store.loadCustomRules()
            } catch {
                log("自定义规则有误，已忽略：\(error)", level: .error)
                notice = "自定义规则有错误，已忽略（\(error)）"
            }
            let yaml = ConfigBuilder.build(options).render()
            writeRedactedConfig(yaml, secret: secret)

            phase = .connecting("正在启动…")
            let params = StartParams(
                configYAML: yaml, files: [HelperFile(path: "providers/sub.yaml", data: body)],
                controllerPort: controllerPort, controllerSecret: secret,
                takeOverDNS: settings.takeOverDNS, disableIPv6: settings.disableIPv6)
            let status = try await helper.start(params)
            helperStatus = status
            api = MihomoAPI(port: controllerPort, secret: secret)
            phase = .connected
            lastDelayRefresh = .distantPast
            log("已连接")
            await refreshRuntimeInfo()
        } catch HelperInstaller.InstallError.cancelled {
            api = nil
            phase = .disconnected
            showNotice("已取消授权，没有安装后台组件")
            log("用户取消了管理员授权")
        } catch {
            api = nil
            phase = .failed
            errorMessage = Self.message(for: error)
            log("连接失败：\(error)", level: .error)
            // 失败时后台组件会自行撤销改动；这里只同步一次状态。
            helperStatus = try? await helper.status()
        }
    }

    func disconnect() async {
        guard phase == .connected || phase == .failed || isBusy else { return }
        phase = .disconnecting
        do {
            helperStatus = try await helper.stop()
        } catch {
            log("断开失败：\(error)", level: .error)
            errorMessage = Self.message(for: error)
        }
        api = nil
        activeNode = nil
        phase = settings.subscription == nil ? .needsSubscription : .disconnected
        log("已断开")
    }

    func reconnect() async {
        if phase == .connected || phase == .failed { await disconnect() }
        await connect()
    }

    // MARK: 线路

    func select(_ name: String) async {
        guard let api else { selection = name; return }
        do {
            try await api.select(group: ConfigBuilder.proxyGroup, proxy: name)
            selection = name
            try? await api.closeAllConnections()
        } catch {
            showNotice("切换线路失败：\(Self.message(for: error))")
        }
    }

    func testLatency() async {
        guard let api, !isTestingLatency else { return }
        isTestingLatency = true
        defer { isTestingLatency = false }
        do {
            let result = try await api.groupDelay(ConfigBuilder.autoGroup, url: settings.healthCheckURL)
            nodes = nodes.map { var row = $0; row.delay = result[$0.name]; return row }
        } catch {
            showNotice("测速失败：\(Self.message(for: error))")
        }
    }

    // MARK: 设置

    func update(_ change: (inout AppSettings) -> Void) {
        var next = settings
        change(&next)
        try? store.save(next)
        settings = next
    }

    func uninstallComponents() async {
        if isConnected { await disconnect() }
        do {
            try await installer.uninstall()
            installState = .notInstalled
            showNotice("后台组件已卸载")
        } catch {
            showNotice("卸载失败：\(Self.message(for: error))")
        }
    }

    func openCustomRulesFile() {
        try? paths.prepare()
        if !FileManager.default.fileExists(atPath: paths.customRules.path) {
            let template = """
            # 自定义规则：每行一条，优先于所有内置规则。保存后重新连接生效。
            # 格式：类型,值,目标      目标可以写 PROXY（代理）、DIRECT（直连）、REJECT（拒绝）
            # 例：
            # DOMAIN-SUFFIX,example.com,PROXY
            # DOMAIN,intranet.corp.example,DIRECT
            # IP-CIDR,203.0.113.0/24,DIRECT,no-resolve

            """
            try? template.write(to: paths.customRules, atomically: true, encoding: .utf8)
        }
        NSWorkspace.shared.open(paths.customRules)
    }

    // MARK: 诊断（一键复制给 AI）

    func copyDiagnostics(full: Bool) async {
        guard !isCopyingDiagnostics else { return }
        isCopyingDiagnostics = true
        showNotice("正在收集诊断信息…", seconds: 30)
        defer { isCopyingDiagnostics = false }

        let text = await DiagnosticsService(paths: paths).report(base: diagnosticsBase(), full: full)
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(text, forType: .string)
        logger.info("已复制诊断信息（\(full ? "完整版" : "摘要版")，\(text.count) 字符）")
        showNotice(full ? "已复制完整诊断（含最近连接日志），直接粘贴给 AI 即可" : "已复制诊断信息，直接粘贴给 AI 即可", seconds: 6)
    }

    func diagnosticsBase() -> DiagnosticsInput {
        var input = DiagnosticsInput()
        input.appVersion = Self.appVersion
        input.appPhase = phaseDescription
        input.lastError = errorMessage
        input.settings = settings
        input.userPaths = paths
        if let rules = try? store.loadCustomRules() { input.customRuleCount = rules.count }
        input.customRulesError = customRulesProblem()
        if let subscription = settings.subscription {
            let known = Dictionary(uniqueKeysWithValues: nodes.map { ($0.name, $0.delay) })
            input.nodes = subscription.nodes.map { NodeDelay(name: $0.name, type: $0.type, delay: known[$0.name] ?? nil) }
        }
        return input
    }

    private func customRulesProblem() -> String? {
        do { _ = try store.loadCustomRules(); return nil } catch { return "\(error)" }
    }

    func openLogsFolder() {
        NSWorkspace.shared.activateFileViewerSelecting([URL(fileURLWithPath: SystemPaths.coreLog), paths.appLog])
    }

    var phaseDescription: String {
        switch phase {
        case .needsSubscription: return "尚未添加订阅"
        case .disconnected: return "未连接"
        case let .connecting(step): return "连接中（\(step)）"
        case .connected: return "已连接"
        case .disconnecting: return "正在断开"
        case .failed: return "失败"
        }
    }

    // MARK: 工具

    func showNotice(_ text: String, seconds: Double = 3) {
        notice = text
        noticeTask?.cancel()
        noticeTask = Task { [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(seconds * 1_000_000_000))
            guard !Task.isCancelled else { return }
            self?.notice = nil
        }
    }

    private func writeRedactedConfig(_ yaml: String, secret: String) {
        var redactor = Redactor()
        redactor.add(secret, label: "控制密钥")
        try? paths.prepare()
        try? AtomicFile.write(Data(redactor.redact(yaml).utf8), to: paths.lastConfigRedacted, permissions: 0o600)
    }

    /// 连接前系统实际使用的 DNS（路由器），用于解析 .lan 之类的本地域名。
    static func currentSystemDNS() -> [String] {
        let resolvers = SystemFactsParser.resolvers(fromScutilDNS: Shell.run("/usr/sbin/scutil", ["--dns"], timeout: 5).stdout)
        guard let primary = resolvers.first(where: { !$0.scoped && $0.domain == nil }) else { return [] }
        return primary.nameservers.filter { $0.contains(".") && !$0.hasPrefix("198.18.") && $0 != "127.0.0.1" }
    }

    static func message(for error: Error) -> String {
        if let error = error as? HelperClientError {
            if let helper = error.helperError {
                switch helper.code {
                case "core-missing": return "内置组件不完整，请重新安装 App。"
                case "config-invalid": return "生成的配置没有通过校验。请点\u{201C}遇到问题\u{201D}复制诊断信息排查。"
                case "tun-missing", "controller-timeout":
                    return "没能启动虚拟网卡。请先退出其他代理/VPN 软件再试；仍不行请复制诊断信息。"
                case "network-failed": return "修改系统网络设置失败，已自动撤销。请复制诊断信息排查。"
                default: return helper.message
                }
            }
            return error.localizedDescription
        }
        if let error = error as? LocalizedError, let description = error.errorDescription { return description }
        return error.localizedDescription
    }
}

#if DEBUG
nonisolated(unsafe) var previewDirectories: [URL] = []

extension AppModel {
    /// 仅用于截图渲染：不访问磁盘上的真实设置，也不连接后台组件。
    static func preview(
        phase: Phase, subscription: SubscriptionRecord? = nil, nodes: [NodeRow] = [], selection: String = AppModel.autoSelection,
        activeNode: String? = nil, errorMessage: String? = nil, addError: String? = nil, notice: String? = nil,
        startedAt: Date? = nil
    ) -> AppModel {
        let home = FileManager.default.temporaryDirectory.appendingPathComponent("mytizi-preview-\(UUID().uuidString)")
        let model = AppModel(paths: UserPaths(home: home))
        previewDirectories.append(home)
        model.settings = AppSettings(subscription: subscription)
        model.phase = phase
        model.nodes = nodes
        model.selection = selection
        model.activeNode = activeNode
        model.errorMessage = errorMessage
        model.addError = addError
        model.notice = notice
        if let startedAt { model.helperStatus = HelperStatus(phase: .running, startedAt: startedAt) }
        return model
    }
}
#endif
