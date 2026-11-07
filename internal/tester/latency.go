package tester

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"time"

	"nekospeed/internal/model"
)

const defaultPingURL = "https://www.gstatic.com/generate_204"
const pingTimeout = 10 * time.Second

// Latency 分别测同一目标的 HTTP 复用 RTT 与 HTTPS 首次请求延迟。
// 两项独立记录成功样本和失败率，避免一项失败吞掉另一项的结果。
func Latency(ctx context.Context, d model.Dialer, pingURL string, count int) *model.LatencyResult {
	if count <= 0 {
		count = 1
	}
	res := &model.LatencyResult{HTTPRTT: make([]int, 0, count), HTTPSPing: make([]int, 0, count)}
	httpURL, httpsURL, err := latencyURLs(pingURL)
	if err != nil {
		res.HTTPPacketLoss, res.PacketLoss = 1, 1
		return res
	}
	httpFailed, httpsFailed := 0, 0
	for i := 0; i < count; i++ {
		if rtt, err := httpRTTOnce(ctx, d, httpURL); err != nil {
			httpFailed++
		} else {
			res.HTTPRTT = append(res.HTTPRTT, rtt)
		}
		if delay, code, err := httpsPingOnce(ctx, d, httpsURL); err != nil {
			httpsFailed++
		} else {
			res.HTTPSPing = append(res.HTTPSPing, delay)
			res.HTTPCode = code
		}
	}
	res.HTTPPacketLoss = float64(httpFailed) / float64(count)
	res.PacketLoss = float64(httpsFailed) / float64(count)
	res.AvgHTTPRTT = avgInts(res.HTTPRTT)
	res.AvgHTTPS = avgInts(res.HTTPSPing)
	return res
}

// latencyURLs 保留路径、查询参数和自定义端口；标准端口分别采用80和443。
func latencyURLs(raw string) (string, string, error) {
	if raw == "" {
		raw = defaultPingURL
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", "", fmt.Errorf("ping URL must use HTTP or HTTPS")
	}
	if u.Port() == "80" || u.Port() == "443" {
		host := u.Hostname()
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		u.Host = host
	}
	u.Scheme = "http"
	httpURL := u.String()
	u.Scheme = "https"
	return httpURL, u.String(), nil
}

// httpRTTOnce 用第一次 HEAD 预热，再计第二次 HEAD 到首字节的耗时。
// 必须确认第二次请求复用了连接，否则不把建连耗时冒充复用 RTT。
func httpRTTOnce(ctx context.Context, d model.Dialer, pingURL string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	transport := &http.Transport{DialContext: d.DialContext, Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: noRedirect}
	warm, err := http.NewRequestWithContext(ctx, http.MethodHead, pingURL, nil)
	if err != nil {
		return 0, err
	}
	warm.Header.Set("User-Agent", "nekospeed")
	resp, err := client.Do(warm)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	var firstByte time.Time
	reused := false
	trace := &httptrace.ClientTrace{
		GotConn:              func(info httptrace.GotConnInfo) { reused = info.Reused },
		GotFirstResponseByte: func() { firstByte = time.Now() },
	}
	req := warm.Clone(httptrace.WithClientTrace(ctx, trace))
	start := time.Now()
	resp, err = client.Do(req)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	if !reused || firstByte.IsZero() {
		return 0, fmt.Errorf("HTTP probe did not reuse the warm connection")
	}
	return durationMillis(firstByte.Sub(start)), nil
}

// httpsPingOnce 每次重新建连，验证证书并要求TLS1.3，计请求开始到首字节。
func httpsPingOnce(ctx context.Context, d model.Dialer, pingURL string) (int, int, error) {
	ctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	transport := &http.Transport{
		DialContext: d.DialContext, Proxy: nil, DisableKeepAlives: true,
		TLSHandshakeTimeout: pingTimeout,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS13},
	}
	defer transport.CloseIdleConnections()
	var firstByte time.Time
	trace := &httptrace.ClientTrace{GotFirstResponseByte: func() { firstByte = time.Now() }}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), http.MethodGet, pingURL, nil)
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("User-Agent", "nekospeed")
	client := &http.Client{Transport: transport, CheckRedirect: noRedirect}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if firstByte.IsZero() {
		return 0, 0, fmt.Errorf("HTTPS probe received no response byte")
	}
	return durationMillis(firstByte.Sub(start)), resp.StatusCode, nil
}

func noRedirect(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }

func durationMillis(d time.Duration) int {
	if d < time.Millisecond {
		return 1
	} // 成功的亚毫秒响应不能与失败的0混淆。
	return int(d.Milliseconds())
}

func avgInts(xs []int) int {
	if len(xs) == 0 {
		return 0
	}
	sum := 0
	for _, x := range xs {
		sum += x
	}
	return sum / len(xs)
}
