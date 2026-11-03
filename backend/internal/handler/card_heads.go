package handler

import (
	"context"
	"errors"
	"log"
	"regexp"
	"sync"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
)

// cardHeadSource 是可选能力：能从服务商读取卡头（卡产品）。没实现的服务商在后台手动维护卡头。
type cardHeadSource interface {
	CardHeads(ctx context.Context) ([]db.CardHeadProduct, error)
}

// cardHeadSourceFor 返回服务商的卡头数据源；供卡型、上游型的适配器都看一遍。
func cardHeadSourceFor(n provider.Name) (cardHeadSource, bool) {
	if a, ok := cardSupplyAdapterFor(n); ok {
		if s, ok := a.(cardHeadSource); ok {
			return s, true
		}
	}
	if a, ok := upstreamAdapterFor(n); ok {
		if s, ok := a.(cardHeadSource); ok {
			return s, true
		}
	}
	return nil, false
}

// 卡头取产品名里第一段 6 到 9 位的连续数字，两侧不能是数字。
var cardHeadInName = regexp.MustCompile(`(?:^|[^0-9])([0-9]{6,9})(?:[^0-9]|$)`)

// parseCardHead 从产品名里识别卡头，识别不出时返回空串。
func parseCardHead(name string) string {
	if m := cardHeadInName.FindStringSubmatch(name); m != nil {
		return m[1]
	}
	return ""
}

const (
	cardHeadSyncDelay   = time.Minute
	cardHeadSyncEvery   = 30 * time.Minute
	cardHeadSyncTimeout = time.Minute
)

type cardHeadSyncStatus struct {
	at  int64
	err string
}

// 每家服务商最近一次同步的时间和错误，只记在内存里。
var (
	cardHeadSyncMu     sync.Mutex
	cardHeadSyncByName = map[provider.Name]cardHeadSyncStatus{}
)

// cardHeadSyncState 返回最近一次同步的时间（Unix 秒，没同步过为 0）和错误信息（成功为空）。
func cardHeadSyncState(n provider.Name) (lastAt int64, lastErr string) {
	cardHeadSyncMu.Lock()
	defer cardHeadSyncMu.Unlock()
	s := cardHeadSyncByName[n]
	return s.at, s.err
}

func setCardHeadSyncState(n provider.Name, err error) {
	s := cardHeadSyncStatus{at: time.Now().Unix()}
	if err != nil {
		s.err = err.Error()
	}
	cardHeadSyncMu.Lock()
	cardHeadSyncByName[n] = s
	cardHeadSyncMu.Unlock()
}

var errNoCardHeads = errors.New("服务商没有返回任何卡头")

// syncCardHeads 从数据源读取卡头并写入表里。出错或返回空列表时不改表，只记下错误。
func syncCardHeads(ctx context.Context, n provider.Name, src cardHeadSource) error {
	items, err := src.CardHeads(ctx)
	if err == nil && len(items) == 0 {
		err = errNoCardHeads
	}
	if err == nil {
		err = db.SyncCardHeads(string(n), items, time.Now().Unix())
	}
	setCardHeadSyncState(n, err)
	return err
}

// syncAllCardHeads 同步所有能自动读取卡头的服务商。
func syncAllCardHeads(ctx context.Context) {
	for _, p := range provider.List() {
		src, ok := cardHeadSourceFor(p.Name())
		if !ok {
			continue
		}
		c, cancel := context.WithTimeout(ctx, cardHeadSyncTimeout)
		if err := syncCardHeads(c, p.Name(), src); err != nil && ctx.Err() == nil {
			log.Printf("同步 %s 卡头失败: %v", p.Name(), err)
		}
		cancel()
	}
}

// StartCardHeadSync 启动后台卡头同步：启动 1 分钟后一次，之后每 30 分钟一次。
func StartCardHeadSync(ctx context.Context) {
	go func() {
		t := time.NewTimer(cardHeadSyncDelay)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				syncAllCardHeads(ctx)
				t.Reset(cardHeadSyncEvery)
			}
		}
	}()
}
