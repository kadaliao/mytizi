import Foundation

/// 诊断报告的脱敏。原则：报告要能直接发给 AI / 同事，所以任何能"登录"或"使用"你的账号/节点的东西都不能出现。
///
/// 两层防护：
///   1. 精确替换：已知的秘密（订阅链接、控制密钥、订阅 token）按原文替换；
///   2. 模式替换：即使没登记，常见形态（token=…、Authorization: Bearer …、UUID、password=…）也会被抹掉。
/// 不脱敏的东西（域名、IP、节点名）是排查所必需的，报告头部会明确告知。
public struct Redactor {
    private var secrets: [(value: String, label: String)] = []
    private let homeDirectory: String

    public init(homeDirectory: String = NSHomeDirectory()) {
        self.homeDirectory = homeDirectory
    }

    /// 登记一个必须抹掉的原文。过短的值（<6）会误伤普通文本，直接忽略。
    public mutating func add(_ value: String?, label: String) {
        guard let value, value.count >= 6 else { return }
        secrets.append((value, label))
    }

    /// 订阅链接：整条链接、其 query 里每个值、以及路径里的长 token 都要登记。
    public mutating func addSubscriptionURL(_ urlString: String?) {
        guard let urlString, let url = URL(string: urlString) else { return }
        add(urlString, label: "订阅链接")
        if let items = URLComponents(url: url, resolvingAgainstBaseURL: false)?.queryItems {
            for item in items { add(item.value, label: "订阅参数") }
        }
        for segment in url.pathComponents where segment.count >= 16 { add(segment, label: "订阅令牌") }
    }

    public func redact(_ text: String) -> String {
        var result = text
        // 长的先换，避免短秘密是长秘密的子串时留下残片。
        for secret in secrets.sorted(by: { $0.value.count > $1.value.count }) {
            result = result.replacingOccurrences(of: secret.value, with: "<已脱敏:\(secret.label)>")
        }
        for (pattern, template) in Self.patterns {
            result = result.replacingOccurrences(of: pattern, with: template, options: .regularExpression)
        }
        if homeDirectory.count > 1 {
            result = result.replacingOccurrences(of: homeDirectory, with: "~")
        }
        return result
    }

    private static let patterns: [(String, String)] = [
        // 带 token 的 URL 参数
        ("(?i)([?&](?:token|key|secret|password|passwd|pwd|auth|sign|signature|access_token|api_key|apikey|uuid)=)[^&\\s\"']+", "$1<已脱敏>"),
        ("(?i)(authorization:\\s*(?:bearer|basic)\\s+)[^\\s\"']+", "$1<已脱敏>"),
        ("(?i)(\"?(?:password|passwd|secret|psk|private-key|uuid)\"?\\s*[:=]\\s*\"?)[^\\s\",}]+", "$1<已脱敏>"),
        // 协议链接里的凭据：ss://xxxx@host、trojan://pass@host、vmess://base64…
        ("(?i)\\b(ss|ssr|trojan|vless|hysteria2?|tuic|vmess|anytls)://[^\\s\"'<>]+", "$1://<已脱敏>"),
        // 裸 UUID（vmess/vless 用户 ID）
        ("\\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\\b", "<uuid>"),
    ]
}
