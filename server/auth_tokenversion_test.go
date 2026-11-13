// auth_tokenversion_test.go：改密吊销旧 token 的回归。
// 主要函数/类型：
//   - TestTokenVersionRevokesOnPasswordChange：改密后旧 token 必须失效，新 token 可用。
//   - TestVerifyJWTRejectsWrongSigningMethod：非 HMAC 算法不得被接受。

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

// TestTokenVersionRevokesOnPasswordChange 覆盖这次改动的核心目的：签名有效不等于
// 会话有效——改密必须让改密前签出的 token 立刻失效，否则泄露的 token 会一直活到
// 7 天过期，用户"改密码"这个动作等于没做。
func TestTokenVersionRevokesOnPasswordChange(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer m.Close()
	td := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := New(ctx, m, td, td, td)
	t.Cleanup(func() {
		sc, cc := context.WithTimeout(context.Background(), 5e9)
		defer cc()
		_ = s.Stop(sc)
	})

	// 初始状态：版本 0，签出的 token 应通过中间件。
	oldTok, err := s.signTestJWT()
	if err != nil {
		t.Fatal(err)
	}
	if !tokenPasses(t, s, oldTok) {
		t.Fatal("改密前的 token 应可用")
	}

	// 改密 → 版本 +1。
	if err := s.bumpTokenVersion(); err != nil {
		t.Fatal(err)
	}

	if tokenPasses(t, s, oldTok) {
		t.Fatal("改密后旧 token 必须失效")
	}

	newTok, err := s.signTestJWT()
	if err != nil {
		t.Fatal(err)
	}
	if !tokenPasses(t, s, newTok) {
		t.Fatal("改密后新签发的 token 应可用")
	}
}

// tokenPasses 用一个受保护的接口验证 token 是否还能通过鉴权中间件。
func tokenPasses(t *testing.T, s *Server, tok string) bool {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec.Code != http.StatusUnauthorized
}

// TestVerifyJWTRejectsWrongSigningMethod 保证 alg 混淆类攻击（把算法改成 none 或
// 换成非 HMAC）不会被接受。
func TestVerifyJWTRejectsWrongSigningMethod(t *testing.T) {
	key := []byte(strings.Repeat("k", 32))

	// alg=none 的无签名 token。
	noneTok, err := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{"sub": AdminUsername}).
		SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := verifyJWT(noneTok, key); ok {
		t.Fatal("alg=none 的 token 必须被拒绝")
	}

	// 正确签发的 token 仍应通过，避免上一条把功能测试坏了。
	good, err := signJWT(key, 3)
	if err != nil {
		t.Fatal(err)
	}
	claims, ok := verifyJWT(good, key)
	if !ok {
		t.Fatal("HS256 正常 token 应通过")
	}
	if got := tokenVersionFromClaims(claims); got != 3 {
		t.Fatalf("token 版本应读回 3，得到 %d", got)
	}
}