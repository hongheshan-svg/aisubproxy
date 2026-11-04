package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// G1：HTTP 服务带读超时，防止慢连接一直占着；不设写超时（SSE、长下载、在线更新状态轮询不能被截断）。
func TestHTTPServerTimeouts(t *testing.T) {
	hs := newHTTPServer(gin.New())
	if hs.ReadHeaderTimeout != 10*time.Second || hs.ReadTimeout != 60*time.Second || hs.IdleTimeout != 120*time.Second {
		t.Fatalf("超时不符: header=%v read=%v idle=%v", hs.ReadHeaderTimeout, hs.ReadTimeout, hs.IdleTimeout)
	}
	if hs.WriteTimeout != 0 {
		t.Fatalf("不应设写超时: %v", hs.WriteTimeout)
	}
}

// fakeDrainer 模拟付款引擎的排空：Drain 阻塞到 release 被关闭。
type fakeDrainer struct {
	called  chan struct{}
	release chan struct{}
}

func (f *fakeDrainer) Drain(time.Duration, time.Duration) (int, func()) {
	close(f.called)
	<-f.release
	return 0, func() {}
}

// S1：停止服务时先排空在途付款单，期间 HTTP 照常服务（用户还能查单）；排空结束后才关闭监听。
func TestShutdownDrainsPaymentsBeforeClosingHTTP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/ping", func(c *gin.Context) { c.String(http.StatusOK, "pong") })
	d := &fakeDrainer{called: make(chan struct{}), release: make(chan struct{})}
	s := &Server{engine: engine, pay: d}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- s.Serve(ln) }()
	url := "http://" + ln.Addr().String() + "/ping"
	waitPing(t, url)

	stopped := make(chan error, 1)
	go func() { stopped <- s.Shutdown(context.Background()) }()
	select {
	case <-d.called:
	case <-time.After(5 * time.Second):
		t.Fatal("停止服务应先排空付款单")
	}
	select {
	case err := <-stopped:
		t.Fatalf("排空没结束不应返回: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	waitPing(t, url) // 排空期间 HTTP 照常服务

	close(d.release)
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("停止服务出错: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("排空结束后应关闭 HTTP 并返回")
	}
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("正常关闭时 Serve 应返回 nil: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve 应返回")
	}
	if _, err := http.Get(url); err == nil {
		t.Fatal("关闭后不应再接受请求")
	}
}

func waitPing(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get(url)
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK && string(b) == "pong" {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("HTTP 没有响应: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
