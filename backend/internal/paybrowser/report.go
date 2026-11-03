package paybrowser

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	maskEmailRE  = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9\-]+(?:\.[A-Za-z0-9\-]+)*`)
	maskDigitsRE = regexp.MustCompile(`\d(?:[ \t\-]*\d){6,}`)
)

// maskPageText 抹掉页面文字里的邮箱和长数字串（卡号、手机号之类），写进时间线之前都要过一遍。
func maskPageText(s string) string {
	s = maskEmailRE.ReplaceAllString(s, "***@***")
	return maskDigitsRE.ReplaceAllString(s, "***")
}

// FieldCheck 是一个字段的填写结果。绝不含值。
type FieldCheck struct {
	Label    string // 中文字段名
	Selector string // 命中的选择器
	InFrame  bool   // 在 iframe 里
	Status   string // 已填写、核对一致、核对不一致、填完后不见了
}

// FillReport 是一次 Fill 的结果，用来在真实付款前核实页面结构。不含卡号、邮箱等任何值。
type FillReport struct {
	Fields        []FieldCheck
	ManualAddress string   // 怎么展开折叠的地址字段：未点击、手动地址入口已点击几次；内嵌页上是已选或已点（未展开）地址联想，及点的是第几个 iframe 里的选项
	LinkVerify    string   // Link 内嵌验证框（「Confirm it’s you」）的处理结果，没出现时为空
	LinkSave      string   // Link 保存勾选框的处理结果
	Email         string   // 邮箱框的处理结果
	Phone         string   // 手机框的检查结果
	SavePayment   string   // 「保存付款信息」勾选框的处理结果（只有点数单检查）
	Terms         string   // 「同意条款」勾选框的处理结果
	Absent        []string // 内嵌页上 DOM 里没有的地址字段的中文名，按填写顺序；托管页的地址字段必填，这里总是空
}

// Lines 把结果写成时间线上的中文短句，失败时也能看到走到了哪一步。
func (r FillReport) Lines() []string {
	out := make([]string, 0, len(r.Fields)+4)
	for _, f := range r.Fields {
		where := "主页面"
		if f.InFrame {
			where = "iframe"
		}
		out = append(out, fmt.Sprintf("字段「%s」：命中 %s（%s），%s", f.Label, f.Selector, where, f.Status))
	}
	if len(r.Absent) > 0 {
		out = append(out, "页面没有的字段："+strings.Join(r.Absent, "、"))
	}
	for _, x := range []struct{ name, val string }{
		{"手动地址", r.ManualAddress}, {"Link 验证", r.LinkVerify}, {"Link 保存", r.LinkSave}, {"邮箱", r.Email}, {"手机", r.Phone}, {"保存付款信息", r.SavePayment}, {"同意条款", r.Terms},
	} {
		if x.val != "" {
			out = append(out, x.name+"："+x.val)
		}
	}
	return out
}

// FillReport 返回最近一次 Fill 的结果（Fill 失败时是失败前的部分）。
func (b *Browser) FillReport() FillReport {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.report
	r.Fields = append([]FieldCheck(nil), r.Fields...)
	r.Absent = append([]string(nil), r.Absent...)
	return r
}

func (b *Browser) updateReport(f func(*FillReport)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	f(&b.report)
}

func (b *Browser) noteField(e *elem, f field) {
	b.updateReport(func(r *FillReport) {
		r.Fields = append(r.Fields, FieldCheck{Label: f.label, Selector: e.sel, InFrame: e.frame.parent != "", Status: "已填写"})
	})
}

func (b *Browser) noteFieldStatus(label, status string) {
	b.updateReport(func(r *FillReport) {
		for i := range r.Fields {
			if r.Fields[i].Label == label {
				r.Fields[i].Status = status
			}
		}
	})
}
