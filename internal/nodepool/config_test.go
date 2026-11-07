package nodepool

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metacubex/mihomo/component/resolver"
	D "github.com/miekg/dns"

	"nekospeed/internal/model"
	"nekospeed/internal/subscription"
)

// 使用真正的本地 DNS/SOCKS 服务验证拨号路径，无需公网或真实订阅凭据。
func TestConfigUsesSubscriptionDNSWithoutChangingInput(t *testing.T) {
	for _, proxyPolicy := range []bool{false, true} {
		t.Run(fmt.Sprintf("proxy-policy-%t", proxyPolicy), func(t *testing.T) {
			privateDNS, privateQueries := testDNS(t, "127.0.0.1")
			publicDNS, publicQueries := testDNS(t, "127.0.0.2")
			socksAddr, _ := testSOCKS(t, false)
			_, port, _ := net.SplitHostPort(socksAddr)
			policy := fmt.Sprintf("  nameserver-policy:\n    '+.entry.test': ['tcp://%s']\n", privateDNS)
			if proxyPolicy {
				policy = fmt.Sprintf("  proxy-server-nameserver: ['tcp://%s']\n  proxy-server-nameserver-policy:\n    '+.entry.test': ['tcp://%s']\n", publicDNS, privateDNS)
			}
			original := []byte(fmt.Sprintf(`# preserve comments and formatting
mixed-port: %s
hosts:
  static.test: 192.0.2.10
dns:
  enable: true
  listen: %s
  nameserver: ['tcp://%s']
  default-nameserver: [127.0.0.1]
%sproxies:
  - {name: node, type: socks5, server: node.entry.test, port: %s}
rules: ['MATCH,DIRECT']
`, port, privateDNS, publicDNS, policy, port))
			source := filepath.Join(t.TempDir(), "original.yaml")
			if err := os.WriteFile(source, original, 0600); err != nil {
				t.Fatal(err)
			}
			doc, err := subscription.LoadDocument(source)
			if err != nil {
				t.Fatal(err)
			}
			oldDNS := resolver.DefaultResolver
			session, err := OpenConfig(doc.Config, doc.BaseDir, doc.Nodes)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(session.Close)
			if len(session.Nodes) != 1 {
				t.Fatalf("unexpected nodes: %v", session.Nodes)
			}
			dialer, err := session.NewDialer(session.Nodes[0])
			if err != nil {
				t.Fatal(err)
			}
			checkSOCKSConnection(t, dialer)
			if privateQueries.Load() == 0 || publicQueries.Load() != 0 {
				t.Fatalf("wrong DNS path: private=%d public=%d", privateQueries.Load(), publicQueries.Load())
			}
			if _, exists := resolver.DefaultHosts.Search("node.entry.test", false); exists {
				t.Fatal("server was pinned into hosts instead of using subscription DNS")
			}
			ips, err := resolver.LookupIPv4(context.Background(), "static.test")
			if err != nil || len(ips) != 1 || ips[0].String() != "192.0.2.10" {
				t.Fatalf("hosts not preserved: %v %v", ips, err)
			}
			actual, err := os.ReadFile(source)
			if err != nil || !bytes.Equal(original, actual) || !bytes.Equal(original, doc.Config) {
				t.Fatal("subscription bytes changed")
			}
			session.Close()
			if resolver.DefaultResolver != oldDNS {
				t.Fatal("DNS leaked into next task")
			}
		})
	}
}

func TestProviderOnlyConfigPreservesDialerProxyGroup(t *testing.T) {
	front, frontCalls := testSOCKS(t, true)
	leaf, _ := testSOCKS(t, false)
	frontHost, frontPort, _ := net.SplitHostPort(front)
	leafHost, leafPort, _ := net.SplitHostPort(leaf)
	dir := t.TempDir()
	providerData := []byte(fmt.Sprintf(`proxies:
  - {name: front, type: socks5, server: %s, port: %s}
  - {name: leaf, type: socks5, server: %s, port: %s, dialer-proxy: FRONT}
`, frontHost, frontPort, leafHost, leafPort))
	if err := os.WriteFile(filepath.Join(dir, "nodes.yaml"), providerData, 0600); err != nil {
		t.Fatal(err)
	}
	original := []byte(`proxy-providers:
  local:
    type: file
    path: ./nodes.yaml
proxy-groups:
  - name: FRONT
    type: select
    use: [local]
    filter: '^front$'
rules: ['MATCH,DIRECT']
`)
	source := filepath.Join(dir, "original.yaml")
	if err := os.WriteFile(source, original, 0600); err != nil {
		t.Fatal(err)
	}
	doc, err := subscription.LoadDocument(source)
	if err != nil {
		t.Fatal(err)
	}
	session, err := OpenConfig(doc.Config, doc.BaseDir, doc.Nodes)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if len(session.Nodes) != 2 {
		t.Fatalf("provider nodes not loaded: %v", session.Nodes)
	}
	// 仅选择 leaf 测试，front 和 FRONT 仍保留在原始配置的依赖图中。
	dialer, err := session.NewDialer(model.Node{Name: "leaf"})
	if err != nil {
		t.Fatal(err)
	}
	checkSOCKSConnection(t, dialer)
	if frontCalls.Load() == 0 {
		t.Fatal("dialer-proxy group was bypassed")
	}
	for name, want := range map[string][]byte{"original.yaml": original, "nodes.yaml": providerData} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s was modified", name)
		}
	}
}

func checkSOCKSConnection(t *testing.T, d model.Dialer) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := d.DialContext(ctx, "tcp", "example.invalid:80")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var b [2]byte
	if _, err := io.ReadFull(conn, b[:]); err != nil || string(b[:]) != "ok" {
		t.Fatalf("wrong endpoint: %q %v", b, err)
	}
}

func testDNS(t *testing.T, ip string) (string, *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	count := &atomic.Int32{}
	server := &D.Server{Listener: ln, Handler: D.HandlerFunc(func(w D.ResponseWriter, req *D.Msg) {
		count.Add(1)
		reply := new(D.Msg)
		reply.SetReply(req)
		for _, q := range req.Question {
			if q.Qtype == D.TypeA {
				reply.Answer = append(reply.Answer, &D.A{Hdr: D.RR_Header{Name: q.Name, Rrtype: D.TypeA, Class: D.ClassINET, Ttl: 60}, A: net.ParseIP(ip)})
			}
		}
		_ = w.WriteMsg(reply)
	})}
	go func() { _ = server.ActivateAndServe() }()
	t.Cleanup(func() { _ = server.Shutdown() })
	return ln.Addr().String(), count
}

func testSOCKS(t *testing.T, relay bool) (string, *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	count := &atomic.Int32{}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
				var hello [2]byte
				if _, err := io.ReadFull(conn, hello[:]); err != nil || hello[0] != 5 {
					return
				}
				if _, err := io.CopyN(io.Discard, conn, int64(hello[1])); err != nil {
					return
				}
				if _, err := conn.Write([]byte{5, 0}); err != nil {
					return
				}
				var header [4]byte
				if _, err := io.ReadFull(conn, header[:]); err != nil {
					return
				}
				var host string
				switch header[3] {
				case 1, 4:
					n := 4
					if header[3] == 4 {
						n = 16
					}
					b := make([]byte, n)
					if _, err := io.ReadFull(conn, b); err != nil {
						return
					}
					host = net.IP(b).String()
				case 3:
					var size [1]byte
					if _, err := io.ReadFull(conn, size[:]); err != nil {
						return
					}
					b := make([]byte, int(size[0]))
					if _, err := io.ReadFull(conn, b); err != nil {
						return
					}
					host = string(b)
				default:
					return
				}
				var port [2]byte
				if _, err := io.ReadFull(conn, port[:]); err != nil {
					return
				}
				count.Add(1)
				var upstream net.Conn
				if relay {
					var err error
					upstream, err = net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(port[:])))), 3*time.Second)
					if err != nil {
						return
					}
					defer upstream.Close()
				}
				if _, err := conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
					return
				}
				if !relay {
					_, _ = io.WriteString(conn, "ok")
					return
				}
				go func() { _, _ = io.Copy(upstream, conn) }()
				_, _ = io.Copy(conn, upstream)
			}()
		}
	}()
	return ln.Addr().String(), count
}
