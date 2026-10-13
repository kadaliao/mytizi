import Foundation
import XCTest

enum TestSupport {
    /// 仓库根目录（Tests/MytiziCoreTests/TestSupport.swift 往上三级）。
    static let root: URL = URL(fileURLWithPath: #filePath)
        .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()

    static var vendorDir: URL { root.appendingPathComponent("Vendor/core") }

    /// 当前架构对应的内置 mihomo；没跑过 scripts/fetch-core.sh 时返回 nil，相关测试会被跳过。
    static var coreBinary: URL? {
        #if arch(arm64)
        let name = "mihomo-arm64"
        #else
        let name = "mihomo-amd64"
        #endif
        let url = vendorDir.appendingPathComponent(name)
        return FileManager.default.isExecutableFile(atPath: url.path) ? url : nil
    }

    static func requireCore() throws -> URL {
        guard let core = coreBinary,
              FileManager.default.fileExists(atPath: vendorDir.appendingPathComponent("geoip.metadb").path),
              FileManager.default.fileExists(atPath: vendorDir.appendingPathComponent("geosite.dat").path)
        else { throw XCTSkip("缺少 Vendor/core，先运行 scripts/fetch-core.sh") }
        return core
    }

    /// 准备一个带地理库的 mihomo 工作目录。
    static func makeCoreHome() throws -> URL {
        let home = FileManager.default.temporaryDirectory
            .appendingPathComponent("mytizi-test-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: home.appendingPathComponent("providers"), withIntermediateDirectories: true)
        for name in ["geoip.metadb", "geosite.dat"] {
            try FileManager.default.copyItem(
                at: vendorDir.appendingPathComponent(name), to: home.appendingPathComponent(name))
        }
        return home
    }

    /// 一份最小的 Clash 订阅：一个 ss、一个 http 节点（服务器是文档保留地址，不会真的连）。
    static let sampleClashSubscription = """
    proxies:
      - {name: "节点-香港", type: ss, server: 203.0.113.10, port: 8388, cipher: aes-128-gcm, password: "secret-pass"}
      - {name: "节点-网关", type: http, server: 192.0.2.5, port: 7890}
    """
}
