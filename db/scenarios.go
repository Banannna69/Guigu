// scenarios.go：场景包——行业增强层的数据层。
// 主要函数/类型：
//   - ScenarioPack / ScenarioSnapshot：场景包本身与建任务时的冻结快照。
//   - ListScenarioPacks / CreateScenarioPack / UpdateScenarioPack / DeleteScenarioPack：CRUD。
//   - AttachTaskScenarios / TaskScenarioSummaries：任务与场景的绑定与回读。
//
// 设计约束（与 task_categories 的本质区别）：
//   - 场景**只追加**，不替换。通用模板在选中场景时仍然完整生效，场景正文接在其后；
//     不选则行为与改动前逐字一致。通用能力不能因为行业化而被削弱。
//   - 可多选并叠加：「金融」+「API 安全」同时生效，按 position 顺序拼接。
//   - 建任务时冻结快照：之后编辑或删除场景包，不影响已建任务的可复现性。

package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ScenarioPack 是一个行业/场景增强包。
type ScenarioPack struct {
	ID            int64    `json:"id"`
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	PlannerAppend string   `json:"planner_append"`
	WorkerAppend  string   `json:"worker_append"`
	Skills        []string `json:"skills"`
	DisabledTools []string `json:"disabled_tools"`
	Builtin       bool     `json:"builtin"`
	CreatedAt     time.Time `json:"created_at"`
}

// ScenarioSnapshot 是某个场景包在任务创建瞬间的冻结副本。
type ScenarioSnapshot struct {
	PackID        int64    `json:"pack_id"`
	Name          string   `json:"name"`
	PlannerAppend string   `json:"planner_append"`
	WorkerAppend  string   `json:"worker_append"`
	Skills        []string `json:"skills"`
	DisabledTools []string `json:"disabled_tools"`
}

// maxScenarioAppendBytes 限制单段追加正文。场景是补充说明，不是第二份模板——
// 过长会挤占通用模板的注意力，也会让任务快照无谓膨胀。
const maxScenarioAppendBytes = 16 << 10

// NormalizeScenarioAppend 裁剪并校验一段追加正文。
func NormalizeScenarioAppend(s string) (string, error) {
	s = strings.TrimSpace(s)
	if len(s) > maxScenarioAppendBytes {
		return "", fmt.Errorf("场景追加正文超过 %d 字节", maxScenarioAppendBytes)
	}
	return s, nil
}

// cleanNameList 去空、去重、保持顺序。
func cleanNameList(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func jsonList(v []string) []byte {
	if v == nil {
		v = []string{}
	}
	b, _ := json.Marshal(v)
	return b
}

func parseJSONList(raw string) []string {
	var out []string
	if raw == "" {
		return []string{}
	}
	if json.Unmarshal([]byte(raw), &out) != nil {
		return []string{}
	}
	return out
}

// ListScenarioPacks 返回全部场景包（内置在前，其余按创建时间）。
func (d *DB) ListScenarioPacks() ([]ScenarioPack, error) {
	rows, err := d.Query(`
SELECT id, name, description, planner_append, worker_append,
       skills::text, disabled_tools::text, builtin, created_at
FROM scenario_packs
ORDER BY builtin DESC, created_at ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ScenarioPack{}
	for rows.Next() {
		var p ScenarioPack
		var skills, disabled string
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.PlannerAppend, &p.WorkerAppend,
			&skills, &disabled, &p.Builtin, &p.CreatedAt); err != nil {
			return nil, err
		}
		p.Skills = parseJSONList(skills)
		p.DisabledTools = parseJSONList(disabled)
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetScenarioPack 按 id 取一个场景包。
func (d *DB) GetScenarioPack(id int64) (*ScenarioPack, error) {
	var p ScenarioPack
	var skills, disabled string
	err := d.QueryRow(`
SELECT id, name, description, planner_append, worker_append,
       skills::text, disabled_tools::text, builtin, created_at
FROM scenario_packs WHERE id=$1`, id).
		Scan(&p.ID, &p.Name, &p.Description, &p.PlannerAppend, &p.WorkerAppend,
			&skills, &disabled, &p.Builtin, &p.CreatedAt)
	if err != nil {
		return nil, err
	}
	p.Skills = parseJSONList(skills)
	p.DisabledTools = parseJSONList(disabled)
	return &p, nil
}

// ScenarioPackInput 是创建/更新场景包的入参。
type ScenarioPackInput struct {
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	PlannerAppend string   `json:"planner_append"`
	WorkerAppend  string   `json:"worker_append"`
	Skills        []string `json:"skills"`
	DisabledTools []string `json:"disabled_tools"`
}

// normalizeScenarioInput 归一入参。名称是唯一键的一部分，必须非空。
func normalizeScenarioInput(in ScenarioPackInput) (ScenarioPackInput, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return in, fmt.Errorf("场景包名称不能为空")
	}
	var err error
	if in.PlannerAppend, err = NormalizeScenarioAppend(in.PlannerAppend); err != nil {
		return in, fmt.Errorf("planner 追加正文: %w", err)
	}
	if in.WorkerAppend, err = NormalizeScenarioAppend(in.WorkerAppend); err != nil {
		return in, fmt.Errorf("worker 追加正文: %w", err)
	}
	in.Name = name
	in.Description = strings.TrimSpace(in.Description)
	in.Skills = cleanNameList(in.Skills)
	in.DisabledTools = cleanNameList(in.DisabledTools)
	return in, nil
}

// CreateScenarioPack 新建场景包。同名（nkey）冲突时报错而不是静默覆盖——
// 用户点「新建」时预期是新的一条，覆盖掉旧的会丢数据。
func (d *DB) CreateScenarioPack(in ScenarioPackInput) (int64, error) {
	in, err := normalizeScenarioInput(in)
	if err != nil {
		return 0, err
	}
	var id int64
	err = d.QueryRow(`
INSERT INTO scenario_packs(name, nkey, description, planner_append, worker_append, skills, disabled_tools)
VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7::jsonb)
RETURNING id`,
		in.Name, companyNKey(in.Name), in.Description, in.PlannerAppend, in.WorkerAppend,
		string(jsonList(in.Skills)), string(jsonList(in.DisabledTools))).Scan(&id)
	if err != nil && strings.Contains(err.Error(), "duplicate key") {
		return 0, fmt.Errorf("已存在同名场景包：%s", in.Name)
	}
	return id, err
}

// UpdateScenarioPack 更新场景包（内置包也可改正文，但内置标记保留）。
func (d *DB) UpdateScenarioPack(id int64, in ScenarioPackInput) error {
	in, err := normalizeScenarioInput(in)
	if err != nil {
		return err
	}
	res, err := d.Exec(`
UPDATE scenario_packs
SET name=$2, nkey=$3, description=$4, planner_append=$5, worker_append=$6,
    skills=$7::jsonb, disabled_tools=$8::jsonb, updated_at=now()
WHERE id=$1`,
		id, in.Name, companyNKey(in.Name), in.Description, in.PlannerAppend, in.WorkerAppend,
		string(jsonList(in.Skills)), string(jsonList(in.DisabledTools)))
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			return fmt.Errorf("已存在同名场景包：%s", in.Name)
		}
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("场景包不存在")
	}
	return nil
}

// DeleteScenarioPack 删除场景包。已建任务的快照不受影响（快照是独立副本）。
func (d *DB) DeleteScenarioPack(id int64) error {
	_, err := d.Exec(`DELETE FROM scenario_packs WHERE id=$1`, id)
	return err
}

// MaxTaskScenarioCount 限制一个任务能挂多少个场景包。多选是为了叠加，
// 不是让人挂二十个——拼接过长会淹没通用模板。
const MaxTaskScenarioCount = 6

// AttachTaskScenarios 在任务创建事务内绑定场景包并冻结快照。
func AttachTaskScenarios(tx *sql.Tx, taskID int64, packIDs []int64) error {
	if len(packIDs) == 0 {
		return nil
	}
	if len(packIDs) > MaxTaskScenarioCount {
		return fmt.Errorf("最多选择 %d 个场景包", MaxTaskScenarioCount)
	}
	for pos, pid := range packIDs {
		if pid <= 0 {
			continue
		}
		// 在事务内读包并写快照：保证快照与包内容一致。
		var name, plannerAppend, workerAppend, skills, disabled string
		if err := tx.QueryRow(`
SELECT name, planner_append, worker_append, skills::text, disabled_tools::text
FROM scenario_packs WHERE id=$1`, pid).Scan(&name, &plannerAppend, &workerAppend, &skills, &disabled); err != nil {
			return fmt.Errorf("场景包 #%d 不存在", pid)
		}
		snap := ScenarioSnapshot{
			PackID: pid, Name: name,
			PlannerAppend: plannerAppend, WorkerAppend: workerAppend,
			Skills: parseJSONList(skills), DisabledTools: parseJSONList(disabled),
		}
		blob, _ := json.Marshal(snap)
		if _, err := tx.Exec(`
INSERT INTO task_scenarios(task_id, pack_id, snapshot, position)
VALUES ($1,$2,$3::jsonb,$4)
ON CONFLICT (task_id, pack_id) DO NOTHING`, taskID, pid, string(blob), pos); err != nil {
			return err
		}
	}
	return nil
}

// taskScenarioRows 读取任务绑定的场景快照（按 position 顺序）。
func (d *DB) taskScenarioRows(taskID int64) ([]ScenarioSnapshot, error) {
	rows, err := d.Query(`
SELECT snapshot::text FROM task_scenarios WHERE task_id=$1 ORDER BY position, pack_id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ScenarioSnapshot{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var s ScenarioSnapshot
		if json.Unmarshal([]byte(raw), &s) != nil {
			continue
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// TaskScenarioAppend 返回该任务全部场景的**拼接追加正文**（按 position 顺序）。
//
// agent 取 "planner" 或 "worker"。没有绑定场景时返回空串——调用方据此完全不追加，
// 行为与改动前逐字一致。
func (d *DB) TaskScenarioAppend(taskID int64, agent string) string {
	snaps, err := d.taskScenarioRows(taskID)
	if err != nil || len(snaps) == 0 {
		return ""
	}
	var b strings.Builder
	for _, s := range snaps {
		var body string
		switch agent {
		case "planner":
			body = s.PlannerAppend
		case "worker":
			body = s.WorkerAppend
		default:
			continue
		}
		body = strings.TrimSpace(body)
		if body == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(body)
	}
	return b.String()
}

// TaskScenarioSkills 返回该任务场景带来的额外 skill 目录名（去重）。
func (d *DB) TaskScenarioSkills(taskID int64) []string {
	snaps, err := d.taskScenarioRows(taskID)
	if err != nil {
		return nil
	}
	var all []string
	for _, s := range snaps {
		all = append(all, s.Skills...)
	}
	return cleanNameList(all)
}

// TaskScenarioDisabledTools 返回该任务场景要求禁用的工具名前缀（去重）。
func (d *DB) TaskScenarioDisabledTools(taskID int64) []string {
	snaps, err := d.taskScenarioRows(taskID)
	if err != nil {
		return nil
	}
	var all []string
	for _, s := range snaps {
		all = append(all, s.DisabledTools...)
	}
	return cleanNameList(all)
}

// TaskScenarioSummaries 返回任务的场景名列表（供 UI 展示）。
func (d *DB) TaskScenarioSummaries(taskID int64) []ScenarioSnapshot {
	snaps, err := d.taskScenarioRows(taskID)
	if err != nil {
		return nil
	}
	return snaps
}
// SeedBuiltinScenarios 把内置场景包幂等播种进库。
//
// ON CONFLICT (nkey) DO NOTHING：用户改过或删过的场景包不会被重启复活，
// 与拦截规则、技能可见性同一套策略。
func (d *DB) SeedBuiltinScenarios(seeds []BuiltinScenarioSeedRow) error {
	for _, in := range seeds {
		if _, err := d.Exec(`
INSERT INTO scenario_packs(name, nkey, description, planner_append, worker_append, skills, disabled_tools, builtin)
VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7::jsonb,true)
ON CONFLICT (nkey) DO NOTHING`,
			in.Name, in.NKey, in.Description, in.PlannerAppend, in.WorkerAppend,
			string(jsonList(in.Skills)), string(jsonList(in.DisabledTools))); err != nil {
			return err
		}
	}
	return nil
}

// BuiltinScenarioSeedRow 是播种内置场景包的入参（server 侧从 agent 包转换而来）。
type BuiltinScenarioSeedRow struct {
	NKey          string
	Name          string
	Description   string
	PlannerAppend string
	WorkerAppend  string
	Skills        []string
	DisabledTools []string
}
