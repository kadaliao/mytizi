import Foundation

/// 全局常量。改名只需要改这里和 scripts/build-app.sh 顶部的变量。
public enum AppInfo {
    public static let displayName = "MyTizi"
    public static let bundleID = "com.mytizi.app"
    public static let helperLabel = "com.mytizi.helper"

    /// App 与 helper 之间的协议版本。不兼容变化时递增，App 会提示更新组件。
    public static let ipcVersion = 1

    /// 内置核心使用的 TUN 地址。.1 给 mihomo，.2 留给系统 DNS 指向（不会被 fake-ip 池分配）。
    public static let tunGateway = "198.18.0.1"
    public static let tunDNS = "198.18.0.2"
}

/// root 拥有的系统位置：由安装脚本创建，helper 读写，App 只读。
public enum SystemPaths {
    public static let supportDir = "/Library/Application Support/MyTizi"
    public static let binDir = supportDir + "/bin"
    public static let coreBinary = binDir + "/mihomo"
    /// mihomo 的工作目录（-d）：geoip.metadb、geosite.dat、providers/、cache.db
    public static let coreHome = supportDir + "/core-home"
    public static let componentsManifest = supportDir + "/components.json"
    /// 修改网络设置之前写入的恢复点；存在即表示"可能有未恢复的改动"。
    public static let networkState = supportDir + "/network-state.json"

    public static let helperBinary = "/Library/PrivilegedHelperTools/" + AppInfo.helperLabel
    public static let daemonPlist = "/Library/LaunchDaemons/" + AppInfo.helperLabel + ".plist"
    public static let socket = "/var/run/mytizi.sock"

    /// 日志放在用户可读的位置，App 与排查用的 AI 都不需要 root 就能读。
    public static let logDir = "/Library/Logs/MyTizi"
    public static let helperLog = logDir + "/helper.log"
    public static let coreLog = logDir + "/mihomo.log"
}
