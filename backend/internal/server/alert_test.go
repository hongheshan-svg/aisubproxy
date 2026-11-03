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
	if !strings.Contains(buf.String(), "[payengine] 告警: 单据 PO1 <失败> & 重试") {
		t.Fatalf("日志: %q", buf.String())
	}
}
