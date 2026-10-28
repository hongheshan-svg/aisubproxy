package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

func serveLatestRelease(t *testing.T, body string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/releases/latest" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": "v1.5.1",
			"html_url": "https://github.com/o/r/releases/tag/v1.5.1",
			"body":     body,
			"assets":   []map[string]any{{"name": "cdk-bundle-linux-amd64.tgz"}},
		})
	}))
	t.Cleanup(srv.Close)
	old := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = old })
	t.Setenv("CDK_GITHUB_REPO", "o/r")
}

func getVersion(t *testing.T, refresh bool) map[string]any {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	url := "/api/v1/admin/system/version"
	if refresh {
		url += "?refresh=1"
	}
	c.Request = httptest.NewRequest(http.MethodGet, url, nil)
	AdminSystemVersion(c)
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestVersionIncludesReleaseNotes(t *testing.T) {
	serveLatestRelease(t, "### 新增\n- 卡池\n")
	out := getVersion(t, true)
	if out["latest"] != "1.5.1" || out["release_notes"] != "### 新增\n- 卡池\n" {
		t.Fatalf("应带上最新版本的更新说明：%v", out)
	}
	// 走缓存的第二次请求也要有
	if out2 := getVersion(t, false); out2["release_notes"] != "### 新增\n- 卡池\n" {
		t.Fatalf("缓存命中时丢了更新说明：%v", out2["release_notes"])
	}
}

func TestVersionReleaseNotesTruncated(t *testing.T) {
	serveLatestRelease(t, strings.Repeat("说", maxReleaseNotesRunes+50))
	notes, _ := getVersion(t, true)["release_notes"].(string)
	if utf8.RuneCountInString(notes) > maxReleaseNotesRunes+40 || !strings.Contains(notes, "完整内容见 Release 页面") {
		t.Fatalf("超长说明应截断并提示，得到 %d 个字", utf8.RuneCountInString(notes))
	}
	if !utf8.ValidString(notes) {
		t.Fatal("截断不能切坏多字节字符")
	}
}
