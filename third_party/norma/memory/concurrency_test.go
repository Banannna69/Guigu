package memory

import (
	"fmt"
	"sync"
	"testing"
)

// TestConcurrentSaveSameStoreKeepsAllEntries 钉住并发写不丢条目。
//
// 每个 agent 各自 new 一个 Store 指向同一目录（宿主按 run 构造），所以锁必须按
// 目录生效而不是按实例。没有它时 rebuildIndex 会各自按自己的 scan 重写 MEMORY.md，
// 后完成的那次会把先完成的条目从索引里抹掉。
func TestConcurrentSaveSameStoreKeepsAllEntries(t *testing.T) {
	dir := t.TempDir()
	const n = 20
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// 刻意每轮用新 Store，模拟"每个 agent 一个实例"。
			st := NewStore(dir)
			if err := st.Save(Memory{
				Name:        fmt.Sprintf("entry-%02d", i),
				Description: fmt.Sprintf("第 %d 条", i),
				Type:        TypeProject,
				Content:     "content",
			}); err != nil {
				t.Errorf("Save(%d): %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	// 所有条目文件都在。
	mems, err := NewStore(dir).Scan()
	if err != nil {
		t.Fatal(err)
	}
	if len(mems) != n {
		t.Fatalf("应写入 %d 条，实际 %d 条（并发写丢失）", n, len(mems))
	}
	// 索引里也必须有全部条目——这是 rebuildIndex 竞态的直接受害者。
	idx := NewStore(dir).Index()
	for i := 0; i < n; i++ {
		want := fmt.Sprintf("entry-%02d", i)
		if !contains(idx, want) {
			t.Fatalf("索引缺少 %s（rebuildIndex 竞态）\n%s", want, idx)
		}
	}
}

func contains(hay, needle string) bool {
	return len(hay) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(hay); i++ {
			if hay[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
