package nodepool

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"github.com/metacubex/mihomo/config"
	"github.com/metacubex/mihomo/dns"
)

// ECSProfile 用该地区运营商的样本网段向 DNS 上游提示客户端位置。
type ECSProfile struct {
	Province string `json:"province"`
	ISP      string `json:"isp"`
	Subnet   string `json:"subnet"`
}

func ECSProfiles() []ECSProfile {
	var profiles []ECSProfile
	for province, isps := range ecsProfiles {
		for isp, subnet := range isps {
			profiles = append(profiles, ECSProfile{province, isp, subnet})
		}
	}
	sort.Slice(profiles, func(i, j int) bool {
		if profiles[i].Province != profiles[j].Province {
			return profiles[i].Province < profiles[j].Province
		}
		return profiles[i].ISP < profiles[j].ISP
	})
	return profiles
}

// ResolveECS 校验参数，返回规范网段；自定义 IP/CIDR 优先，空参数保持订阅默认。
func ResolveECS(province, isp, custom string) (string, error) {
	province, isp, custom = strings.TrimSpace(province), strings.TrimSpace(isp), strings.TrimSpace(custom)
	if custom != "" {
		prefix, err := netip.ParsePrefix(custom)
		if err != nil {
			addr, addrErr := netip.ParseAddr(custom)
			if addrErr != nil || addr.Zone() != "" {
				return "", fmt.Errorf("DNS ECS 必须是有效 IP 或 CIDR 网段")
			}
			addr = addr.Unmap()
			prefix = netip.PrefixFrom(addr, addr.BitLen())
		}
		return prefix.Masked().String(), nil
	}
	if province == "" && isp == "" {
		return "", nil
	}
	if province == "" || isp == "" {
		return "", fmt.Errorf("DNS ECS 省份和运营商必须同时指定，或直接填写自定义网段")
	}
	province = strings.NewReplacer("内蒙古自治区", "内蒙古", "广西壮族自治区", "广西", "西藏自治区", "西藏", "宁夏回族自治区", "宁夏", "新疆维吾尔自治区", "新疆").Replace(province)
	province = strings.TrimSuffix(strings.TrimSuffix(province, "省"), "市")
	switch strings.ToLower(isp) {
	case "电信", "中国电信", "telecom", "ct":
		isp = "电信"
	case "联通", "中国联通", "网通", "unicom", "cu":
		isp = "联通"
	case "移动", "中国移动", "mobile", "cm":
		isp = "移动"
	}
	if subnet := ecsProfiles[province][isp]; subnet != "" {
		return subnet, nil
	}
	return "", fmt.Errorf("没有 %q %q 的 DNS ECS 预设；使用 -list-dns-ecs 查看，或填写自定义网段", province, isp)
}

// 复用 Mihomo 的 ECS 客户端包装器，保留上游协议、地址、代理名与其他参数。
func applyECS(c *config.DNS, subnet string) error {
	if subnet == "" {
		return nil
	}
	if !c.Enable {
		return fmt.Errorf("DNS ECS 需要 Clash 订阅启用 dns.enable 并配置 DNS 上游")
	}
	servers := [][]dns.NameServer{c.NameServer, c.Fallback, c.DefaultNameserver, c.ProxyServerNameserver, c.DirectNameServer}
	for _, policy := range c.NameServerPolicy {
		servers = append(servers, policy.NameServers)
	}
	for _, policy := range c.ProxyServerPolicy {
		servers = append(servers, policy.NameServers)
	}
	for _, list := range servers {
		for i := range list {
			ns := &list[i]
			switch ns.Net {
			case "rcode":
				continue // 本地生成的 DNS 响应没有上游查询。
			case "", "udp", "tcp", "tls", "https", "quic":
			default:
				return fmt.Errorf("DNS ECS 不支持 %q DNS 上游；请配置 UDP/TCP/DoH/DoT/DoQ", ns.Net)
			}
			if ns.Params == nil {
				ns.Params = make(map[string]string)
			}
			ns.Params["ecs"] = subnet
			ns.Params["ecs-override"] = "true"
		}
	}
	return nil
}

// Regional ISP DNS address prefixes are ECS hints, not upstream DNS endpoints.
// Sources and limitations are documented in README.md.
var ecsProfiles = map[string]map[string]string{
	"北京":  {"电信": "219.141.136.0/24", "联通": "123.123.123.0/24", "移动": "211.138.30.0/24"},
	"天津":  {"电信": "219.150.32.0/24", "联通": "202.99.104.0/24", "移动": "211.137.160.0/24"},
	"河北":  {"电信": "222.222.202.0/24", "联通": "202.99.160.0/24", "移动": "211.143.60.0/24"},
	"山西":  {"电信": "59.49.49.0/24", "联通": "202.99.192.0/24", "移动": "211.138.106.0/24"},
	"内蒙古": {"电信": "219.148.162.0/24", "联通": "202.99.224.0/24", "移动": "211.138.91.0/24"},
	"辽宁":  {"电信": "219.148.204.0/24", "联通": "202.96.69.0/24", "移动": "211.137.32.0/24"},
	"吉林":  {"电信": "219.149.194.0/24", "联通": "202.98.0.0/24", "移动": "211.141.16.0/24"},
	"黑龙江": {"电信": "219.147.198.0/24", "联通": "202.97.224.0/24", "移动": "211.137.241.0/24"},
	"上海":  {"电信": "202.96.209.0/24", "联通": "210.22.70.0/24", "移动": "211.136.112.0/24"},
	"江苏":  {"电信": "218.2.2.0/24", "联通": "221.6.4.0/24", "移动": "221.131.143.0/24"},
	"浙江":  {"电信": "202.101.172.0/24", "联通": "221.12.1.0/24", "移动": "211.140.13.0/24"},
	"安徽":  {"电信": "61.132.163.0/24", "联通": "218.104.78.0/24", "移动": "211.138.180.0/24"},
	"福建":  {"电信": "218.85.152.0/24", "联通": "218.104.128.0/24", "移动": "211.138.151.0/24"},
	"江西":  {"电信": "202.101.224.0/24", "联通": "220.248.192.0/24", "移动": "211.141.90.0/24"},
	"山东":  {"电信": "219.146.1.0/24", "联通": "202.102.128.0/24", "移动": "218.201.96.0/24"},
	"河南":  {"电信": "222.88.88.0/24", "联通": "202.102.224.0/24", "移动": "211.138.24.0/24"},
	"湖北":  {"电信": "202.103.24.0/24", "联通": "218.104.111.0/24", "移动": "211.137.58.0/24"},
	"湖南":  {"电信": "59.51.78.0/24", "联通": "58.20.127.0/24", "移动": "211.142.210.0/24"},
	"广东":  {"电信": "202.96.128.0/24", "联通": "210.21.196.0/24", "移动": "211.136.20.0/24"},
	"广西":  {"电信": "202.103.225.0/24", "联通": "221.7.128.0/24", "移动": "211.138.245.0/24"},
	"海南":  {"电信": "202.100.192.0/24", "联通": "221.11.132.0/24", "移动": "221.176.88.0/24"},
	"重庆":  {"电信": "61.128.192.0/24", "联通": "221.5.203.0/24", "移动": "218.201.4.0/24"},
	"四川":  {"电信": "61.139.2.0/24", "联通": "119.6.6.0/24", "移动": "211.137.82.0/24"},
	"贵州":  {"电信": "202.98.192.0/24", "联通": "221.13.30.0/24", "移动": "211.139.5.0/24"},
	"云南":  {"电信": "222.172.200.0/24", "联通": "221.3.131.0/24", "移动": "211.139.29.0/24"},
	"西藏":  {"电信": "202.98.224.0/24", "联通": "221.13.65.0/24", "移动": "211.139.73.0/24"},
	"陕西":  {"电信": "218.30.19.0/24", "联通": "221.11.1.0/24", "移动": "211.137.130.0/24"},
	"甘肃":  {"电信": "202.100.64.0/24", "联通": "221.7.34.0/24", "移动": "218.203.160.0/24"},
	"青海":  {"电信": "202.100.138.0/24", "联通": "221.207.58.0/24", "移动": "211.138.75.0/24"},
	"宁夏":  {"电信": "202.100.96.0/24", "联通": "221.199.12.0/24", "移动": "218.203.123.0/24"},
	"新疆":  {"电信": "61.128.114.0/24", "联通": "221.7.1.0/24", "移动": "218.202.152.0/24"},
}
