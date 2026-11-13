// auth.go：JWT 登录认证与密码管理（密钥加载、签发、校验、鉴权中间件与认证接口）。
// 主要函数/类型：
//   - loadOrCreateJWTKey：加载或生成 JWT 签名密钥。
//   - signJWT / verifyJWT / extractToken：签发 / 校验 / 从请求提取 JWT。
//   - requireAuth：鉴权中间件，拦截未认证请求。
//   - authInit / authLogin / authChangePassword / authStatus：初始化 / 登录 / 改密 / 认证状态接口。

package server

import (
	"crypto/rand"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"artex/db"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

const (
	jwtKeyFilename = "jwt.key"
	authPassKey    = "auth.password_hash"
	// authTokenVersionKey holds a counter bumped on every password change. Tokens
	// carry the value they were issued under; a token whose claim no longer
	// matches is rejected. Without it "change my password" could not revoke a
	// stolen token — the signature stays valid until its 7-day expiry, so someone
	// holding a leaked token would simply keep their access after the very action
	// meant to remove it.
	authTokenVersionKey = "auth.token_version"
	jwtTTL              = 7 * 24 * time.Hour
	keyChars            = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

	// 下限与 setup 页的前端校验一致——校验只放在前端等于没放，直接打 API 就能
	// 绕过。上限是 bcrypt 的硬限制：超过 72 字节 GenerateFromPassword 会返回
	// ErrPasswordTooLong，提前挡掉好过让用户收到一句含义不明的「密码加密失败」。
	minPasswordRunes = 8
	maxPasswordBytes = 72
)

// errDataSourceUnavailable 是密码相关读操作失败时统一的回复。这些 handler 绝不能
// 把"读不到"当成"没有设置"：authInit 曾因此在数据库报错时放行，让未认证请求覆盖
// 掉已有的管理员密码。
const errDataSourceUnavailable = "数据源暂时不可用，请稍后重试"

// validatePassword 返回空串表示通过，否则返回可直接展示给用户的中文原因。
func validatePassword(pw string) string {
	if utf8.RuneCountInString(pw) < minPasswordRunes {
		return fmt.Sprintf("密码长度至少 %d 位", minPasswordRunes)
	}
	if len(pw) > maxPasswordBytes {
		return fmt.Sprintf("密码长度不能超过 %d 字节", maxPasswordBytes)
	}
	return ""
}

// loadOrCreateJWTKey reads the 32-byte signing key from keyDir/jwt.key. keyDir is
// the project base dir (next to the executable), NOT the browsable workspace root
// (dataDir) — the signing key must never be listable/downloadable via the file
// manager. Legacy installs kept it at dataDir/jwt.key; if present there and not yet
// at the new location, it is migrated (key preserved, so sessions stay valid) and
// the old file removed so it disappears from the workspace. On first run a random
// key is generated and persisted.
func loadOrCreateJWTKey(keyDir, dataDir string) ([]byte, error) {
	path := filepath.Join(keyDir, jwtKeyFilename)
	// one-time migration out of the old in-workspace location.
	if legacy := filepath.Join(dataDir, jwtKeyFilename); legacy != path {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if data, rerr := os.ReadFile(legacy); rerr == nil {
				if werr := os.WriteFile(path, data, 0o600); werr == nil {
					_ = os.Remove(legacy)
					log.Printf("[auth] JWT key 已从 %s 迁移到 %s（移出可浏览工作区）", legacy, path)
				}
			}
		}
	}
	if data, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(data))) >= 32 {
		return []byte(strings.TrimSpace(string(data))), nil
	}
	buf := make([]byte, 32)
	for i := range buf {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(keyChars))))
		if err != nil {
			return nil, fmt.Errorf("generate jwt key: %w", err)
		}
		buf[i] = keyChars[n.Int64()]
	}
	if err := os.WriteFile(path, buf, 0600); err != nil {
		return nil, fmt.Errorf("write jwt key: %w", err)
	}
	log.Printf("[auth] 新 JWT key 已写入 %s", path)
	return buf, nil
}

// AdminUsername 是固定的管理员用户名。写死而不是做成可配置项：这是个单管理员
// 平台，用户名可改只会增加一处需要同步的地方（token 的 sub、审计署名、前端回显），
// 而不会带来任何实际收益。
const AdminUsername = "Guigu"

// signJWT issues a 7-day HS256 token for the admin user.
func signJWT(key []byte, tokenVersion int) (string, error) {
	return jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": AdminUsername,
		"tv":  tokenVersion,
		"exp": jwt.NewNumericDate(time.Now().Add(jwtTTL)).Unix(),
		"iat": jwt.NewNumericDate(time.Now()).Unix(),
	}).SignedString(key)
}

// verifyJWT returns the token's claims when the signature and expiry are valid.
// Callers must still compare the token version: a valid signature only proves the
// token came from this install, not that it survived a password change.
func verifyJWT(tokenStr string, key []byte) (jwt.MapClaims, bool) {
	t, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return key, nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil || !t.Valid {
		return nil, false
	}
	claims, ok := t.Claims.(jwt.MapClaims)
	if !ok {
		return nil, false
	}
	return claims, true
}

// tokenVersionFromClaims reads the "tv" claim. Tokens issued before the claim
// existed report 0, so the first password change invalidates them too.
func tokenVersionFromClaims(claims jwt.MapClaims) int {
	switch v := claims["tv"].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	default:
		return 0
	}
}

// extractToken reads the JWT from Authorization: Bearer header,
// artex_token cookie, or ?token= query param (for SSE connections).
func extractToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	if c, err := r.Cookie("artex_token"); err == nil && c.Value != "" {
		return c.Value
	}
	return r.URL.Query().Get("token")
}

// requireAuth wraps h with JWT validation.
// /api/auth/* and /api/health are exempt.
func (s *Server) requireAuth(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		// Health probes are exempt: container orchestrators and systemd cannot hold
		// a JWT, and /api/health/deep only exposes component states (ok/error,
		// free-disk bytes) — never configuration or credentials.
		if strings.HasPrefix(p, "/api/auth/") || p == "/api/health" || p == "/api/health/deep" {
			h.ServeHTTP(w, r)
			return
		}
		tok := extractToken(r)
		if tok == "" {
			writeErr(w, 401, "未授权")
			return
		}
		claims, ok := verifyJWT(tok, s.jwtKey)
		if !ok {
			writeErr(w, 401, "token 无效或已过期")
			return
		}
		// A valid signature is not enough: a password change must revoke tokens
		// issued before it, otherwise a leaked token survives the very action
		// meant to cut it off.
		if tokenVersionFromClaims(claims) != s.currentTokenVersion() {
			writeErr(w, 401, "登录状态已失效，请重新登录")
			return
		}
		h.ServeHTTP(w, r)
	})
}

// GET /api/auth/status — reports whether the admin password has been initialised.
// 读失败必须回 503 而不是 initialized:false：前端在 initialized:false 时会把用户
// 送到 /setup 去设置密码（login/page.tsx），把数据库故障包装成 200 等于把用户往
// 覆盖已有密码的路上推。
func (s *Server) authStatus(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	hash, _, err := pg.GetSetting(authPassKey)
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	notInitialized := hash == ""
	if notInitialized {
		// A brand-new instance on a public address can be claimed by whoever POSTs
		// /api/auth/init first. That is a deliberate trade-off (no setup token by
		// request), so it must at least be LOUD: warn once per source address
		// instead of silently serving a page that hands the install away.
		s.warnUnclaimedInstance(r)
	}
	writeJSON(w, 200, map[string]any{"initialized": !notInitialized, "setup_required": notInitialized})
}

// warnUnclaimedInstance logs a warning when an unconfigured instance is reached from
// a non-loopback address. Rate-limited per address so a polling setup page does not
// flood the log; the point is that an operator reading logs can see it.
func (s *Server) warnUnclaimedInstance(r *http.Request) {
	ip := clientIPFrom(r)
	if ip == "" || ip == "127.0.0.1" || ip == "::1" {
		return // local setup is the intended path
	}
	if !s.setupWarnSeen.compareAndSwap(ip) {
		return
	}
	log.Printf("[auth] 警告：实例尚未设置管理员密码，且正被非本机地址 %s 访问。"+
		"任何先到者都可以通过 /api/auth/init 接管该实例；请立即完成初始化，或在初始化前限制访问来源。", ip)
}

// POST /api/auth/init — sets the password for the first time; rejected if already set.
func (s *Server) authInit(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	existing, _, err := pg.GetSetting(authPassKey)
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	if existing != "" {
		writeErr(w, 403, "密码已设置")
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := decodeJSONBounded(w, r, &req, maxAuthJSONBodyBytes); err != nil {
		if !writeJSONBodyTooLarge(w, err) {
			writeErr(w, 400, "密码不能为空")
		}
		return
	}
	if req.Password == "" {
		writeErr(w, 400, "密码不能为空")
		return
	}
	if msg := validatePassword(req.Password); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeErr(w, 500, "密码加密失败")
		return
	}
	// 用 INSERT ... ON CONFLICT DO NOTHING 而不是 upsert：上面那次 GetSetting 只是
	// 快速失败路径，真正"仅首次可设"的保证落在主键约束上。bcrypt 要跑几十毫秒，
	// 这期间别的请求完全可能先把密码设好，而读检查本身也可能因故障而失效。
	inserted, err := pg.InsertSettingIfAbsent(authPassKey, string(hash))
	if err != nil {
		writeErr(w, 500, "保存失败: "+err.Error())
		return
	}
	if !inserted {
		writeErr(w, 403, "密码已设置")
		return
	}
	tok, err := signJWT(s.jwtKey, s.currentTokenVersion())
	if err != nil {
		writeErr(w, 500, "token 生成失败")
		return
	}
	writeJSON(w, 200, map[string]any{"token": tok})
}

// POST /api/auth/change-password — changes the admin password. Requires a valid
// token (this route is under /api/auth/* which requireAuth exempts, so the token
// is validated here) AND the current password.
func (s *Server) authChangePassword(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	claims, ok := verifyJWT(extractToken(r), s.jwtKey)
	if !ok || tokenVersionFromClaims(claims) != s.currentTokenVersion() {
		writeErr(w, 401, "未授权")
		return
	}
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := decodeJSONBounded(w, r, &req, maxAuthJSONBodyBytes); err != nil {
		if !writeJSONBodyTooLarge(w, err) {
			writeErr(w, 400, "请求格式错误")
		}
		return
	}
	if req.NewPassword == "" {
		writeErr(w, 400, "新密码不能为空")
		return
	}
	if msg := validatePassword(req.NewPassword); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	hash, ok, err := pg.GetSetting(authPassKey)
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	if !ok || hash == "" {
		writeErr(w, 403, "密码未初始化，请先设置密码")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.OldPassword)); err != nil {
		writeErr(w, 401, "当前密码错误")
		return
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		writeErr(w, 500, "密码加密失败")
		return
	}
	if err := pg.SetSetting(authPassKey, string(newHash)); err != nil {
		writeErr(w, 500, "保存失败: "+err.Error())
		return
	}
	// Revoke every token issued before this change, then hand the caller a fresh
	// one so they are not logged out by their own password change.
	if err := s.bumpTokenVersion(); err != nil {
		writeErr(w, 500, "密码已更新但会话失效失败，请重新登录: "+err.Error())
		return
	}
	tok, err := signJWT(s.jwtKey, s.currentTokenVersion())
	if err != nil {
		writeErr(w, 500, "密码已更新，请重新登录")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "token": tok})
}

// POST /api/auth/login — validates username/password and returns a JWT.
func (s *Server) authLogin(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSONBounded(w, r, &req, maxAuthJSONBodyBytes); err != nil {
		if !writeJSONBodyTooLarge(w, err) {
			writeErr(w, 400, "请求格式错误")
		}
		return
	}
	ip := clientIPFrom(r)
	uname := strings.TrimSpace(req.Username)
	// 暴力破解防护：先看该来源与账号是否处于锁定冷却，命中就直接 429、不再做口令
	// 比对。放在 bcrypt 之前还顺带缓解了「海量尝试把 CPU / 连接打满」。
	if locked, retry := s.loginLimiter.locked(ip, uname); locked {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())))
		writeErr(w, http.StatusTooManyRequests,
			fmt.Sprintf("尝试过于频繁，账号或来源已临时锁定，请 %d 秒后再试", int(retry.Seconds())))
		return
	}
	if req.Username != AdminUsername {
		s.registerLoginFailure(pg, ip, uname)
		pg.RecordAudit(uname, db.AuditCategoryAuth, "login_failed", "fail", ip, "用户名错误", nil)
		writeErr(w, 401, "用户名或密码错误")
		return
	}
	hash, ok, err := pg.GetSetting(authPassKey)
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	if !ok || hash == "" {
		writeErr(w, 403, "密码未初始化，请先设置密码")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)); err != nil {
		s.registerLoginFailure(pg, ip, uname)
		pg.RecordAudit(uname, db.AuditCategoryAuth, "login_failed", "fail", ip, "密码错误", nil)
		writeErr(w, 401, "用户名或密码错误")
		return
	}
	tok, err := signJWT(s.jwtKey, s.currentTokenVersion())
	if err != nil {
		writeErr(w, 500, "token 生成失败")
		return
	}
	s.loginLimiter.reset(ip, uname) // 成功登录清零该来源+账号的失败计数
	pg.RecordAudit(uname, db.AuditCategoryAuth, "login", "success", ip, "登录成功", nil)
	writeJSON(w, 200, map[string]any{"token": tok})
}

// registerLoginFailure 记一次登录失败；若恰好触发锁定则写日志与审计。
func (s *Server) registerLoginFailure(pg *db.DB, ip, username string) {
	if s.loginLimiter.fail(ip, username, s.loginPolicySnapshot()) {
		log.Printf("[auth] 暴力破解锁定 用户=%q ip=%s", username, ip)
		// 锁定是安全事件而非普通失败：单列一个 action，便于审计页筛选。
		if pg != nil {
			pg.RecordAudit(username, db.AuditCategoryAuth, "lockout", "fail", ip,
				"失败次数超阈值，账号或来源已临时锁定", nil)
		}
	}
}

// currentTokenVersion reads the persisted password-change counter. Missing or
// unparseable means 0 — the same value a token issued before the claim existed
// carries, so upgrading does not silently log everyone out.
func (s *Server) currentTokenVersion() int {
	if s.m == nil || s.m.pg == nil {
		return 0
	}
	return settingInt(s.m.pg, authTokenVersionKey)
}

// bumpTokenVersion increments the counter, invalidating every token issued up to
// this point. Called on password change.
func (s *Server) bumpTokenVersion() error {
	if s.m == nil || s.m.pg == nil {
		return fmt.Errorf("数据库不可用")
	}
	return s.m.pg.SetSetting(authTokenVersionKey, strconv.Itoa(s.currentTokenVersion()+1))
}

// signTestJWT signs a token at the CURRENT version. Test-only helper so the many
// handlers exercised through the mux keep working once tokens carry a version.
func (s *Server) signTestJWT() (string, error) {
	return signJWT(s.jwtKey, s.currentTokenVersion())
}

// setupWarnLimiter 记录已告警过的来源地址，避免 setup 页轮询刷屏。
type setupWarnLimiter struct {
	mu   sync.Mutex
	seen map[string]bool
}

// compareAndSwap 在首次见到该地址时返回 true（调用方据此记一条日志）。
func (l *setupWarnLimiter) compareAndSwap(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seen == nil {
		l.seen = map[string]bool{}
	}
	if l.seen[key] {
		return false
	}
	// 上限保护：不同来源无限增长会把 map 撑大（与登录限流同一套理由）。
	if len(l.seen) > 4096 {
		l.seen = map[string]bool{}
	}
	l.seen[key] = true
	return true
}
