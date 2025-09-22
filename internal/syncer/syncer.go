package syncer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zionnode/xray-admin/internal/store"
	"github.com/zionnode/xray-admin/internal/xray"
)

type Summary struct {
	Added   int `json:"added"`
	Updated int `json:"updated"`
	Removed int `json:"removed"`
	Failed  int `json:"failed"`
}

// Sync 把 target 用户集声明式同步到 Xray（按 email=UID 为键）；成功后更新本地 DB 与快照。
// concurrency 为并发 worker 数（建议 64~128）。mode: "replace"（含删除）或 "upsert"（不删）。
func Sync(apiAddr string, tags []string, target map[string]store.User, mode string,
	concurrency int, db *store.DB, snapDir string, rawJSON []byte) (Summary, error) {

	if strings.TrimSpace(mode) == "" {
		mode = "replace"
	}
	if concurrency <= 0 {
		concurrency = 1
	}
	_ = os.MkdirAll(snapDir, 0o755)
	currentJSON := filepath.Join(snapDir, "current.json")

	prev := db.Snapshot()
	adds, upds, dels := diff(prev, target, mode)

	cli, err := xray.NewClient(apiAddr, tags, 8*time.Second)
	if err != nil {
		return Summary{}, err
	}
	defer cli.Close()

	type job struct {
		typ string       // "add" | "upd" | "del"
		u   store.User   // 用于 add / del
		old store.User   // upd: 旧值
		new store.User   // upd: 新值
	}
	jobs := make(chan job, concurrency*2)

	var mu sync.Mutex
	sum := Summary{}

	worker := func(id int) {
		for j := range jobs {
			var e error
			switch j.typ {
			case "add":
				e = withRetry(3, func() error { return addUser(cli, j.u) })
				if e == nil {
					_ = db.Upsert(j.u)
					mu.Lock(); sum.Added++; mu.Unlock()
				}
			case "upd":
				e = withRetry(3, func() error {
					if err := cli.Remove(j.old.Email); err != nil { return err }
					return addUser(cli, j.new)
				})
				if e == nil {
					_ = db.Upsert(j.new)
					mu.Lock(); sum.Updated++; mu.Unlock()
				}
			case "del":
				e = withRetry(3, func() error { return cli.Remove(j.u.Email) })
				if e == nil {
					_ = db.Delete(j.u.UID)
					mu.Lock(); sum.Removed++; mu.Unlock()
				}
			}
			if e != nil {
				mu.Lock(); sum.Failed++; mu.Unlock()
			}
		}
	}

	// 起 worker
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(id int) { defer wg.Done(); worker(id) }(i + 1)
	}

	// 投喂任务：先 add，再 upd（删后加），最后 del
	for _, u := range adds {
		jobs <- job{typ: "add", u: u}
	}
	for _, p := range upds {
		jobs <- job{typ: "upd", old: p.old, new: p.new}
	}
	if strings.EqualFold(mode, "replace") {
		for _, u := range dels {
			jobs <- job{typ: "del", u: u}
		}
	}
	close(jobs)
	wg.Wait()

	// 写快照（即使有失败也记录原始输入，便于审计与重试）
	now := time.Now().Unix()
	wrap := map[string]any{"revision": now, "payload": json.RawMessage(rawJSON)}
	b, _ := json.MarshalIndent(wrap, "", "  ")
	_ = os.WriteFile(currentJSON, b, 0644)
	ts := time.Unix(now, 0).UTC().Format("20060102T150405Z")
	_ = os.WriteFile(filepath.Join(snapDir, fmt.Sprintf("snapshot-%s.json", ts)), rawJSON, 0644)

	return sum, nil
}

type updatePair struct{ old, new store.User }

func diff(prev, target map[string]store.User, mode string) (adds []store.User, upds []updatePair, dels []store.User) {
	for uid, nu := range target {
		if ou, ok := prev[uid]; !ok {
			adds = append(adds, nu)
		} else if changed(ou, nu) {
			upds = append(upds, updatePair{old: ou, new: nu})
		}
	}
	if strings.EqualFold(mode, "replace") {
		for uid, ou := range prev {
			if _, ok := target[uid]; !ok {
				dels = append(dels, ou)
			}
		}
	}
	return
}

func changed(a, b store.User) bool {
	return a.UUID != b.UUID || a.Proto != b.Proto || a.Level != b.Level || a.Flow != b.Flow
}

func addUser(cli *xray.Client, u store.User) error {
	switch u.Proto {
	case "vless":
		return cli.AddVLESS(u.Email, u.UUID, u.Level, u.Flow)
	case "vmess":
		return cli.AddVMess(u.Email, u.UUID, u.Level)
	default:
		return fmt.Errorf("unsupported proto: %s", u.Proto)
	}
}

func withRetry(n int, f func() error) error {
	var err error
	for i := 0; i < n; i++ {
		if err = f(); err == nil {
			return nil
		}
		time.Sleep(time.Duration(200*(i+1)) * time.Millisecond)
	}
	return err
}