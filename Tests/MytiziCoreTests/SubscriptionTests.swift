import Foundation
import XCTest
@testable import MytiziCore

final class SubscriptionParsingTests: XCTestCase {
    func testNormalizesPastedLinks() throws {
        XCTAssertEqual(try SubscriptionService.normalizedURL("  https://sub.example.com/api/v1/client/subscribe?token=abc \n").absoluteString,
                       "https://sub.example.com/api/v1/client/subscribe?token=abc")
        // 复制时被软换行拆成两行
        XCTAssertEqual(try SubscriptionService.normalizedURL("https://sub.example.com/x?token=\nabc").absoluteString,
                       "https://sub.example.com/x?token=abc")
        // 一键导入链接
        XCTAssertEqual(
            try SubscriptionService.normalizedURL("clash://install-config?url=https%3A%2F%2Fsub.example.com%2Fa%3Ftoken%3D1&name=x").absoluteString,
            "https://sub.example.com/a?token=1")
    }

    func testRejectsNonSubscriptionInput() {
        for bad in ["", "hello", "ftp://example.com/a", "https://", "example.com/sub"] {
            XCTAssertThrowsError(try SubscriptionService.normalizedURL(bad), bad) { error in
                XCTAssertEqual(error as? SubscriptionError, .invalidURL)
            }
        }
    }

    func testParsesUserInfoHeader() throws {
        let usage = try XCTUnwrap(SubscriptionService.parseUserInfo(
            "upload=455727941; download=6174315083; total=1073741824000; expire=1671815872"))
        XCTAssertEqual(usage.upload, 455_727_941)
        XCTAssertEqual(usage.download, 6_174_315_083)
        XCTAssertEqual(usage.total, 1_073_741_824_000)
        XCTAssertEqual(usage.used, 6_630_043_024)
        XCTAssertEqual(usage.expire, Date(timeIntervalSince1970: 1_671_815_872))
        XCTAssertNil(SubscriptionService.parseUserInfo("garbage"))
        XCTAssertNil(try XCTUnwrap(SubscriptionService.parseUserInfo("download=1; total=2; expire=0")).expire, "expire=0 表示不过期")
    }

    func testParsesTitleFromHeaders() {
        XCTAssertEqual(SubscriptionService.parseTitle(profileTitle: "base64:5oiR55qE6K6i6ZiF", disposition: nil), "我的订阅")
        XCTAssertEqual(SubscriptionService.parseTitle(profileTitle: "Plain", disposition: nil), "Plain")
        XCTAssertEqual(SubscriptionService.parseTitle(profileTitle: nil, disposition: "attachment; filename*=UTF-8''%E8%8A%82%E7%82%B9.yaml"), "节点")
        XCTAssertEqual(SubscriptionService.parseTitle(profileTitle: nil, disposition: "attachment; filename=\"company.yaml\""), "company")
        XCTAssertNil(SubscriptionService.parseTitle(profileTitle: nil, disposition: nil))
    }

    func testValidateCatchesHTMLAndEmpty() {
        XCTAssertThrowsError(try SubscriptionService.validate(body: Data())) {
            XCTAssertEqual($0 as? SubscriptionError, .empty)
        }
        XCTAssertThrowsError(try SubscriptionService.validate(body: Data("<!DOCTYPE html><html>login</html>".utf8))) {
            guard case .notASubscription = $0 as? SubscriptionError else { return XCTFail("应识别为网页") }
        }
        XCTAssertNoThrow(try SubscriptionService.validate(body: Data("proxies:\n  - {name: a}".utf8)))
    }
}

/// 回环上的最小 HTTP 服务器，让 fetch 测试走真实的 URLSession。
final class LoopbackHTTPServer {
    private let fd: Int32
    let port: Int
    private var running = true

    init(status: Int = 200, headers: [String: String] = [:], body: Data) throws {
        let listener = socket(AF_INET, SOCK_STREAM, 0)
        var yes: Int32 = 1
        setsockopt(listener, SOL_SOCKET, SO_REUSEADDR, &yes, socklen_t(MemoryLayout<Int32>.size))
        var address = sockaddr_in()
        address.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
        address.sin_family = sa_family_t(AF_INET)
        address.sin_addr = in_addr(s_addr: inet_addr("127.0.0.1"))
        address.sin_port = 0
        _ = withUnsafePointer(to: &address) { $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { bind(listener, $0, socklen_t(MemoryLayout<sockaddr_in>.size)) } }
        listen(listener, 8)
        var assigned = sockaddr_in()
        var length = socklen_t(MemoryLayout<sockaddr_in>.size)
        _ = withUnsafeMutablePointer(to: &assigned) { $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { getsockname(listener, $0, &length) } }

        var head = "HTTP/1.1 \(status) X\r\nContent-Length: \(body.count)\r\nConnection: close\r\n"
        for (key, value) in headers { head += "\(key): \(value)\r\n" }
        let payload = Data((head + "\r\n").utf8) + body

        fd = listener
        port = Int(UInt16(bigEndian: assigned.sin_port))
        Thread.detachNewThread {
            while true {
                let client = accept(listener, nil, nil)
                if client < 0 { break }
                var buffer = [UInt8](repeating: 0, count: 8192)
                _ = read(client, &buffer, buffer.count)
                payload.withUnsafeBytes { _ = write(client, $0.baseAddress, payload.count) }
                close(client)
            }
        }
    }

    deinit {
        shutdown(fd, SHUT_RDWR)
        close(fd)
    }

    var url: URL { URL(string: "http://127.0.0.1:\(port)/sub?token=t")! }
}

final class SubscriptionFetchTests: XCTestCase {
    func testFetchReadsBodyAndHeaders() async throws {
        let server = try LoopbackHTTPServer(
            headers: [
                "subscription-userinfo": "upload=1; download=2; total=10; expire=1893456000",
                "profile-title": "base64:5YWs5Y+4",
                "profile-update-interval": "12",
            ],
            body: Data(TestSupport.sampleClashSubscription.utf8))
        let fetched = try await SubscriptionService().fetch(url: server.url)
        XCTAssertEqual(String(decoding: fetched.body, as: UTF8.self), TestSupport.sampleClashSubscription)
        XCTAssertEqual(fetched.usage?.total, 10)
        XCTAssertEqual(fetched.title, "公司")
        XCTAssertEqual(fetched.updateIntervalHours, 12)
    }

    func testFetchReportsHTTPStatus() async throws {
        let server = try LoopbackHTTPServer(status: 403, body: Data("forbidden".utf8))
        do {
            _ = try await SubscriptionService().fetch(url: server.url)
            XCTFail("应当抛出")
        } catch {
            XCTAssertEqual(error as? SubscriptionError, .http(403))
        }
    }

    func testFetchRejectsHTMLPage() async throws {
        let server = try LoopbackHTTPServer(body: Data("<html><body>请登录</body></html>".utf8))
        do {
            _ = try await SubscriptionService().fetch(url: server.url)
            XCTFail("应当抛出")
        } catch {
            guard case .notASubscription = error as? SubscriptionError else { return XCTFail("错误类型不对：\(error)") }
        }
    }

    func testFetchReportsUnreachableServer() async throws {
        let port = FreePort.find()
        do {
            _ = try await SubscriptionService().fetch(url: URL(string: "http://127.0.0.1:\(port)/x")!, timeout: 3)
            XCTFail("应当抛出")
        } catch {
            guard case .unreachable = error as? SubscriptionError else { return XCTFail("错误类型不对：\(error)") }
        }
    }
}

final class CoreProbeTests: XCTestCase {
    private func probe(_ body: String) async throws -> [ProbedNode] {
        let core = try TestSupport.requireCore()
        let work = FileManager.default.temporaryDirectory.appendingPathComponent("probe-test-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: work, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: work) }
        return try await CoreProbe.nodes(in: Data(body.utf8), core: core, workDirectory: work)
    }

    func testClashYAMLSubscription() async throws {
        let nodes = try await probe(TestSupport.sampleClashSubscription)
        XCTAssertEqual(nodes.map(\.name), ["节点-香港", "节点-网关"])
        XCTAssertEqual(nodes.map(\.type), ["Shadowsocks", "Http"])
    }

    /// 通用订阅：整段 base64，里面是一行一个的 ss:// vmess:// trojan:// 链接。
    func testBase64URIListSubscription() async throws {
        let vmess = try JSONSerialization.data(withJSONObject: [
            "v": "2", "ps": "JP-03", "add": "203.0.113.3", "port": "443",
            "id": "b831381d-6324-4d53-ad4f-8cda48b30811", "aid": "0", "net": "ws", "type": "none",
            "host": "example.com", "path": "/ws", "tls": "tls",
        ]).base64EncodedString()
        let list = [
            "ss://YWVzLTEyOC1nY206dGVzdHBhc3M@203.0.113.1:8388#HK-01",
            "trojan://pass123@203.0.113.2:443?sni=example.com#US-02",
            "vmess://\(vmess)",
        ].joined(separator: "\n")
        let nodes = try await probe(Data(list.utf8).base64EncodedString())
        XCTAssertEqual(Set(nodes.map(\.name)), ["HK-01", "US-02", "JP-03"])
        XCTAssertEqual(Set(nodes.map(\.type)), ["Shadowsocks", "Trojan", "Vmess"])
    }

    func testGarbageYieldsNoNodes() async throws {
        let nodes = try await probe("this is not a subscription")
        XCTAssertEqual(nodes, [])
    }
}
