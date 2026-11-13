// safehttp.go：用户可控 URL 的出口守卫——拒绝把连接建立到环回/链路本地等敏感地址。
// 主要函数/类型：
//   - AllowLocalEnv / allowLocal：逃生口环境变量与判定。
//   - IsBlockedIP / BlockInternalDial：地址段判定与 http.Transport 拨号钩子。
//   - Transport / Client：带守卫、带连接池复用的 Transport 与 Client 构造。
//
// 为什么需要一层共享包：本平台有多处会向「用户填写的 URL」发起请求——LLM 的
// base_url、通知渠道的 webhook、FOFA 数据源、代理清单源。这些地址若指向
// 127.0.0.1 或 169.254.169.254，响应内容会被写进错误信息并回显到界面上，等于给出
// 一条半盲的读取原语（云环境下还能读出实例凭据）。每个调用点各写一遍守卫既容易漏，
// 也容易写得不一样，所以集中在这里，由各调用点复用同一个 Client。
package safehttp

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"
)

// AllowLocalEnv 是逃生口开关名（设为 1/true 即允许连接本机/链路本地地址）。
//
// 导出是为了让测试能明确地打开它——本仓库的用例大量使用 127.0.0.1 上的 httptest
// 假服务端，不打开就全部被守卫拦下。
const AllowLocalEnv = "ARTEX_ALLOW_LOCAL_TARGETS"

// allowLocal 决定是否放行环回 / 链路本地地址。
//
// 默认拒绝。这些地址段不是 LLM 网关、IM 机器人或公网数据源会出现的地方，而它们
// 能打到的东西很敏感：同机另一个服务的管理端口、以及云环境的元数据端点。
// 但「本机自建服务」（本地跑一个 ollama 或 mock LLM）是真实存在的用法，
// 所以留一个显式逃生口而不是硬编码放行。
func allowLocal() bool {
	v := strings.TrimSpace(os.Getenv(AllowLocalEnv))
	return v == "1" || strings.EqualFold(v, "true")
}

// IsBlockedIP 报告目标 IP 是否属于「默认不允许连接」的地址段。
//
// 只拒绝环回、链路本地（含云元数据 169.254.169.254）、未指定与组播。
// **不拒绝** RFC1918 私网：内网自建 LLM 网关 / 通知服务是很常见的合法用法，
// 把它们一并挡掉会让功能在真实环境里直接不可用。这条取舍是刻意的——
// 挡住真正敏感的目标，同时不把正常部署一起废掉。
func IsBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	// IPv4-mapped IPv6（::ffff:127.0.0.1）要还原成 IPv4 再判，否则绕过检查。
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}

// BlockInternalDial 是 http.Transport 拨号器的 Control 钩子，在**连接建立时**
// 检查目标地址。
//
// 为什么设在拨号阶段而不是只在保存配置时校验：这里才是最终生效点。它同时覆盖
// 两种绕过配置校验的情形——DNS 重绑定（校验时解析到公网 IP、真正连接时解析到
// 内网）与重定向（同主机跳转仍可能把路径指到别处）。
func BlockInternalDial(_, address string, _ syscall.RawConn) error {
	if allowLocal() {
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("无法解析目标地址 %q", host)
	}
	if IsBlockedIP(ip) {
		return fmt.Errorf("拒绝连接到本机/链路本地地址 %s（如确需连接本机服务，设置 %s=1）", ip, AllowLocalEnv)
	}
	return nil
}

// dialer 是各 Transport 共用的拨号器：带守卫、带合理超时。
var dialer = &net.Dialer{Timeout: 10 * time.Second, Control: BlockInternalDial}

// Transport 在默认 Transport 的基础上只加一个拨号守卫。
//
// 用 Clone 保留默认的全部调优（连接池、HTTP/2、超时），避免为了加一个检查
// 而改动其它行为。**每次调用返回新的 Transport**——需要长连接的调用方应自己
// 持有一个并复用，用完记得 CloseIdleConnections。
func Transport() *http.Transport {
	t, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t = &http.Transport{}
	}
	clone := t.Clone()
	clone.DialContext = dialer.DialContext
	return clone
}

// Client 构造一个带守卫的 Client。timeout 传 0 表示用 Transport 自带的行为。
//
// 调用方应当持有返回的 Client 复用，而不是每次请求都新建——每次新建等于每次
// 丢弃一个连接池，空闲连接要等 GC 触发才回收。
func Client(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: Transport()}
}

// NoRedirectClient 额外拒绝跨主机重定向。
//
// 用于凭据就在 URL 里的场景（IM webhook 的 access_token、数据源的 API key）：
// 跟随跨主机跳转等于把凭据交给重定向目标。同主机跳转（如末尾补斜杠）仍允许。
func NoRedirectClient(timeout time.Duration) *http.Client {
	c := Client(timeout)
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("重定向次数过多")
		}
		if len(via) > 0 && req.URL.Host != via[0].URL.Host {
			return fmt.Errorf("拒绝跨主机重定向（%s → %s）", via[0].URL.Host, req.URL.Host)
		}
		return nil
	}
	return c
}
