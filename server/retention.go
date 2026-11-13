// retention.go：数据留存清理（防止长跑实例把库和磁盘撑满）。
// 主要函数/类型：
//   - retentionPolicy / retentionPolicySnapshot：从 settings 读取的留存策略。
//   - (s *Server) RunRetention：后台循环，按日清理过期数据。
//   - pruneActivity / pruneTraffic / pruneDeliveries：三类数据的按龄删除。
//
// 为什么需要：activity 每次工具调用一行、流量库每个请求一行、投递历史每次推送
// 一行，全都没有上限。单机自托管跑几个月后 PostgreSQL 与 SQLite 会单调增长到
// 撑不住，而且没有任何界面能看出是哪一类在涨。
//
// 为什么默认全关（0 = 不限）：删数据是不可逆的，不能因为升级就替用户做主。运维
// 明确设置天数后才开始清理，且只清「已归档任务」的活动记录——探索图历史是排障
// 依据，正在跑或保留中的任务不能被动。

package server

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"time"
)

// retentionPolicy 是三类数据的留存天数。0 表示不限（默认）。
type retentionPolicy struct {
	// Enabled 是总开关，关掉时下面三项都不生效。
	Enabled bool
	// ActivityDays 清理「已归档任务」的 activity 行。正在跑/未归档的任务不受影响。
	ActivityDays int
	// TrafficDays 清理流量库里早于 N 天的 exchange（含其正文与全文索引）。
	TrafficDays int
	// DeliveryDays 清理通知投递历史（已终态的行）。
	DeliveryDays int
	// AuditDays 清理审计日志。合规保留期通常远长于活动/流量，因此独立配置。
	AuditDays int
}

// retentionPolicySnapshot 读取当前策略。任一项 <= 0 即视为不清理该类别。
func (s *Server) retentionPolicySnapshot() retentionPolicy {
	var pol retentionPolicy
	if s == nil || s.m == nil || s.m.pg == nil {
		return pol
	}
	pg := s.m.pg
	pol.Enabled = pg.GetBool(settingRetentionEnabled, false)
	if !pol.Enabled {
		return pol
	}
	pol.ActivityDays = settingInt(pg, settingRetentionActivityDays)
	pol.TrafficDays = settingInt(pg, settingRetentionTrafficDays)
	pol.DeliveryDays = settingInt(pg, settingRetentionDeliveryDays)
	pol.AuditDays = settingInt(pg, settingRetentionAuditDays)
	return pol
}

const (
	settingRetentionEnabled      = "retention.enabled"
	settingRetentionActivityDays = "retention.activity_days"
	settingRetentionTrafficDays  = "retention.traffic_days"
	settingRetentionDeliveryDays = "retention.delivery_days"
	settingRetentionAuditDays    = "retention.audit_days"

	// retentionTick 是清理周期。按天级别的留存用小时级检查足够，且启动后第一轮
	// 立即执行，便于运维改完设置马上看到效果。
	retentionTick = 6 * time.Hour
	// retentionBatch 限制单次删除行数，避免一次大事务长时间锁表。
	retentionBatch = 5000
)

// RunRetention is the background loop started by New. Exits on ctx cancellation so
// Server.Stop can join it like every other background loop.
func (s *Server) RunRetention(ctx context.Context) {
	if s == nil || s.m == nil || s.m.pg == nil {
		return
	}
	// 启动后先跑一轮：运维刚改完设置不必等一个周期。
	s.runRetentionOnce(ctx)
	t := time.NewTicker(retentionTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.runRetentionOnce(ctx)
		}
	}
}

// runRetentionOnce performs one pruning pass. Every category is independent: a
// failure in one must not stop the others, and none of them is fatal.
func (s *Server) runRetentionOnce(ctx context.Context) {
	pol := s.retentionPolicySnapshot()
	if !pol.Enabled {
		return
	}
	if pol.ActivityDays > 0 {
		if n, err := s.pruneActivity(ctx, pol.ActivityDays); err != nil {
			log.Printf("[retention] activity 清理失败: %v", err)
		} else if n > 0 {
			log.Printf("[retention] 清理 activity %d 行（早于 %d 天，仅已归档任务）", n, pol.ActivityDays)
		}
	}
	if pol.TrafficDays > 0 {
		if n, err := s.pruneTraffic(ctx, pol.TrafficDays); err != nil {
			log.Printf("[retention] traffic 清理失败: %v", err)
		} else if n > 0 {
			log.Printf("[retention] 清理 traffic %d 条（早于 %d 天）", n, pol.TrafficDays)
		}
	}
	if pol.AuditDays > 0 {
		if n, err := s.pruneAudit(ctx, pol.AuditDays); err != nil {
			log.Printf("[retention] 审计清理失败: %v", err)
		} else if n > 0 {
			log.Printf("[retention] 清理审计日志 %d 条（早于 %d 天）", n, pol.AuditDays)
		}
	}
	if pol.DeliveryDays > 0 {
		if n, err := s.pruneDeliveries(ctx, pol.DeliveryDays); err != nil {
			log.Printf("[retention] 投递历史清理失败: %v", err)
		} else if n > 0 {
			log.Printf("[retention] 清理投递历史 %d 条（早于 %d 天）", n, pol.DeliveryDays)
		}
	}
}

// pruneActivity deletes old activity rows for ARCHIVED tasks only.
//
// Scoping to archived tasks is the safety property: activity is the audit trail of
// how an intent was executed, and losing it for a live or merely-paused task means
// the operator can no longer explain what the agent did. Once a task is archived
// the operator has explicitly declared it finished.
func (s *Server) pruneActivity(ctx context.Context, days int) (int64, error) {
	cutoff := time.Now().AddDate(0, 0, -days)
	res, err := s.m.pg.ExecContext(ctx, `
DELETE FROM activity a
WHERE a.id IN (
    SELECT a2.id FROM activity a2
    JOIN explorations e ON e.id = a2.exploration_id
    JOIN tasks t ON t.exploration_id = e.id
    WHERE t.archived_at IS NOT NULL AND t.archived_at < $1
    LIMIT $2
)`, cutoff, retentionBatch)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// pruneTraffic deletes recorded exchanges older than the cutoff.
//
// The traffic store is SQLite and owns its own schema (bodies + full-text index),
// so the deletion goes through it rather than through SQL here.
func (s *Server) pruneTraffic(ctx context.Context, days int) (int64, error) {
	tr := s.m.Traffic()
	if tr == nil {
		return 0, nil
	}
	cutoff := time.Now().AddDate(0, 0, -days).Unix()
	return tr.DeleteExchangesBefore(ctx, cutoff, retentionBatch)
}

// pruneDeliveries deletes terminal notification-delivery history older than the
// cutoff. Pending/sending rows are never touched — they are still work to do.
func (s *Server) pruneDeliveries(ctx context.Context, days int) (int64, error) {
	if s.m.pg == nil {
		return 0, nil
	}
	cutoff := time.Now().AddDate(0, 0, -days)
	return s.m.pg.DeleteTerminalDeliveriesBefore(ctx, cutoff, retentionBatch)
}

// retentionStatus is what the settings page shows, so the operator can see both
// the configured policy and whether anything is actually due.
type retentionStatus struct {
	Enabled      bool  `json:"enabled"`
	ActivityDays int   `json:"activity_days"`
	TrafficDays  int   `json:"traffic_days"`
	DeliveryDays int   `json:"delivery_days"`
	AuditDays    int   `json:"audit_days"`
	ActivityRows int64 `json:"activity_rows"`
	TrafficRows  int64 `json:"traffic_rows"`
	AuditRows    int64 `json:"audit_rows"`
}

// retentionStatusSnapshot 汇总当前策略与各类数据的现有行数。
func (s *Server) retentionStatusSnapshot(ctx context.Context) retentionStatus {
	pol := s.retentionPolicySnapshot()
	st := retentionStatus{
		Enabled: pol.Enabled, ActivityDays: pol.ActivityDays,
		TrafficDays: pol.TrafficDays, DeliveryDays: pol.DeliveryDays,
		AuditDays: pol.AuditDays,
	}
	if s.m != nil && s.m.pg != nil {
		_ = s.m.pg.QueryRowContext(ctx, `SELECT count(*) FROM activity`).Scan(&st.ActivityRows)
		_ = s.m.pg.QueryRowContext(ctx, `SELECT count(*) FROM audit_logs`).Scan(&st.AuditRows)
	}
	if tr := s.m.Traffic(); tr != nil {
		if n, err := tr.Count(); err == nil {
			st.TrafficRows = int64(n)
		}
	}
	return st
}

// GET /api/settings/retention — 当前留存策略 + 各类数据现有行数。
func (s *Server) getRetentionSettings(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	writeJSON(w, 200, s.retentionStatusSnapshot(r.Context()))
}

// PUT /api/settings/retention — 更新留存策略。0 = 关闭该类别的清理。
func (s *Server) putRetentionSettings(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var body struct {
		Enabled      *bool `json:"enabled"`
		ActivityDays *int  `json:"activity_days"`
		TrafficDays  *int  `json:"traffic_days"`
		DeliveryDays *int  `json:"delivery_days"`
		AuditDays    *int  `json:"audit_days"`
	}
	if !decodeJSON(w, r, &body, maxSmallJSONBodyBytes) {
		return
	}
	// 负数一律拒绝：用户输入 -1 想要"不限"是误解，静默接受会让清理立即删除
	// 全部符合条件的数据，而 -1 天意味着"未来"——那是最危险的误操作。
	for name, v := range map[string]*int{
		"activity_days": body.ActivityDays, "traffic_days": body.TrafficDays,
		"delivery_days": body.DeliveryDays,
		"audit_days":    body.AuditDays,
	} {
		if v != nil && *v < 0 {
			writeErr(w, 400, name+" 不能为负数（0 = 不限）")
			return
		}
	}
	if body.Enabled != nil {
		if err := pg.SetBool(settingRetentionEnabled, *body.Enabled); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	for key, v := range map[string]*int{
		settingRetentionActivityDays: body.ActivityDays,
		settingRetentionTrafficDays:  body.TrafficDays,
		settingRetentionDeliveryDays: body.DeliveryDays,
		settingRetentionAuditDays:    body.AuditDays,
	} {
		if v == nil {
			continue
		}
		if err := pg.SetSetting(key, strconv.Itoa(*v)); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	writeJSON(w, 200, s.retentionStatusSnapshot(r.Context()))
}

// pruneAudit deletes audit rows older than the cutoff. Separate from the other
// categories because audit retention answers to compliance, not to disk pressure.
func (s *Server) pruneAudit(ctx context.Context, days int) (int64, error) {
	if s.m == nil || s.m.pg == nil {
		return 0, nil
	}
	return s.m.pg.DeleteAuditLogsBefore(time.Now().AddDate(0, 0, -days), retentionBatch)
}
