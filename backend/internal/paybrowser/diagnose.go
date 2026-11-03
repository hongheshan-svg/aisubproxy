package paybrowser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/chromedp/cdproto/runtime"
)

const (
	diagTextLimit  = 200 // 诊断里正文最多保留的字数
	diagSegLimit   = 24  // 地址里超过这么长的路径段（如 cs_live_ 会话号）只留开头
	diagSegKeep    = 8
	diagTitleLimit = 100
)

// safeLocation 只留主机和路径，去掉 query 与 fragment；过长的路径段（支付会话号之类）只留开头。
func safeLocation(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "（无法解析）"
	}
	if u.Host == "" {
		return "（" + u.Scheme + "）"
	}
	segs := strings.Split(u.EscapedPath(), "/")
	for i, s := range segs {
		if len(s) > diagSegLimit {
			segs[i] = s[:diagSegKeep] + "…"
		}
	}
	return u.Host + strings.Join(segs, "/")
}

// diagSels 是诊断要统计命中数的选择器组。
func diagSels() []struct {
	label string
	sels  []string
} {
	return []struct {
		label string
		sels  []string
	}{
		{"卡号", Sel.CardNumber}, {"有效期", Sel.Expiry}, {"CVC", Sel.CVC},
		{"国家", Sel.Country}, {"姓名", Sel.Name}, {"街道地址", Sel.Line1}, {"城市", Sel.City}, {"州/省", Sel.State}, {"邮编", Sel.Zip},
		{"邮箱", Sel.Email}, {"手机", Sel.Phone}, {"Link 保存", Sel.LinkSave},
		{"支付按钮", Sel.PayButton}, {"金额", Sel.Amount}, {"成功", Sel.Success}, {"提示文字", Sel.DeclineText}, {"3DS 框", Sel.ChallengeFrame},
	}
}

// diagInfoJS 读地址、标题和正文。开始填卡后正文不出页面（touched 为 true 时不读）。
const diagInfoJS = `((touched) => ({
	href: location.href,
	title: document.title,
	text: touched || !document.body ? '' : document.body.innerText.replace(/\s+/g, ' ').trim().slice(0, 400),
}))`

// diagCountJS 返回各选择器可见的命中数，以及存在但不可见的元素总数。选择器写错时算 0。
const diagCountJS = `((sels, visible) => {
	const counts = []
	let hidden = 0
	for (const s of sels) {
		let list = []
		try { list = Array.from(document.querySelectorAll(s)) } catch (e) {}
		const seen = list.filter(visible).length
		counts.push(seen)
		hidden += list.length - seen
	}
	return { counts, hidden }
})`

// value 在 frame 的执行环境里求值，按 JSON 解到 res。
func (b *Browser) value(ctx context.Context, f frameRef, expr string, res any) error {
	r, exc, err := runtime.Evaluate(expr).WithContextID(f.ctx).WithReturnByValue(true).Do(ctx)
	if err != nil {
		b.forgetWorld(f.id)
		return err
	}
	if exc != nil {
		return fmt.Errorf("页面脚本出错: %s", exc.Text)
	}
	return json.Unmarshal(r.Value, res)
}

// Diagnose 返回定位页面问题用的几行文字：地址（无 query）、标题、正文开头（只在开始填卡之前有）、可见 frame 的主机、
// 各字段每个候选选择器的可见命中数（如「CVC 0/0/1/0/0|0/0/0/0/0」，竖线左边是主页面，右边是子 frame 合计）。
// 文字都已脱敏。任何一步读不出来都不算错：能拿到多少给多少，受 ctx 约束。
func (b *Browser) Diagnose(ctx context.Context) []string {
	b.mu.Lock()
	touched := b.cardTouched
	b.mu.Unlock()

	var lines []string
	err := b.do(ctx, func(ctx context.Context) error {
		frames, err := b.frames(ctx)
		if err != nil {
			return err
		}
		var info struct{ Href, Title, Text string }
		if err := b.value(ctx, frames[0], fmt.Sprintf("%s(%t)", diagInfoJS, touched), &info); err != nil {
			return err
		}
		lines = append(lines, "地址："+safeLocation(info.Href))
		if t := truncateRunes(maskPageText(strings.TrimSpace(info.Title)), diagTitleLimit); t != "" {
			lines = append(lines, "标题："+t)
		}
		if t := truncateRunes(maskPageText(info.Text), diagTextLimit); t != "" {
			lines = append(lines, "正文开头："+t)
		}
		lines = append(lines, "可见 frame："+b.frameHosts(ctx, frames))
		lines = append(lines, "选择器可见命中（主页面|子 frame）："+b.selectorHits(ctx, frames))
		return nil
	})
	if err != nil {
		reason := "读取失败"
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			reason = "读取超时（页面可能卡死）"
		}
		lines = append(lines, "页面定位信息"+reason)
	}
	return lines
}

func (b *Browser) frameHosts(ctx context.Context, frames []frameRef) string {
	var hosts []string
	seen := map[string]bool{}
	for _, f := range frames {
		var h string
		if b.value(ctx, f, `location.host`, &h) != nil || h == "" || seen[h] {
			continue
		}
		seen[h] = true
		hosts = append(hosts, h)
	}
	if len(hosts) == 0 {
		return "（无）"
	}
	return strings.Join(hosts, "、")
}

func (b *Browser) selectorHits(ctx context.Context, frames []frameRef) string {
	var parts []string
	for _, g := range diagSels() {
		expr := fmt.Sprintf("%s(%s, %s)", diagCountJS, mustJSON(g.sels), visibleJS)
		main := make([]int, len(g.sels))
		sub := make([]int, len(g.sels))
		hidden := 0
		for i, f := range frames {
			var r struct {
				Counts []int
				Hidden int
			}
			if b.value(ctx, f, expr, &r) != nil {
				continue
			}
			hidden += r.Hidden
			into := sub
			if i == 0 {
				into = main
			}
			for k := range into {
				if k < len(r.Counts) {
					into[k] += r.Counts[k]
				}
			}
		}
		part := fmt.Sprintf("%s %s|%s", g.label, joinInts(main), joinInts(sub))
		if hidden > 0 {
			part += fmt.Sprintf("（另有 %d 处不可见）", hidden)
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "；")
}

func joinInts(v []int) string {
	s := make([]string, len(v))
	for i, n := range v {
		s[i] = fmt.Sprint(n)
	}
	return strings.Join(s, "/")
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
