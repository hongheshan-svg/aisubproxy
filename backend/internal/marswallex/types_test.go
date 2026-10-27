package marswallex

import (
	"encoding/json"
	"testing"
	"time"
)

func TestFormatAmount(t *testing.T) {
	cases := map[int64]string{12345: "123.45", 5: "0.05", 100: "1.00", 0: "0.00"}
	for in, want := range cases {
		got, err := FormatAmount(in)
		if err != nil || got != want {
			t.Errorf("FormatAmount(%d)=%q,%v want %q", in, got, err, want)
		}
	}
	if _, err := FormatAmount(-1); err == nil {
		t.Error("负数应报错")
	}
}

func TestParseAmount(t *testing.T) {
	ok := map[string]int64{"100.50": 10050, "100.5": 10050, "100": 10000, "0.05": 5, "89246.14": 8924614}
	for in, want := range ok {
		got, err := ParseAmount(in)
		if err != nil || got != want {
			t.Errorf("ParseAmount(%q)=%d,%v want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "-1", "1.234", "1e3", " 1", "1.", ".5", "abc", "1234567890123"} {
		if _, err := ParseAmount(in); err == nil {
			t.Errorf("ParseAmount(%q) 应报错", in)
		}
	}
}

func TestTimestampUnmarshal(t *testing.T) {
	var ts Timestamp
	if err := json.Unmarshal([]byte(`1766123535`), &ts); err != nil || ts.Unix() != 1766123535 {
		t.Errorf("秒数字: %v %v", ts, err)
	}
	ts = Timestamp{}
	if err := json.Unmarshal([]byte(`1766123822742`), &ts); err != nil || ts.UnixMilli() != 1766123822742 {
		t.Errorf("毫秒数字: %v %v", ts, err)
	}
	ts = Timestamp{}
	if err := json.Unmarshal([]byte(`"1766123535"`), &ts); err != nil || ts.Unix() != 1766123535 {
		t.Errorf("秒字符串: %v %v", ts, err)
	}
	ts = Timestamp{}
	want := time.Date(2025, 12, 15, 10, 55, 3, 0, time.UTC)
	if err := json.Unmarshal([]byte(`"2025-12-15T10:55:03.000+00:00"`), &ts); err != nil || !ts.Equal(want) {
		t.Errorf("ISO: %v %v", ts, err)
	}
	ts = Timestamp{}
	if err := json.Unmarshal([]byte(`null`), &ts); err != nil || !ts.IsZero() {
		t.Errorf("null: %v %v", ts, err)
	}
	if err := json.Unmarshal([]byte(`"bad"`), &ts); err == nil {
		t.Error("bad 应报错")
	}
	out, _ := json.Marshal(Timestamp{Time: time.Unix(1766123535, 0)})
	if string(out) != "1766123535" {
		t.Errorf("Marshal=%s", out)
	}
}

func TestCountUnmarshal(t *testing.T) {
	var c Count
	for in, want := range map[string]Count{`"12"`: 12, `12`: 12, `null`: 0} {
		c = 99
		if err := json.Unmarshal([]byte(in), &c); err != nil || c != want {
			t.Errorf("%s -> %d,%v want %d", in, c, err, want)
		}
	}
	if err := json.Unmarshal([]byte(`"x"`), &c); err == nil {
		t.Error(`"x" 应报错`)
	}
	var p Page[int]
	if err := json.Unmarshal([]byte(`{"count":"2","data":[1,2]}`), &p); err != nil || p.Count != 2 || len(p.Data) != 2 {
		t.Errorf("Page: %+v %v", p, err)
	}
}
