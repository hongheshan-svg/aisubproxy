package handler

import (
	"fmt"
	"testing"
	"time"
)

// 审计 O3 组 auth N-1：限流表满了淘汰键时，不能把正在锁定中的键挤出去。

// 单 IP 先把 admin 猜到锁定，再用一批随机用户名各失败一次灌满表：admin 的锁定必须还在。
func TestO3FailLimiterFullTableKeepsLockedKey(t *testing.T) {
	c := oaHNewClock()
	l := oaHLimiter(c, 100)
	for i := 0; i < 5; i++ {
		l.failed("10.0.0.1|admin")
	}
	if ok, _ := l.allowed("10.0.0.1|admin"); ok {
		t.Fatal("5 次失败应锁定")
	}
	for i := 0; i < 300; i++ {
		c.add(time.Millisecond)
		l.failed(fmt.Sprintf("10.0.0.1|random-%d", i))
	}
	if n := l.size(); n > 100 {
		t.Fatalf("表应封顶 100 个键，实际 %d", n)
	}
	if ok, wait := l.allowed("10.0.0.1|admin"); ok || wait <= 0 {
		t.Fatalf("锁定中的 admin 键被随机用户名的失败挤出了表，锁定失效：ok=%v wait=%v", ok, wait)
	}
}

// 表里全是锁定中的键时，新键的失败不记录（不淘汰任何锁定键），表也不超上限。
func TestO3FailLimiterAllLockedRejectsNewKey(t *testing.T) {
	c := oaHNewClock()
	l := oaHLimiter(c, 3)
	locked := []string{"ip|a", "ip|b", "ip|c"}
	for _, k := range locked {
		for i := 0; i < 5; i++ {
			l.failed(k)
		}
	}
	for i := 0; i < 20; i++ {
		l.failed(fmt.Sprintf("ip|new-%d", i))
	}
	if n := l.size(); n != 3 {
		t.Fatalf("表应仍是 3 个锁定键，实际 %d", n)
	}
	for _, k := range locked {
		if ok, _ := l.allowed(k); ok {
			t.Fatalf("锁定键 %s 不应被挤掉", k)
		}
	}
	// 锁过期后，新键照常可以记录（过期的键会被淘汰或清理）
	c.add(16 * time.Minute)
	l.failed("ip|late")
	if n := l.size(); n > 3 {
		t.Fatalf("表不应超上限，实际 %d", n)
	}
	l.mu.Lock()
	_, recorded := l.entries["ip|late"]
	l.mu.Unlock()
	if !recorded {
		t.Fatal("锁都过期后，新键的失败应能记录")
	}
}

// 优先淘汰未锁定、最久没失败的键：锁定键排在最前面时，被淘汰的是它后面最早的未锁定键。
func TestO3FailLimiterEvictsOldestUnlocked(t *testing.T) {
	c := oaHNewClock()
	l := oaHLimiter(c, 3)
	for i := 0; i < 5; i++ {
		l.failed("ip|admin")
	}
	c.add(time.Second)
	l.failed("ip|u1")
	c.add(time.Second)
	l.failed("ip|u2")
	c.add(time.Second)
	l.failed("ip|u3") // 表满：应淘汰 u1（最早的未锁定键），不动 admin
	l.mu.Lock()
	_, hasAdmin := l.entries["ip|admin"]
	_, hasU1 := l.entries["ip|u1"]
	_, hasU2 := l.entries["ip|u2"]
	_, hasU3 := l.entries["ip|u3"]
	l.mu.Unlock()
	if !hasAdmin || hasU1 || !hasU2 || !hasU3 {
		t.Fatalf("应淘汰 u1、保留 admin/u2/u3：admin=%v u1=%v u2=%v u3=%v", hasAdmin, hasU1, hasU2, hasU3)
	}
}
