package payengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// 付款熔断：连续 3 单提交后失败，或连续 3 单填表失败，就暂停派发新单，直到管理员恢复。
//
// 状态全部从 pay_orders 派生，不另存计数（另存就要和单据双写，迟早对不上）。唯一存下来的是恢复点：
// 管理员点恢复的时刻，计数从这里重新开始。
const (
	breakerThreshold    = 3
	settingBreakerReset = "pay_breaker_reset"
)

// 暂停原因。
const (
	BreakerReasonSubmit = "submit_failures" // 连续提交后失败
	BreakerReasonFill   = "fill_failures"   // 连续填表失败
	BreakerReasonCheck  = "check_failed"    // 状态查不出来，按暂停处理
)

// ErrBreakerNotPaused 没有暂停时不需要恢复。
var ErrBreakerNotPaused = errors.New("付款没有暂停，无需恢复")

// fillFailureCodes 是算作填表失败的错误码：页面变了或卡数据有问题，继续跑只会接着失败。
var fillFailureCodes = map[string]bool{
	"fill_failed":           true,
	"card_sensitive_failed": true,
	"card_expiry_invalid":   true,
	"submit_not_clicked":    true,
	"amount_changed":        true,
	// 点数单填表后确认不了「保存付款信息」没勾选：页面变了，继续跑每单都会注资后放弃
	"save_payment_unconfirmed": true,
}

// BreakerStatus 是熔断的当前状态。
type BreakerStatus struct {
	Paused                 bool     `json:"paused"`
	Reason                 string   `json:"reason"`
	ReasonText             string   `json:"reason_text"`
	Threshold              int      `json:"threshold"`
	SubmittedFailureStreak int      `json:"submitted_failure_streak"` // 恢复点之后当前的连续提交后失败数
	FillFailureStreak      int      `json:"fill_failure_streak"`      // 恢复点之后当前的连续填表失败数
	TrippedAt              *int64   `json:"tripped_at"`               // 触发暂停的那一单结束的时刻
	OrderNos               []string `json:"order_nos"`                // 触发暂停的连续失败单号
	LastResumeAt           *int64   `json:"last_resume_at"`
	LastResumeBy           string   `json:"last_resume_by"`
}

// breakerReset 是恢复点。finished_at 只精确到秒：恢复那一秒里已经结束的单记在 Skip 里排除，
// 恢复之后同一秒内才结束的单照常计数。
type breakerReset struct {
	At   int64    `json:"at"`
	Skip []string `json:"skip,omitempty"`
	By   string   `json:"by"`
	Note string   `json:"note"`
}

func loadBreakerReset() (breakerReset, error) {
	var r breakerReset
	raw, err := db.GetSetting(settingBreakerReset)
	if err != nil || raw == "" {
		return r, err
	}
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return r, fmt.Errorf("熔断恢复点已损坏: %w", err)
	}
	return r, nil
}

// breakerOrder 是计算熔断用到的单据字段。
type breakerOrder struct {
	no, state, failure, code, resolvedBy string
	submitted                            bool
	finishedAt                           int64
}

// submittedFailed 这张已提交的单是不是提交后失败。人工定论过的（resolved_by 非空）说明当时已经转了人工，
// 后来定成成功也算：否则管理员处理完待人工单，暂停就自己解除了，没人真正决定恢复。
func (o breakerOrder) submittedFailed() bool {
	return o.state == StateNeedsReview || o.resolvedBy != "" || (o.state == StateFailed && o.failure == FailureDeclined)
}

// computeBreaker 按结束时间顺序扫恢复点之后的真实付款单（试运行不算）。
// 暂停是粘性的：任一时刻连续失败达到阈值就算触发，之后在途单成功也不解除，只有管理员恢复才解除。
func computeBreaker(ctx context.Context) (BreakerStatus, error) {
	st := BreakerStatus{Threshold: breakerThreshold, OrderNos: []string{}}
	reset, err := loadBreakerReset()
	if err != nil {
		return st, err
	}
	if reset.At > 0 {
		at := reset.At
		st.LastResumeAt, st.LastResumeBy = &at, reset.By
	}
	skip := make(map[string]bool, len(reset.Skip))
	for _, no := range reset.Skip {
		skip[no] = true
	}
	rows, err := db.DB.QueryContext(ctx, `SELECT order_no, state, failure, error_code, resolved_by, submitted_at IS NOT NULL, finished_at
		FROM pay_orders WHERE dry_run = 0 AND finished_at IS NOT NULL AND finished_at >= ? ORDER BY finished_at, id`, reset.At)
	if err != nil {
		return st, err
	}
	defer rows.Close()

	var submitRun, fillRun []string
	trip := func(reason string, run []string, at int64) {
		if st.Paused {
			return
		}
		tripped := at
		st.Paused, st.Reason, st.TrippedAt = true, reason, &tripped
		st.OrderNos = append([]string{}, run...)
	}
	for rows.Next() {
		var o breakerOrder
		if err := rows.Scan(&o.no, &o.state, &o.failure, &o.code, &o.resolvedBy, &o.submitted, &o.finishedAt); err != nil {
			return st, err
		}
		if skip[o.no] {
			continue
		}
		switch {
		case o.submitted:
			// 走到了提交，说明填表是好的
			fillRun = nil
			if o.submittedFailed() {
				submitRun = append(submitRun, o.no)
			} else if o.state == StateSucceeded {
				submitRun = nil
			}
		case o.state == StateFailed && o.failure == FailureNotSubmitted && fillFailureCodes[o.code]:
			fillRun = append(fillRun, o.no)
		}
		// 没提交也不是填表失败的单（会话失效、账号已订阅、注资失败等）和熔断无关，两条都不动
		if len(submitRun) >= breakerThreshold {
			trip(BreakerReasonSubmit, submitRun, o.finishedAt)
		}
		if len(fillRun) >= breakerThreshold {
			trip(BreakerReasonFill, fillRun, o.finishedAt)
		}
	}
	if err := rows.Err(); err != nil {
		return st, err
	}
	st.SubmittedFailureStreak, st.FillFailureStreak = len(submitRun), len(fillRun)
	st.ReasonText = breakerReasonText(st.Reason)
	return st, nil
}

func breakerReasonText(reason string) string {
	switch reason {
	case BreakerReasonSubmit:
		return fmt.Sprintf("连续 %d 单提交后失败（被拒或转人工）", breakerThreshold)
	case BreakerReasonFill:
		return fmt.Sprintf("连续 %d 单填表失败", breakerThreshold)
	case BreakerReasonCheck:
		return "熔断状态查询失败，按暂停处理"
	}
	return ""
}

// BreakerStatus 返回熔断当前状态。查不出来时返回错误，不告诉调用方「没暂停」。
func (s *Service) BreakerStatus(ctx context.Context) (BreakerStatus, error) {
	return computeBreaker(ctx)
}

// ResumeBreaker 管理员恢复派发：写入恢复点，计数从这一刻重新开始。没有暂停时返回 ErrBreakerNotPaused。
// 恢复点损坏时也允许恢复（写一个新的），否则暂停解不开。
func (s *Service) ResumeBreaker(ctx context.Context, by, note string) (BreakerStatus, error) {
	cur, err := computeBreaker(ctx)
	if err == nil && !cur.Paused {
		return cur, ErrBreakerNotPaused
	}
	if ctx.Err() != nil {
		return cur, ctx.Err()
	}
	// 与 finished_at 同一个时钟（store.move 用 time.Now，不用 opts.Now）
	at := time.Now().UTC().Unix()
	rows, err := db.DB.QueryContext(ctx, `SELECT order_no FROM pay_orders WHERE dry_run = 0 AND finished_at = ?`, at)
	if err != nil {
		return cur, err
	}
	defer rows.Close()
	r := breakerReset{At: at, By: by, Note: note}
	for rows.Next() {
		var no string
		if err := rows.Scan(&no); err != nil {
			return cur, err
		}
		r.Skip = append(r.Skip, no)
	}
	if err := rows.Err(); err != nil {
		return cur, err
	}
	rows.Close()
	b, err := json.Marshal(r)
	if err != nil {
		return cur, err
	}
	if err := db.SetSetting(settingBreakerReset, string(b)); err != nil {
		return cur, err
	}
	s.mu.Lock()
	s.breakerAlerted = ""
	s.mu.Unlock()
	s.Kick()
	return computeBreaker(ctx)
}

// breakerPaused 判断要不要暂停派发，暂停时告警（同一次暂停同一北京日一条）。查询出错按暂停处理：
// 这时数据库多半也有问题，不能在不知道状态时继续派发。服务停止时不告警。
func (s *Service) breakerPaused(ctx context.Context) bool {
	st, err := computeBreaker(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return true
		}
		log.Printf("[payengine] 熔断状态查询失败，暂停派发: %v", err)
		s.breakerAlert(BreakerReasonCheck, 0, fmt.Sprintf("付款熔断状态查询失败，已暂停派发新单：%v", err))
		return true
	}
	if !st.Paused {
		return false
	}
	var resumeAt int64
	if st.LastResumeAt != nil {
		resumeAt = *st.LastResumeAt
	}
	s.breakerAlert(st.Reason, resumeAt, fmt.Sprintf("付款已暂停：%s，单号 %s。在途单照常确认，新单不再派发。排查原因后请到后台恢复。",
		st.ReasonText, strings.Join(st.OrderNos, "、")))
	return true
}

// breakerAlert 按「恢复点 + 原因 + 北京日」去重；去重记录只在内存里，重启后多发一条可以接受。
func (s *Service) breakerAlert(reason string, resumeAt int64, text string) {
	day := time.Now().In(time.FixedZone("UTC+8", 8*3600)).Format("2006-01-02")
	key := fmt.Sprintf("%d|%s|%s", resumeAt, reason, day)
	s.mu.Lock()
	dup := s.breakerAlerted == key
	s.breakerAlerted = key
	s.mu.Unlock()
	if !dup {
		s.opts.Alert(text)
	}
}
