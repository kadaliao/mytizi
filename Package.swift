// swift-tools-version: 5.9
import PackageDescription

// MyTizi：极简 macOS TUN 代理客户端。
//
//   MytiziKit        共享层：路径、IPC 协议、Unix socket、Shell（无第三方依赖）
//   MytiziCore       App 侧逻辑：订阅、配置生成、规则、诊断（无 UI）
//   MytiziHelperKit  root helper 逻辑：mihomo 进程管理、DNS/IPv6 接管与恢复
//   MytiziHelper     helper 可执行文件（LaunchDaemon）
//   MyTizi           SwiftUI App
let package = Package(
    name: "MyTizi",
    platforms: [.macOS(.v13)],
    products: [
        .executable(name: "MyTizi", targets: ["MyTizi"]),
        .executable(name: "mytizi-helper", targets: ["MytiziHelper"]),
    ],
    targets: [
        .target(name: "MytiziKit"),
        .target(name: "MytiziCore", dependencies: ["MytiziKit"]),
        .target(name: "MytiziHelperKit", dependencies: ["MytiziKit"]),
        .executableTarget(name: "MytiziHelper", dependencies: ["MytiziHelperKit"]),
        .executableTarget(name: "MyTizi", dependencies: ["MytiziCore"]),
        .testTarget(name: "MytiziKitTests", dependencies: ["MytiziKit"]),
        .testTarget(name: "MytiziCoreTests", dependencies: ["MytiziCore"]),
        .testTarget(name: "MytiziHelperKitTests", dependencies: ["MytiziHelperKit", "MytiziCore"]),
    ]
)
