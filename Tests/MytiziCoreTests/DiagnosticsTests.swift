import XCTest
import MytiziKit
@testable import MytiziCore

final class RedactorTests: XCTestCase {
    let subscriptionURL = "https://sub.example.com/api/v1/client/subscribe?token=9f8e7d6c5b4a39281706f5e4d3c2b1a0&flag=meta"

    func testKnownSecretsAreRemovedEverywhere() {
        var redactor = Redactor(homeDirectory: "/Users/alice")
        redactor.addSubscriptionURL(subscriptionURL)
        redactor.add("controller-secret-123", label: "控制密钥")
        let text = """
        fetched \(subscriptionURL) ok
        bare token 9f8e7d6c5b4a39281706f5e4d3c2b1a0 leaked in log
        Authorization: Bearer controller-secret-123
        path /Users/alice/Library/Application Support/MyTizi/settings.json
        """
        let result = redactor.redact(text)
        XCTAssertFalse(result.contains("9f8e7d6c5b4a39281706f5e4d3c2b1a0"))
        XCTAssertFalse(result.contains("controller-secret-123"))
        XCTAssertFalse(result.contains("/Users/alice"))
        XCTAssertTrue(result.contains("~/Library/Application Support/MyTizi/settings.json"))
        XCTAssertTrue(result.contains("<已脱敏:订阅链接>"))
    }

    func testPatternsCatchUnregisteredSecrets() {
        let redactor = Redactor(homeDirectory: "/nonexistent")
        let samples = [
            "GET /sub?token=abcdef123456&x=1",
            "password: hunter2hunter2",
            "\"uuid\": \"b831381d-6324-4d53-ad4f-8cda48b30811\"",
            "vmess://eyJ2IjoiMiIsInBzIjoiSlAtMDMifQ==",
            "ss://YWVzLTEyOC1nY206dGVzdHBhc3M@203.0.113.1:8388#HK",
            "trojan://pass123@203.0.113.2:443?sni=example.com#US",
            "Authorization: Bearer abc.def.ghi",
        ]
        for sample in samples {
            let redacted = redactor.redact(sample)
            for secret in ["abcdef123456", "hunter2hunter2", "b831381d", "eyJ2IjoiMiIs", "YWVzLTEyOC1n", "pass123@", "abc.def.ghi"] {
                XCTAssertFalse(redacted.contains(secret), "\(sample) → \(redacted)")
            }
        }
    }

    func testOrdinaryDiagnosticTextSurvives() {
        let redactor = Redactor(homeDirectory: "/Users/alice")
        let text = "[TCP] 198.18.0.1:60822 --> www.baidu.com:443 match DomainSuffix(baidu.com) using DIRECT\nnameserver : 223.5.5.5"
        XCTAssertEqual(redactor.redact(text), text)
    }

    func testShortValuesAreNotRegisteredToAvoidMangling() {
        var redactor = Redactor(homeDirectory: "/x")
        redactor.add("ab", label: "短")
        XCTAssertEqual(redactor.redact("abab"), "abab")
    }
}

final class SystemFactsParserTests: XCTestCase {
    func testScutilDNS() {
        let text = """
        DNS configuration

        resolver #1
          nameserver[0] : 198.18.0.2
          if_index : 14 (en1)
          flags    : Request A records
          reach    : 0x00020002 (Reachable,Directly Reachable Address)

        resolver #2
          domain   : local
          options  : mdns
          timeout  : 5
          order    : 300000

        resolver #3
          domain   : claude.ai
          nameserver[0] : 127.0.0.1
          port     : 1053
          order    : 100100

        DNS configuration (for scoped queries)

        resolver #1
          nameserver[0] : 192.168.2.1
          if_index : 14 (en1)
        """
        let resolvers = SystemFactsParser.resolvers(fromScutilDNS: text)
        XCTAssertEqual(resolvers.count, 4)
        XCTAssertEqual(resolvers[0].nameservers, ["198.18.0.2"])
        XCTAssertFalse(resolvers[0].scoped)
        XCTAssertEqual(resolvers[2].domain, "claude.ai")
        XCTAssertEqual(resolvers[2].port, 1053)
        XCTAssertTrue(resolvers[3].scoped)
    }

    func testResolverFileFromRealMachine() {
        let file = SystemFactsParser.resolverFile(name: "claude.ai", content: "# Managed by some-proxy-tool; do not edit.\nnameserver 127.0.0.1\nport 1053\ntimeout 3\n")
        XCTAssertEqual(file.nameservers, ["127.0.0.1"])
        XCTAssertEqual(file.port, 1053)
    }

    func testSystemProxy() {
        let proxy = SystemFactsParser.systemProxy(fromScutilProxy: """
        <dictionary> {
          HTTPEnable : 1
          HTTPPort : 7890
          HTTPProxy : 127.0.0.1
          HTTPSEnable : 0
          SOCKSEnable : 0
        }
        """)
        XCTAssertEqual(proxy.http, "127.0.0.1:7890")
        XCTAssertNil(proxy.https)
        XCTAssertTrue(proxy.isAnyEnabled)
        XCTAssertFalse(SystemFactsParser.systemProxy(fromScutilProxy: "<dictionary> {\n  HTTPEnable : 0\n}").isAnyEnabled)
    }

    func testIfconfig() {
        let text = """
        lo0: flags=8049<UP,LOOPBACK,RUNNING,MULTICAST> mtu 16384
        \tinet 127.0.0.1 netmask 0xff000000
        \tinet6 ::1 prefixlen 128
        en1: flags=8863<UP,BROADCAST> mtu 1500
        \tinet 192.168.2.205 netmask 0xffffff00 broadcast 192.168.2.255
        \tinet6 fe80::1c2b:3d4e:5f60:7182%en1 prefixlen 64 secured scopeid 0xe
        \tinet6 2408:8207:1234::5 prefixlen 64 autoconf secured
        utun7: flags=8051<UP,POINTOPOINT,RUNNING,MULTICAST> mtu 9000
        \tinet 198.18.0.1 --> 198.18.0.1 netmask 0xfffffffc
        utun0: flags=8051<UP,POINTOPOINT,RUNNING,MULTICAST> mtu 1500
        \tinet6 fe80::1%utun0 prefixlen 64 scopeid 0x10
        """
        XCTAssertEqual(SystemFactsParser.tunInterfaces(fromIfconfig: text), [TunInterface(name: "utun7", addresses: ["198.18.0.1"])])
        XCTAssertEqual(SystemFactsParser.globalIPv6Interfaces(fromIfconfig: text), ["en1"], "只算物理网卡上的全局地址，链路本地和 utun 不算")
    }

    func testConflictingProcesses() {
        let ps = """
          101 /usr/libexec/syslogd
          202 /Applications/Clash Verge.app/Contents/MacOS/verge-mihomo
          303 /usr/local/bin/sing-box
          404 /Library/Application Support/MyTizi/bin/mihomo
          505 /usr/sbin/tailscaled
        """
        let found = SystemFactsParser.conflictingProcesses(fromPS: ps)
        XCTAssertEqual(found.count, 3)
        XCTAssertFalse(found.contains { $0.contains("/MyTizi/") }, "自己的核心不算冲突")
    }
}

final class RouteLogTests: XCTestCase {
    func testParsesMihomoConnectionLines() {
        let line = #"time="2026-10-13T19:05:01.123+08:00" level=info msg="[TCP] 198.18.0.1:60822 --> www.baidu.com:443 match DomainSuffix(baidu.com) using DIRECT""#
        let entry = RouteLogEntry.parse(line)
        XCTAssertEqual(entry?.host, "www.baidu.com")
        XCTAssertEqual(entry?.port, 443)
        XCTAssertEqual(entry?.rule, "DomainSuffix(baidu.com)")
        XCTAssertEqual(entry?.route, .direct)

        let proxied = #"level=info msg="[TCP] 198.18.0.1:60823(curl) --> www.google.com:443 match GeoSite(geolocation-!cn) using PROXY[AUTO[香港-01]]""#
        XCTAssertEqual(RouteLogEntry.parse(proxied)?.route, .proxy)
        XCTAssertEqual(RouteLogEntry.parse(proxied)?.chain, "PROXY[AUTO[香港-01]]")

        let fallback = #"level=info msg="[TCP] 198.18.0.1:1 --> unknown.example:443 match Match using PROXY[AUTO[x]]""#
        XCTAssertEqual(RouteLogEntry.parse(fallback)?.rule, "Match")
        XCTAssertNil(RouteLogEntry.parse("level=info msg=\"Start initial configuration\""))
    }

    func testLatestEntryForHostIsTheLastOne() {
        let log = """
        level=info msg="[TCP] 198.18.0.1:1 --> a.com:443 match Match using PROXY[x]"
        level=info msg="[TCP] 198.18.0.1:2 --> b.com:443 match Match using DIRECT"
        level=info msg="[TCP] 198.18.0.1:3 --> a.com:443 match DomainSuffix(a.com) using DIRECT"
        """
        XCTAssertEqual(RouteLogEntry.latest(for: "a.com", in: log)?.chain, "DIRECT")
        XCTAssertNil(RouteLogEntry.latest(for: "c.com", in: log))
    }
}

final class AnalyzerTests: XCTestCase {
    private func healthyInput() -> DiagnosticsInput {
        var input = DiagnosticsInput()
        input.now = Date(timeIntervalSince1970: 1_790_000_000)
        input.installState = .upToDate
        input.settings.subscription = SubscriptionRecord(
            url: "https://sub.example.com/x?token=abcdef123456", name: "公司", addedAt: input.now, updatedAt: input.now,
            nodes: [ProbedNode(name: "HK", type: "Shadowsocks")])
        input.helperStatus = HelperStatus(phase: .running, corePID: 99, startedAt: input.now, controllerPort: 19097, controllerSecret: "s", tunInterface: "utun7")
        var facts = SystemFacts()
        facts.tunInterfaces = [TunInterface(name: "utun7", addresses: [AppInfo.tunGateway])]
        facts.dnsResolvers = [DNSResolver(index: 1, nameservers: [AppInfo.tunDNS], domain: nil, port: nil, scoped: false)]
        input.facts = facts
        input.nodes = [NodeDelay(name: "HK", type: "Shadowsocks", delay: 80)]
        return input
    }

    func testHealthyStateHasNoFindings() {
        XCTAssertEqual(DiagnosticsAnalyzer.findings(healthyInput()), [])
    }

    func testDNSNotPointingAtTUNIsAProblem() {
        var input = healthyInput()
        input.facts.dnsResolvers = [DNSResolver(index: 1, nameservers: ["192.168.2.1"], domain: nil, port: nil, scoped: false)]
        let findings = DiagnosticsAnalyzer.findings(input)
        XCTAssertEqual(findings.first?.severity, .problem)
        XCTAssertTrue(findings.first?.title.contains("DNS") == true)
    }

    func testMissingTUNAndRivalTUN() {
        var input = healthyInput()
        input.facts.tunInterfaces = [TunInterface(name: "utun9", addresses: ["198.18.0.1"].map { _ in "198.18.0.9" })]
        let titles = DiagnosticsAnalyzer.findings(input).map(\.title)
        XCTAssertTrue(titles.contains("找不到 TUN 网卡"))
        XCTAssertTrue(titles.contains("检测到另一个 TUN 在使用同一地址段"))
    }

    func testIPv6AndSystemProxyWarnings() {
        var input = healthyInput()
        input.facts.globalIPv6Interfaces = ["en1"]
        input.facts.systemProxy = SystemProxyInfo(http: "127.0.0.1:7890", https: nil, socks: nil, pac: nil)
        let findings = DiagnosticsAnalyzer.findings(input)
        XCTAssertEqual(Set(findings.map(\.title)), ["IPv6 仍然开启", "系统代理仍然开启"])
        XCTAssertTrue(findings.allSatisfy { $0.severity == .warning })
    }

    func testStrandedDNSAfterDisconnectIsFlaggedWithTheFixCommand() {
        var input = healthyInput()
        input.helperStatus = HelperStatus(phase: .stopped)
        input.facts.services = [ServiceNetworkInfo(name: "Wi-Fi", device: "en1", dnsServers: [AppInfo.tunDNS], dnsAutomatic: false, ipv6: "Off")]
        let finding = DiagnosticsAnalyzer.findings(input).first
        XCTAssertEqual(finding?.severity, .problem)
        XCTAssertTrue(finding?.detail.contains("networksetup -setdnsservers \"Wi-Fi\" Empty") == true)
    }

    func testStaleResolverFilesFromOtherTools() {
        var input = healthyInput()
        input.facts.deadLoopbackResolvers = [SystemFactsParser.resolverFile(name: "claude.ai", content: "nameserver 127.0.0.1\nport 1053\n")]
        let finding = DiagnosticsAnalyzer.findings(input).first { $0.title.contains("/etc/resolver") }
        XCTAssertNotNil(finding)
        XCTAssertTrue(finding!.detail.contains("claude.ai") && finding!.detail.contains("1053"))
    }

    func testSubscriptionProblems() {
        var input = healthyInput()
        input.settings.subscription?.usage = SubscriptionUsage(upload: 0, download: 2_000, total: 1_000, expire: input.now.addingTimeInterval(-86400))
        input.settings.subscription?.updatedAt = input.now.addingTimeInterval(-9 * 86400)
        let titles = DiagnosticsAnalyzer.findings(input).map(\.title)
        XCTAssertTrue(titles.contains("订阅已过期"))
        XCTAssertTrue(titles.contains("订阅流量已用完"))
        XCTAssertTrue(titles.contains("订阅超过 7 天没有更新"))
        input.settings.subscription = nil
        XCTAssertTrue(DiagnosticsAnalyzer.findings(input).map(\.title).contains("还没有添加订阅"))
    }

    func testAllNodesDownAndCrash() {
        var input = healthyInput()
        input.nodes = [NodeDelay(name: "a", type: "Http", delay: nil), NodeDelay(name: "b", type: "Http", delay: nil)]
        XCTAssertTrue(DiagnosticsAnalyzer.findings(input).map(\.title).contains("所有线路测速都失败"))
        input.helperStatus = HelperStatus(phase: .crashed, lastExit: CoreExit(at: input.now, code: 9, bySignal: true, expected: false))
        XCTAssertTrue(DiagnosticsAnalyzer.findings(input).map(\.title).contains("核心意外退出"))
    }

    func testSelfTestFindings() {
        var input = healthyInput()
        input.selfTest = [
            SelfTestResult(name: "国内 CDN", url: "https://qpic.cn/", expected: .direct, reachable: true, status: 200, milliseconds: 90, observed: "Match → PROXY[x]", observedRoute: .proxy),
            SelfTestResult(name: "海外", url: "https://www.google.com/", expected: .proxy, reachable: false, milliseconds: 8000, error: "timeout"),
        ]
        let findings = DiagnosticsAnalyzer.findings(input)
        XCTAssertTrue(findings.contains { $0.title.contains("国内 CDN") && $0.severity == .warning })
        XCTAssertTrue(findings.contains { $0.title.contains("海外") && $0.severity == .problem })
    }

    func testUninstalledHelperIsReportedOnce() {
        var input = healthyInput()
        input.installState = .notInstalled
        input.helperStatus = nil
        input.helperError = "后台组件没有运行，需要安装或修复。"
        XCTAssertEqual(DiagnosticsAnalyzer.findings(input).map(\.title), ["后台组件没有安装"], "没装时 socket 本来就不存在，不重复报")
    }

    func testInstalledButUnreachableHelperIsAProblem() {
        var input = healthyInput()
        input.installState = .upToDate
        input.helperStatus = nil
        input.helperError = "与后台组件通信失败：timeout"
        let finding = DiagnosticsAnalyzer.findings(input).first
        XCTAssertEqual(finding?.title, "无法连接后台组件")
        XCTAssertTrue(finding?.detail.contains("launchctl print system/com.mytizi.helper") == true)
    }
}

final class ReportTests: XCTestCase {
    func testReportIsSelfContainedAndNeverLeaksSecrets() {
        var input = DiagnosticsInput()
        input.appVersion = "0.1.0"
        input.appPhase = "已连接"
        input.settings.subscription = SubscriptionRecord(
            url: "https://sub.example.com/api/v1/client/subscribe?token=9f8e7d6c5b4a39281706f5e4d3c2b1a0", name: "公司",
            addedAt: Date(), updatedAt: Date(), nodes: [ProbedNode(name: "HK-01", type: "Shadowsocks")])
        input.helperStatus = HelperStatus(phase: .running, corePID: 99, startedAt: Date(), controllerPort: 19097, controllerSecret: "super-secret-controller-key", tunInterface: "utun7")
        input.coreLog = """
        time="t" level=warning msg="dial https://sub.example.com/api/v1/client/subscribe?token=9f8e7d6c5b4a39281706f5e4d3c2b1a0 failed"
        time="t" level=info msg="[TCP] 198.18.0.1:1 --> secret-site.example:443 match Match using PROXY[x]"
        """
        input.helperLog = "Authorization: Bearer super-secret-controller-key"
        input.userPaths = UserPaths(home: URL(fileURLWithPath: NSHomeDirectory()))

        let summary = DiagnosticsReport.render(input, redactor: DiagnosticsService.makeRedactor(for: input), includeFullLogs: false)
        let full = DiagnosticsReport.render(input, redactor: DiagnosticsService.makeRedactor(for: input), includeFullLogs: true)

        for report in [summary, full] {
            XCTAssertFalse(report.contains("9f8e7d6c5b4a39281706f5e4d3c2b1a0"), "订阅 token 泄漏")
            XCTAssertFalse(report.contains("super-secret-controller-key"), "控制密钥泄漏")
            XCTAssertTrue(report.contains("sub.example.com"), "订阅主机用于判断来源，应保留")
            for heading in ["## 给 AI 的说明", "## 1. 自动识别的结论", "## 2. 当前状态", "## 8. 文件位置", "## 9. 日志"] {
                XCTAssertTrue(report.contains(heading), "缺少 \(heading)")
            }
            XCTAssertTrue(report.contains(SystemPaths.coreLog), "必须给出核心日志的绝对路径")
            XCTAssertTrue(report.contains("match X using Y"), "要解释日志格式")
        }
        XCTAssertFalse(summary.contains("secret-site.example"), "摘要版不含连接日志里的域名")
        XCTAssertTrue(full.contains("secret-site.example"), "完整版包含最近连接")
        XCTAssertTrue(full.contains("完整版"))
    }

    func testReportRendersWithEmptyInput() {
        let report = DiagnosticsReport.render(DiagnosticsInput(), redactor: Redactor(), includeFullLogs: false)
        XCTAssertTrue(report.contains("还没有添加订阅"))
        XCTAssertTrue(report.contains("尚未添加订阅"))
    }
}
