// sync_fofa_test.go：FOFA 同步的纯函数与脱敏回归。
// 主要函数/类型：
//   - TestFofaStripURLsKeepsOnlyHost：错误文本里的地址被截断，key 不外泄。
//   - TestSplitFofaHost：三种 host 形态的解析。
//   - TestBuildFofaURL：默认端口的省略与补全。
//   - TestNormalizeTaskCollectionIDs：合集 id 的校验与去重。

package server

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"artex/db"
)

// TestFofaStripURLsKeepsOnlyHost 是安全要点：FOFA 的 key 在 query string 上，
// 错误一律回给浏览器，不脱敏就是把 key 交给任何能看到错误提示的人。
func TestFofaStripURLsKeepsOnlyHost(t *testing.T) {
	const key = "SUPER_SECRET_FOFA_KEY"
	in := fmt.Sprintf(`Get "https://fofa.info/api/v1/info/my?email=a@b.c&key=%s": dial tcp: timeout`, key)
	got := fofaStripURLs(in)
	if strings.Contains(got, key) {
		t.Fatalf("key 泄露：%q", got)
	}
	if !strings.Contains(got, "https://fofa.info/…") {
		t.Fatalf("应保留 scheme://host 便于定位，实际：%q", got)
	}
}

// TestFofaRedactURLInURLField 覆盖 *url.Error 分支（URL 字段含 key）。
func TestFofaRedactURLInURLField(t *testing.T) {
	const key = "SECRET123"
	err := &url.Error{Op: "Get", URL: "https://fofa.info/api/v1/search/all?key=" + key, Err: errors.New("timeout")}
	got := fofaRedact(err)
	if strings.Contains(got, key) {
		t.Fatalf("key 泄露：%q", got)
	}
	if !strings.Contains(got, "fofa.info") {
		t.Fatalf("应保留主机名，实际：%q", got)
	}
}

func TestSplitFofaHost(t *testing.T) {
	cases := []struct {
		in                     string
		scheme, hostname, port string
	}{
		{"https://a.example.com", "https", "a.example.com", ""},
		{"https://a.example.com:8443", "https", "a.example.com", "8443"},
		{"a.example.com:8080", "", "a.example.com", "8080"},
		{"1.2.3.4", "", "1.2.3.4", ""},
		{"", "", "", ""},
	}
	for _, c := range cases {
		s, h, p := splitFofaHost(c.in)
		if s != c.scheme || h != c.hostname || p != c.port {
			t.Errorf("splitFofaHost(%q) = (%q,%q,%q)，期望 (%q,%q,%q)",
				c.in, s, h, p, c.scheme, c.hostname, c.port)
		}
	}
}

func TestBuildFofaURL(t *testing.T) {
	cases := []struct {
		scheme, protocol, host, port, want string
	}{
		{"https", "", "a.com", "", "https://a.com"},
		{"", "https", "a.com", "443", "https://a.com"},     // 默认端口省略
		{"", "http", "a.com", "80", "http://a.com"},        // 默认端口省略
		{"", "http", "a.com", "8080", "http://a.com:8080"}, // 非默认端口保留
		{"", "", "a.com", "443", "https://a.com"},          // 有 443 无协议 → https
		{"", "", "a.com", "8080", "http://a.com:8080"},
		{"", "ftp", "a.com", "21", "http://a.com:21"},
		{"", "", "", "", ""}, // 无主机名
	}
	for _, c := range cases {
		got := buildFofaURL(c.scheme, c.protocol, c.host, c.port)
		if got != c.want {
			t.Errorf("buildFofaURL(%q,%q,%q,%q) = %q，期望 %q",
				c.scheme, c.protocol, c.host, c.port, got, c.want)
		}
	}
}

// TestNormalizeTaskCollectionIDs 覆盖合集 id 的校验口径。
func TestNormalizeTaskCollectionIDs(t *testing.T) {
	if got, err := db.NormalizeTaskCollectionIDs(nil); err != nil || got != nil {
		t.Fatalf("空输入应返回 nil,nil，得到 %v,%v", got, err)
	}
	// 去重保序。
	got, err := db.NormalizeTaskCollectionIDs([]int64{3, 1, 3, 2})
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{3, 1, 2}
	if len(got) != len(want) {
		t.Fatalf("去重后应 %v，得到 %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("去重应保序：%v vs %v", got, want)
		}
	}
	// 非法 id。
	if _, err := db.NormalizeTaskCollectionIDs([]int64{0}); err == nil {
		t.Error("id=0 应报错")
	}
	if _, err := db.NormalizeTaskCollectionIDs([]int64{-5}); err == nil {
		t.Error("负 id 应报错")
	}
	// 超上限。
	tooMany := make([]int64, db.MaxTaskCollectionCount+1)
	for i := range tooMany {
		tooMany[i] = int64(i + 1)
	}
	if _, err := db.NormalizeTaskCollectionIDs(tooMany); err == nil {
		t.Error("超过上限应报错")
	}
}
// TestFofaSyncIsMutuallyExclusive 钉住并发保护：第二次同步必须被拒。
//
// 没有它，两次并发同步会重复 Upsert 同一批资产、对同一合集名并发 Upsert（后到者
// 覆盖成员统计），两边的 imported 计数也互相干扰。
func TestFofaSyncIsMutuallyExclusive(t *testing.T) {
	s := &Server{}
	if !s.beginFofaSync() {
		t.Fatal("第一次同步应成功获取锁")
	}
	if s.beginFofaSync() {
		t.Fatal("第二次同步必须被拒绝")
	}
	s.endFofaSync()
	if !s.beginFofaSync() {
		t.Fatal("释放后应能再次获取")
	}
	s.endFofaSync()
}
