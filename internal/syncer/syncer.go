package syncer

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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

	totalJobs := len(adds) + len(upds) + len(dels)
	var processed int64
	var okAdd int64
	var okUpd int64
	var okDel int64
	var failed int64

	// 进度 ticker：每 1 秒打印一次当前进度
	stopProg := make(chan struct{})
	go func() {
		if totalJobs == 0 {
			return
		}
		tk := time.NewTicker(1 * time.Second)
		defer tk.Stop()
		for {
			select {
			case <-tk.C:
				p := atomic.LoadInt64(&processed)
				a := atomic.LoadInt64(&okAdd)
				u := atomic.LoadInt64(&okUpd)
				d := atomic.LoadInt64(&okDel)
				f := atomic.LoadInt64(&failed)
				log.Printf("progress: %d/%d (%.1f%%) added=%d updated=%d removed=%d failed=%d",
					p, totalJobs, 100*float64(p)/float64(totalJobs), a, u, d, f)
			case <-stopProg:
				return
			}
		}
	}()

	var wg sync.WaitGroup
	worker := func(id int) {
		for j := range jobs {
			var e error
			switch j.typ {
			case "add":
				e = withRetry(3, func() error { return addUser(cli, j.u) })
				if e == nil {
					_ = db.Upsert(j.u)
					atomic.AddInt64(&okAdd, 1)
				}
			case "upd":
				e = withRetry(3, func() error {
					if err := cli.Remove(j.old.Email); err != nil {
						return err
					}
					return addUser(cli, j.new)
				})
				if e == nil {
					_ = db.Upsert(j.new)
					atomic.AddInt64(&okUpd, 1)
				}
			case "del":
				e = withRetry(3, func() error { return cli.Remove(j.u.Email) })
				if e == nil {
					_ = db.Delete(j.u.UID)
					atomic.AddInt64(&okDel, 1)
				}
			}
			if e != nil {
				atomic.AddInt64(&failed, 1)
			}

			// 里程碑日志：每处理 100 条也打一次
			n := atomic.AddInt64(&processed, 1)
			if totalJobs > 0 && n%100 == 0 {
				a := atomic.LoadInt64(&okAdd)
				u := atomic.LoadInt64(&okUpd)
				d := atomic.LoadInt64(&okDel)
				f := atomic.LoadInt64(&failed)
				log.Printf("progress: %d/%d (%.1f%%) added=%d updated=%d removed=%d failed=%d",
					n, totalJobs, 100*float64(n)/float64(totalJobs), a, u, d, f)
			}
		}
		wg.Done()
	}

	// 起 worker
	if totalJobs > 0 {
		for i := 0; i < concurrency; i++ {
			wg.Add(1)
			go worker(i + 1)
		}
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
	close(stopProg)

	// 写快照（即使有失败也记录原始输入，便于审计与重试）
	now := time.Now().Unix()
	wrap := map[string]any{"revision": now, "payload": json.RawMessage(rawJSON)}
	b, _ := json.MarshalIndent(wrap, "", "  ")
	_ = os.WriteFile(currentJSON, b, 0644)
	ts := time.Unix(now, 0).UTC().Format("20060102T150405Z")
	_ = os.WriteFile(filepath.Join(snapDir, fmt.Sprintf("snapshot-%s.json", ts)), rawJSON, 0644)

	// 汇总
	sum := Summary{
		Added:   int(atomic.LoadInt64(&okAdd)),
		Updated: int(atomic.LoadInt64(&okUpd)),
		Removed: int(atomic.LoadInt64(&okDel)),
		Failed:  int(atomic.LoadInt64(&failed)),
	}
	log.Printf("SYNC SUMMARY: added=%d updated=%d removed=%d failed=%d (total=%d)",
		sum.Added, sum.Updated, sum.Removed, sum.Failed, totalJobs)

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