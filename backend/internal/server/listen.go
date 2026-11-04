package server

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/payengine"
)

const (
	// 读请求头、读整个请求、空闲连接的时限：慢连接不能一直占着。不设写超时：在线更新状态、截图、导出等响应不能被截断。
	httpReadHeaderTimeout = 10 * time.Second
	httpReadTimeout       = 60 * time.Second
	httpIdleTimeout       = 120 * time.Second
	// httpShutdownTimeout 是排空付款单之后，等正在处理的 HTTP 请求结束的时限。
	httpShutdownTimeout = 10 * time.Second
)

// drainer 是停止服务时排空在途付款单的能力，*payengine.Service 直接满足。
type drainer interface {
	Drain(max, every time.Duration) (left int, resume func())
}

// newHTTPServer 构造带读超时的 HTTP 服务（G1）。
func newHTTPServer(h http.Handler) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: httpReadHeaderTimeout,
		ReadTimeout:       httpReadTimeout,
		IdleTimeout:       httpIdleTimeout,
	}
}

// Serve 在已经打开的监听上提供服务。先监听、再 Serve，调用方能在端口确实开始监听后做事
// （例如容器内在线升级的「完成启动」确认）。Shutdown 关闭后返回 nil。
func (s *Server) Serve(ln net.Listener) error {
	hs := newHTTPServer(s.engine.Handler())
	s.mu.Lock()
	s.http = hs
	s.mu.Unlock()
	if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown 停止服务：先停止派发新付款单，等在途单走到确认或终态（最多 payengine.DrainMax，每 30 秒记一行日志写明还剩几张），
// 期间 HTTP 照常服务；然后关闭 HTTP 监听，等正在处理的请求结束（最多 10 秒）。到点没走完的付款单不打断，重启后由恢复逻辑接管。
func (s *Server) Shutdown(ctx context.Context) error {
	if s.pay != nil {
		if left, _ := s.pay.Drain(payengine.DrainMax, payengine.DrainLogEvery); left > 0 {
			log.Printf("[server] 还有 %d 张在途付款单没处理完，照旧停止服务，重启后由恢复逻辑接管", left)
		}
	}
	s.mu.Lock()
	hs := s.http
	s.mu.Unlock()
	if hs == nil {
		return nil
	}
	sctx, cancel := context.WithTimeout(ctx, httpShutdownTimeout)
	defer cancel()
	return hs.Shutdown(sctx)
}
