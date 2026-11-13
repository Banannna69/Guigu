// background_test.go：后台 goroutine 追踪与优雅退出的回归。
// 主要函数/类型：
//   - TestBackgroundStopJoinsLoops：Stop 必须等到所有被追踪的循环退出才返回。
//   - TestBackgroundRejectsAfterStop：Stop 之后不得再起新循环（fail-closed）。
//   - TestBackgroundRecoversPanic：单个循环 panic 不得带崩进程。

package server

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// newBareServer 构造一个只用于后台追踪测试的 Server，不触碰数据库。
func newBareServer(t *testing.T) *Server {
	t.Helper()
	return &Server{ctx: context.Background()}
}

// TestBackgroundStopJoinsLoops 是这次改动的核心回归：
// Stop 之前每个 background 起的循环都必须已经退出。原来的实现里这些循环是裸
// `go func()`，Stop 无从等待，关停时会撞上正在拆除的连接池。
func TestBackgroundStopJoinsLoops(t *testing.T) {
	s := newBareServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	s.ctx = ctx

	var exited atomic.Int32
	for i := 0; i < 5; i++ {
		s.background("test-loop", func(ctx context.Context) {
			<-ctx.Done()
			exited.Add(1)
		})
	}

	cancel() // 模拟 main 里的 shutdown(agent.AbortShutdown)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	if err := s.Stop(stopCtx); err != nil {
		t.Fatalf("Stop 返回错误，说明有循环没退出: %v", err)
	}
	if got := exited.Load(); got != 5 {
		t.Fatalf("Stop 返回时仍有循环在跑：退出 %d/5", got)
	}
}

// TestBackgroundRejectsAfterStop 保证关停后不会再起新循环——否则 Stop 之后仍有
// 写入，join 的语义就失效了。
func TestBackgroundRejectsAfterStop(t *testing.T) {
	s := newBareServer(t)
	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err := s.Stop(stopCtx); err != nil {
		t.Fatalf("空 Server 的 Stop 应立即返回: %v", err)
	}

	var ran atomic.Bool
	s.background("after-stop", func(context.Context) { ran.Store(true) })
	time.Sleep(50 * time.Millisecond)
	if ran.Load() {
		t.Fatal("Stop 之后仍启动了后台循环")
	}
}

// TestBackgroundRecoversPanic 保证单个循环 panic 不会带崩进程（原来裸 goroutine
// panic 会直接终止整个服务，在测试里则表现为后面某个无关测试神秘失败）。
func TestBackgroundRecoversPanic(t *testing.T) {
	s := newBareServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	s.ctx = ctx

	done := make(chan struct{})
	s.background("panics", func(context.Context) {
		defer close(done)
		panic("boom")
	})
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("panic 的循环没有返回")
	}

	// panic 已 recover，Stop 仍能正常 join。
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer stopCancel()
	if err := s.Stop(stopCtx); err != nil {
		t.Fatalf("panic 后 Stop 失败: %v", err)
	}
	cancel()
}

// TestStopReportsDeadline 保证 Stop 在循环不退出时如实报错，而不是假装成功。
func TestStopReportsDeadline(t *testing.T) {
	s := newBareServer(t)
	s.ctx = context.Background() // 永不取消

	block := make(chan struct{})
	started := make(chan struct{})
	s.background("stuck", func(context.Context) {
		close(started)
		<-block
	})
	<-started

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer stopCancel()
	if err := s.Stop(stopCtx); err == nil {
		t.Fatal("循环卡住时 Stop 必须返回错误")
	}
	close(block)
}
