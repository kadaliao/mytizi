import Foundation
import MytiziHelperKit
import MytiziKit

// root LaunchDaemon：看管内置 mihomo，并负责连接期间的 DNS / IPv6 接管与恢复。
// 由安装脚本以 `--owner-uid <安装者 uid>` 启动；只有该用户的 App 能连接 socket。

signal(SIGPIPE, SIG_IGN)

var ownerUID: uid_t = 0
var arguments = CommandLine.arguments.dropFirst()
while let argument = arguments.popFirst() {
    switch argument {
    case "--owner-uid":
        if let value = arguments.popFirst(), let uid = UInt32(value) { ownerUID = uid }
    case "--version":
        print("mytizi-helper ipc \(AppInfo.ipcVersion)")
        exit(0)
    default:
        break
    }
}
guard ownerUID != 0 else {
    FileHandle.standardError.write(Data("缺少 --owner-uid，拒绝启动（root 不是合法的 owner）\n".utf8))
    exit(64)
}

let paths = HelperPaths.system
let log = RotatingLog(path: paths.helperLog, maxBytes: 1024 * 1024, keep: 2)
let coreLog = RotatingLog(path: paths.coreLog)
let network = NetworkGuard(statePath: paths.networkState, log: { log.line($0) })
let supervisor = CoreSupervisor(paths: paths, network: network, log: log, coreLog: coreLog)

// 崩溃恢复：helper 启动时核心一定没有在运行，恢复点还在就说明上次没有收尾。
if network.hasPendingState {
    log.line("发现上次遗留的网络改动，正在恢复")
    let failures = network.restore()
    if !failures.isEmpty { log.line("恢复遗留改动时有失败：\(failures)（恢复点已保留，下次启动继续尝试）") }
}

let server = HelperServer(paths: paths, ownerUID: ownerUID, supervisor: supervisor, log: log)

// SIGTERM（launchctl bootout / 关机）：先停核心并还原网络，再退出。
var signalSources: [DispatchSourceSignal] = []
for signalNumber in [SIGTERM, SIGINT] {
    signal(signalNumber, SIG_IGN)
    let source = DispatchSource.makeSignalSource(signal: signalNumber, queue: .global())
    source.setEventHandler {
        log.line("收到信号 \(signalNumber)，停止并退出")
        supervisor.shutdown()
        server.stopListening()
        exit(0)
    }
    source.resume()
    signalSources.append(source)
}

do {
    try server.run()
} catch {
    log.line("helper 异常退出：\(error)")
    exit(1)
}
