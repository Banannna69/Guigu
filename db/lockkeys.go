// lockkeys.go：全部 PostgreSQL advisory lock key 的集中声明。
// 主要函数/类型：
//   - 一组 LockKey* 常量：每个互斥用途一个 key，绝不重复。
//   - AllLockKeys：供测试遍历校验唯一性。
//
// 为什么要集中：advisory lock 的 key 空间是**全局唯一**的，而
// pg_advisory_lock（session 级）与 pg_advisory_xact_lock（事务级）共享同一个空间——
// 两个不同用途只要用了同一个数字就会互相阻塞。这类冲突的表现极其隐晦：不是报错，
// 而是「偶尔不触发」「偶尔卡一下」，且没有任何日志。
//
// 实际踩过的坑：证据写入（事务级）与 scheduler tick（会话级）曾同时用
// 7337741004，导致慢的证据写入会让 scheduler 的 try 锁直接失败、静默跳过整轮
// 触发器——表现为「触发器偶尔不触发」。
//
// 新增锁时必须在这里登记，并跑 TestLockKeysUnique。

package db

const (
	// 7337741001 —— schema 迁移。跨进程串行化 DDL 与种子数据，防止两个实例
	// 同时初始化出不一致的结构。
	schemaMigrationLockKey int64 = 7337741001
	// 7337741002 —— 测试套件的跨包串行锁。所有包共享同一个 PG，并行跑会互相踩。
	testSuiteLockKey int64 = 7337741002
	// 7337741003 —— 公司归属的变更。公司范围会影响资产归属的批量刷新，必须串行。
	companyScopeMutationLock int64 = 7337741003
	// 7337741004 —— 漏洞证据写入。content-addressed 落盘 + 元数据绑定必须原子。
	findingEvidenceLockKey int64 = 7337741004
	// 7337741005 —— scheduler 的每轮 tick。**注意曾与证据锁撞号**，已改为 1005。
	schedulerTickLockKey int64 = 7337741005
	// 7337741010 —— 漏洞复测。同一条 finding 的并发复测会互相覆盖结果。
	findingRetestLockKey int64 = 7337741010
	// 7337741011 —— 痕迹清理。清理期间不能让新的流量写入进来。
	traceCleanLockKey int64 = 7337741011
)

// AllLockKeys 列出全部 key，供测试断言唯一。
func AllLockKeys() []int64 {
	return []int64{
		schemaMigrationLockKey,
		testSuiteLockKey,
		companyScopeMutationLock,
		findingEvidenceLockKey,
		schedulerTickLockKey,
		findingRetestLockKey,
		traceCleanLockKey,
	}
}
