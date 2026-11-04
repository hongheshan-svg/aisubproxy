package handler

import (
	"container/list"
	"sync"
	"time"
)

// 简单的内存级登录限流：同一 key（客户端IP+用户名）在窗口期内失败超过阈值即锁定。
const (
	loginMaxAttempts = 5
	loginLockWindow  = 15 * time.Minute
)

// authLimiterMaxKeys 是登录、安装限流表各自最多记多少个键（审计 A-4）。每个键几十字节，满了也就一两 MB。
const authLimiterMaxKeys = 10000

// failLimiterSweepEvery 是记失败时顺带清理过期键的最小间隔：清理要扫整张表，不在每次请求里做。
const failLimiterSweepEvery = time.Minute

var (
	loginLimiter = newFailLimiter(loginMaxAttempts, loginLockWindow, authLimiterMaxKeys)
	setupLimiter = newFailLimiter(setupMaxAttempts, setupLockWindow, authLimiterMaxKeys)
)

// failLimiter 是按键计失败次数的内存限流表：窗口内失败到上限就锁定一个窗口长的时间。
// 未鉴权的请求就能往表里写键（随机用户名、换 IP），所以表有上限：满了再来新键就淘汰最久没有失败的那个；
// 窗口和锁都过了的键在记失败时顺带清掉（最多每分钟扫一次）。不另起清理协程。
type failLimiter struct {
	maxFailures int
	window      time.Duration // 计数窗口，也是锁定时长
	maxKeys     int
	now         func() time.Time // 测试可替换

	mu        sync.Mutex
	entries   map[string]*failEntry
	order     *list.List // 按最后一次失败从早到晚
	lastSweep time.Time
}

type failEntry struct {
	key         string
	failures    int
	windowStart time.Time
	lockedUntil time.Time
	elem        *list.Element
}

func newFailLimiter(maxFailures int, window time.Duration, maxKeys int) *failLimiter {
	return &failLimiter{
		maxFailures: maxFailures,
		window:      window,
		maxKeys:     maxKeys,
		now:         time.Now,
		entries:     map[string]*failEntry{},
		order:       list.New(),
	}
}

// allowed 返回是否允许尝试，以及若被锁定还需等待多久。
func (l *failLimiter) allowed(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[key]
	if e == nil {
		return true, 0
	}
	now := l.now()
	if now.Before(e.lockedUntil) {
		return false, e.lockedUntil.Sub(now)
	}
	// 锁已过期：重置
	if !e.lockedUntil.IsZero() {
		l.remove(e)
	}
	return true, 0
}

// failed 记一次失败，达到阈值则锁定。
func (l *failLimiter) failed(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if now.Sub(l.lastSweep) >= failLimiterSweepEvery {
		l.sweepLocked(now)
	}
	e := l.entries[key]
	if e != nil && now.Sub(e.windowStart) > l.window {
		l.remove(e) // 窗口已过：从头计
		e = nil
	}
	if e == nil {
		if len(l.entries) >= l.maxKeys {
			if oldest := l.order.Front(); oldest != nil {
				l.remove(oldest.Value.(*failEntry))
			}
		}
		e = &failEntry{key: key, windowStart: now}
		e.elem = l.order.PushBack(e)
		l.entries[key] = e
	} else {
		l.order.MoveToBack(e.elem)
	}
	e.failures++
	if e.failures >= l.maxFailures {
		e.lockedUntil = now.Add(l.window)
	}
}

// succeeded 成功后清除该 key 的失败计数。
func (l *failLimiter) succeeded(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e := l.entries[key]; e != nil {
		l.remove(e)
	}
}

// sweep 清掉窗口和锁都已过期的键，返回清了几个。
func (l *failLimiter) sweep() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sweepLocked(l.now())
}

func (l *failLimiter) sweepLocked(now time.Time) int {
	l.lastSweep = now
	n := 0
	for el := l.order.Front(); el != nil; {
		next := el.Next()
		if e := el.Value.(*failEntry); !now.Before(e.lockedUntil) && now.Sub(e.windowStart) > l.window {
			l.remove(e)
			n++
		}
		el = next
	}
	return n
}

func (l *failLimiter) remove(e *failEntry) {
	l.order.Remove(e.elem)
	delete(l.entries, e.key)
}

func (l *failLimiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

func (l *failLimiter) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = map[string]*failEntry{}
	l.order = list.New()
	l.lastSweep = time.Time{}
}

// loginAllowed 返回是否允许尝试登录，以及若被锁定还需等待多久。
func loginAllowed(key string) (bool, time.Duration) { return loginLimiter.allowed(key) }

// loginFailed 记一次失败，达到阈值则锁定。
func loginFailed(key string) { loginLimiter.failed(key) }

// loginSucceeded 登录成功后清除该 key 的失败计数。
func loginSucceeded(key string) { loginLimiter.succeeded(key) }
