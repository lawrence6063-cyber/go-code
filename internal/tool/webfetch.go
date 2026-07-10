// Package tool 中的 webfetch.go 实现 web_fetch 工具：抓取指定 URL 并回流文本内容。
// 这是本工具集里唯一真实的出网通道，SSRF 防护是核心设计点，采用三层纵深：
//  1. Scheme 白名单：仅允许 http/https（CheckPermission 阶段即拒绝其余 scheme）。
//  2. DNS 解析级地址校验 + 直连已校验 IP：不把域名交给标准库自行再解析，而是自己解析、
//     校验、再显式拨号到通过校验的具体 IP，堵住"校验通过后 DNS 被换成内网 IP"的
//     rebinding/TOCTOU 缺口。
//  3. 重定向复校验：因校验逻辑内嵌在 DialContext 里，每一次跳转产生的新连接都会重新
//     走一遍同样的校验，天然获得"重定向目标复检"的效果，无需在 CheckRedirect 里重复实现。
package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/alaindong/cogent/internal/observe"
	"github.com/alaindong/cogent/internal/permission"
	"github.com/alaindong/cogent/internal/types"
)

// fetchTimeout 是单次抓取的总超时（含 DNS/连接/传输）。
const fetchTimeout = 15 * time.Second

// maxFetchBytes 是响应体回流内容的字节上限，防止超大页面撑爆上下文。
const maxFetchBytes = 256 * 1024

// maxFetchRedirects 是跟随重定向的次数上限。
const maxFetchRedirects = 5

// ErrSSRFBlocked 表示目标地址解析到了私有/保留/内网地址段，被 SSRF 防护拦截。
var ErrSSRFBlocked = errors.New("target address is private or reserved, blocked")

// extraBlockedCIDRs 是用户安全规则额外点名需要拒绝的地址段（叠加在标准私有/保留地址段之上）。
var extraBlockedCIDRs = mustParseCIDRs("9.0.0.0/8", "11.0.0.0/8", "21.0.0.0/8", "30.0.0.0/8")

// mustParseCIDRs 解析一组编译期常量 CIDR 字面量；输入非法即视为代码缺陷，panic 快速失败
// （对标 regexp.MustCompile 的用法：仅用于程序自身硬编码的字面量，不用于任何外部输入）。
func mustParseCIDRs(cidrs ...string) []*net.IPNet {
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic("tool: invalid CIDR literal " + c)
		}
		nets = append(nets, n)
	}
	return nets
}

// isBlockedIP 报告 ip 是否命中私有/保留/内网地址段：标准库 net.IP 判定（回环、私有、
// 链路本地、未指定、多播）之外，再叠加用户安全规则点名的额外段（9/11/21/30.0.0.0/8）。
func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
	for _, n := range extraBlockedCIDRs {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// safeDialContext 是 http.Transport 的 DialContext：真实建连前解析 host 得到具体 IP 并逐一
// 校验，首个通过校验的 IP 才发起拨号；全部命中拦截或解析失败则返回 ErrSSRFBlocked。
func safeDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("split host port %q: %w", addr, err)
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", host, err)
	}
	for _, ipAddr := range ips {
		if isBlockedIP(ipAddr.IP) {
			continue
		}
		d := net.Dialer{Timeout: fetchTimeout}
		return d.DialContext(ctx, network, net.JoinHostPort(ipAddr.IP.String(), port))
	}
	return nil, ErrSSRFBlocked
}

// webFetchTool 抓取指定 URL 并回流文本内容；这是一条真实的出网通道，即便不写本地文件也
// 应经人在环确认，默认非只读、非并发安全、权限 ask（沿用 Defaults，不覆写）。
type webFetchTool struct {
	Defaults
	client *http.Client
	tracer observe.Tracer
}

// NewWebFetch 构造 web_fetch 工具，内置带 SSRF 防护 DialContext 的 http.Client。
func NewWebFetch(tracer observe.Tracer) Tool {
	return &webFetchTool{
		client: &http.Client{
			Timeout:   fetchTimeout,
			Transport: &http.Transport{DialContext: safeDialContext},
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= maxFetchRedirects {
					return fmt.Errorf("stopped after %d redirects", maxFetchRedirects)
				}
				if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
					return fmt.Errorf("redirect to unsupported scheme %q blocked", req.URL.Scheme)
				}
				return nil
			},
		},
		tracer: tracer,
	}
}

func (t *webFetchTool) Name() string { return "web_fetch" }
func (t *webFetchTool) Description() string {
	return "Fetch a URL over HTTP(S) and return its text content (HTML tags stripped). " +
		"Only http/https URLs are allowed; requests to private/internal/reserved IP " +
		"addresses are blocked (SSRF protection), including redirect targets."
}

func (t *webFetchTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"url":{"type":"string","description":"the http(s) URL to fetch"}},"required":["url"]}`)
}

// webFetchInput 是 web_fetch 的入参。
type webFetchInput struct {
	URL string `json:"url"`
}

// CheckPermission 做无网络 I/O 的静态校验（scheme/host 格式），命中即 Deny；
// IP 级别的地址段拦截无需在此重复解析域名，统一由 Call 内的 safeDialContext 权威执行。
func (t *webFetchTool) CheckPermission(_ context.Context, input json.RawMessage) (permission.Decision, error) {
	var in webFetchInput
	if err := json.Unmarshal(input, &in); err != nil {
		return permission.Decision{Behavior: permission.BehaviorDeny, Reason: "invalid url input"}, nil
	}
	if err := validateFetchURL(in.URL); err != nil {
		return permission.Decision{Behavior: permission.BehaviorDeny, Reason: err.Error()}, nil
	}
	return permission.Decision{Behavior: permission.BehaviorAsk}, nil
}

// validateFetchURL 校验 URL 可解析、scheme 为 http/https、且带有非空 host。
func validateFetchURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported scheme %q, only http/https allowed", u.Scheme)
	}
	if u.Hostname() == "" {
		return errors.New("url must have a host")
	}
	return nil
}

// Call 校验 URL、经 SSRF 安全的 client 抓取，并把响应体规整为文本回流。
func (t *webFetchTool) Call(ctx context.Context, input json.RawMessage, _ ProgressSink) (types.ToolResult, error) {
	var in webFetchInput
	if err := json.Unmarshal(input, &in); err != nil {
		return types.ToolResult{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}
	if err := validateFetchURL(in.URL); err != nil {
		return types.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	ctx, end := t.tracer.Start(ctx, "web.fetch", observe.Attr{Key: "url.host", Value: hostOf(in.URL)})
	body, contentType, err := t.doFetch(ctx, in.URL)
	end(err)
	if err != nil {
		if errors.Is(err, ErrSSRFBlocked) {
			return types.ToolResult{Content: "target address is private/internal, blocked (SSRF protection)", IsError: true}, nil
		}
		return types.ToolResult{Content: fmt.Sprintf("fetch failed: %v", err), IsError: true}, nil
	}
	return types.ToolResult{Content: renderFetchBody(body, contentType)}, nil
}

// doFetch 发起 GET 请求并读取至多 maxFetchBytes+1 字节的响应体（+1 用于判定是否发生截断）。
func (t *webFetchTool) doFetch(ctx context.Context, rawURL string) (body []byte, contentType string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return nil, "", fmt.Errorf("http status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchBytes+1))
	if err != nil {
		return nil, "", err
	}
	return data, resp.Header.Get("Content-Type"), nil
}

// hostOf 从原始 URL 提取 host，仅用于 trace 属性；解析失败返回空串。
func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Hostname()
	}
	return ""
}

// htmlScriptStyleRe 匹配 <script>/<style> 整块（含内容），渲染前先剔除避免噪声混入正文。
var htmlScriptStyleRe = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</(script|style)>`)

// htmlTagRe 匹配任意 HTML 标签，粗粒度剥离用于把标签替换为空格。
var htmlTagRe = regexp.MustCompile(`<[^>]*>`)

// renderFetchBody 把响应体规整为回流文本：Content-Type 含 html 时做轻量标签剥离
// （正则粗粒度实现，不引入 HTML 解析依赖，允许残留少量格式噪声），其余按纯文本回流；
// 超过 maxFetchBytes 时截断并标注。
func renderFetchBody(data []byte, contentType string) string {
	truncated := len(data) > maxFetchBytes
	if truncated {
		data = data[:maxFetchBytes]
	}
	text := string(data)
	if strings.Contains(strings.ToLower(contentType), "html") {
		text = htmlScriptStyleRe.ReplaceAllString(text, "")
		text = htmlTagRe.ReplaceAllString(text, " ")
		text = strings.Join(strings.Fields(text), " ")
	}
	if truncated {
		text += "\n... [truncated]"
	}
	return text
}
