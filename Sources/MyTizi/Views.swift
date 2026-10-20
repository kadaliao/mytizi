import AppKit
import MytiziCore
import MytiziKit
import SwiftUI

// MARK: 根视图

struct RootView: View {
    @EnvironmentObject var model: AppModel

    var body: some View {
        ZStack(alignment: .bottom) {
            if model.phase == .needsSubscription {
                OnboardingView()
            } else {
                MainView()
            }
            if let notice = model.notice {
                NoticeBanner(text: notice)
                    .padding(.bottom, 14)
                    .transition(.move(edge: .bottom).combined(with: .opacity))
            }
        }
        .animation(.easeInOut(duration: 0.2), value: model.notice)
        .frame(width: 360, height: 580)
        .background(Color(nsColor: .windowBackgroundColor))
    }
}

struct NoticeBanner: View {
    let text: String

    var body: some View {
        Text(text)
            .font(.callout)
            .multilineTextAlignment(.center)
            .padding(.horizontal, 14)
            .padding(.vertical, 9)
            .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 10))
            .overlay(RoundedRectangle(cornerRadius: 10).stroke(Color.primary.opacity(0.08)))
            .shadow(color: .black.opacity(0.12), radius: 8, y: 2)
            .padding(.horizontal, 20)
    }
}

// MARK: 引导：添加订阅

struct OnboardingView: View {
    @EnvironmentObject var model: AppModel
    @State private var link = ""
    @FocusState private var focused: Bool

    var body: some View {
        VStack(spacing: 0) {
            Spacer(minLength: 40)
            Image(systemName: "network.badge.shield.half.filled")
                .font(.system(size: 60))
                .foregroundStyle(.tint)
            Text("添加订阅")
                .font(.title.weight(.semibold))
                .padding(.top, 14)
            Text("粘贴服务商或公司提供的订阅链接，\n其余的规则、DNS 和 IPv6 都由我们处理。")
                .font(.callout)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
                .padding(.top, 6)

            VStack(spacing: 10) {
                TextField("https://…", text: $link)
                    .textFieldStyle(.roundedBorder)
                    .controlSize(.large)
                    .focused($focused)
                    .disabled(model.isAddingSubscription)
                    .onSubmit(add)

                // 系统的 PasteButton：用户主动点击，不会触发"允许粘贴"的授权弹窗；
                // 直接读 NSPasteboard 在新版 macOS 上会弹窗，对小白很吓人。
                PasteButton(payloadType: String.self) { strings in
                    guard let text = strings.first, (try? SubscriptionService.normalizedURL(text)) != nil else { return }
                    Task { @MainActor in
                        link = text
                        add()
                    }
                }
                .controlSize(.large)
                .disabled(model.isAddingSubscription)

                Button(action: add) {
                    HStack(spacing: 8) {
                        if model.isAddingSubscription { ProgressView().controlSize(.small) }
                        Text(model.addProgress ?? "添加并检测")
                    }
                    .frame(maxWidth: .infinity)
                }
                .buttonStyle(.borderedProminent)
                .controlSize(.large)
                .keyboardShortcut(.defaultAction)
                .disabled(link.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || model.isAddingSubscription)

                if let error = model.addError {
                    Text(error)
                        .font(.callout)
                        .foregroundStyle(.red)
                        .multilineTextAlignment(.center)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            .padding(.horizontal, 28)
            .padding(.top, 26)

            Spacer()
            Text("订阅内容只保存在这台电脑上")
                .font(.caption)
                .foregroundStyle(.tertiary)
                .padding(.bottom, 16)
        }
        .onAppear { focused = true }
    }

    private func add() {
        let text = link
        Task { await model.addSubscription(text) }
    }

}

// MARK: 主界面

struct MainView: View {
    @EnvironmentObject var model: AppModel
    @State private var showingAdvanced = false

    var body: some View {
        VStack(spacing: 0) {
            header
            Spacer(minLength: 8)
            StatusSection()
            Spacer(minLength: 12)
            VStack(spacing: 10) {
                if let issue = model.connectivityIssue { ConnectivityCard(text: issue) }
                if model.phase == .failed { FailureCard() }
                SubscriptionCard()
                LinePicker()
            }
            .padding(.horizontal, 20)
            Spacer(minLength: 12)
            footer
        }
        .sheet(isPresented: $showingAdvanced) { AdvancedSheet().environmentObject(model) }
    }

    private var header: some View {
        HStack {
            Text(AppInfo.displayName).font(.headline)
            Spacer()
            Menu {
                Button("更新订阅") { Task { await model.refreshSubscription() } }
                    .disabled(model.isRefreshingSubscription)
                Button(model.isCheckingUpdate ? "正在检查更新…" : "检查更新…") {
                    Task { await model.checkForUpdates(manual: true) }
                }
                .disabled(model.isCheckingUpdate)
                Button("高级设置…") { showingAdvanced = true }
            } label: {
                Image(systemName: "ellipsis.circle").font(.title3)
            }
            .menuStyle(.borderlessButton)
            .menuIndicator(.hidden)
            .fixedSize()
        }
        .padding(.horizontal, 20)
        .padding(.top, 16)
    }

    private var footer: some View {
        HStack {
            TroubleMenu()
            Spacer()
            if let update = model.availableUpdate {
                Menu {
                    Button("下载 v\(update.version)") { model.openUpdate() }
                    Button("忽略此版本") { model.skipUpdate() }
                } label: {
                    Label("新版本 v\(update.version)", systemImage: "arrow.down.circle.fill")
                        .font(.caption)
                        .foregroundStyle(.tint)
                }
                .menuStyle(.borderlessButton)
                .fixedSize()
                .help("发现新版本，点击下载")
            } else {
                Text(AppModel.appVersion == "dev" ? "dev" : "v\(AppModel.appVersion)")
                    .font(.caption)
                    .foregroundStyle(.tertiary)
            }
        }
        .padding(.horizontal, 20)
        .padding(.bottom, 14)
    }
}

struct StatusSection: View {
    @EnvironmentObject var model: AppModel

    var body: some View {
        VStack(spacing: 14) {
            Button {
                Task { await model.toggle() }
            } label: {
                ZStack {
                    Circle()
                        .fill(tint.gradient)
                        .shadow(color: tint.opacity(model.isConnected ? 0.45 : 0.15), radius: model.isConnected ? 18 : 6)
                    if case .connecting = model.phase {
                        ProgressView().controlSize(.large).tint(.white)
                    } else if model.phase == .disconnecting {
                        ProgressView().controlSize(.large).tint(.white)
                    } else {
                        Image(systemName: "power").font(.system(size: 44, weight: .semibold)).foregroundStyle(.white)
                    }
                }
                .frame(width: 132, height: 132)
            }
            .buttonStyle(.plain)
            .disabled(model.isBusy)
            .help(model.isConnected ? "点击断开" : "点击连接")

            VStack(spacing: 4) {
                Text(title).font(.title2.weight(.semibold))
                Text(subtitle)
                    .font(.callout)
                    .foregroundStyle(.secondary)
                    .multilineTextAlignment(.center)
                    .lineLimit(3)
                    .frame(minHeight: 20)
            }
            .padding(.horizontal, 24)
        }
    }

    private var tint: Color {
        switch model.phase {
        case .connected: return .green
        case .connecting, .disconnecting: return .orange
        case .failed: return .red
        default: return Color(nsColor: .systemGray)
        }
    }

    private var title: String {
        switch model.phase {
        case .connected: return "已连接"
        case .connecting: return "正在连接…"
        case .disconnecting: return "正在断开…"
        case .failed: return "连接失败"
        default: return "未连接"
        }
    }

    private var subtitle: String {
        switch model.phase {
        case .connected:
            let node = model.activeNode ?? "…"
            let delay = model.nodes.first { $0.name == model.activeNode }?.delay
            let started = model.helperStatus?.startedAt.map { "　已运行 \(Self.duration(since: $0))" } ?? ""
            return "\(node)\(delay.map { " · \($0) ms" } ?? "")\(started)"
        case let .connecting(step): return step
        case .failed: return "点击重试"
        default: return "点击上方按钮连接"
        }
    }

    static func duration(since date: Date) -> String {
        let seconds = Int(Date().timeIntervalSince(date))
        if seconds >= 3600 { return "\(seconds / 3600) 小时 \(seconds % 3600 / 60) 分" }
        if seconds >= 60 { return "\(seconds / 60) 分钟" }
        return "不到 1 分钟"
    }
}

struct FailureCard: View {
    @EnvironmentObject var model: AppModel

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Label {
                Text(model.errorMessage ?? "连接失败")
                    .font(.callout)
                    .fixedSize(horizontal: false, vertical: true)
            } icon: {
                Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.red)
            }
            HStack {
                Button("复制诊断信息") { Task { await model.copyDiagnostics(full: false) } }
                    .disabled(model.isCopyingDiagnostics)
                Text("粘贴给 AI 即可排查").font(.caption).foregroundStyle(.secondary)
            }
        }
        .padding(12)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Color.red.opacity(0.08), in: RoundedRectangle(cornerRadius: 10))
    }
}

struct ConnectivityCard: View {
    @EnvironmentObject var model: AppModel
    let text: String

    var body: some View {
        HStack(spacing: 8) {
            Label {
                Text(text)
                    .font(.callout)
                    .fixedSize(horizontal: false, vertical: true)
            } icon: {
                Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.orange)
            }
            Spacer(minLength: 6)
            Button("诊断") { Task { await model.copyDiagnostics(full: false) } }
                .controlSize(.small)
                .disabled(model.isCopyingDiagnostics)
        }
        .padding(12)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Color.orange.opacity(0.10), in: RoundedRectangle(cornerRadius: 10))
    }
}

struct SubscriptionCard: View {
    @EnvironmentObject var model: AppModel

    var body: some View {
        if let subscription = model.settings.subscription {
            VStack(alignment: .leading, spacing: 8) {
                HStack {
                    Image(systemName: "link").foregroundStyle(.secondary)
                    Text(subscription.name).font(.callout.weight(.medium)).lineLimit(1)
                    Spacer()
                    if model.isRefreshingSubscription {
                        ProgressView().controlSize(.small)
                    } else {
                        Button {
                            Task { await model.refreshSubscription() }
                        } label: {
                            Image(systemName: "arrow.clockwise")
                        }
                        .buttonStyle(.borderless)
                        .help("立即更新订阅")
                    }
                }
                Text("\(subscription.host) · \(subscription.nodes.count) 条线路 · \(Self.updatedText(subscription.updatedAt))")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                if let usage = subscription.usage, usage.total > 0 {
                    UsageBar(usage: usage)
                }
            }
            .padding(12)
            .background(Color.primary.opacity(0.05), in: RoundedRectangle(cornerRadius: 10))
        }
    }

    static func updatedText(_ date: Date?) -> String {
        guard let date else { return "尚未更新" }
        let formatter = RelativeDateTimeFormatter()
        formatter.locale = Locale(identifier: "zh_CN")
        formatter.unitsStyle = .short
        return formatter.localizedString(for: date, relativeTo: Date()) + "更新"
    }
}

struct UsageBar: View {
    let usage: SubscriptionUsage

    var body: some View {
        let fraction = min(1, Double(usage.used) / Double(max(usage.total, 1)))
        VStack(alignment: .leading, spacing: 4) {
            GeometryReader { proxy in
                ZStack(alignment: .leading) {
                    Capsule().fill(Color.primary.opacity(0.1))
                    Capsule().fill(fraction > 0.9 ? Color.red : Color.accentColor).frame(width: proxy.size.width * fraction)
                }
            }
            .frame(height: 5)
            HStack {
                Text("已用 \(bytes(usage.used)) / \(bytes(usage.total))")
                Spacer()
                if let expire = usage.expire { Text("到期 \(expire.formatted(.dateTime.year().month().day().locale(Locale(identifier: "zh_CN"))))") }
            }
            .font(.caption2)
            .foregroundStyle(.secondary)
        }
    }

    private func bytes(_ value: Int64) -> String { ByteCountFormatter.string(fromByteCount: value, countStyle: .binary) }
}

struct LinePicker: View {
    @EnvironmentObject var model: AppModel

    var body: some View {
        HStack {
            Label("线路", systemImage: "point.3.connected.trianglepath.dotted")
                .font(.callout)
            Spacer()
            Menu {
                Button {
                    Task { await model.select(AppModel.autoSelection) }
                } label: {
                    if model.selection == AppModel.autoSelection {
                        Label("自动选择（延迟最低）", systemImage: "checkmark")
                    } else {
                        Text("自动选择（延迟最低）")
                    }
                }
                Divider()
                ForEach(model.nodes) { node in
                    Button {
                        Task { await model.select(node.name) }
                    } label: {
                        let title = "\(node.name)\(node.delay.map { "　\($0) ms" } ?? "")"
                        if model.selection == node.name {
                            Label(title, systemImage: "checkmark")
                        } else {
                            Text(title)
                        }
                    }
                }
                Divider()
                Button("重新测速") { Task { await model.testLatency() } }
                    .disabled(model.isTestingLatency)
            } label: {
                Text(model.selection == AppModel.autoSelection ? "自动选择" : model.selection)
                    .lineLimit(1)
            }
            .menuStyle(.borderlessButton)
            .fixedSize()
            .disabled(!model.isConnected)
        }
        .padding(12)
        .background(Color.primary.opacity(0.05), in: RoundedRectangle(cornerRadius: 10))
        .help(model.isConnected ? "选择走哪条线路" : "连接后可以切换线路")
    }
}

/// 常驻在底部的"遇到问题"：一键复制给 AI 的诊断信息是这里最重要的功能。
struct TroubleMenu: View {
    @EnvironmentObject var model: AppModel

    var body: some View {
        Menu {
            Button("复制诊断信息（给 AI 排查）") { Task { await model.copyDiagnostics(full: false) } }
            Button("复制完整诊断（含最近连接日志）") { Task { await model.copyDiagnostics(full: true) } }
            Divider()
            Button("在访达中显示日志") { model.openLogsFolder() }
            Divider()
            Button("重新连接") { Task { await model.reconnect() } }
                .disabled(model.isBusy)
        } label: {
            Label("遇到问题", systemImage: "lifepreserver")
        }
        .menuStyle(.borderlessButton)
        .fixedSize()
    }
}

// MARK: 高级设置

struct AdvancedSheet: View {
    @EnvironmentObject var model: AppModel
    @Environment(\.dismiss) private var dismiss
    @State private var confirmRemove = false
    @State private var confirmUninstall = false

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text("高级设置").font(.title3.weight(.semibold))

            Toggle(isOn: binding(\.takeOverDNS)) {
                VStack(alignment: .leading, spacing: 2) {
                    Text("接管系统 DNS")
                    Text("让所有域名解析都经过规则引擎。关闭后部分 App 可能绕过规则。").font(.caption).foregroundStyle(.secondary)
                }
            }
            Toggle(isOn: binding(\.disableIPv6)) {
                VStack(alignment: .leading, spacing: 2) {
                    Text("连接期间关闭 IPv6")
                    Text("IPv6 流量不经过虚拟网卡，会绕过规则。断开后自动恢复原设置。").font(.caption).foregroundStyle(.secondary)
                }
            }
            Toggle(isOn: binding(\.autoConnectOnLaunch)) { Text("打开 App 时自动连接") }
            Toggle(isOn: binding(\.autoCheckUpdates)) {
                VStack(alignment: .leading, spacing: 2) {
                    Text("自动检查更新")
                    Text("每天检查一次 GitHub Releases，只提示，不会自动替换 App。").font(.caption).foregroundStyle(.secondary)
                }
            }

            Divider()
            HStack {
                Button("编辑自定义规则…") { model.openCustomRulesFile() }
                Button("更换订阅…") { confirmRemove = true }
            }
            HStack {
                Button(model.isCheckingUpdate ? "正在检查更新…" : "检查更新…") {
                    dismiss()
                    Task { await model.checkForUpdates(manual: true) }
                }
                .disabled(model.isCheckingUpdate)
                Link("项目主页", destination: URL(string: AppInfo.projectURL) ?? URL(string: "https://github.com")!)
                    .font(.callout)
                Spacer()
                Text("v\(AppModel.appVersion)").font(.caption).foregroundStyle(.secondary)
            }
            Button("卸载后台组件…", role: .destructive) { confirmUninstall = true }

            Text("修改网络相关选项后，需要重新连接才会生效。").font(.caption).foregroundStyle(.secondary)

            HStack {
                Spacer()
                if model.isConnected {
                    Button("重新连接") {
                        dismiss()
                        Task { await model.reconnect() }
                    }
                }
                Button("完成") { dismiss() }.keyboardShortcut(.defaultAction)
            }
        }
        .padding(22)
        .frame(width: 380)
        .confirmationDialog("更换订阅会断开当前连接并删除已保存的订阅。", isPresented: $confirmRemove) {
            Button("更换订阅", role: .destructive) {
                dismiss()
                Task { await model.removeSubscription() }
            }
        }
        .confirmationDialog("卸载后会断开连接并移除系统里的后台组件，需要管理员授权。", isPresented: $confirmUninstall) {
            Button("卸载", role: .destructive) { Task { await model.uninstallComponents() } }
        }
    }

    private func binding(_ keyPath: WritableKeyPath<AppSettings, Bool>) -> Binding<Bool> {
        Binding(get: { model.settings[keyPath: keyPath] }, set: { value in model.update { $0[keyPath: keyPath] = value } })
    }
}

// MARK: 菜单栏

struct MenuBarContent: View {
    @EnvironmentObject var model: AppModel
    @Environment(\.openWindow) private var openWindow

    var body: some View {
        Text(statusText)
        Divider()
        if model.phase != .needsSubscription {
            Button(model.isConnected ? "断开" : "连接") { Task { await model.toggle() } }
                .disabled(model.isBusy)
        }
        Button("打开主窗口") {
            openWindow(id: "main")
            NSApp.activate(ignoringOtherApps: true)
        }
        Divider()
        Button("复制诊断信息（给 AI 排查）") { Task { await model.copyDiagnostics(full: false) } }
        Divider()
        Button("退出 \(AppInfo.displayName)") { NSApp.terminate(nil) }.keyboardShortcut("q")
    }

    private var statusText: String {
        switch model.phase {
        case .connected: return "已连接 · \(model.activeNode ?? "")"
        case .connecting: return "正在连接…"
        case .disconnecting: return "正在断开…"
        case .failed: return "连接失败"
        case .needsSubscription: return "尚未添加订阅"
        case .disconnected: return "未连接"
        }
    }
}
