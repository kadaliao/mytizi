import XCTest
@testable import MytiziKit

/// 样本来自真实机器的 `netstat -rn -f inet`。
final class RoutingTableTests: XCTestCase {
    /// 普通情况：Wi-Fi 是主出口，有线是第二条（scoped），mihomo 的子网路由挂在 utun7 上。
    static let normal = """
    Routing tables

    Internet:
    Destination        Gateway            Flags               Netif Expire
    default            192.168.2.1        UGScg                 en0       
    default            192.168.2.1        UGScIg                en1       
    1                  198.18.0.1         UGSc                utun7       
    2/7                198.18.0.1         UGSc                utun7       
    100.64/10          link#25            UCS                 utun4       
    100.100.100.100    100.100.100.100    UH                  utun4       
    127                127.0.0.1          UCS                   lo0       
    169.254            link#6             UCS                   en0      !
    192.168.2          link#6             UCS                   en0      !
    192.168.2.1        a8:5e:45:1:2:3     UHLWIir               en0   1189
    198.18.0.1         198.18.0.1         UH                  utun7       
    224.0.0/4          link#6             UmCS                  en0      !
    """

    /// Tailscale 开了 exit node：它的 utun 排到第一位，物理网卡的默认路由变成 scoped。
    static let exitNode = """
    Routing tables

    Internet:
    Destination        Gateway            Flags               Netif Expire
    default            utun4              UCSg                utun4       
    default            192.168.2.1        UGScIg                en0       
    100.64/10          link#25            UCS                 utun4       
    """

    func testParsesDefaultRoutesInPriorityOrder() {
        let routes = RoutingTable.defaultRoutes(fromNetstat: Self.normal)
        XCTAssertEqual(routes, [
            DefaultRoute(gateway: "192.168.2.1", interface: "en0", flags: "UGScg"),
            DefaultRoute(gateway: "192.168.2.1", interface: "en1", flags: "UGScIg"),
        ])
        XCTAssertEqual(RoutingTable.physicalEgress(in: routes), "en0")
    }

    func testExitNodeDoesNotBecomeTheEgress() {
        let routes = RoutingTable.defaultRoutes(fromNetstat: Self.exitNode)
        XCTAssertEqual(routes.first?.interface, "utun4", "第一条默认路由确实是 VPN 的")
        XCTAssertEqual(RoutingTable.physicalEgress(in: routes), "en0", "出口必须跳过 utun，取物理网卡")
    }

    func testOlderNetstatLayoutWithRefsAndUseColumns() {
        let text = """
        Destination        Gateway            Flags        Refs      Use   Netif Expire
        default            10.0.0.1           UGSc           93        0     en5
        """
        XCTAssertEqual(RoutingTable.defaultRoutes(fromNetstat: text), [DefaultRoute(gateway: "10.0.0.1", interface: "en5", flags: "UGSc")])
    }

    func testOfflineAndNonPhysicalDefaultsGiveNoEgress() {
        XCTAssertEqual(RoutingTable.defaultRoutes(fromNetstat: "Routing tables\n\nInternet:\n"), [])
        XCTAssertNil(RoutingTable.physicalEgress(in: []))
        let vpnOnly = "Destination Gateway Flags Netif Expire\ndefault ppp0 UCSg ppp0\ndefault link#30 UCSg ipsec0\n"
        XCTAssertNil(RoutingTable.physicalEgress(in: RoutingTable.defaultRoutes(fromNetstat: vpnOnly)), "ppp/ipsec 不是物理出口")
    }

    func testPhysicalInterfaceNames() {
        for name in ["en0", "en12", "bridge0"] { XCTAssertTrue(RoutingTable.isPhysicalInterface(name), name) }
        for name in ["utun4", "ppp0", "ipsec0", "awdl0", "llw0", "lo0", "en", "enX", "bridge"] {
            XCTAssertFalse(RoutingTable.isPhysicalInterface(name), name)
        }
    }
}
