package chatgpt

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestParseTrace(t *testing.T) {
	got := ParseTrace("fl=1\nip=1.2.3.4\r\ncolo=MNL\nloc=ph\nbad line\n")
	if got.IP != "1.2.3.4" || got.Loc != "PH" || got.Colo != "MNL" {
		t.Fatalf("%+v", got)
	}
}

func TestTraceOKAndNoAuthHeaders(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cdn-cgi/trace" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			w.WriteHeader(500)
			return
		}
		w.Write([]byte("ip=9.9.9.9\nloc=PH\ncolo=MNL\n"))
	}))
	info, err := c.Trace(context.Background())
	if err != nil || info.IP != "9.9.9.9" || info.Loc != "PH" {
		t.Fatalf("%+v %v", info, err)
	}
}

func TestTraceBlockedAndInvalid(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("cf-mitigated", "challenge")
		w.WriteHeader(403)
	}))
	if _, err := c.Trace(context.Background()); !errors.Is(err, ErrBlocked) {
		t.Errorf("应为被拦截: %v", err)
	}
	c = newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<html></html>")) }))
	if _, err := c.Trace(context.Background()); err == nil {
		t.Error("非 trace 内容应报错")
	}
}

func TestTraceTimeoutAndConnect(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(600 * time.Millisecond) }))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := c.Trace(ctx); !errors.Is(err, ErrTraceTimeout) {
		t.Errorf("应超时: %v", err)
	}
	c, _ = New("http://127.0.0.1:1")
	if _, err := c.Trace(context.Background()); err == nil || errors.Is(err, ErrBlocked) {
		t.Errorf("连不上应报错: %v", err)
	}
}
