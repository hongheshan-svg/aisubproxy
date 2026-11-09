package cardpool

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
)

// 已知问题清单【涉及敏感数据】：redactPAN 只处理 Message，Code 和被包裹的底层错误里的卡号原样带出。

const panForRedact = "4111 1111 1111 1111"

func TestRedactPANCoversCodeAndWrapped(t *testing.T) {
	src := &marswallex.Error{
		Outcome: marswallex.OutcomeRejected, HTTPStatus: 400,
		Code:    "BAD_CARD_4111111111111111",
		Message: "card " + panForRedact + " invalid",
		Err:     fmt.Errorf("upstream said %s: %w", "4111-1111-1111-1111", context.DeadlineExceeded),
	}
	got := redactPAN(src)
	text := got.Error()
	for _, leak := range []string{"4111111111111111", panForRedact, "4111-1111-1111-1111"} {
		if strings.Contains(text, leak) {
			t.Fatalf("错误全文不应带卡号 %q: %s", leak, text)
		}
	}
	var me *marswallex.Error
	if !errors.As(got, &me) || me.Outcome != marswallex.OutcomeRejected || me.HTTPStatus != 400 {
		t.Fatalf("错误类型与分类应保留: %#v", got)
	}
	if !errors.Is(got, context.DeadlineExceeded) {
		t.Fatal("被包裹的底层错误仍应能用 errors.Is 认出")
	}
	if !strings.Contains(src.Message, panForRedact) {
		t.Fatal("不应改动原错误")
	}
}

func TestRedactPANOtherErrors(t *testing.T) {
	base := errors.New("dial failed")
	src := fmt.Errorf("get card %s: %w", panForRedact, base)
	got := redactPAN(src)
	if strings.Contains(got.Error(), panForRedact) {
		t.Fatalf("不是平台错误类型的也应对整条文本打码: %s", got)
	}
	if !errors.Is(got, base) {
		t.Fatal("errors.Is 应照常可用")
	}
	if redactPAN(nil) != nil {
		t.Fatal("nil 原样返回")
	}
	plain := errors.New("没有卡号")
	if got := redactPAN(plain); got.Error() != "没有卡号" {
		t.Fatalf("没有卡号的错误文本不变: %s", got)
	}
}
