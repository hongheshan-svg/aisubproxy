package cardpool

import (
	"strings"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
)

// 已知问题清单【涉及敏感数据】：scrubProfile 不抹中文姓名和证件有效期，平台错误原文带这两项时原样返回给管理员。
// 中文名两个字以上就换（原规则要 4 个字符以上，中文名基本都不够）；单字的姓不单独换，免得把说明里的常用字都换掉。

func TestScrubProfileChineseNameAndIDPeriod(t *testing.T) {
	p := &marswallex.CardholderProfile{
		FirstName: "XIAOMING", LastName: "WANG",
		ChineseLastName: "王", ChineseFirstName: "小明",
		IDNo: "110101199001011234", IDPeriodBegin: "2020-01-01", IDPeriodEnd: "2040-01-01",
	}
	text := "持卡人 王小明 证件 110101199001011234 有效期 2020-01-01 至 2040-01-01，名 小明 与证件不符，王先生请核对"
	got := scrubProfile(text, p)
	for _, leak := range []string{"王小明", "小明", "2020-01-01", "2040-01-01", "110101199001011234"} {
		if strings.Contains(got, leak) {
			t.Fatalf("不应留下 %q: %s", leak, got)
		}
	}
	if !strings.Contains(got, "王先生") {
		t.Fatalf("单字的姓不单独替换: %s", got)
	}
}

// 复姓、两字名：姓+名连写、姓、名各自两个字以上都要换。
func TestScrubProfileCompoundSurname(t *testing.T) {
	p := &marswallex.CardholderProfile{ChineseLastName: "欧阳", ChineseFirstName: "娜娜"}
	got := scrubProfile("欧阳娜娜 / 欧阳 / 娜娜", p)
	if strings.Contains(got, "欧阳") || strings.Contains(got, "娜娜") {
		t.Fatalf("复姓和名都应替换: %s", got)
	}
}
