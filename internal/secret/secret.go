// Package secret 集中维护敏感信息脱敏规则，供 session 落盘与 observe 入 trace 共用同一套规则，
// 避免两处脱敏不一致。仅依赖标准库（叶子包，不引入任何业务依赖），正则预编译为包级变量避免热路径重复编译。
// 设计原则：宁可少脱敏低熵业务串，必脱敏高熵凭据/令牌（OPTIMIZE_SPEC S2/S3）。
package secret

import "regexp"

// placeholder 是脱敏后的占位串。
const placeholder = "[REDACTED]"

// fieldRule 匹配 JSON 中的凭据字段值（api_key/apikey/secret/token/password），容忍转义引号；
// 保留键名与首尾引号，仅替换值（捕获组 1 与 3）。
var fieldRule = regexp.MustCompile(`(?i)("(?:api[_-]?key|secret|token|password)"\s*:\s*")(\\?"|[^"])*?(")`)

// tokenRules 是各类高熵凭据/令牌的整体替换规则（命中即整体替换为占位串）。
var tokenRules = []*regexp.Regexp{
	regexp.MustCompile(`sk-[A-Za-z0-9_-]{8,}`),                                                       // OpenAI/DeepSeek 风格 API key
	regexp.MustCompile(`gh[posu]_[A-Za-z0-9]{20,}`),                                                  // GitHub PAT（ghp_/gho_/ghs_/ghu_）
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),                                                           // AWS Access Key ID
	regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`),                                               // Slack token
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/-]{16,}=*`),                                      // HTTP Bearer 头令牌
	regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`),              // JWT（三段式）
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`), // PEM 私钥块
}

// Redact 把 b 中命中的凭据/令牌替换为占位串，规则覆盖常见云厂商与凭据格式。
// 先按字段规则脱敏 JSON 值，再按各类令牌规则整体替换；空输入原样返回。
func Redact(b []byte) []byte {
	if len(b) == 0 {
		return b
	}
	out := fieldRule.ReplaceAll(b, []byte(`${1}`+placeholder+`${3}`))
	for _, re := range tokenRules {
		out = re.ReplaceAll(out, []byte(placeholder))
	}
	return out
}

// RedactString 是 Redact 的字符串便捷封装，供 span 属性等字符串场景使用。
func RedactString(s string) string {
	if s == "" {
		return s
	}
	return string(Redact([]byte(s)))
}
