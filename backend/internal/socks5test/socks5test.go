// Package socks5test 提供测试用的假 SOCKS5 服务端，只给测试用。
package socks5test

import (
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
)

// Server 是测试用的最小 SOCKS5 服务端（RFC 1928 CONNECT + RFC 1929 账号密码），只转发到回环地址或 localhost。
type Server struct {
	Addr       string
	user, pass string
	AuthOK     atomic.Int32 // 账号密码正确的握手次数
	AuthFail   atomic.Int32 // 账号密码错误的握手次数
	mu         sync.Mutex
	targets    []string // 收到的 CONNECT 目标，域名原样记录（用来确认域名交给代理端解析）
}

// New 起一个要求 user/pass 的假 SOCKS5 服务；user 为空表示不要认证。测试结束自动关闭。
func New(t testing.TB, user, pass string) *Server {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Addr: ln.Addr().String(), user: user, pass: pass}
	var wg sync.WaitGroup
	var conns sync.Map
	t.Cleanup(func() {
		ln.Close()
		conns.Range(func(k, _ any) bool { k.(net.Conn).Close(); return true })
		wg.Wait()
	})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			conns.Store(c, true)
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer conns.Delete(c)
				defer c.Close()
				s.serve(c, &conns)
			}()
		}
	}()
	return s
}

// Targets 返回收到过的 CONNECT 目标。
func (s *Server) Targets() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.targets...)
}

func (s *Server) serve(c net.Conn, conns *sync.Map) {
	head := make([]byte, 2)
	if _, err := io.ReadFull(c, head); err != nil || head[0] != 5 {
		return
	}
	methods := make([]byte, head[1])
	if _, err := io.ReadFull(c, methods); err != nil {
		return
	}
	want := byte(0x00)
	if s.user != "" {
		want = 0x02
	}
	offered := false
	for _, m := range methods {
		offered = offered || m == want
	}
	if !offered {
		c.Write([]byte{5, 0xff})
		return
	}
	c.Write([]byte{5, want})
	if want == 0x02 {
		user, pass, ok := readUserPass(c)
		if !ok {
			return
		}
		if user != s.user || pass != s.pass {
			s.AuthFail.Add(1)
			c.Write([]byte{1, 1})
			return
		}
		s.AuthOK.Add(1)
		c.Write([]byte{1, 0})
	}
	req := make([]byte, 4)
	if _, err := io.ReadFull(c, req); err != nil || req[0] != 5 || req[1] != 1 {
		return
	}
	var host string
	switch req[3] {
	case 1:
		b := make([]byte, 4)
		if _, err := io.ReadFull(c, b); err != nil {
			return
		}
		host = net.IP(b).String()
	case 3:
		n := make([]byte, 1)
		if _, err := io.ReadFull(c, n); err != nil {
			return
		}
		b := make([]byte, n[0])
		if _, err := io.ReadFull(c, b); err != nil {
			return
		}
		host = string(b)
	case 4:
		b := make([]byte, 16)
		if _, err := io.ReadFull(c, b); err != nil {
			return
		}
		host = net.IP(b).String()
	default:
		return
	}
	pb := make([]byte, 2)
	if _, err := io.ReadFull(c, pb); err != nil {
		return
	}
	target := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(pb))))
	s.mu.Lock()
	s.targets = append(s.targets, target)
	s.mu.Unlock()
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		c.Write([]byte{5, 2, 0, 1, 0, 0, 0, 0, 0, 0}) // 不允许
		return
	}
	up, err := net.Dial("tcp", target)
	if err != nil {
		c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0}) // 拒绝连接
		return
	}
	conns.Store(up, true)
	defer conns.Delete(up)
	defer up.Close()
	c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
	done := make(chan struct{}, 2)
	go func() { io.Copy(up, c); done <- struct{}{} }()
	go func() { io.Copy(c, up); done <- struct{}{} }()
	<-done
	up.Close()
	c.Close()
	<-done
}

func readUserPass(c net.Conn) (string, string, bool) {
	b := make([]byte, 2)
	if _, err := io.ReadFull(c, b); err != nil || b[0] != 1 {
		return "", "", false
	}
	u := make([]byte, b[1])
	if _, err := io.ReadFull(c, u); err != nil {
		return "", "", false
	}
	if _, err := io.ReadFull(c, b[:1]); err != nil {
		return "", "", false
	}
	p := make([]byte, b[0])
	if _, err := io.ReadFull(c, p); err != nil {
		return "", "", false
	}
	return string(u), string(p), true
}
