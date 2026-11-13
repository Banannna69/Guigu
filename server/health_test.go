// health_test.go：健康检查 liveness / readiness 语义的回归。
// 主要函数/类型：
//   - TestHealthAlwaysOK：liveness 永远 200（重启修不好数据库故障）。
//   - TestHealthReportsDependencies：依赖状态被如实报告，且不影响 liveness 状态码。
//   - TestHealthDeepFailsWithoutDB：readiness 在依赖坏掉时 503。

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestHealthAlwaysOK 钉住 liveness 的契约：不管依赖状态如何都返回 200。
//
// 这不是"忽略问题"，而是分层：容器健康检查若因数据库断连就判死，编排系统会
// 不断重启一个本身健康的进程，而重启对数据库故障毫无帮助——只会把问题放大成
// 重启循环。
func TestHealthAlwaysOK(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer m.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	td := t.TempDir()
	s := New(ctx, m, td, td, td)
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = s.Stop(stopCtx)
	})

	for _, path := range []string{"/api/health", "/api/health/deep"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s 在数据库正常时应 200，得到 %d：%s", path, rec.Code, rec.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s 响应不是 JSON: %v", path, err)
		}
		if body["service"] != "artex" {
			t.Fatalf("%s 缺少 service 字段: %v", path, body)
		}
		if body["version"] == nil || body["version"] == "" {
			t.Fatalf("%s 缺少 version 字段: %v", path, body)
		}
	}
}

// TestHealthReportsDependencies 保证依赖状态可见（运维能看出是哪一层坏了），
// 同时不改变 liveness 的状态码。
func TestHealthReportsDependencies(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer m.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	td := t.TempDir()
	s := New(ctx, m, td, td, td)
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = s.Stop(stopCtx)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	var body struct {
		OK           bool           `json:"ok"`
		Dependencies map[string]any `json:"dependencies"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.OK {
		t.Fatal("数据库正常时 ok 应为 true")
	}
	dbState, ok := body.Dependencies["db"].(map[string]any)
	if !ok {
		t.Fatalf("应报告 db 依赖状态: %v", body.Dependencies)
	}
	if dbState["state"] != "ok" {
		t.Fatalf("db 状态应为 ok，得到 %v", dbState)
	}
	// 后台循环与磁盘也在报告里。
	for _, key := range []string{"background", "disk_free_bytes", "llm"} {
		if _, ok := body.Dependencies[key]; !ok {
			t.Errorf("依赖报告缺少 %s: %v", key, body.Dependencies)
		}
	}
}

// TestHealthDeepFailsWithoutDB 钉住 readiness：数据库不可用时 503。
//
// 用 pg.PingContext 在关闭连接池后失败来模拟，而不是真的停库——效果的等价性
// 足够，且不会影响同包其他用例。
func TestHealthDeepFailsWithoutDB(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	td := t.TempDir()
	s := New(ctx, m, td, td, td)
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = s.Stop(stopCtx)
	})

	m.Close() // 关掉连接池 → PingContext 必然失败

	req := httptest.NewRequest(http.MethodGet, "/api/health/deep", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("数据库不可用时 deep 应 503，得到 %d：%s", rec.Code, rec.Body.String())
	}

	// 同一个坏掉的依赖，liveness 仍是 200。
	req2 := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("liveness 不应因依赖故障改变状态码，得到 %d", rec2.Code)
	}
}