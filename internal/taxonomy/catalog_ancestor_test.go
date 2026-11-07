package taxonomy

import (
	"reflect"
	"sync"
	"testing"
)

/* TX-CATALOG-CACHE：ancestorSet 缓存封装回归——所有返回值必须是缓存条目的
   独立拷贝：调用方改写返回 map 不得污染缓存或后续调用结果。
   读锁命中 / 写锁新计算两条路径可直接确定性覆盖；写锁 double-check 分支
   与读锁命中共享同一个 copyBoolSet(缓存条目) 返回，由并发测试在 -race 下
   尽力覆盖。fixture 用 smallCatalog（8 节点小图），不触发全量 Load。 */

// 期望值按 smallEdges：T 的严格祖先 = C D B E F X（不含 T 自身、不含 Z）。
func wantAncestorsT() map[string]bool {
	return map[string]bool{"C": true, "D": true, "B": true, "E": true, "F": true, "X": true}
}

// 读锁命中（缓存已填）与新计算（冷缓存）两条路径返回的都必须是独立拷贝：
// 修改返回值后，缓存与后续调用结果不受影响。
func TestAncestorSetReturnsCopyAllPaths(t *testing.T) {
	for _, tc := range []struct {
		path string
		warm bool // true：先调用一次填缓存，再断言走读锁命中路径
	}{
		{path: "新计算（冷缓存，写锁路径）"},
		{path: "读锁命中（缓存已填）", warm: true},
	} {
		c := smallCatalog() // 每个 case 一张干净的 Catalog（anc 为空）
		if tc.warm {
			c.ancestorSet("T") // 预填充缓存
		}
		got := c.ancestorSet("T")
		if !reflect.DeepEqual(got, wantAncestorsT()) {
			t.Fatalf("%s：结果 = %v，期望 %v", tc.path, got, wantAncestorsT())
		}
		// 污染返回值
		got["Z"] = true
		delete(got, "F")
		// 后续调用（此时走读锁命中缓存）不受污染影响
		again := c.ancestorSet("T")
		if !reflect.DeepEqual(again, wantAncestorsT()) {
			t.Errorf("%s：调用方改写返回 map 后缓存被污染，后续结果 = %v", tc.path, again)
		}
		// Ancestors 公开 API 同样不受影响
		if ids := c.Ancestors([]string{"T"}); len(ids) != 6 {
			t.Errorf("%s：Ancestors(T) = %v（%d 个），期望 6 个", tc.path, ids, len(ids))
		}
	}
}

// 冷缓存与热缓存（读锁命中）返回的 map 互不共享：一次修改不影响另一次结果。
func TestAncestorSetColdAndHotIndependent(t *testing.T) {
	c := smallCatalog()
	cold := c.ancestorSet("T") // 冷：新计算
	hot := c.ancestorSet("T")  // 热：读锁命中缓存
	delete(cold, "X")
	cold["Z"] = true
	if !reflect.DeepEqual(hot, wantAncestorsT()) {
		t.Errorf("冷路径返回值被改写后，热路径结果 = %v", hot)
	}
	hot["Y"] = true
	if third := c.ancestorSet("T"); !reflect.DeepEqual(third, wantAncestorsT()) {
		t.Errorf("热路径返回值被改写后，第三次结果 = %v", third)
	}
}

// nil 也安全：无先修的知识点返回空 map（非 nil），对其写入不影响缓存。
func TestAncestorSetEmptySafe(t *testing.T) {
	c := smallCatalog()
	got := c.ancestorSet("X") // X 无先修，祖先集合为空
	if got == nil {
		t.Fatal("ancestorSet(X) 返回 nil，期望非 nil 空 map")
	}
	if len(got) != 0 {
		t.Fatalf("ancestorSet(X) = %v，期望空", got)
	}
	got["Z"] = true // 调用方误写
	if again := c.ancestorSet("X"); len(again) != 0 {
		t.Errorf("空集合被调用方写入后污染缓存：%v", again)
	}
}

// 并发热路径：多 goroutine 同时冷启动同一热点 id + 各自私有改写，
// -race 下无数据竞争且每次结果都等于期望集合（拷贝语义保证；
// 并发时序可能覆盖写锁 double-check 命中分支）。
func TestAncestorSetConcurrentHotPath(t *testing.T) {
	c := smallCatalog()
	const workers = 32
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			got := c.ancestorSet("T")
			if !reflect.DeepEqual(got, wantAncestorsT()) {
				t.Errorf("worker %d：结果 = %v，期望 %v", i, got, wantAncestorsT())
				return
			}
			got["Z"] = true // 各自污染自己的拷贝
			delete(got, "E")
		}(i)
	}
	close(start)
	wg.Wait()
	if final := c.ancestorSet("T"); !reflect.DeepEqual(final, wantAncestorsT()) {
		t.Errorf("并发改写后缓存被污染：%v", final)
	}
}
