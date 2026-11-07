package nodepool

import (
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/component/dialer"
	"github.com/metacubex/mihomo/component/resolver"
	"github.com/metacubex/mihomo/config"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/dns"
	_ "github.com/metacubex/mihomo/hub/executor" // config.Parse 的 temporaryUpdateGeneral 链接目标。
	"github.com/metacubex/mihomo/listener/inner"
	"github.com/metacubex/mihomo/tunnel"

	"nekospeed/internal/model"
)

// Mihomo 的解析器和代理注册表是进程全局状态，完整配置任务必须串行使用。
var configMu sync.Mutex

// ConfigSession 使用订阅的出站、DNS、hosts 与 proxy provider。
// 分流规则不加载；代理组展开为默认首选节点，仅作为前置/DNS 的节点别名。
// 直接调用内核代理，不启动订阅中的本地监听、DNS 服务端、TUN 或防火墙。
type ConfigSession struct {
	Nodes   []model.Node
	proxies map[string]C.Proxy
	close   func()
	once    sync.Once
}

func OpenConfig(data []byte, baseDir string, nodes []model.Node, ecs string) (*ConfigSession, error) {
	configMu.Lock()
	oldHome := C.Path.HomeDir()
	tempDir := ""
	if baseDir == "" {
		var err error
		tempDir, err = os.MkdirTemp("", "nekospeed-core-*")
		if err != nil {
			configMu.Unlock()
			return nil, err
		}
		baseDir = tempDir
	}
	C.SetHomeDir(baseDir)
	release := func() {
		C.SetHomeDir(oldHome)
		if tempDir != "" {
			_ = os.RemoveAll(tempDir)
		}
		configMu.Unlock()
	}

	// 只调整内存中的运行配置，不回写或重新序列化订阅文件。
	raw, err := config.UnmarshalRawConfig(data)
	if err != nil {
		release()
		return nil, fmt.Errorf("读取 Clash 配置: %w", err)
	}
	removeRouting(raw)
	cfg, err := config.ParseRawConfig(raw)
	if err != nil {
		release()
		return nil, fmt.Errorf("加载完整 Clash 配置: %w", err)
	}
	if err := applyECS(cfg.DNS, ecs); err != nil {
		release()
		return nil, err
	}
	oldHosts, oldDNS := resolver.DefaultHosts, resolver.DefaultResolver
	oldProxyDNS, oldDirectDNS := resolver.ProxyServerHostResolver, resolver.DirectHostResolver
	oldIPv6, oldSystemHosts := resolver.DisableIPv6, resolver.UseSystemHosts
	oldProxies, oldProviders := tunnel.Proxies(), tunnel.Providers()
	oldRules, oldRuleProviders := tunnel.Rules(), tunnel.RuleProviders()
	oldMode, oldStatus, oldInner := tunnel.Mode(), tunnel.Status(), inner.GetTunnel()
	oldInterface, oldMark := dialer.DefaultInterface.Load(), dialer.DefaultRoutingMark.Load()
	oldConcurrent, oldDelay := dialer.GetTcpConcurrent(), adapter.UnifiedDelay.Load()

	s := &ConfigSession{Nodes: append([]model.Node(nil), nodes...), proxies: make(map[string]C.Proxy)}
	for name, p := range cfg.Proxies {
		s.proxies[name] = p
	}
	s.close = func() {
		// 停止 provider 更新/健康检查，避免下一份订阅继承旧任务。
		for _, p := range cfg.Providers {
			if closer, ok := p.(io.Closer); ok {
				_ = closer.Close()
			}
		}
		for _, p := range cfg.RuleProviders {
			if closer, ok := p.(io.Closer); ok {
				_ = closer.Close()
			}
		}
		closed := make(map[C.Proxy]bool)
		for _, p := range s.proxies {
			if !closed[p] {
				_ = p.Close()
				closed[p] = true
			}
		}
		tunnel.UpdateProxies(oldProxies, oldProviders)
		tunnel.UpdateRules(oldRules, nil, oldRuleProviders)
		tunnel.SetMode(oldMode)
		switch oldStatus {
		case tunnel.Running:
			tunnel.OnRunning()
		case tunnel.Inner:
			tunnel.OnInnerLoading()
		default:
			tunnel.OnSuspend()
		}
		inner.New(oldInner)
		resolver.DefaultHosts, resolver.DefaultResolver = oldHosts, oldDNS
		resolver.ProxyServerHostResolver, resolver.DirectHostResolver = oldProxyDNS, oldDirectDNS
		resolver.DisableIPv6, resolver.UseSystemHosts = oldIPv6, oldSystemHosts
		dialer.DefaultInterface.Store(oldInterface)
		dialer.DefaultRoutingMark.Store(oldMark)
		dialer.SetTcpConcurrent(oldConcurrent)
		adapter.UnifiedDelay.Store(oldDelay)
		release()
	}

	tunnel.UpdateProxies(cfg.Proxies, cfg.Providers)
	tunnel.UpdateRules(nil, nil, cfg.RuleProviders)
	tunnel.SetMode(tunnel.Direct)
	inner.New(tunnel.Tunnel)
	tunnel.OnInnerLoading()
	resolver.DefaultHosts = resolver.NewHosts(cfg.Hosts)
	resolver.DisableIPv6 = !cfg.General.IPv6
	dialer.DefaultInterface.Store(cfg.General.Interface)
	dialer.DefaultRoutingMark.Store(int32(cfg.General.RoutingMark))
	dialer.SetTcpConcurrent(cfg.General.TCPConcurrent)
	adapter.UnifiedDelay.Store(cfg.General.UnifiedDelay)
	installDNS(cfg.DNS, cfg.General.IPv6)

	for _, p := range cfg.RuleProviders {
		if err := p.Initial(); err != nil {
			s.Close()
			return nil, fmt.Errorf("加载规则集 %q: %w", p.Name(), err)
		}
	}
	for _, p := range cfg.Providers {
		if err := p.Initial(); err != nil {
			s.Close()
			return nil, fmt.Errorf("加载节点列表 %q: %w", p.Name(), err)
		}
	}
	aliases, err := expandGroups(cfg.Proxies)
	if err != nil {
		s.Close()
		return nil, err
	}
	for _, p := range cfg.Proxies {
		if p.Type() == C.Selector {
			_ = p.Close()
		}
	}
	cfg.Proxies = aliases
	s.proxies = make(map[string]C.Proxy, len(aliases))
	for name, p := range aliases {
		s.proxies[name] = p
	}
	tunnel.UpdateProxies(aliases, cfg.Providers)
	seen := make(map[string]bool)
	for _, n := range s.Nodes {
		seen[n.Name] = true
	}
	var providerNames []string
	for name := range cfg.Providers {
		providerNames = append(providerNames, name)
	}
	sort.Strings(providerNames)
	for _, name := range providerNames {
		for _, p := range cfg.Providers[name].Proxies() {
			if seen[p.Name()] || p.Addr() == "" {
				continue
			}
			server, _, err := net.SplitHostPort(p.Addr())
			if err != nil {
				server = p.Addr()
			}
			s.Nodes = append(s.Nodes, model.Node{Index: len(s.Nodes) + 1, Name: p.Name(), Type: p.Type().String(), Server: server})
			s.proxies[p.Name()] = p
			seen[p.Name()] = true
		}
	}
	tunnel.OnRunning()
	return s, nil
}

func (s *ConfigSession) NewDialer(node model.Node) (model.Dialer, error) {
	p, ok := s.proxies[node.Name]
	if !ok {
		return nil, fmt.Errorf("完整配置中不存在节点 %q", node.Name)
	}
	return &proxyDialer{proxy: p}, nil
}

func (s *ConfigSession) Close() { s.once.Do(s.close) }

// 只保留 DNS 策略显式依赖的 rule provider，不下载或应用分流规则集。
func removeRouting(raw *config.RawConfig) {
	raw.Rule, raw.SubRules = nil, nil
	raw.Mode = tunnel.Direct
	raw.Sniffer = config.RawSniffer{}
	needed := make(map[string]bool)
	add := func(value string) {
		if strings.HasPrefix(strings.ToLower(value), "rule-set:") {
			for _, name := range strings.Split(value[len("rule-set:"):], ",") {
				needed[strings.TrimSpace(name)] = true
			}
		} else if fields := strings.Split(value, ","); len(fields) > 1 && strings.EqualFold(fields[0], "RULE-SET") {
			needed[strings.TrimSpace(fields[1])] = true
		}
	}
	for pair := raw.DNS.NameServerPolicy.Oldest(); pair != nil; pair = pair.Next() {
		add(pair.Key)
	}
	for pair := raw.DNS.ProxyServerNameserverPolicy.Oldest(); pair != nil; pair = pair.Next() {
		add(pair.Key)
	}
	for _, value := range raw.DNS.FakeIPFilter {
		add(value)
	}
	for name := range raw.RuleProvider {
		if !needed[name] {
			delete(raw.RuleProvider, name)
		}
	}
	// 用内核自身的排序、filter/use/default-selected 逻辑找出默认首选，
	// 但不保留自动测速、故障切换或负载均衡等组策略。
	for _, group := range raw.ProxyGroup {
		group["type"] = "select"
		group["interval"] = 0
	}
}

func expandGroups(proxies map[string]C.Proxy) (map[string]C.Proxy, error) {
	aliases := make(map[string]C.Proxy, len(proxies))
	var expand func(C.Proxy, map[string]bool) (C.Proxy, error)
	expand = func(p C.Proxy, visiting map[string]bool) (C.Proxy, error) {
		if p.Type() != C.Selector {
			return p, nil
		}
		if visiting[p.Name()] {
			return nil, fmt.Errorf("代理组 %q 的默认首选存在循环引用", p.Name())
		}
		visiting[p.Name()] = true
		next := p.Unwrap(&C.Metadata{}, false)
		if next == nil {
			return nil, fmt.Errorf("代理组 %q 没有默认首选节点", p.Name())
		}
		return expand(next, visiting)
	}
	for name, p := range proxies {
		actual, err := expand(p, make(map[string]bool))
		if err != nil {
			return nil, err
		}
		aliases[name] = actual
	}
	return aliases, nil
}

// 与 Mihomo executor 的出站 DNS 初始化一致，但不启动 dns.listen。
func installDNS(c *config.DNS, ipv6 bool) {
	resolver.DefaultResolver = nil
	resolver.ProxyServerHostResolver = nil
	resolver.DirectHostResolver = nil
	resolver.UseSystemHosts = c.UseSystemHosts
	if !c.Enable {
		return
	}
	r := dns.NewResolver(dns.Config{
		Main: c.NameServer, Fallback: c.Fallback,
		IPv6: c.IPv6 && ipv6, IPv6Timeout: c.IPv6Timeout,
		FallbackIPFilter: c.FallbackIPFilter, FallbackDomainFilter: c.FallbackDomainFilter,
		FallbackLazyQuery: c.FallbackLazyQuery, Default: c.DefaultNameserver,
		Policy: c.NameServerPolicy, ProxyServer: c.ProxyServerNameserver,
		ProxyServerPolicy: c.ProxyServerPolicy, DirectServer: c.DirectNameServer,
		DirectFollowPolicy: c.DirectFollowPolicy,
		CacheAlgorithm:     c.CacheAlgorithm, CacheMaxSize: c.CacheMaxSize,
	})
	resolver.DefaultResolver = r
	if r.ProxyResolver.Invalid() {
		resolver.ProxyServerHostResolver = r.ProxyResolver
	} else {
		resolver.ProxyServerHostResolver = r.Resolver
	}
	if r.DirectResolver.Invalid() {
		resolver.DirectHostResolver = r.DirectResolver
	} else {
		resolver.DirectHostResolver = r.Resolver
	}
}
