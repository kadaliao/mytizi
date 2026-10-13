import CryptoKit
import Foundation
import MytiziKit

/// 随 App 打包的组件清单（build-app.sh 生成），安装时原样复制到系统目录。
/// App 每次启动对比"包内清单"和"已安装清单"，不一致就提示更新组件——
/// 比较内容哈希而不是版本号，所以改了任何一个二进制都会触发。
public struct ComponentsManifest: Codable, Equatable {
    public var core: String
    public var coreSha256: String
    public var geoipSha256: String
    public var geositeSha256: String
    public var helperSha256: String
    public var geoPublishedAt: String
    public var ipcVersion: Int
    public var appVersion: String
}

public enum InstallState: Equatable {
    case notInstalled
    case outdated(reason: String)
    case upToDate
}

/// 包内资源位置。Resources/core 放核心与地理库，Helpers 放 helper。
public struct BundleComponents {
    public let coreDirectory: URL
    public let helper: URL

    public var core: URL { coreDirectory.appendingPathComponent("mihomo") }
    public var geoip: URL { coreDirectory.appendingPathComponent("geoip.metadb") }
    public var geosite: URL { coreDirectory.appendingPathComponent("geosite.dat") }
    public var manifest: URL { coreDirectory.appendingPathComponent("components.json") }

    public init(coreDirectory: URL, helper: URL) {
        self.coreDirectory = coreDirectory
        self.helper = helper
    }

    public static func main(_ bundle: Bundle = .main) -> BundleComponents {
        BundleComponents(
            coreDirectory: bundle.bundleURL.appendingPathComponent("Contents/Resources/core", isDirectory: true),
            helper: bundle.bundleURL.appendingPathComponent("Contents/Helpers/mytizi-helper"))
    }

    public func loadManifest() -> ComponentsManifest? {
        guard let data = try? Data(contentsOf: manifest) else { return nil }
        return try? JSONDecoder().decode(ComponentsManifest.self, from: data)
    }
}

public struct HelperInstaller {
    public let bundle: BundleComponents
    private let client: HelperClient

    public init(bundle: BundleComponents = .main(), client: HelperClient = HelperClient()) {
        self.bundle = bundle
        self.client = client
    }

    public static func installedManifest() -> ComponentsManifest? {
        guard let data = FileManager.default.contents(atPath: SystemPaths.componentsManifest) else { return nil }
        return try? JSONDecoder().decode(ComponentsManifest.self, from: data)
    }

    public func state() async -> InstallState {
        guard let installed = Self.installedManifest() else { return .notInstalled }
        guard let bundled = bundle.loadManifest() else { return .upToDate } // 开发运行（没有打包清单）时不打扰
        if installed != bundled { return .outdated(reason: Self.describeDifference(installed, bundled)) }
        do {
            _ = try await client.hello()
        } catch HelperClientError.notRunning {
            return .outdated(reason: "后台组件没有在运行")
        } catch HelperClientError.protocolMismatch {
            return .outdated(reason: "后台组件协议版本不匹配")
        } catch {
            return .outdated(reason: "\(error.localizedDescription)")
        }
        return .upToDate
    }

    static func describeDifference(_ installed: ComponentsManifest, _ bundled: ComponentsManifest) -> String {
        var parts: [String] = []
        if installed.core != bundled.core || installed.coreSha256 != bundled.coreSha256 { parts.append("核心 \(installed.core) → \(bundled.core)") }
        if installed.geoipSha256 != bundled.geoipSha256 || installed.geositeSha256 != bundled.geositeSha256 { parts.append("地理数据库已更新") }
        if installed.helperSha256 != bundled.helperSha256 { parts.append("后台组件已更新") }
        return parts.isEmpty ? "组件清单不一致" : parts.joined(separator: "，")
    }

    // MARK: 安装 / 卸载（弹一次系统管理员授权）

    public enum InstallError: LocalizedError {
        case missingResource(String)
        case cancelled
        case failed(String)

        public var errorDescription: String? {
            switch self {
            case let .missingResource(name): return "App 安装包不完整，缺少 \(name)。"
            case .cancelled: return "已取消授权。"
            case let .failed(output): return "安装后台组件失败：\(output)"
            }
        }
    }

    public func install() async throws {
        for url in [bundle.core, bundle.geoip, bundle.geosite, bundle.helper, bundle.manifest]
        where !FileManager.default.fileExists(atPath: url.path) {
            throw InstallError.missingResource(url.lastPathComponent)
        }
        // 先校验包内文件和清单一致，避免把被篡改/不完整的文件以 root 身份装进系统。
        if let manifest = bundle.loadManifest() {
            let mismatches = [
                ("mihomo", bundle.core, manifest.coreSha256), ("geoip.metadb", bundle.geoip, manifest.geoipSha256),
                ("geosite.dat", bundle.geosite, manifest.geositeSha256), ("mytizi-helper", bundle.helper, manifest.helperSha256),
            ].filter { Self.sha256(of: $0.1) != $0.2 }
            if let first = mismatches.first { throw InstallError.failed("\(first.0) 的校验和与清单不一致，请重新下载 App") }
        }
        let script = Self.installScript(
            appPath: bundle.coreDirectory.deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent().path,
            ownerUID: getuid())
        try await runPrivileged(script)
        // 等 helper 起来
        for _ in 0..<40 {
            if (try? await client.hello()) != nil { return }
            try? await Task.sleep(nanoseconds: 250_000_000)
        }
        throw InstallError.failed("组件已写入，但后台服务没有启动。可在\u{201C}遇到问题\u{201D}里复制诊断信息排查。")
    }

    public func uninstall() async throws {
        try await runPrivileged(Self.uninstallScript())
    }

    private func runPrivileged(_ script: String) async throws {
        try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<Void, Error>) in
            DispatchQueue.global(qos: .userInitiated).async {
                let source = "do shell script \(Self.appleScriptQuote("/bin/sh -c " + Self.shellQuote(script))) with administrator privileges"
                var errorInfo: NSDictionary?
                _ = NSAppleScript(source: source)?.executeAndReturnError(&errorInfo)
                if let errorInfo {
                    let number = (errorInfo[NSAppleScript.errorNumber] as? Int) ?? 0
                    if number == -128 {
                        continuation.resume(throwing: InstallError.cancelled)
                    } else {
                        let message = (errorInfo[NSAppleScript.errorMessage] as? String) ?? "\(errorInfo)"
                        continuation.resume(throwing: InstallError.failed(message))
                    }
                } else {
                    continuation.resume()
                }
            }
        }
    }

    // MARK: 脚本

    static func daemonPlist(ownerUID: uid_t) -> String {
        """
        <?xml version="1.0" encoding="UTF-8"?>
        <!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
        <plist version="1.0"><dict>
        <key>Label</key><string>\(AppInfo.helperLabel)</string>
        <key>ProgramArguments</key><array><string>\(SystemPaths.helperBinary)</string><string>--owner-uid</string><string>\(ownerUID)</string></array>
        <key>RunAtLoad</key><true/>
        <key>KeepAlive</key><true/>
        <key>ThrottleInterval</key><integer>5</integer>
        <key>StandardErrorPath</key><string>\(SystemPaths.logDir)/helper.stderr.log</string>
        <key>StandardOutPath</key><string>\(SystemPaths.logDir)/helper.stderr.log</string>
        </dict></plist>
        """
    }

    /// 以 root 执行的安装脚本。所有路径来自常量或已校验的 App 路径，不拼接任何用户输入。
    static func installScript(appPath: String, ownerUID: uid_t) -> String {
        let label = AppInfo.helperLabel
        return """
        set -eu
        APP=\(shellQuote(appPath))
        SUP=\(shellQuote(SystemPaths.supportDir))
        RES="$APP/Contents/Resources/core"
        HELPER=\(shellQuote(SystemPaths.helperBinary))
        PLIST=\(shellQuote(SystemPaths.daemonPlist))
        LOGDIR=\(shellQuote(SystemPaths.logDir))

        # 旧 helper 收到 SIGTERM 会先还原网络再退出。
        /bin/launchctl bootout system/\(label) 2>/dev/null || true

        /bin/mkdir -p "$SUP/bin" "$SUP/core-home/providers" "$LOGDIR" /Library/PrivilegedHelperTools
        /usr/sbin/chown root:wheel "$SUP" "$SUP/bin" "$SUP/core-home" "$SUP/core-home/providers" "$LOGDIR"
        /bin/chmod 755 "$SUP" "$SUP/bin" "$SUP/core-home" "$SUP/core-home/providers" "$LOGDIR"
        /usr/bin/install -m 755 -o root -g wheel "$RES/mihomo" "$SUP/bin/mihomo"
        /usr/bin/install -m 644 -o root -g wheel "$RES/geoip.metadb" "$SUP/core-home/geoip.metadb"
        /usr/bin/install -m 644 -o root -g wheel "$RES/geosite.dat" "$SUP/core-home/geosite.dat"
        /usr/bin/install -m 755 -o root -g wheel "$APP/Contents/Helpers/mytizi-helper" "$HELPER"
        /usr/bin/xattr -cr "$SUP" "$HELPER" 2>/dev/null || true

        /usr/bin/printf '%s\\n' \(shellQuote(daemonPlist(ownerUID: ownerUID))) > "$PLIST"
        /usr/sbin/chown root:wheel "$PLIST"
        /bin/chmod 644 "$PLIST"

        # 清单最后写：它存在就表示上面的文件都已就位。
        /usr/bin/install -m 644 -o root -g wheel "$RES/components.json" "$SUP/components.json"

        /bin/launchctl bootstrap system "$PLIST"
        /bin/launchctl enable system/\(label) 2>/dev/null || true
        """
    }

    static func uninstallScript() -> String {
        """
        set -u
        /bin/launchctl bootout system/\(AppInfo.helperLabel) 2>/dev/null || true
        /bin/rm -f \(shellQuote(SystemPaths.daemonPlist)) \(shellQuote(SystemPaths.helperBinary)) \(shellQuote(SystemPaths.socket))
        /bin/rm -rf \(shellQuote(SystemPaths.supportDir))
        exit 0
        """
    }

    static func shellQuote(_ text: String) -> String {
        "'" + text.replacingOccurrences(of: "'", with: "'\\''") + "'"
    }

    static func appleScriptQuote(_ text: String) -> String {
        "\"" + text.replacingOccurrences(of: "\\", with: "\\\\").replacingOccurrences(of: "\"", with: "\\\"") + "\""
    }

    public static func sha256(of url: URL) -> String? {
        guard let handle = try? FileHandle(forReadingFrom: url) else { return nil }
        defer { try? handle.close() }
        var hasher = SHA256()
        while let chunk = try? handle.read(upToCount: 1 << 20), !chunk.isEmpty { hasher.update(data: chunk) }
        return hasher.finalize().map { String(format: "%02x", $0) }.joined()
    }
}
