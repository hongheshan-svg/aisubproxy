package handler

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

// codeqlTgz 把 名字 → 内容 的条目打成 tgz，写到 dir 下返回路径。
func codeqlTgz(t *testing.T, dir string, entries []struct{ name, body string }) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.body)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "bundle.tgz")
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// 条目名含 ".." 的一律拒绝，即使 Clean 之后仍落在目标目录内（见 codeql #9）。
func TestCodeqlExtractRejectsDotDotNames(t *testing.T) {
	for _, name := range []string{"../evil.txt", "a/../b.txt", "x..y/../z.txt", "a/b/../../../evil.txt"} {
		root := t.TempDir()
		dest := filepath.Join(root, "dest")
		if err := os.MkdirAll(dest, 0o755); err != nil {
			t.Fatal(err)
		}
		tgz := codeqlTgz(t, root, []struct{ name, body string }{{name, "x"}})
		if err := extractTarGz(tgz, dest); err == nil {
			t.Errorf("条目 %q 应被拒绝，但解压成功", name)
		}
		// 不论报错与否，dest 之外不能有文件，dest 之内也不能落下被清洗后的文件
		if _, err := os.Stat(filepath.Join(root, "evil.txt")); err == nil {
			t.Errorf("条目 %q 写到了 dest 之外", name)
		}
		entries, _ := os.ReadDir(dest)
		if len(entries) != 0 {
			t.Errorf("条目 %q 不应落盘，但 dest 里有 %d 项", name, len(entries))
		}
	}
}

// 正常的发布包条目照常解出。
func TestCodeqlExtractNormalEntriesOK(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "dest")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	tgz := codeqlTgz(t, root, []struct{ name, body string }{
		{"cdk-recharge", "bin"},
		{"web/assets/index-abc.js", "js"},
		{"VERSION", "1.5.2"},
	})
	if err := extractTarGz(tgz, dest); err != nil {
		t.Fatalf("正常条目解压失败: %v", err)
	}
	for name, want := range map[string]string{"cdk-recharge": "bin", "web/assets/index-abc.js": "js", "VERSION": "1.5.2"} {
		got, err := os.ReadFile(filepath.Join(dest, name))
		if err != nil {
			t.Fatalf("%s 未解出: %v", name, err)
		}
		if string(got) != want {
			t.Errorf("%s 内容 %q，应为 %q", name, got, want)
		}
	}
}
