// NekoSpeed — 机场测速工具（CLI 一次性命令 + 内嵌 Web UI）。
// 参考 miaospeed 架构：mihomo 内核作拨号层，输出复刻 MiaoKo 风格的 PNG 结果图 + JSON。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"nekospeed/internal/nodepool"
	"nekospeed/internal/runner"
	"nekospeed/internal/webui"
)

func main() {
	var (
		source      = flag.String("sub", "", "订阅链接或本地文件路径（Clash YAML 或分享链接订阅）")
		config      = flag.String("config", "", "本地 Clash 配置文件路径（等价于 -sub 的本地文件）")
		tests       = flag.String("tests", "all", "测试项，逗号分隔：speed,media,ai,topo,nat 或 all")
		threads     = flag.Int("threads", 8, "测速线程数（1-32）")
		duration    = flag.Int("duration", 10, "测速时长秒（1-30）")
		single      = flag.Bool("single", false, "单线程测速（等价于 -threads 1）")
		filter      = flag.String("filter", "", "节点名过滤正则")
		concurrency = flag.Int("concurrency", 1, "同时测试的节点数（默认 1，避免节点间抢带宽）")
		outDir      = flag.String("o", "out", "输出目录")
		mmdb        = flag.String("mmdb", "", "MaxMind GeoIP2 mmdb 文件路径（不提供则用在线 API）")
		fontPath    = flag.String("font", "", "自定义中文字体路径（默认自动探测系统字体）")
		watermark   = flag.String("watermark", "", "图片水印文字（默认无水印）")
		name        = flag.String("name", "", "订阅名称，用于输出文件名前缀（覆盖自动提取）")
		speedURL    = flag.String("speed-url", "", "自定义测速文件 URL（默认 Google/Cloudflare 大文件）")
		pingURL     = flag.String("ping-url", "https://www.gstatic.com/generate_204", "HTTP/HTTPS 延迟测试目标（复用 RTT 与首次请求）")
		pingCount   = flag.Int("ping-count", 4, "每节点延迟测试次数")
		bandwidth   = flag.String("bandwidth", "", "本地带宽标签（如 1Gbps/1000Mbps），未指定时自动取测速峰值")
		preflight   = flag.Bool("preflight", false, "已停用，仅兼容旧命令；不覆盖订阅 DNS 或入口 IP")
		theme       = flag.String("theme", "", "渲染主题名称（如 blue_dark），为空时随机选择")
		proxy       = flag.String("proxy", "", "拉取订阅使用的代理（如 http://127.0.0.1:7890 或 socks5://127.0.0.1:1080），默认读取 HTTPS_PROXY 环境变量")
		web         = flag.String("web", "", "启动内嵌 Web UI（如 :18189 或 18189），非空即启用")
		dnsProvince = flag.String("dns-province", "", "DNS ECS 省份预设（如广东），需同时指定 -dns-isp")
		dnsISP      = flag.String("dns-isp", "", "DNS ECS 运营商：电信/联通/移动，需同时指定 -dns-province")
		dnsECS      = flag.String("dns-ecs", "", "自定义 DNS ECS IP/CIDR，优先于省份/运营商预设")
		listDNSECS  = flag.Bool("list-dns-ecs", false, "列出 DNS ECS 省份/运营商预设后退出")
	)
	flag.Parse()
	if *listDNSECS {
		fmt.Println("省份\t运营商\tECS 网段")
		for _, profile := range nodepool.ECSProfiles() {
			fmt.Printf("%s\t%s\t%s\n", profile.Province, profile.ISP, profile.Subnet)
		}
		return
	}

	if *web != "" {
		addr := *web
		if !strings.Contains(addr, ":") {
			addr = ":" + addr
		}
		fatalIf(webui.Serve(addr), "Web UI")
		return
	}

	src := *source
	if src == "" {
		src = *config
	}
	if src == "" {
		fmt.Fprintln(os.Stderr, "错误：必须提供 -sub <订阅URL|文件> 或 -config <clash.yaml>")
		flag.Usage()
		os.Exit(2)
	}

	_, err := runner.Run(context.Background(), runner.Options{
		Source:      src,
		Tests:       *tests,
		Threads:     *threads,
		Duration:    *duration,
		Concurrency: *concurrency,
		PingCount:   *pingCount,
		Single:      *single,
		Filter:      *filter,
		OutDir:      *outDir,
		MMDB:        *mmdb,
		FontPath:    *fontPath,
		Watermark:   *watermark,
		Name:        *name,
		SpeedURL:    *speedURL,
		PingURL:     *pingURL,
		Bandwidth:   *bandwidth,
		Preflight:   *preflight,
		Theme:       *theme,
		Proxy:       *proxy,
		DNSProvince: *dnsProvince,
		DNSISP:      *dnsISP,
		DNSECS:      *dnsECS,
	}, func(format string, args ...any) {
		fmt.Printf(format+"\n", args...)
	})
	fatalIf(err, "执行测速")
}

func fatalIf(err error, what string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s失败: %v\n", what, err)
		os.Exit(1)
	}
}
