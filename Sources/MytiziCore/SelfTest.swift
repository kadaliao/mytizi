import Foundation
import MytiziKit

public enum Route: String, Equatable {
    case direct = "直连"
    case proxy = "代理"
    case reject = "拒绝"
}

public struct SelfTestResult: Equatable {
    public var name: String
    public var url: String
    public var expected: Route?
    public var reachable: Bool
    public var status: Int?
    public var milliseconds: Int?
    public var error: String?
    /// 从核心日志里读到的实际命中规则与出口，例如 "DomainSuffix(baidu.com) → DIRECT"。
    public var observed: String?
    public var observedRoute: Route?
    /// 需要人注意但不属于"不通"的情况（比如 DNS 没有被接管）。
    public var concern: String?

    /// 期望的出口和实际出口不一致（只有两者都已知才判断）。
    public var routeMismatch: Bool {
        guard let expected, let observedRoute else { return false }
        return expected != observedRoute
    }

    public var passed: Bool { reachable && !routeMismatch && concern == nil }
}

/// 核心日志里一条连接记录的解析：`[TCP] 198.18.0.1:60822 --> www.baidu.com:443 match DomainSuffix(baidu.com) using DIRECT`
public struct RouteLogEntry: Equatable {
    public var host: String
    public var port: Int
    public var rule: String
    public var chain: String

    public var route: Route {
        if chain.hasPrefix("DIRECT") { return .direct }
        if chain.hasPrefix("REJECT") { return .reject }
        return .proxy
    }

    public static func parse(_ line: String) -> RouteLogEntry? {
        guard let regex = try? NSRegularExpression(
            pattern: #"-->\s+(\S+?):(\d+)\s+match\s+(.+?)\s+using\s+(.+?)"?\s*$"#)
        else { return nil }
        let range = NSRange(line.startIndex..., in: line)
        guard let match = regex.firstMatch(in: line, range: range), match.numberOfRanges == 5 else { return nil }
        func group(_ index: Int) -> String? { Range(match.range(at: index), in: line).map { String(line[$0]) } }
        guard let host = group(1), let port = group(2).flatMap(Int.init), let rule = group(3), let chain = group(4) else { return nil }
        return RouteLogEntry(host: host, port: port, rule: rule, chain: chain)
    }

    /// 在日志文本里找到某个主机最近的一条记录。
    public static func latest(for host: String, in log: String) -> RouteLogEntry? {
        for line in log.split(separator: "\n").reversed() where line.contains("--> \(host):") {
            if let entry = parse(String(line)), entry.host == host { return entry }
        }
        return nil
    }
}

/// 实机自检：经由真实的网络栈（TUN）访问几个有代表性的地址，并对照核心日志确认实际走了哪条路。
/// 这是排查"国内网站变慢 / 海外打不开 / CDN 图片失败"最有价值的一段信息。
public enum SelfTest {
    public struct Probe {
        public var name: String
        public var url: String
        public var expected: Route
    }

    public static let probes: [Probe] = [
        Probe(name: "国内网站", url: "https://www.baidu.com/", expected: .direct),
        // 腾讯的根域名（qpic.cn）不会返回内容，用它自检会把“CDN 正常”误报成超时；换成一个真实的静态图片。
        Probe(name: "国内 CDN（腾讯图片）", url: "https://thirdqq.qlogo.cn/g?b=sdk&nk=10000&s=100", expected: .direct),
        Probe(name: "国内 CDN（阿里）", url: "https://g.alicdn.com/", expected: .direct),
        Probe(name: "海外（gstatic 204）", url: "https://www.gstatic.com/generate_204", expected: .proxy),
        Probe(name: "海外（google）", url: "https://www.google.com/generate_204", expected: .proxy),
    ]

    /// `coreLog` 是读取核心日志文本的闭包（App 里读 /Library/Logs/MyTizi/mihomo.log）。
    public static func run(coreLog: @escaping () -> String) async -> [SelfTestResult] {
        // 五个探测并发跑：节点全挂时最坏耗时从 5×8 秒降到约 8 秒。
        // 每条结果仍按 probes 的顺序返回（host 不同，日志归属不会串）。
        var ordered = Array<SelfTestResult?>(repeating: nil, count: probes.count)
        await withTaskGroup(of: (Int, SelfTestResult).self) { group in
            for (index, probe) in probes.enumerated() {
                group.addTask { (index, await run(probe, coreLog: coreLog)) }
            }
            for await (index, result) in group { ordered[index] = result }
        }
        return [dnsCheck()] + ordered.compactMap { $0 }
    }

    static func run(_ probe: Probe, coreLog: () -> String) async -> SelfTestResult {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.timeoutIntervalForRequest = 8
        // 绕开系统代理设置，测的是 TUN 本身。
        configuration.connectionProxyDictionary = [:]
        let session = URLSession(configuration: configuration)
        defer { session.invalidateAndCancel() }

        var result = SelfTestResult(name: probe.name, url: probe.url, expected: probe.expected, reachable: false)
        let started = Date()
        do {
            let (_, response) = try await session.data(from: URL(string: probe.url)!)
            result.reachable = true
            result.status = (response as? HTTPURLResponse)?.statusCode
        } catch {
            result.error = (error as? URLError).map { "\($0.code.rawValue) \($0.localizedDescription)" } ?? error.localizedDescription
        }
        result.milliseconds = Int(Date().timeIntervalSince(started) * 1000)

        // 日志是异步落盘的，稍等一下再读。
        try? await Task.sleep(nanoseconds: 400_000_000)
        if let host = URL(string: probe.url)?.host, let entry = RouteLogEntry.latest(for: host, in: coreLog()) {
            result.observed = "\(entry.rule) → \(entry.chain)"
            result.observedRoute = entry.route
        }
        return result
    }

    /// 解析一个国内域名：TUN 接管 DNS 成功时应当拿到 198.18.0.0/16 的 fake-ip。
    static func dnsCheck() -> SelfTestResult {
        var result = SelfTestResult(name: "DNS 是否被接管", url: "getaddrinfo www.baidu.com", expected: nil, reachable: false)
        var hints = addrinfo(ai_flags: 0, ai_family: AF_INET, ai_socktype: SOCK_STREAM, ai_protocol: 0, ai_addrlen: 0, ai_canonname: nil, ai_addr: nil, ai_next: nil)
        var head: UnsafeMutablePointer<addrinfo>?
        let started = Date()
        let code = getaddrinfo("www.baidu.com", nil, &hints, &head)
        result.milliseconds = Int(Date().timeIntervalSince(started) * 1000)
        guard code == 0, let first = head else {
            result.error = String(cString: gai_strerror(code))
            return result
        }
        defer { freeaddrinfo(head) }
        var buffer = [CChar](repeating: 0, count: Int(INET_ADDRSTRLEN))
        first.pointee.ai_addr.withMemoryRebound(to: sockaddr_in.self, capacity: 1) { pointer in
            var address = pointer.pointee.sin_addr
            inet_ntop(AF_INET, &address, &buffer, socklen_t(INET_ADDRSTRLEN))
        }
        let ip = String(cString: buffer)
        result.reachable = true
        if ip.hasPrefix("198.18.") {
            result.observed = "\(ip)（fake-ip，已被接管）"
        } else {
            result.observed = "\(ip)（真实地址）"
            result.concern = "解析到的是真实地址而不是 fake-ip：系统 DNS 没有经过 TUN（未连接时这是正常的）。"
        }
        return result
    }
}
