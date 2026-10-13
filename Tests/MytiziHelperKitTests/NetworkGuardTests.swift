import Foundation
import XCTest
import MytiziKit
@testable import MytiziHelperKit

/// 在内存里模拟 networksetup：有状态，能校验"改完再恢复"是否回到原样。
final class FakeNetworkSetup: CommandRunner {
    struct Service { var device: String; var enabled = true; var dns: [String] = []; var v6 = IPv6Setting(mode: .automatic) }

    var services: [(name: String, service: Service)]
    var commands: [[String]] = []
    var failOn: ((String, [String]) -> Bool)?

    init(services: [(String, Service)]) { self.services = services.map { ($0.0, $0.1) } }

    func service(_ name: String) -> Service { services.first { $0.name == name }!.service }

    private func update(_ name: String, _ change: (inout Service) -> Void) {
        guard let index = services.firstIndex(where: { $0.name == name }) else { return }
        change(&services[index].service)
    }

    func run(_ executable: String, _ arguments: [String]) -> ShellResult {
        commands.append([executable] + arguments)
        if failOn?(executable, arguments) == true { return ShellResult(status: 1, stdout: "", stderr: "** Error: simulated", timedOut: false) }
        func ok(_ text: String = "") -> ShellResult { ShellResult(status: 0, stdout: text, stderr: "", timedOut: false) }
        guard executable.hasSuffix("networksetup") else { return ok() }

        switch arguments[0] {
        case "-listnetworkserviceorder":
            var text = "An asterisk (*) denotes that a network service is disabled.\n"
            for (index, entry) in services.enumerated() {
                text += "(\(entry.service.enabled ? String(index + 1) : "*")) \(entry.name)\n"
                text += "(Hardware Port: \(entry.name), Device: \(entry.service.device))\n\n"
            }
            return ok(text)
        case "-getdnsservers":
            let s = service(arguments[1])
            return ok(s.dns.isEmpty ? "There aren't any DNS Servers set on \(arguments[1]).\n" : s.dns.joined(separator: "\n") + "\n")
        case "-setdnsservers":
            update(arguments[1]) { $0.dns = Array(arguments[2...]) == ["Empty"] ? [] : Array(arguments[2...]) }
            return ok()
        case "-getinfo":
            let v6 = service(arguments[1]).v6
            var text = "DHCP Configuration\nIP address: 192.168.2.205\n"
            switch v6.mode {
            case .automatic: text += "IPv6: Automatic\nIPv6 IP address: none\nIPv6 Router: none\n"
            case .off: text += "IPv6: Off\n"
            case .linkLocal: text += "IPv6: Link-local\n"
            case .manual: text += "IPv6: Manual\nIPv6 IP address: \(v6.address ?? "")\nIPv6 Prefix Length: \(v6.prefixLength ?? "")\nIPv6 Router: \(v6.router ?? "none")\n"
            }
            return ok(text + "Ethernet Address: aa:bb\n")
        case "-setv6off": update(arguments[1]) { $0.v6 = IPv6Setting(mode: .off) }; return ok()
        case "-setv6automatic": update(arguments[1]) { $0.v6 = IPv6Setting(mode: .automatic) }; return ok()
        case "-setv6LinkLocal": update(arguments[1]) { $0.v6 = IPv6Setting(mode: .linkLocal) }; return ok()
        case "-setv6manual":
            update(arguments[1]) { $0.v6 = IPv6Setting(mode: .manual, address: arguments[2], prefixLength: arguments[3], router: arguments.count > 4 ? arguments[4] : nil) }
            return ok()
        default: return ok()
        }
    }
}

final class NetworkGuardTests: XCTestCase {
    var stateDirectory: URL!
    var statePath: String { stateDirectory.appendingPathComponent("network-state.json").path }

    override func setUpWithError() throws {
        stateDirectory = FileManager.default.temporaryDirectory.appendingPathComponent("guard-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: stateDirectory, withIntermediateDirectories: true)
    }

    override func tearDown() { try? FileManager.default.removeItem(at: stateDirectory) }

    private func makeMachine() -> FakeNetworkSetup {
        FakeNetworkSetup(services: [
            ("USB Controls", .init(device: "usbmodemABC123")),
            ("Ethernet", .init(device: "en0", dns: ["10.0.0.53", "10.0.0.54"])),
            ("Wi-Fi", .init(device: "en1")),
            ("Thunderbolt Bridge", .init(device: "bridge0", enabled: false)),
            ("Home", .init(device: "en5", v6: IPv6Setting(mode: .manual, address: "fd00::5", prefixLength: "64", router: "fd00::1"))),
            ("Quiet", .init(device: "en6", v6: IPv6Setting(mode: .off))),
            ("Tailscale", .init(device: "")),
        ])
    }

    private func makeGuard(_ machine: FakeNetworkSetup) -> NetworkGuard {
        NetworkGuard(statePath: statePath, runner: machine)
    }

    func testApplyOnlyTouchesEnabledEthernetLikeServices() throws {
        let machine = makeMachine()
        let summary = try makeGuard(machine).apply(takeOverDNS: true, disableIPv6: true)

        XCTAssertEqual(Set(summary.dnsServices), ["Ethernet", "Wi-Fi", "Home", "Quiet"])
        for name in ["Ethernet", "Wi-Fi", "Home", "Quiet"] {
            XCTAssertEqual(machine.service(name).dns, [AppInfo.tunDNS], name)
        }
        XCTAssertEqual(machine.service("USB Controls").dns, [], "非 en/bridge 设备不动")
        XCTAssertEqual(machine.service("Tailscale").dns, [])
        XCTAssertEqual(machine.service("Thunderbolt Bridge").dns, [], "已禁用的服务不动")
        XCTAssertEqual(Set(summary.ipv6Services), ["Ethernet", "Wi-Fi", "Home"], "本来就关闭的 IPv6 不记录")
        for name in ["Ethernet", "Wi-Fi", "Home", "Quiet"] { XCTAssertEqual(machine.service(name).v6.mode, .off, name) }
        XCTAssertTrue(FileManager.default.fileExists(atPath: statePath))
    }

    func testRestoreReturnsEverythingToTheOriginalState() throws {
        let machine = makeMachine()
        let before = machine.services.map { "\($0.name)|\($0.service.dns)|\($0.service.v6)" }
        let guardian = makeGuard(machine)
        try guardian.apply(takeOverDNS: true, disableIPv6: true)
        XCTAssertEqual(guardian.restore(), [])

        XCTAssertEqual(machine.services.map { "\($0.name)|\($0.service.dns)|\($0.service.v6)" }, before)
        XCTAssertFalse(FileManager.default.fileExists(atPath: statePath), "全部恢复后才删除恢复点")
        XCTAssertEqual(machine.service("Quiet").v6.mode, .off, "原本关闭的 IPv6 恢复后仍然关闭")
        XCTAssertEqual(machine.service("Home").v6.address, "fd00::5", "手动 IPv6 要连地址一起恢复")
    }

    func testSnapshotIsWrittenBeforeAnySystemChange() throws {
        let machine = makeMachine()
        var snapshotExistedAtFirstChange: Bool?
        machine.failOn = { [statePath] _, arguments in
            if snapshotExistedAtFirstChange == nil, arguments.first == "-setdnsservers" {
                snapshotExistedAtFirstChange = FileManager.default.fileExists(atPath: statePath)
            }
            return false
        }
        try makeGuard(machine).apply(takeOverDNS: true, disableIPv6: true)
        XCTAssertEqual(snapshotExistedAtFirstChange, true, "必须先落盘恢复点再改系统，否则崩溃后无法还原")
    }

    func testRestoreWorksFromANewInstanceAfterACrash() throws {
        let machine = makeMachine()
        let before = machine.services.map { "\($0.name)|\($0.service.dns)|\($0.service.v6)" }
        try makeGuard(machine).apply(takeOverDNS: true, disableIPv6: true)
        // 模拟 helper 崩溃重启：新实例只靠磁盘上的恢复点。
        let restarted = makeGuard(machine)
        XCTAssertTrue(restarted.hasPendingState)
        XCTAssertEqual(restarted.restore(), [])
        XCTAssertEqual(machine.services.map { "\($0.name)|\($0.service.dns)|\($0.service.v6)" }, before)
    }

    func testFailureMidwayRollsEverythingBack() throws {
        let machine = makeMachine()
        let before = machine.services.map { "\($0.name)|\($0.service.dns)|\($0.service.v6)" }
        var setCount = 0
        machine.failOn = { _, arguments in
            guard arguments.first == "-setdnsservers", arguments.last == AppInfo.tunDNS else { return false }
            setCount += 1
            return setCount == 2
        }
        XCTAssertThrowsError(try makeGuard(machine).apply(takeOverDNS: true, disableIPv6: true))
        machine.failOn = nil
        XCTAssertEqual(machine.services.map { "\($0.name)|\($0.service.dns)|\($0.service.v6)" }, before)
        XCTAssertFalse(FileManager.default.fileExists(atPath: statePath))
    }

    func testFailedRestoreKeepsTheSnapshotForRetry() throws {
        let machine = makeMachine()
        let guardian = makeGuard(machine)
        try guardian.apply(takeOverDNS: true, disableIPv6: false)
        machine.failOn = { _, arguments in arguments.first == "-setdnsservers" && arguments.contains("Empty") }
        XCTAssertFalse(guardian.restore().isEmpty)
        XCTAssertTrue(FileManager.default.fileExists(atPath: statePath), "没恢复干净就不能删恢复点")
        machine.failOn = nil
        XCTAssertEqual(guardian.restore(), [])
        XCTAssertFalse(FileManager.default.fileExists(atPath: statePath))
    }

    func testReapplyPicksUpNewlyPluggedServicesWithoutOverwritingTheirSnapshot() throws {
        let machine = makeMachine()
        let guardian = makeGuard(machine)
        try guardian.apply(takeOverDNS: true, disableIPv6: true)
        machine.services.append(("USB Ethernet", .init(device: "en9", dns: ["192.168.9.1"])))
        let summary = try guardian.apply(takeOverDNS: true, disableIPv6: true)
        XCTAssertTrue(summary.dnsServices.contains("USB Ethernet"))
        XCTAssertEqual(machine.service("Ethernet").dns, [AppInfo.tunDNS])
        XCTAssertEqual(guardian.restore(), [])
        XCTAssertEqual(machine.service("Ethernet").dns, ["10.0.0.53", "10.0.0.54"], "二次应用不能把已接管的 DNS 当成原始值")
        XCTAssertEqual(machine.service("USB Ethernet").dns, ["192.168.9.1"])
    }

    func testOptionsAreIndependent() throws {
        let dnsOnly = makeMachine()
        try makeGuard(dnsOnly).apply(takeOverDNS: true, disableIPv6: false)
        XCTAssertEqual(dnsOnly.service("Wi-Fi").v6.mode, .automatic)

        try? FileManager.default.removeItem(atPath: statePath)
        let v6Only = makeMachine()
        try makeGuard(v6Only).apply(takeOverDNS: false, disableIPv6: true)
        XCTAssertEqual(v6Only.service("Ethernet").dns, ["10.0.0.53", "10.0.0.54"])
        XCTAssertEqual(v6Only.service("Wi-Fi").v6.mode, .off)
    }

    func testRestoreWithoutStateIsANoOp() {
        let machine = makeMachine()
        XCTAssertEqual(makeGuard(machine).restore(), [])
        XCTAssertTrue(machine.commands.filter { $0.dropFirst().first?.hasPrefix("-set") == true }.isEmpty)
    }
}

final class NetworkSetupParserTests: XCTestCase {
    /// 取自真实机器的输出。
    func testServiceOrderFromRealMachine() {
        let text = """
        An asterisk (*) denotes that a network service is disabled.
        (1) USB Controls
        (Hardware Port: USB Controls, Device: usbmodemABC1234567892)

        (2) Ethernet
        (Hardware Port: Ethernet, Device: en0)

        (3) Thunderbolt Bridge
        (Hardware Port: Thunderbolt Bridge, Device: bridge0)

        (4) Wi-Fi
        (Hardware Port: Wi-Fi, Device: en1)

        (5) iPhone USB
        (Hardware Port: iPhone USB, Device: en10)

        (6) Tailscale
        (Hardware Port: io.tailscale.ipn.macsys, Device: )

        """
        XCTAssertEqual(NetworkSetupParser.services(fromServiceOrder: text).map(\.name),
                       ["Ethernet", "Thunderbolt Bridge", "Wi-Fi", "iPhone USB"])
    }

    func testDisabledServicesAreSkipped() {
        let text = "(*) Old Adapter\n(Hardware Port: Old Adapter, Device: en7)\n\n(1) Wi-Fi\n(Hardware Port: Wi-Fi, Device: en1)\n"
        XCTAssertEqual(NetworkSetupParser.services(fromServiceOrder: text).map(\.name), ["Wi-Fi"])
    }

    func testDNSServers() {
        XCTAssertEqual(NetworkSetupParser.dnsServers(from: "There aren't any DNS Servers set on Wi-Fi.\n").automatic, true)
        let manual = NetworkSetupParser.dnsServers(from: "223.5.5.5\n119.29.29.29\n")
        XCTAssertFalse(manual.automatic)
        XCTAssertEqual(manual.servers, ["223.5.5.5", "119.29.29.29"])
    }

    func testIPv6SettingFromRealGetInfo() {
        let wifi = """
        Manually Using DHCP Router Configuration
        IP address: 192.168.2.205
        Subnet mask: 255.255.255.0
        Router: 192.168.2.1
        IPv6: Automatic
        IPv6 IP address: none
        IPv6 Router: none
        Wi-Fi ID: 5c:1b:f4:96:93:49
        """
        XCTAssertEqual(NetworkSetupParser.ipv6Setting(fromInfo: wifi), IPv6Setting(mode: .automatic))
        let manual = "IPv6: Manual\nIPv6 IP address: fd00::5\nIPv6 Prefix Length: 64\nIPv6 Router: fd00::1\n"
        XCTAssertEqual(NetworkSetupParser.ipv6Setting(fromInfo: manual),
                       IPv6Setting(mode: .manual, address: "fd00::5", prefixLength: "64", router: "fd00::1"))
        XCTAssertEqual(NetworkSetupParser.ipv6Setting(fromInfo: "IPv6: Off\n")?.mode, .off)
        XCTAssertNil(NetworkSetupParser.ipv6Setting(fromInfo: "IPv6: Something New\n"), "不认识的状态宁可跳过也不要乱改")
        XCTAssertNil(NetworkSetupParser.ipv6Setting(fromInfo: "no ipv6 line"))
    }
}
