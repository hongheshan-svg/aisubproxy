package server

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"
)

// 卡池告警先写日志（Telegram 未配置时也不丢），再 HTML 转义后发送：parse_mode=HTML 下未转义的 < 或 & 会让整条消息被拒收。
func TestCardPoolAlertLogsAndEscapes(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	var sent []string
	alert := cardPoolAlert(func(text string) { sent = append(sent, text) })

	raw := `持卡人 h1 备注：A&B <x> "q"`
	alert(raw)

	if len(sent) != 1 {
		t.Fatalf("应发送 1 条: %v", sent)
	}
	if strings.ContainsAny(sent[0], "<>") || strings.Contains(sent[0], "&B") {
		t.Fatalf("未转义: %q", sent[0])
	}
	if !strings.Contains(sent[0], "A&amp;B &lt;x&gt;") {
		t.Fatalf("转义结果: %q", sent[0])
	}
	if !strings.Contains(buf.String(), "[cardpool] 告警: "+raw) {
		t.Fatalf("日志应记原文: %q", buf.String())
	}
}
