import Foundation
import XCTest
import MytiziKit
@testable import MytiziCore

final class AppVersionTests: XCTestCase {
    func testParsesCommonTagShapes() {
        XCTAssertEqual(AppVersion("v0.1.0")?.numbers, [0, 1, 0])
        XCTAssertEqual(AppVersion(" 1.10.2 ")?.numbers, [1, 10, 2])
        XCTAssertEqual(AppVersion("0.2.0-beta.1")?.prerelease, "beta.1")
        XCTAssertEqual(AppVersion("0.2.0+build7")?.numbers, [0, 2, 0])
        XCTAssertEqual(AppVersion("0.2")?.description, "0.2")
        XCTAssertNil(AppVersion("dev"))
        XCTAssertNil(AppVersion("v"))
        XCTAssertNil(AppVersion("1.x.0"))
    }

    func testComparesNumericallyNotLexically() {
        XCTAssertLessThan(AppVersion("0.9.0")!, AppVersion("0.10.0")!)
        XCTAssertLessThan(AppVersion("1.0.0-beta")!, AppVersion("1.0.0")!)
        XCTAssertGreaterThan(AppVersion("1.0.1")!, AppVersion("1.0.0-beta.2")!)
        XCTAssertFalse(AppVersion("1.0.0")! < AppVersion("1.0")!)
    }

    func testIsNewerIgnoresUnparseableCurrentVersion() {
        XCTAssertTrue(UpdateChecker.isNewer("0.2.0", than: "0.1.0"))
        XCTAssertFalse(UpdateChecker.isNewer("0.1.0", than: "0.1.0"))
        XCTAssertFalse(UpdateChecker.isNewer("0.1.0", than: "dev"))
    }

    func testNormalizesGitHubTags() {
        XCTAssertEqual(UpdateChecker.normalizedTag("v0.2.0"), "0.2.0")
        XCTAssertEqual(UpdateChecker.normalizedTag("V1.0"), "1.0")
        XCTAssertEqual(UpdateChecker.normalizedTag(" 0.3.0 "), "0.3.0")
    }

    func testOldSettingsGetUpdateDefaults() throws {
        let old = Data(#"{"takeOverDNS": false, "autoConnectOnLaunch": true}"#.utf8)
        let settings = try SettingsStore.decoder.decode(AppSettings.self, from: old)
        XCTAssertFalse(settings.takeOverDNS)
        XCTAssertTrue(settings.autoConnectOnLaunch)
        XCTAssertTrue(settings.autoCheckUpdates, "旧版本没有这个字段，默认开启")
        XCTAssertNil(settings.lastUpdateCheck)
        XCTAssertNil(settings.skippedUpdateVersion)
    }
}

/// 回环服务器：`/releases/latest` 302 到 tag 页面，用来验证“不调 GitHub API、靠重定向拿版本号”的做法。
final class UpdateLoopbackServer {
    private let fd: Int32
    let port: Int

    init() throws {
        let listener = socket(AF_INET, SOCK_STREAM, 0)
        var yes: Int32 = 1
        setsockopt(listener, SOL_SOCKET, SO_REUSEADDR, &yes, socklen_t(MemoryLayout<Int32>.size))
        var address = sockaddr_in()
        address.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
        address.sin_family = sa_family_t(AF_INET)
        address.sin_addr = in_addr(s_addr: inet_addr("127.0.0.1"))
        address.sin_port = 0
        _ = withUnsafePointer(to: &address) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
                bind(listener, $0, socklen_t(MemoryLayout<sockaddr_in>.size))
            }
        }
        listen(listener, 8)
        var assigned = sockaddr_in()
        var length = socklen_t(MemoryLayout<sockaddr_in>.size)
        _ = withUnsafeMutablePointer(to: &assigned) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { getsockname(listener, $0, &length) }
        }
        fd = listener
        port = Int(UInt16(bigEndian: assigned.sin_port))
        let port = port
        Thread.detachNewThread {
            while true {
                let client = accept(listener, nil, nil)
                if client < 0 { break }
                var buffer = [UInt8](repeating: 0, count: 8192)
                let count = read(client, &buffer, buffer.count)
                let request = String(decoding: buffer.prefix(max(count, 0)), as: UTF8.self)
                let path = request.split(separator: " ").dropFirst().first.map(String.init) ?? "/"
                let response: String
                if path.hasSuffix("/releases/latest") {
                    response = "HTTP/1.1 302 Found\r\n"
                        + "Location: http://127.0.0.1:\(port)/o/r/releases/tag/v0.2.0\r\n"
                        + "Content-Length: 0\r\nConnection: close\r\n\r\n"
                } else {
                    let body = """
                    <html><a href="/o/r/releases/download/v0.2.0/MyTizi-0.2.0.dmg">download</a></html>
                    """
                    response = "HTTP/1.1 200 OK\r\nContent-Length: \(body.utf8.count)\r\nConnection: close\r\n\r\n\(body)"
                }
                Data(response.utf8).withUnsafeBytes { _ = write(client, $0.baseAddress, $0.count) }
                close(client)
            }
        }
    }

    deinit {
        shutdown(fd, SHUT_RDWR)
        close(fd)
    }

    var baseURL: URL { URL(string: "http://127.0.0.1:\(port)")! }
}

final class UpdateCheckerTests: XCTestCase {
    func testLatestReadsVersionFromRedirect() async throws {
        let server = try UpdateLoopbackServer()
        let checker = UpdateChecker(repository: "o/r", baseURL: server.baseURL, timeout: 5)
        let info = try await checker.latest()
        XCTAssertEqual(info.tag, "v0.2.0")
        XCTAssertEqual(info.version, "0.2.0")
        XCTAssertEqual(info.releaseURL.absoluteString, "http://127.0.0.1:\(server.port)/o/r/releases/tag/v0.2.0")
        XCTAssertEqual(
            info.downloadURL?.absoluteString,
            "http://127.0.0.1:\(server.port)/o/r/releases/download/v0.2.0/MyTizi-0.2.0.dmg")
        XCTAssertTrue(UpdateChecker.isNewer(info.version, than: "0.1.0"))
    }

    func testRejectsRepositoryNamesThatCouldEscapeThePath() {
        XCTAssertNil(UpdateChecker(repository: "../evil").latestURL)
        XCTAssertNil(UpdateChecker(repository: "a/b/c").latestURL)
        XCTAssertNotNil(UpdateChecker(repository: "kadaliao/mytizi").latestURL)
    }
}
