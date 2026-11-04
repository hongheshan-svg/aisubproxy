package handler

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// D-1：引擎会写出 01-open-failed.png 这类带连字符的步骤截图，后台必须能取到。
func TestPayShotServesHyphenatedStepName(t *testing.T) {
	_, dir := payEnv(t)
	const no = "PO260101000000abcdef02"
	if err := os.MkdirAll(filepath.Join(dir, no), 0o755); err != nil {
		t.Fatal(err)
	}
	png := []byte("\x89PNG\r\n\x1a\nopen-failed")
	if err := os.WriteFile(filepath.Join(dir, no, "01-open-failed.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	w := payDo(AdminPayShot, "GET", "/orders/:no/shots/:name", "/orders/"+no+"/shots/01-open-failed.png", "")
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), png) {
		t.Fatalf("带连字符的截图应可取: %d %s", w.Code, w.Body.String())
	}
	// 连字符放开后，路径穿越等仍然拒绝
	for _, name := range []string{"01-..-x.png", "..-01.png", "01-a/b.png"} {
		if w := payDo(AdminPayShot, "GET", "/orders/:no/shots/:name", "/orders/"+no+"/shots/"+name, ""); w.Code != http.StatusBadRequest && w.Code != http.StatusNotFound {
			t.Errorf("%s 不应放行: %d", name, w.Code)
		}
	}
}
