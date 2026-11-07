package nodepool

import (
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"github.com/metacubex/mihomo/config"
)

// ParseDNS 校验单个 DNS 上游；空值沿用订阅，裸 IP/地址默认使用 UDP。
func ParseDNS(server string) (*url.URL, error) {
	server = strings.TrimSpace(server)
	if server == "" {
		return nil, nil
	}
	if addr, err := netip.ParseAddr(server); err == nil && addr.Is6() {
		server = "[" + server + "]"
	}
	if !strings.Contains(server, "://") {
		server = "udp://" + server
	}
	u, err := url.Parse(server)
	if err != nil || u.Hostname() == "" || strings.ContainsAny(server, " \t\r\n") {
		return nil, fmt.Errorf("DNS 上游地址无效，请填写 IP[:端口] 或 DNS URL")
	}
	switch u.Scheme {
	case "udp", "tcp", "tls", "http", "https", "quic":
	default:
		return nil, fmt.Errorf("DNS 上游仅支持 UDP/TCP/DoH/DoT/DoQ")
	}
	if strings.HasSuffix(u.Host, ":") {
		return nil, fmt.Errorf("DNS 上游端口不能为空")
	}
	if port := u.Port(); port != "" {
		p, err := strconv.ParseUint(port, 10, 16)
		if err != nil || p == 0 {
			return nil, fmt.Errorf("DNS 上游端口必须为 1–65535")
		}
	}
	if u.Scheme != "http" && u.Scheme != "https" && (u.Path != "" || u.RawQuery != "" || u.User != nil) {
		return nil, fmt.Errorf("UDP/TCP/DoT/DoQ DNS 上游只能包含主机、端口和 #参数")
	}
	return u, nil
}

// 先替换内存中的上游并清除策略，再由内核解析，避免加载已被覆盖的私有 DNS 策略。
func overrideDNS(c *config.RawDNS, upstream *url.URL) {
	if upstream == nil {
		return
	}
	servers := []string{upstream.String()}
	c.Enable = true
	c.NameServer = servers
	c.ProxyServerNameserver = servers
	c.DirectNameServer = servers
	c.Fallback = nil
	c.FallbackFilter = config.RawFallbackFilter{}
	c.NameServerPolicy = nil
	c.ProxyServerNameserverPolicy = nil
	c.RespectRules = false
	c.DirectNameServerFollowPolicy = false
	// 域名形式的上游仍需订阅的 bootstrap；IP 上游可直接自举。
	if _, err := netip.ParseAddr(upstream.Hostname()); err == nil {
		c.DefaultNameserver = servers
	}
}
