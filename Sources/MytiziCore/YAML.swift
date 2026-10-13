import Foundation

/// 极小的 YAML 值与输出器，只覆盖生成 mihomo 配置所需的子集。
///
/// 不引入第三方 YAML 库的原因：配置只需要"写"，不需要"读"；订阅内容交给 mihomo 自己解析。
/// 所有字符串一律加双引号，避免 `no`、`on`、`+.lan`、`198.18.0.1/16` 之类被 YAML 当成别的类型。
public indirect enum YAML: Equatable {
    case string(String)
    case int(Int)
    case bool(Bool)
    case array([YAML])
    case map([(String, YAML)])

    public static func == (lhs: YAML, rhs: YAML) -> Bool { lhs.render() == rhs.render() }

    public func render() -> String {
        var lines: [String] = []
        switch self {
        case let .map(pairs): emitMap(pairs, indent: 0, into: &lines)
        case let .array(items): emitArray(items, indent: 0, into: &lines)
        default: lines.append(Self.scalar(self))
        }
        return lines.joined(separator: "\n") + "\n"
    }

    // MARK: 输出

    private func emitMap(_ pairs: [(String, YAML)], indent: Int, into lines: inout [String]) {
        let pad = String(repeating: " ", count: indent)
        for (key, value) in pairs {
            let k = Self.key(key)
            switch value {
            case let .map(inner):
                if inner.isEmpty {
                    lines.append("\(pad)\(k): {}")
                } else {
                    lines.append("\(pad)\(k):")
                    emitMap(inner, indent: indent + 2, into: &lines)
                }
            case let .array(items):
                if items.isEmpty {
                    lines.append("\(pad)\(k): []")
                } else {
                    lines.append("\(pad)\(k):")
                    emitArray(items, indent: indent + 2, into: &lines)
                }
            default:
                lines.append("\(pad)\(k): \(Self.scalar(value))")
            }
        }
    }

    private func emitArray(_ items: [YAML], indent: Int, into lines: inout [String]) {
        let pad = String(repeating: " ", count: indent)
        for item in items {
            switch item {
            case let .map(pairs):
                if pairs.isEmpty {
                    lines.append("\(pad)- {}")
                    continue
                }
                // 第一对跟在 "- " 后面，其余按 indent+2 对齐。
                var inner: [String] = []
                emitMap(pairs, indent: indent + 2, into: &inner)
                inner[0] = "\(pad)- " + inner[0].dropFirst(indent + 2)
                lines.append(contentsOf: inner)
            case let .array(nested):
                lines.append("\(pad)-")
                emitArray(nested, indent: indent + 2, into: &lines)
            default:
                lines.append("\(pad)- \(Self.scalar(item))")
            }
        }
    }

    private static func key(_ key: String) -> String {
        let plain = key.range(of: "^[A-Za-z0-9_][A-Za-z0-9_.-]*$", options: .regularExpression) != nil
        return plain ? key : quote(key)
    }

    private static func scalar(_ value: YAML) -> String {
        switch value {
        case let .string(text): return quote(text)
        case let .int(number): return String(number)
        case let .bool(flag): return flag ? "true" : "false"
        default: return "null"
        }
    }

    /// 双引号字符串。JSON 转义是 YAML 双引号转义的子集，可以直接复用规则。
    static func quote(_ text: String) -> String {
        var result = "\""
        for scalar in text.unicodeScalars {
            switch scalar {
            case "\"": result += "\\\""
            case "\\": result += "\\\\"
            case "\n": result += "\\n"
            case "\r": result += "\\r"
            case "\t": result += "\\t"
            default:
                if scalar.value < 0x20 || scalar.value == 0x7F {
                    result += String(format: "\\u%04X", scalar.value)
                } else {
                    result.unicodeScalars.append(scalar)
                }
            }
        }
        return result + "\""
    }
}

extension YAML: ExpressibleByStringLiteral, ExpressibleByIntegerLiteral, ExpressibleByBooleanLiteral,
    ExpressibleByArrayLiteral
{
    public init(stringLiteral value: String) { self = .string(value) }
    public init(integerLiteral value: Int) { self = .int(value) }
    public init(booleanLiteral value: Bool) { self = .bool(value) }
    public init(arrayLiteral elements: YAML...) { self = .array(elements) }
}

extension YAML {
    public static func strings(_ values: [String]) -> YAML { .array(values.map { .string($0) }) }
}
