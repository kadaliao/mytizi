import XCTest
import MytiziKit
@testable import MytiziCore

final class YAMLTests: XCTestCase {
    func testScalarsAreAlwaysQuotedSoYAMLCannotReinterpretThem() {
        let yaml = YAML.map([("mode", "no"), ("a", "198.18.0.1/16"), ("n", 7), ("b", true)])
        XCTAssertEqual(yaml.render(), "mode: \"no\"\na: \"198.18.0.1/16\"\nn: 7\nb: true\n")
    }

    func testKeysThatAreNotPlainGetQuoted() {
        let yaml = YAML.map([("+.lan", .strings(["1.1.1.1"])), ("fake-ip-range", "x")])
        XCTAssertEqual(yaml.render(), "\"+.lan\":\n  - \"1.1.1.1\"\nfake-ip-range: \"x\"\n")
    }

    func testNestedMapsAndArraysOfMaps() {
        let yaml = YAML.map([
            ("tun", .map([("enable", true), ("dns-hijack", .strings(["any:53"]))])),
            ("groups", .array([
                .map([("name", "A"), ("use", .strings(["sub"]))]),
                .map([("name", "B"), ("proxies", .strings([]))]),
            ])),
            ("empty", .map([])),
        ])
        XCTAssertEqual(yaml.render(), """
        tun:
          enable: true
          dns-hijack:
            - "any:53"
        groups:
          - name: "A"
            use:
              - "sub"
          - name: "B"
            proxies: []
        empty: {}

        """)
    }

    func testQuotingEscapesControlCharactersAndKeepsChinese() {
        XCTAssertEqual(YAML.quote("a\"b\\c\nd\u{1}"), "\"a\\\"b\\\\c\\nd\\u0001\"")
        XCTAssertEqual(YAML.quote("节点-香港"), "\"节点-香港\"")
    }
}

final class RuleCatalogTests: XCTestCase {
    private let domainPattern = "^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$"

    func testAllDomainsAreWellFormedLowercase() {
        for domain in RuleCatalog.directDomains + RuleCatalog.proxyDomains {
            XCTAssertNotNil(domain.range(of: domainPattern, options: .regularExpression), "格式不合法：\(domain)")
        }
    }

    func testNoDomainAppearsTwice() {
        let all = RuleCatalog.directDomains + RuleCatalog.proxyDomains
        let duplicates = Dictionary(grouping: all, by: { $0 }).filter { $1.count > 1 }.keys.sorted()
        XCTAssertEqual(duplicates, [])
    }

    /// 一个域名如果是另一个的子域，后者已经覆盖前者，留着只会让清单越来越难维护。
    func testNoEntryIsRedundantBecauseAParentIsAlreadyListed() {
        for list in [RuleCatalog.directDomains, RuleCatalog.proxyDomains] {
            let set = Set(list)
            for domain in list {
                var parent = domain
                while let dot = parent.firstIndex(of: ".") {
                    parent = String(parent[parent.index(after: dot)...])
                    XCTAssertFalse(set.contains(parent), "\(domain) 已被 \(parent) 覆盖")
                }
            }
        }
    }

    /// 直连与代理互相不能覆盖，否则同一个域名的去向取决于规则先后，难以推理。
    func testDirectAndProxyListsNeverShadowEachOther() {
        let direct = Set(RuleCatalog.directDomains)
        for domain in RuleCatalog.proxyDomains {
            var candidate = domain
            while true {
                XCTAssertFalse(direct.contains(candidate), "\(domain) 与直连清单中的 \(candidate) 冲突")
                guard let dot = candidate.firstIndex(of: ".") else { break }
                candidate = String(candidate[candidate.index(after: dot)...])
            }
        }
    }

    /// 此前同类工具踩坑记录里的关键域名必须在内置清单里。
    func testTailscaleDomainsNeverGetFakeIP() {
        // Tailscale 把 socket 绑在物理网卡上，fake-ip 会被发到网关黑洞；这些域名必须返回真实地址。
        for domain in ["+.tailscale.com", "+.tailscale.io", "+.ts.net"] {
            XCTAssertTrue(RuleCatalog.fakeIPFilter.contains(domain), domain)
        }
        XCTAssertEqual(RuleCatalog.tunRouteExclude, ["100.64.0.0/10"])
        XCTAssertTrue(RuleCatalog.privateIPv4.contains("100.64.0.0/10"), "路由层排除之外，规则里也保持直连（mixed-port 入口用）")
    }

    func testKnownProblemDomainsArePinned() {
        let direct = Set(RuleCatalog.directDomains)
        for domain in ["qpic.cn", "qlogo.cn", "weixin.com", "feishu.cn", "feishucdn.com", "byteimg.com", "larksuite.com", "hdslb.com", "alicdn.com"] {
            XCTAssertTrue(direct.contains(domain), "缺少直连域名 \(domain)")
        }
        let proxy = Set(RuleCatalog.proxyDomains)
        for domain in ["googlevideo.com", "googleusercontent.com", "ytimg.com", "claude.ai", "anthropic.com", "claudeusercontent.com", "twimg.com"] {
            XCTAssertTrue(proxy.contains(domain), "缺少代理域名 \(domain)")
        }
    }
}

final class ConfigBuilderTests: XCTestCase {
    private func options(_ configure: (inout ConfigOptions) -> Void = { _ in }) -> ConfigOptions {
        var options = ConfigOptions(controllerPort: 19097, controllerSecret: "s3cret")
        configure(&options)
        return options
    }

    func testRuleLadderOrder() {
        let rules = ConfigBuilder.rules(options {
            $0.customRules = ["DOMAIN-SUFFIX,example.com,PROXY"]
            $0.directHosts = ["sub.example.org"]
        })
        func index(_ prefix: String) -> Int {
            guard let i = rules.firstIndex(where: { $0.hasPrefix(prefix) }) else {
                XCTFail("缺少规则 \(prefix)"); return -1
            }
            return i
        }
        XCTAssertEqual(rules.first, "DOMAIN-SUFFIX,example.com,PROXY", "自定义规则必须最先匹配")
        XCTAssertLessThan(index("DOMAIN,sub.example.org"), index("IP-CIDR,10.0.0.0/8"))
        XCTAssertLessThan(index("IP-CIDR,192.168.0.0/16"), index("DOMAIN-SUFFIX,qpic.cn,DIRECT"))
        XCTAssertLessThan(index("DOMAIN-SUFFIX,qpic.cn,DIRECT"), index("GEOSITE,cn,DIRECT"))
        XCTAssertLessThan(index("DOMAIN-SUFFIX,googlevideo.com,PROXY"), index("GEOSITE,cn,DIRECT"),
                          "海外固定域名要先于 GeoSite，防止数据库把它们误判成国内")
        XCTAssertLessThan(index("GEOSITE,cn,DIRECT"), index("AND,((NETWORK,UDP),(DST-PORT,443)),REJECT"))
        XCTAssertLessThan(index("AND,((NETWORK,UDP),(DST-PORT,443)),REJECT"), index("GEOSITE,geolocation-!cn"))
        XCTAssertLessThan(index("GEOSITE,geolocation-!cn"), index("GEOIP,CN,DIRECT"))
        XCTAssertEqual(rules.last, "MATCH,PROXY", "兜底必须是代理，且是最后一条")
    }

    func testEveryRuleTargetsAKnownPolicy() {
        for rule in ConfigBuilder.rules(options()) {
            let parts = rule.split(separator: ",").map(String.init)
            XCTAssertTrue(
                parts.contains { ["DIRECT", "PROXY", "REJECT"].contains($0) } || rule.hasPrefix("AND,"),
                "规则没有已知目标：\(rule)")
        }
    }

    func testIPv6IsOffEverywhere() {
        let text = ConfigBuilder.build(options()).render()
        XCTAssertTrue(text.contains("\nipv6: false\n"))
        XCTAssertTrue(text.contains("  ipv6: false\n"), "dns.ipv6 必须关闭，否则会返回 AAAA 让应用绕开 TUN")
        XCTAssertFalse(text.contains("inet6-address"), "不创建 IPv6 TUN 地址")
    }

    func testTunDNSAndGeoDefaults() {
        let text = ConfigBuilder.build(options()).render()
        XCTAssertTrue(text.contains("enhanced-mode: \"fake-ip\""))
        XCTAssertTrue(text.contains("\"any:53\"") && text.contains("\"tcp://any:53\""))
        XCTAssertTrue(text.contains("geo-auto-update: false"), "内置数据库，不允许核心联网更新")
        XCTAssertTrue(text.contains("auto-detect-interface: true"))
        XCTAssertTrue(text.contains("external-controller: \"127.0.0.1:19097\""), "控制器只监听回环")
        XCTAssertTrue(text.contains("allow-lan: false"))
        XCTAssertTrue(text.contains("route-exclude-address:\n    - \"100.64.0.0/10\""), "Tailscale 的地址段在路由层就不进 TUN")
    }

    func testEgressInterfaceIsPinnedOnlyWhenKnown() {
        let auto = ConfigBuilder.build(options()).render()
        XCTAssertFalse(auto.contains("interface-name"), "没拿到路由表时交给核心自动检测")
        let pinned = ConfigBuilder.build(options { $0.egressInterface = "en0" }).render()
        XCTAssertTrue(pinned.contains("\ninterface-name: \"en0\"\n"), "出站绑定到物理网卡，其他 VPN 抢了默认路由也抢不走")
        XCTAssertTrue(pinned.contains("auto-detect-interface: true"), "自动检测保留为兜底（核心里显式 interface-name 优先）")
        XCTAssertFalse(ConfigBuilder.build(options { $0.egressInterface = "" }).render().contains("interface-name"))
    }

    func testMixedPortIsOptional() {
        XCTAssertFalse(ConfigBuilder.build(options()).render().contains("mixed-port"))
        let text = ConfigBuilder.build(options { $0.mixedPort = 17897 }).render()
        XCTAssertTrue(text.hasPrefix("mixed-port: 17897\n"))
    }

    func testLocalDNSPolicyOnlyUsesRealRouterAddresses() {
        let none = ConfigBuilder.build(options { $0.localDNS = ["198.18.0.2", "127.0.0.1"] }).render()
        XCTAssertFalse(none.contains("nameserver-policy"), "指向 TUN 自身的 DNS 会造成解析回环")
        let some = ConfigBuilder.build(options { $0.localDNS = ["192.168.2.1"] }).render()
        XCTAssertTrue(some.contains("\"+.lan\":\n      - \"192.168.2.1\""))
    }

    func testGroupsReferenceTheProvider() {
        let text = ConfigBuilder.build(options()).render()
        XCTAssertTrue(text.contains("name: \"AUTO\"") && text.contains("type: \"url-test\""))
        XCTAssertTrue(text.contains("name: \"PROXY\"") && text.contains("type: \"select\""))
        XCTAssertTrue(text.contains("proxy-providers:\n  sub:\n    type: \"file\""))
    }

    // MARK: 用真实核心校验（需要先运行 scripts/fetch-core.sh）

    func testGeneratedConfigPassesMihomoValidation() throws {
        let core = try TestSupport.requireCore()
        let home = try TestSupport.makeCoreHome()
        defer { try? FileManager.default.removeItem(at: home) }
        try TestSupport.sampleClashSubscription.write(
            to: home.appendingPathComponent("providers/sub.yaml"), atomically: true, encoding: .utf8)

        let config = ConfigBuilder.build(options {
            $0.mixedPort = 17897
            $0.directHosts = ["sub.example.org"]
            $0.customRules = ["DOMAIN-SUFFIX,example.com,PROXY", "IP-CIDR,203.0.113.0/24,DIRECT,no-resolve"]
            $0.localDNS = ["192.168.2.1"]
            $0.egressInterface = "en0"
        }).render()
        let configURL = home.appendingPathComponent("config.yaml")
        try config.write(to: configURL, atomically: true, encoding: .utf8)

        let result = Shell.run(core.path, ["-t", "-d", home.path, "-f", configURL.path], timeout: 30)
        XCTAssertTrue(result.succeeded, "mihomo -t 失败：\n\(result.combined)")
        XCTAssertTrue(result.combined.contains("test is successful"), result.combined)
    }

    func testProbeConfigPassesMihomoValidation() throws {
        let core = try TestSupport.requireCore()
        let home = try TestSupport.makeCoreHome()
        defer { try? FileManager.default.removeItem(at: home) }
        try TestSupport.sampleClashSubscription.write(
            to: home.appendingPathComponent("providers/sub.yaml"), atomically: true, encoding: .utf8)
        let configURL = home.appendingPathComponent("probe.yaml")
        try ConfigBuilder.buildProbe(controllerPort: 19098, secret: "x").render()
            .write(to: configURL, atomically: true, encoding: .utf8)

        let result = Shell.run(core.path, ["-t", "-d", home.path, "-f", configURL.path], timeout: 30)
        XCTAssertTrue(result.succeeded, "mihomo -t 失败：\n\(result.combined)")
    }
}

final class CustomRulesTests: XCTestCase {
    func testParsesCommentsAliasesAndNoResolve() throws {
        let text = """
        # 我的规则
        DOMAIN-SUFFIX,example.com,代理
        domain,api.example.com , direct   # 行尾说明

        IP-CIDR,203.0.113.0/24,DIRECT,no-resolve
        """
        XCTAssertEqual(try CustomRules.parse(text), [
            "DOMAIN-SUFFIX,example.com,PROXY",
            "DOMAIN,api.example.com,DIRECT",
            "IP-CIDR,203.0.113.0/24,DIRECT,no-resolve",
        ])
    }

    func testReportsLineNumbers() {
        XCTAssertThrowsError(try CustomRules.parse("# ok\nDOMAIN,only-two")) { error in
            XCTAssertEqual((error as? CustomRuleError)?.line, 2)
        }
        XCTAssertThrowsError(try CustomRules.parse("MATCH,x,PROXY")) { error in
            XCTAssertEqual((error as? CustomRuleError)?.line, 1)
        }
        XCTAssertThrowsError(try CustomRules.parse("DOMAIN,a.com,SOMEWHERE"))
        XCTAssertThrowsError(try CustomRules.parse("DOMAIN,a.com,PROXY,no-resolve"), "no-resolve 只对 IP/GEOIP 规则有意义")
    }
}
