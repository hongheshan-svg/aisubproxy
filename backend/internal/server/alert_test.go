package server

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"
)

// 通用告警函数：日志前缀随调用方变化，同样先写日志再转义发送。
func TestAlertSenderPrefix(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	var sent []string
	alert := alertSender("payengine", func(text string) { sent = append(sent, text) })
	alert("单据 PO1 <失败> & 重试")
	if len(sent) != 1 || strings.ContainsAny(sent[0], "<>") || !strings.Contains(sent[0], "&amp; 重试") {
		t.Fatalf("发送内容: %v", sent)
	}
	// 发到 Telegram 的告警以模块名开头，和同一对话里的其他消息区分开；日志里不加
	if !strings.HasPrefix(sent[0], "【付款】单据 PO1") {
		t.Fatalf("应以【付款】开头: %q", sent[0])
	}
	if strings.Contains(buf.String(), "【付款】") {
		t.Fatalf("日志不应带中文模块名: %q", buf.String())
	}
	for prefix, want := range map[string]string{"cardpool": "【卡池】", "cdk": "【CDK】", "other": "【other】"} {
		var got []string
		alertSender(prefix, func(text string) { got = append(got, text) })("x")
		if len(got) != 1 || got[0] != want+"x" {
			t.Fatalf("%s: %v", prefix, got)
		}
	}
	if !strings.Contains(buf.String(), "[payengine] 告警: 单据 PO1 <失败> & 重试") {
		t.Fatalf("日志: %q", buf.String())
	}
}
