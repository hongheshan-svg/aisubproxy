package handler

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/payengine"
)

// MarsWallex 码预检时核对买家账号：用付款时同一套规则（payengine.InspectAccount），只读，不建单。
// 结论只用于提前告诉买家，付款时引擎照旧再查一次。

const inspectSessionInvalidText = "登录信息已失效，请回到 ChatGPT 刷新那个页面，再重新复制"

const inspectBlockingDefault = "这个账号暂时不能用这张码，请换一个账号。"

// inspectCacheTTL 是同一张码加同一个账号的结论缓存时间。
const inspectCacheTTL = 5 * time.Minute

// inspectCacheMax 是缓存最多放多少条；测试可调小。
var inspectCacheMax = 5000

// inspectNow 是缓存用的时钟，测试可替换。
var inspectNow = time.Now

type inspectCacheEntry struct {
	key     string
	verdict payengine.AccountVerdict
	exp     time.Time
	elem    *list.Element
}

var (
	inspectCacheMu    sync.Mutex
	inspectCacheItems = map[string]*inspectCacheEntry{}
	// inspectCacheOrder 按写入时间从早到晚排。TTL 固定，所以也是按过期时间排
	inspectCacheOrder = list.New()
)

func resetInspectCache() {
	inspectCacheMu.Lock()
	inspectCacheItems = map[string]*inspectCacheEntry{}
	inspectCacheOrder = list.New()
	inspectCacheMu.Unlock()
}

// inspectCacheKey 不含会话原文：只有码的数据库 ID 和小写邮箱的 SHA-256。
func inspectCacheKey(cdkID int64, email string) string {
	sum := sha256.Sum256([]byte(strconv.FormatInt(cdkID, 10) + "|" + strings.ToLower(strings.TrimSpace(email))))
	return hex.EncodeToString(sum[:])
}

func inspectCacheGet(key string) (payengine.AccountVerdict, bool) {
	inspectCacheMu.Lock()
	defer inspectCacheMu.Unlock()
	e := inspectCacheItems[key]
	if e == nil {
		return payengine.AccountVerdict{}, false
	}
	if !inspectNow().Before(e.exp) {
		inspectCacheOrder.Remove(e.elem)
		delete(inspectCacheItems, key)
		return payengine.AccountVerdict{}, false
	}
	return e.verdict, true
}

// inspectCachePut 满了先清过期的，还满就淘汰最早的。
func inspectCachePut(key string, v payengine.AccountVerdict) {
	now := inspectNow()
	inspectCacheMu.Lock()
	defer inspectCacheMu.Unlock()
	if old := inspectCacheItems[key]; old != nil {
		inspectCacheOrder.Remove(old.elem)
		delete(inspectCacheItems, key)
	}
	if len(inspectCacheItems) >= inspectCacheMax {
		for front := inspectCacheOrder.Front(); front != nil; front = inspectCacheOrder.Front() {
			e := front.Value.(*inspectCacheEntry)
			if now.Before(e.exp) {
				break
			}
			inspectCacheOrder.Remove(front)
			delete(inspectCacheItems, e.key)
		}
	}
	for len(inspectCacheItems) >= inspectCacheMax && inspectCacheOrder.Len() > 0 {
		front := inspectCacheOrder.Front()
		inspectCacheOrder.Remove(front)
		delete(inspectCacheItems, front.Value.(*inspectCacheEntry).key)
	}
	e := &inspectCacheEntry{key: key, verdict: v, exp: now.Add(inspectCacheTTL)}
	e.elem = inspectCacheOrder.PushBack(e)
	inspectCacheItems[key] = e
}

// inspectLocalAccount 核对买家账号。先看缓存（命中不计数），再过每张码每小时的次数上限，最后才调引擎。
// 只缓存 eligible 和 ineligible。除调用方 ctx 取消外不返回错误：查不了都体现在 unknown 结论里。
func inspectLocalAccount(ctx context.Context, row db.LocalCDK, raw, email string) (payengine.AccountVerdict, error) {
	key := inspectCacheKey(row.ID, email)
	v, hit := inspectCacheGet(key)
	if !hit {
		engine := payEngine
		switch {
		case engine == nil:
			v = payengine.AccountVerdict{Kind: payengine.VerdictUnknown, Cause: "engine"}
		case !localRateAllow(localRateInspect, "cdk:"+strconv.FormatInt(row.ID, 10)):
			v = payengine.AccountVerdict{Kind: payengine.VerdictUnknown, Cause: "limit"}
		default:
			var err error
			v, err = engine.InspectAccount(ctx, payengine.InspectRequest{PlanKey: row.PlanKey, Region: row.Region, Session: raw})
			if err != nil {
				return payengine.AccountVerdict{}, err
			}
			if v.Kind == payengine.VerdictEligible || v.Kind == payengine.VerdictIneligible {
				inspectCachePut(key, v)
			}
		}
	}
	// 只记码 ID、脱敏邮箱、当前套餐、结论、放弃码、查不了的类别
	log.Printf("[local-cdk] 码 %d 账号核对: 账号=%s 当前套餐=%s 结论=%s 放弃码=%s 类别=%s 缓存=%t",
		row.ID, maskEmailForAdmin(email), v.CurrentPlan, v.Kind, v.BlockingCode, v.Cause, hit)
	return v, nil
}

// inspectBlockingReason 是 blocking_code 对应的买家中文文案（规格 §6.4）。表里没有的放弃码用通用一句。
func inspectBlockingReason(v payengine.AccountVerdict) string {
	switch v.BlockingCode {
	case "already_subscribed":
		return fmt.Sprintf("这个账号已经有订阅（%s）。这张码只能用在没有订阅的账号上，请换一个账号。", v.CurrentPlan)
	case "credit_no_subscription":
		return "这个账号没有订阅。点数只能加到已订阅 Plus 或 Pro 的账号上，请换一个账号。"
	case "credit_plan_ineligible":
		return fmt.Sprintf("这个账号的套餐是 %s，点数只能加到 Plus 或 Pro 账号上，请换一个账号。", v.CurrentPlan)
	case "credit_not_personal":
		return "这个账号不是个人账号，点数只能加到个人账号上，请换一个账号。"
	case "credit_delinquent":
		return "这个账号有欠费。请先在 ChatGPT 里处理欠费，或换一个账号。"
	case "credit_currency_mismatch":
		return fmt.Sprintf("这个账号按 %s 付款，这张码是 %s 地区的，两者要一致。请换一个账号，或联系卖家换码。", v.AccountCurrency, v.CodeCurrency)
	case "credit_account_unknown", "credit_delinquency_unknown":
		return "暂时没法确认这个账号能不能加点数。请稍后再试，或换一个账号。"
	}
	return inspectBlockingDefault
}
