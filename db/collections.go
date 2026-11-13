// collections.go：资产合集——一次测绘收集到的资产的「显式成员清单」。
// 主要函数/类型：
//   - Collection / UpsertCollection / AddCollectionMembers / ListCollections。
//   - DeleteCollection：删除合集并清理它对任务范围的引用。
//
// 与企业（companies）的区别：企业按**规则**归并资产（rule 匹配即自动归属，且
// assets.company_id 单一归属）；合集是**显式成员列表**，只记录「这一批是某次
// 查询捞到的」，不参与资产归属判定。两者互不干扰。
//
// 与"照抄一份"的差异：删除合集时同时清理 assets.task_ids 里由它引入的任务关联。
// 只靠 ON DELETE CASCADE 清 task_scope 会让资产上残留悬空的任务 id——那个资产
// 仍会出现在任务资产列表里，但它当初被引入的理由（合集）已经不存在了。

package db

import "time"

// Collection 是一个命名的资产合集。
type Collection struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Query       string    `json:"query"`
	Source      string    `json:"source"`
	MemberCount int       `json:"member_count"`
	CreatedAt   time.Time `json:"created_at"`
}

// UpsertCollection 按规范化名（nkey）创建或更新合集，使同一查询重复同步时并进
// 同一个合集而不是不断产生新合集。返回 id。
func (d *DB) UpsertCollection(name, query, source string) (int64, error) {
	if source == "" {
		source = "fofa"
	}
	nkey := companyNKey(name) // 与公司名同一套 lowercase/trim/collapse 规范化
	var id int64
	err := d.QueryRow(`
INSERT INTO asset_collections(name, nkey, query, source)
VALUES ($1,$2,$3,$4)
ON CONFLICT (nkey) DO UPDATE SET
    name       = EXCLUDED.name,
    query      = EXCLUDED.query,
    updated_at = now()
RETURNING id`, name, nkey, query, source).Scan(&id)
	return id, err
}

// AddCollectionMembers 把资产加入合集（幂等）。不存在的资产 id 被跳过。
func (d *DB) AddCollectionMembers(collectionID int64, assetIDs []int64) error {
	if collectionID <= 0 || len(assetIDs) == 0 {
		return nil
	}
	_, err := d.Exec(`
INSERT INTO asset_collection_members(collection_id, asset_id)
SELECT $1, req.asset_id
FROM unnest($2::bigint[]) AS req(asset_id)
JOIN assets a ON a.id = req.asset_id
ON CONFLICT (collection_id, asset_id) DO NOTHING`, collectionID, assetIDs)
	return err
}

// ListCollections 返回全部合集（新→旧），各带成员数。
func (d *DB) ListCollections() ([]Collection, error) {
	rows, err := d.Query(`
SELECT c.id, c.name, c.query, c.source,
       (SELECT count(*) FROM asset_collection_members m WHERE m.collection_id = c.id),
       c.created_at
FROM asset_collections c
ORDER BY c.created_at DESC, c.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Collection{}
	for rows.Next() {
		var c Collection
		if err := rows.Scan(&c.ID, &c.Name, &c.Query, &c.Source, &c.MemberCount, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteCollection 删除合集：成员、引用它的 task_scope 行随 CASCADE 清理，
// 并额外清掉 assets.task_ids 里由它引入的任务关联，避免悬空引用。
func (d *DB) DeleteCollection(id int64) error {
	if id <= 0 {
		return nil
	}
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // 提交后为 no-op

	// 先取出该合集引入的 (任务, 资产) 对。合集删掉后就查不到了，必须先取。
	rows, err := tx.Query(`
SELECT DISTINCT ts.task_id, m.asset_id
FROM task_scope ts
JOIN asset_collection_members m ON m.collection_id = ts.collection_id
WHERE ts.kind='collection' AND ts.collection_id=$1`, id)
	if err != nil {
		return err
	}
	type pair struct{ taskID, assetID int64 }
	var pairs []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.taskID, &p.assetID); err != nil {
			rows.Close()
			return err
		}
		pairs = append(pairs, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// 解除这些资产上的任务归属 —— 但**只在没有其他来源仍把它带进这个任务时**。
	//
	// task_asset_links 的主键是 (task_id, asset_id)，source 是单值：同一资产既被企业
	// 引入又被合集引入时，后跑的 insertTaskCollections 已把 source 覆盖成
	// 'collection'。因此不能按 source 判断，只能查「删除本合集后该资产是否仍能通过
	// 别的途径进入该任务」：
	//   - 仍在某个 task_scope 匹配的范围内（企业/域名/网段/ICP/其他合集）
	//   - 或被本任务的 fact/intent 锚定过（agent 发现）
	// 只有两者都不成立，才从 task_ids 里摘掉。
	for _, p := range pairs {
		var keep bool
		if err := tx.QueryRow(`
SELECT EXISTS (
    SELECT 1
    FROM task_scope ts
    WHERE ts.task_id = $1
      AND NOT (ts.kind = 'collection' AND ts.collection_id = $3)
      AND EXISTS (SELECT 1 FROM assets a WHERE a.id = $2 AND `+taskScopeAssetMatchSQL+`)
)
OR EXISTS (
    SELECT 1
    FROM exploration_anchors ea
    JOIN exploration_nodes en ON en.id = ea.node_id
    JOIN tasks t ON t.exploration_id = en.exploration_id
    WHERE t.id = $1 AND ea.asset_id = $2
)`, p.taskID, p.assetID, id).Scan(&keep); err != nil {
			return err
		}
		if keep {
			// 仍通过其他来源在任务里：只把来源标注改回中性值，不动 task_ids。
			// 具体是哪个来源已经无法从单值 source 还原，标 system 比留着误导性的
			// 'collection' 更诚实。
			if _, err := tx.Exec(`
UPDATE task_asset_links
SET source='system', source_summary='任务执行期间自动关联', source_node_id=NULL
WHERE task_id=$1 AND asset_id=$2 AND source='collection'`, p.taskID, p.assetID); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.Exec(`
UPDATE assets
SET task_ids = array_remove(task_ids, $1)
WHERE id=$2 AND $1 = ANY(task_ids)`, p.taskID, p.assetID); err != nil {
			return err
		}
		if _, err := tx.Exec(`
DELETE FROM task_asset_links
WHERE task_id=$1 AND asset_id=$2`, p.taskID, p.assetID); err != nil {
			return err
		}
	}

	if _, err := tx.Exec(`DELETE FROM asset_collections WHERE id=$1`, id); err != nil {
		return err
	}
	return tx.Commit()
}