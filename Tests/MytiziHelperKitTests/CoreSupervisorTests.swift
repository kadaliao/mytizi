import Foundation
import XCTest
import MytiziKit
import MytiziCore
@testable import MytiziHelperKit

/// 用真实 mihomo（不开 TUN、普通用户权限）+ 模拟的 networksetup 验证 helper 的完整生命周期。
final class CoreSupervisorTests: XCTestCase {
    static let root = URL(fileURLWithPath: #filePath)
        .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
    static var vendor: URL { root.appendingPathComponent("Vendor/core") }

    var work: URL!
    var machine: FakeNetworkSetup!
    var supervisor: CoreSupervisor!
    var paths: HelperPaths!
    var coreLog: RotatingLog!

    override func setUpWithError() throws {
        #if arch(arm64)
        let core = Self.vendor.appendingPathComponent("mihomo-arm64")
        #else
        let core = Self.vendor.appendingPathComponent("mihomo-amd64")
        #endif
        guard FileManager.default.isExecutableFile(atPath: core.path),
              FileManager.default.fileExists(atPath: Self.vendor.appendingPathComponent("geosite.dat").path)
        else { throw XCTSkip("缺少 Vendor/core，先运行 scripts/fetch-core.sh") }

        work = FileManager.default.temporaryDirectory.appendingPathComponent("sup-\(UUID().uuidString.prefix(8))")
        let home = work.appendingPathComponent("home")
        try FileManager.default.createDirectory(at: home, withIntermediateDirectories: true)
        for name in ["geoip.metadb", "geosite.dat"] {
            try FileManager.default.copyItem(at: Self.vendor.appendingPathComponent(name), to: home.appendingPathComponent(name))
        }
        paths = HelperPaths(
            coreBinary: core.path, coreHome: home.path, networkState: work.appendingPathComponent("state.json").path,
            helperLog: work.appendingPathComponent("helper.log").path, coreLog: work.appendingPathComponent("core.log").path,
            socket: "/tmp/mtz-\(UUID().uuidString.prefix(6)).sock")
        machine = FakeNetworkSetup(services: [("Wi-Fi", .init(device: "en1", dns: ["192.168.2.1"]))])
        let log = RotatingLog(path: paths.helperLog)
        coreLog = RotatingLog(path: paths.coreLog)
        let guardian = NetworkGuard(statePath: paths.networkState, runner: machine)
        supervisor = CoreSupervisor(paths: paths, network: guardian, log: log, coreLog: coreLog)
    }

    override func tearDown() {
        supervisor?.shutdown()
        if let work { try? FileManager.default.removeItem(at: work) }
        if let paths { unlink(paths.socket) }
    }

    private func params(takeOverDNS: Bool = true, disableIPv6: Bool = true, config: String? = nil) -> StartParams {
        let port = FreePort.find()
        let secret = "test-secret"
        let yaml = config ?? ConfigBuilder.build(ConfigOptions(controllerPort: port, controllerSecret: secret, tun: false)).render()
        return StartParams(
            configYAML: yaml, files: [HelperFile(path: "providers/sub.yaml", data: Data("""
            proxies:
              - {name: "a", type: http, server: 192.0.2.5, port: 7890}
            """.utf8))],
            controllerPort: port, controllerSecret: secret, takeOverDNS: takeOverDNS, disableIPv6: disableIPv6, expectTUN: false)
    }

    func testStartRunStop() async throws {
        let start = params()
        let response = supervisor.start(start)
        XCTAssertTrue(response.ok, "\(String(describing: response.error))")
        let status = try XCTUnwrap(response.status)
        XCTAssertEqual(status.phase, .running)
        XCTAssertNotNil(status.corePID)
        XCTAssertEqual(status.controllerSecret, "test-secret", "运行中才返回密钥，App 重启后据此重新接上")
        XCTAssertEqual(status.changes.dnsServices, ["Wi-Fi"])
        XCTAssertEqual(machine.service("Wi-Fi").dns, [AppInfo.tunDNS])
        XCTAssertEqual(machine.service("Wi-Fi").v6.mode, .off)

        let version = try await MihomoAPI(port: start.controllerPort, secret: start.controllerSecret).version()
        XCTAssertFalse(version.isEmpty)

        let stopped = supervisor.stop()
        XCTAssertEqual(stopped.status?.phase, .stopped)
        XCTAssertNil(stopped.status?.controllerSecret)
        XCTAssertEqual(machine.service("Wi-Fi").dns, ["192.168.2.1"], "停止后 DNS 回到原值")
        XCTAssertEqual(machine.service("Wi-Fi").v6.mode, .automatic)
        XCTAssertFalse(FileManager.default.fileExists(atPath: paths.networkState))
        XCTAssertNil(supervisor.status().corePID)
    }

    func testDNSIsRestoredBeforeTheCoreIsKilled() throws {
        XCTAssertTrue(supervisor.start(params()).ok)
        var corePIDAliveWhenDNSRestored: Bool?
        let pid = try XCTUnwrap(supervisor.status().corePID)
        machine.failOn = { _, arguments in
            if arguments.first == "-setdnsservers", arguments.last == "192.168.2.1", corePIDAliveWhenDNSRestored == nil {
                corePIDAliveWhenDNSRestored = kill(pid, 0) == 0
            }
            return false
        }
        _ = supervisor.stop()
        XCTAssertEqual(corePIDAliveWhenDNSRestored, true, "先还原 DNS、再停核心，中间不会出现解析到死地址的窗口")
    }

    func testCrashRestoresNetworkAndReportsIt() throws {
        XCTAssertTrue(supervisor.start(params()).ok)
        let pid = try XCTUnwrap(supervisor.status().corePID)
        XCTAssertEqual(machine.service("Wi-Fi").dns, [AppInfo.tunDNS])

        kill(pid, SIGKILL)
        let deadline = Date().addingTimeInterval(5)
        while supervisor.status().phase != .crashed, Date() < deadline { Thread.sleep(forTimeInterval: 0.05) }

        let status = supervisor.status()
        XCTAssertEqual(status.phase, .crashed)
        XCTAssertEqual(status.lastExit?.expected, false)
        XCTAssertEqual(status.lastExit?.bySignal, true)
        XCTAssertEqual(machine.service("Wi-Fi").dns, ["192.168.2.1"], "核心被杀后必须立刻还原 DNS，否则用户断网")
        XCTAssertEqual(machine.service("Wi-Fi").v6.mode, .automatic)
        // crashed 之后仍然可以重新启动
        XCTAssertTrue(supervisor.start(params()).ok)
        XCTAssertEqual(supervisor.status().phase, .running)
    }

    func testInvalidConfigIsRejectedBeforeTouchingTheNetwork() {
        let response = supervisor.start(params(config: "this: [is: not valid"))
        XCTAssertFalse(response.ok)
        XCTAssertEqual(response.error?.code, "config-invalid")
        XCTAssertEqual(supervisor.status().phase, .stopped)
        XCTAssertTrue(machine.commands.allSatisfy { !($0.dropFirst().first?.hasPrefix("-set") ?? false) }, "校验失败不应改动网络")
    }

    func testRestartWhileRunningReplacesTheInstance() throws {
        XCTAssertTrue(supervisor.start(params()).ok)
        let first = try XCTUnwrap(supervisor.status().corePID)
        XCTAssertTrue(supervisor.start(params()).ok)
        let second = try XCTUnwrap(supervisor.status().corePID)
        XCTAssertNotEqual(first, second)
        XCTAssertNotEqual(kill(first, 0), 0, "旧核心必须已经退出")
        XCTAssertEqual(machine.service("Wi-Fi").dns, [AppInfo.tunDNS])
        _ = supervisor.stop()
        XCTAssertEqual(machine.service("Wi-Fi").dns, ["192.168.2.1"], "重启两次也只能还原到用户的原始值")
    }

    func testFilePathValidation() {
        XCTAssertTrue(CoreSupervisor.isAllowedFilePath("providers/sub.yaml"))
        for bad in ["../etc/passwd", "providers/../x", "/etc/passwd", "providers/a/b", "config.yaml", "providers/", "providers/a b"] {
            XCTAssertFalse(CoreSupervisor.isAllowedFilePath(bad), bad)
        }
        let response = supervisor.updateFiles([HelperFile(path: "../escape", data: Data("x".utf8))])
        XCTAssertFalse(response.ok)
        XCTAssertFalse(FileManager.default.fileExists(atPath: work.appendingPathComponent("escape").path))
    }

    func testSocketRoundTripAndAccessControl() throws {
        let log = RotatingLog(path: paths.helperLog)
        let server = HelperServer(paths: paths, ownerUID: getuid(), supervisor: supervisor, log: log)
        Thread.detachNewThread { try? server.run() }
        defer { server.stopListening() }
        var hello: HelperResponse?
        for _ in 0..<50 {
            hello = try? UnixSocket.request(path: paths.socket, HelperRequest(method: .hello), as: HelperResponse.self)
            if hello != nil { break }
            Thread.sleep(forTimeInterval: 0.05)
        }
        XCTAssertEqual(hello?.hello?.ipcVersion, AppInfo.ipcVersion)
        XCTAssertEqual(hello?.hello?.ownerUID, getuid())
        XCTAssertEqual(hello?.hello?.coreInstalled, true)

        let status = try UnixSocket.request(path: paths.socket, HelperRequest(method: .status), as: HelperResponse.self)
        XCTAssertEqual(status.status?.phase, .stopped)

        // 经 socket 完整走一遍 start / stop
        let started = try UnixSocket.request(path: paths.socket, HelperRequest(method: .start, start: params()), as: HelperResponse.self, timeout: 30)
        XCTAssertTrue(started.ok, "\(String(describing: started.error))")
        XCTAssertEqual(started.status?.phase, .running)
        let stopped = try UnixSocket.request(path: paths.socket, HelperRequest(method: .stop), as: HelperResponse.self, timeout: 15)
        XCTAssertEqual(stopped.status?.phase, .stopped)

        let mode = try FileManager.default.attributesOfItem(atPath: paths.socket)[.posixPermissions] as? Int
        XCTAssertEqual(mode, 0o600, "socket 文件只属于安装者")
    }

    func testServerRejectsAForeignUID() throws {
        let log = RotatingLog(path: paths.helperLog)
        // owner 设成别人：当前用户的连接必须被拒绝（不返回任何内容）。
        let server = HelperServer(paths: paths, ownerUID: getuid() + 1, supervisor: supervisor, log: log)
        Thread.detachNewThread { try? server.run() }
        defer { server.stopListening() }
        Thread.sleep(forTimeInterval: 0.3)
        XCTAssertThrowsError(try UnixSocket.request(path: paths.socket, HelperRequest(method: .status), as: HelperResponse.self, timeout: 2))
    }
}
