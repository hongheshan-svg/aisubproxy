package handler

import (
	"container/list"
	"time"
)

// 这几个函数只有测试在用，从生产文件挪到这里（H2-12）。

func resetLocalRate() {
	localRateMu.Lock()
	localRateHits = map[string]*localRateEntry{}
	localRateOrder = list.New()
	localRateMu.Unlock()
	resetInspectCache() // 测试里每个库的码 ID 都从 1 开始，账号核对的缓存要一起清
}

func localRateKeys() int {
	localRateMu.Lock()
	defer localRateMu.Unlock()
	return len(localRateHits)
}

func localRateHas(kind, ip string) bool {
	localRateMu.Lock()
	defer localRateMu.Unlock()
	return localRateHits[kind+"|"+rateIPKey(ip)] != nil
}

func resetWalletSyncThrottle() {
	walletSyncMu.Lock()
	walletSyncAt, walletSyncLast = time.Time{}, nil
	walletSyncMu.Unlock()
}
