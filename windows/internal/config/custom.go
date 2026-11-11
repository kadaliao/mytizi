package config

import (
	"fmt"
	"strings"
)

// CustomRuleError 指出 custom-rules.txt 的第几行有什么问题。
type CustomRuleError struct {
	Line    int
	Message string
}

func (e *CustomRuleError) Error() string { return fmt.Sprintf("第 %d 行：%s", e.Line, e.Message) }

var allowedRuleTypes = map[string]bool{
	"DOMAIN": true, "DOMAIN-SUFFIX": true, "DOMAIN-KEYWORD": true, "IP-CIDR": true, "IP-CIDR6": true,
	"GEOIP": true, "GEOSITE": true, "PROCESS-NAME": true, "DST-PORT": true,
}

var targetAliases = map[string]string{
	"PROXY": "PROXY", "DIRECT": "DIRECT", "REJECT": "REJECT",
	"代理": "PROXY", "直连": "DIRECT", "拒绝": "REJECT",
}

// ParseCustomRules 解析 custom-rules.txt。每行一条规则，# 开头为注释，行尾 " # 说明" 也会被忽略。
// 目标只允许 PROXY / DIRECT / REJECT（也接受 代理 / 直连 / 拒绝），其余在这里直接报行号，
// 不让一份写坏的规则拖到 mihomo -t 才整体失败。
//
// 后台服务收到 App 发来的规则后会再调用一次：只有通过这里的规则才会进入以 SYSTEM 身份运行的核心配置。
func ParseCustomRules(text string) ([]string, error) {
	var result []string
	text = strings.TrimPrefix(text, "\ufeff") // 记事本保存的 UTF-8 带 BOM
	for index, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if i := strings.Index(line, " #"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		number := index + 1
		parts := strings.Split(line, ",")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		if len(parts) < 3 {
			return nil, &CustomRuleError{number, "格式应为 类型,值,目标，例如 DOMAIN-SUFFIX,example.com,PROXY"}
		}
		kind := strings.ToUpper(parts[0])
		if !allowedRuleTypes[kind] {
			return nil, &CustomRuleError{number, "不支持的规则类型 " + parts[0]}
		}
		if parts[1] == "" {
			return nil, &CustomRuleError{number, "缺少匹配值"}
		}
		if strings.ContainsAny(parts[1], "()\"'\x00") {
			return nil, &CustomRuleError{number, "匹配值里有不允许的字符：" + parts[1]}
		}
		target, ok := targetAliases[strings.ToUpper(parts[2])]
		if !ok {
			target, ok = targetAliases[parts[2]]
		}
		if !ok {
			return nil, &CustomRuleError{number, "目标必须是 PROXY、DIRECT 或 REJECT，收到 " + parts[2]}
		}
		rule := []string{kind, parts[1], target}
		if len(parts) > 3 {
			if len(parts) != 4 || strings.ToLower(parts[3]) != "no-resolve" ||
				!(strings.HasPrefix(kind, "IP-CIDR") || kind == "GEOIP") {
				return nil, &CustomRuleError{number, "多余的参数 " + strings.Join(parts[3:], ",")}
			}
			rule = append(rule, "no-resolve")
		}
		result = append(result, strings.Join(rule, ","))
	}
	return result, nil
}

// CustomRulesTemplate 是第一次打开自定义规则文件时写入的说明。
const CustomRulesTemplate = "# 自定义规则：每行一条，优先于所有内置规则。保存后重新连接生效。\r\n" +
	"# 格式：类型,值,目标      目标可以写 PROXY（代理）、DIRECT（直连）、REJECT（拒绝）\r\n" +
	"# 例：\r\n" +
	"# DOMAIN-SUFFIX,example.com,PROXY\r\n" +
	"# DOMAIN,intranet.corp.example,DIRECT\r\n" +
	"# IP-CIDR,203.0.113.0/24,DIRECT,no-resolve\r\n" +
	"# PROCESS-NAME,WeChat.exe,DIRECT\r\n" +
	"\r\n"
