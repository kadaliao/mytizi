import Foundation

// App ⇄ helper 的协议：Unix socket 上的按行 JSON，一次连接一问一答。

public enum HelperMethod: String, Codable {
    case hello
    case status
    case start
    case stop
    /// 只替换 core-home 下的文件（订阅刷新），不重启核心。
    case updateFiles
}

/// 写入 core-home 的文件。path 必须是 `providers/` 下的相对路径，helper 会再校验一次。
public struct HelperFile: Codable, Equatable {
    public var path: String
    public var base64: String

    public init(path: String, data: Data) {
        self.path = path
        self.base64 = data.base64EncodedString()
    }
}

public struct StartParams: Codable {
    public var configYAML: String
    public var files: [HelperFile]
    public var controllerPort: Int
    public var controllerSecret: String
    /// 把系统 DNS 指向 TUN，确保所有解析都被 mihomo 接管。
    public var takeOverDNS: Bool
    /// 连接期间在系统层关闭 IPv6（断开/崩溃后恢复）。
    public var disableIPv6: Bool
    /// 配置里启用了 TUN 时为 true：helper 会等 TUN 网卡真正出现才算连接成功。
    public var expectTUN: Bool
    /// 配置里 `interface-name` 固定的出口网卡（App 生成配置时从路由表取得）；nil 表示交给核心自动检测。
    /// helper 会在启动后与网络变化时重新核对，并通过控制接口改过去。
    public var egressInterface: String?

    public init(
        configYAML: String, files: [HelperFile], controllerPort: Int, controllerSecret: String,
        takeOverDNS: Bool, disableIPv6: Bool, expectTUN: Bool = true, egressInterface: String? = nil
    ) {
        self.configYAML = configYAML
        self.files = files
        self.controllerPort = controllerPort
        self.controllerSecret = controllerSecret
        self.takeOverDNS = takeOverDNS
        self.disableIPv6 = disableIPv6
        self.expectTUN = expectTUN
        self.egressInterface = egressInterface
    }
}

public struct HelperRequest: Codable {
    public var method: HelperMethod
    public var start: StartParams?
    public var files: [HelperFile]?

    public init(method: HelperMethod, start: StartParams? = nil, files: [HelperFile]? = nil) {
        self.method = method
        self.start = start
        self.files = files
    }
}

public struct HelperError: Codable, Error, Equatable {
    public var code: String
    public var message: String
    /// 供排查用的原始信息（核心日志尾部等），不直接展示给用户。
    public var detail: String?

    public init(code: String, message: String, detail: String? = nil) {
        self.code = code
        self.message = message
        self.detail = detail
    }
}

public struct HelloInfo: Codable, Equatable {
    public var ipcVersion: Int
    public var helperPID: Int32
    public var ownerUID: UInt32
    public var coreInstalled: Bool

    public init(ipcVersion: Int, helperPID: Int32, ownerUID: UInt32, coreInstalled: Bool) {
        self.ipcVersion = ipcVersion
        self.helperPID = helperPID
        self.ownerUID = ownerUID
        self.coreInstalled = coreInstalled
    }
}

/// helper 为了能在崩溃后恢复而做过的网络改动，诊断报告会原样展示。
public struct NetworkChangeSummary: Codable, Equatable {
    public var dnsServices: [String]
    public var ipv6Services: [String]

    public init(dnsServices: [String] = [], ipv6Services: [String] = []) {
        self.dnsServices = dnsServices
        self.ipv6Services = ipv6Services
    }

    public var isEmpty: Bool { dnsServices.isEmpty && ipv6Services.isEmpty }
}

public struct CoreExit: Codable, Equatable {
    public var at: Date
    public var code: Int32
    /// true 表示收到信号而不是正常退出。
    public var bySignal: Bool
    public var expected: Bool
    public var logTail: String?

    public init(at: Date, code: Int32, bySignal: Bool, expected: Bool, logTail: String? = nil) {
        self.at = at
        self.code = code
        self.bySignal = bySignal
        self.expected = expected
        self.logTail = logTail
    }
}

public struct HelperStatus: Codable, Equatable {
    public enum Phase: String, Codable {
        case stopped, starting, running, stopping, crashed
    }

    public var phase: Phase
    public var corePID: Int32?
    public var startedAt: Date?
    public var controllerPort: Int?
    /// 仅在 running 时返回：同一个用户的 App 重新打开后据此重新接上控制器。
    public var controllerSecret: String?
    public var tunInterface: String?
    /// 核心当前固定的出口网卡（物理默认网卡）；nil 表示核心在自动检测。
    public var egressInterface: String?
    public var changes: NetworkChangeSummary
    public var lastExit: CoreExit?

    public init(
        phase: Phase, corePID: Int32? = nil, startedAt: Date? = nil, controllerPort: Int? = nil,
        controllerSecret: String? = nil, tunInterface: String? = nil, egressInterface: String? = nil,
        changes: NetworkChangeSummary = .init(), lastExit: CoreExit? = nil
    ) {
        self.phase = phase
        self.corePID = corePID
        self.startedAt = startedAt
        self.controllerPort = controllerPort
        self.controllerSecret = controllerSecret
        self.tunInterface = tunInterface
        self.egressInterface = egressInterface
        self.changes = changes
        self.lastExit = lastExit
    }
}

public struct HelperResponse: Codable {
    public var ok: Bool
    public var error: HelperError?
    public var hello: HelloInfo?
    public var status: HelperStatus?

    public init(ok: Bool, error: HelperError? = nil, hello: HelloInfo? = nil, status: HelperStatus? = nil) {
        self.ok = ok
        self.error = error
        self.hello = hello
        self.status = status
    }

    public static func failure(_ code: String, _ message: String, detail: String? = nil) -> HelperResponse {
        HelperResponse(ok: false, error: HelperError(code: code, message: message, detail: detail))
    }
}

public enum IPCCoding {
    public static let encoder: JSONEncoder = {
        let encoder = JSONEncoder()
        encoder.dateEncodingStrategy = .iso8601
        return encoder
    }()

    public static let decoder: JSONDecoder = {
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .iso8601
        return decoder
    }()
}
