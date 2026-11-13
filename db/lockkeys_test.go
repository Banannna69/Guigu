package db

import "testing"

// TestLockKeysUnique 断言所有 advisory lock key 互不相同。
//
// PostgreSQL 的 advisory lock key 是全局唯一的命名空间，而且 session 级的
// pg_advisory_lock 与事务级的 pg_advisory_xact_lock **共享同一个空间**。
// 两个不同用途只要用了同一个数字就会互相阻塞——表现不是报错，而是「偶尔
// 不触发」「偶尔卡一下」，且没有任何日志。
//
// 实际发生过：证据写入（事务级，7337741004）与 scheduler tick（会话级，
// 同一个号码）撞号，慢的证据写入会让 scheduler 的 try 锁直接失败、静默跳过
// 整轮触发器。
//
// 这个测试就是防止再一次撞号的唯一防线。
func TestLockKeysUnique(t *testing.T) {
	seen := map[int64]string{}
	// 名称表与 AllLockKeys 的顺序一一对应，冲突时能直接指出是哪两个用途。
	names := []string{
		"schemaMigration", "testSuite", "companyScopeMutation",
		"findingEvidence", "schedulerTick", "findingRetest", "traceClean",
	}
	keys := AllLockKeys()
	if len(keys) != len(names) {
		t.Fatalf("AllLockKeys 有 %d 个 key，但名称表有 %d 个——新增锁时记得同步两处", len(keys), len(names))
	}
	for i, k := range keys {
		if prev, dup := seen[k]; dup {
			t.Fatalf("advisory lock key %d 被两个用途共用：%s 与 %s。"+
				"两者会互相阻塞（session 级与事务级共享同一空间），"+
				"表现为「偶尔不触发」且无日志。请改号并在 db/lockkeys.go 登记", k, prev, names[i])
		}
		seen[k] = names[i]
	}
}

// TestLockKeysInExpectedRange 断言所有 key 落在约定的号段内。
//
// 留这个断言是因为 key 不能随便取：advisory lock 是全局空间，随手取一个小数字
// 很容易和别的库/工具撞上。约定用 73377410xx 段，靠前几位做命名空间隔离。
func TestLockKeysInExpectedRange(t *testing.T) {
	const lo, hi = 7337741000, 7337741099
	for _, k := range AllLockKeys() {
		if k < lo || k > hi {
			t.Fatalf("advisory lock key %d 超出约定号段 [%d, %d]；"+
				"全局空间里随手取号容易与其它工具冲突", k, lo, hi)
		}
	}
}
