package syncer

import (
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

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Summary 用于最终统计输出
type Summary struct {
	Added, Updated, Removed, Failed int64

	// 幂等统计（不算入 Added/Removed/Failed）：
	SkipAddExist   int64 // add 时 already exists
	SkipDelMissing int64 // del/upd-remove 时 not found
}

// Sync
// - xrayAddr: gRPC 地址（host:port）
// - tags:     目标 inbound tag 列表（本次只对这些 tag 同步）
// - users:    远端“权威清单”，key=UID（email），value=User
// - mode:     "replace" | "upsert"
// - concurrency: worker 并发
// - reseed:   true 时对 users 中所有用户执行一次 Add（已存在跳过），不做删除
// - idemMode: "skip"(默认) | "success" | "fail" —— 幂等情况的计数策略
// - db:       本地 DB（保存权威清单）
// - snapDir/raw: 保存原始快照
func Sync(xrayAddr string, tags []string, users map[string]store.User,
	mode string, concurrency int, reseed bool,
	idemMode string,
	db *store.DB, snapDir string, raw []byte,
) (*Summary, error) {

	sum := &Summary{}

	// 1) 快照落盘（尽量不影响主流程，失败仅告警）
	if len(raw) > 0 && snapDir != "" {
		_ = os.MkdirAll(snapDir, 0o755)
		fn := filepath.Join(snapDir, time.Now().Format("20060102-150405")+".json")
		if err := os.WriteFile(fn, raw, 0o644); err != nil {
			log.Printf("warn: write snapshot failed: %v", err)
		}
	}

	// 2) 打开 Xray 客户端
	if len(tags) == 0 {
		log.Printf("no tags to sync, skip")
		return sum, nil
	}
	cli, err := xray.NewClient(xrayAddr, tags, 15*time.Second)
	if err != nil {
		return sum, fmt.Errorf("dial xray %s failed: %w", xrayAddr, err)
	}
	defer cli.Close()

	// 3) 读取本地权威清单
	have, err := db.Load()
	if err != nil {
		return sum, fmt.Errorf("db load failed: %w", err)
	}

	// 4) 计算差异
	adds, upds, dels := plan(have, users, mode, reseed)

	totalJobs := len(adds) + len(upds) + len(dels)
	if totalJobs == 0 {
		log.Printf("nothing to do (adds=0 upds=0 dels=0)")
		// 仍然写回“最新权威清单”
		if err := db.Save(users); err != nil {
			log.Printf("warn: db save failed: %v", err)
		}
		return sum, nil
	}

	log.Printf("plan: adds=%d upds=%d dels=%d (mode=%s reseed=%v)", len(adds), len(upds), len(dels), mode, reseed)

	// 5) 并发执行
	type job struct {
		typ string     // "add" | "del" | "upd"
		u   store.User // upd 也要带上用户，便于日志/分类
	}

	jobCh := make(chan job, totalJobs)
	var wg sync.WaitGroup
	var done int64

	// 幂等识别 + 计数
	recordFail := func(op string, u store.User, err error) {
		atomic.AddInt64(&sum.Failed, 1)
		// 尽力打印出 gRPC code
		if st, ok := status.FromError(err); ok {
			log.Printf("FAIL op=%s proto=%s uid=%s email=%s code=%s msg=%q",
				op, u.Proto, u.UID, u.Email, st.Code(), st.Message())
		} else {
			log.Printf("FAIL op=%s proto=%s uid=%s email=%s err=%v",
				op, u.Proto, u.UID, u.Email, err)
		}
	}

	handleIdempotent := func(kind string, u store.User, err error) bool {
		// 返回 true 表示“此错误已处理完毕（按 skip/success 策略计数），外层无需再按失败处理”
		if err == nil {
			return false
		}
		if kind == "add" && isAlreadyExists(err) {
			switch idemMode {
			case "skip":
				atomic.AddInt64(&sum.SkipAddExist, 1)
				log.Printf("SKIP op=add proto=%s uid=%s email=%s reason=already_exists", u.Proto, u.UID, u.Email)
				return true
			case "success":
				atomic.AddInt64(&sum.Added, 1)
				log.Printf("OK(op=add-exist) proto=%s uid=%s email=%s", u.Proto, u.UID, u.Email)
				return true
			}
			// "fail": 继续外层失败计数
		}
		if (kind == "del" || kind == "upd-remove") && isNotFound(err) {
			switch idemMode {
			case "skip":
				atomic.AddInt64(&sum.SkipDelMissing, 1)
				log.Printf("SKIP op=%s proto=%s uid=%s email=%s reason=not_found", kind, u.Proto, u.UID, u.Email)
				return true
			case "success":
				atomic.AddInt64(&sum.Removed, 1)
				log.Printf("OK(op=%s-miss) proto=%s uid=%s email=%s", kind, u.Proto, u.UID, u.Email)
				return true
			}
			// "fail": 继续外层失败计数
		}
		return false
	}

	worker := func() {
		defer wg.Done()
		for j := range jobCh {
			switch j.typ {
			case "add":
				var err error
				if j.u.Proto == "vless" {
					err = cli.AddVLESS(j.u.Email, j.u.UUID, j.u.Level, j.u.Flow)
				} else {
					err = cli.AddVMess(j.u.Email, j.u.UUID, j.u.Level)
				}
				if err != nil {
					if !handleIdempotent("add", j.u, err) {
						recordFail("add", j.u, err)
					}
				} else {
					atomic.AddInt64(&sum.Added, 1)
				}

			case "del":
				if err := cli.Remove(j.u.Email); err != nil {
					if !handleIdempotent("del", j.u, err) {
						recordFail("del", j.u, err)
					}
				} else {
					atomic.AddInt64(&sum.Removed, 1)
				}

			case "upd":
				// 先删后加（两步各自应用幂等策略）
				if err := cli.Remove(j.u.Email); err != nil {
					if !handleIdempotent("upd-remove", j.u, err) {
						recordFail("upd-remove", j.u, err)
					}
				} else {
					atomic.AddInt64(&sum.Removed, 1)
				}
				var err2 error
				if j.u.Proto == "vless" {
					err2 = cli.AddVLESS(j.u.Email, j.u.UUID, j.u.Level, j.u.Flow)
				} else {
					err2 = cli.AddVMess(j.u.Email, j.u.UUID, j.u.Level)
				}
				if err2 != nil {
					if !handleIdempotent("upd-add", j.u, err2) {
						recordFail("upd-add", j.u, err2)
					}
				} else {
					atomic.AddInt64(&sum.Added, 1)
					atomic.AddInt64(&sum.Updated, 1)
				}
			}

			// 进度日志
			cur := atomic.AddInt64(&done, 1)
			if cur == int64(totalJobs) || cur%200 == 0 {
				perc := float64(cur) * 100 / float64(totalJobs)
				log.Printf("progress: %d/%d (%.1f%%) added=%d updated=%d removed=%d failed=%d",
					cur, totalJobs, perc,
					sum.Added, sum.Updated, sum.Removed, sum.Failed)
			}
		}
	}

	if concurrency <= 0 {
		concurrency = 1
	}
	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go worker()
	}

	// 投递任务（顺序无要求）
	for _, u := range adds {
		jobCh <- job{typ: "add", u: u}
	}
	for _, u := range upds {
		jobCh <- job{typ: "upd", u: u}
	}
	for _, u := range dels {
		jobCh <- job{typ: "del", u: u}
	}

	close(jobCh)
	wg.Wait()

	// 6) 写回最新权威清单
	if err := db.Save(users); err != nil {
		log.Printf("warn: db save failed: %v", err)
	}

	total := int64(totalJobs)
	log.Printf("SYNC SUMMARY: added=%d updated=%d removed=%d failed=%d skipped=%d (add-exist=%d, del-miss=%d) total=%d",
		sum.Added, sum.Updated, sum.Removed, sum.Failed,
		sum.SkipAddExist+sum.SkipDelMissing,
		sum.SkipAddExist, sum.SkipDelMissing,
		total,
	)

	return sum, nil
}

// ---------- 内部工具 ----------

// 计算差异集
func plan(have, want map[string]store.User, mode string, reseed bool) (adds, upds, dels []store.User) {
	if reseed {
		// 只做“全量 Add”（已存在由上层幂等策略处理）
		adds = make([]store.User, 0, len(want))
		for _, u := range want {
			adds = append(adds, u)
		}
		return
	}

	// want 中有而 have 没有 → add
	// 都有但字段变了 → upd
	for uid, wu := range want {
		if hu, ok := have[uid]; !ok {
			adds = append(adds, wu)
		} else if !userEqual(hu, wu) {
			upds = append(upds, wu)
		}
	}

	// replace 才删除：have 中有而 want 没有 → del
	if strings.EqualFold(mode, "replace") {
		for uid, hu := range have {
			if _, ok := want[uid]; !ok {
				dels = append(dels, hu)
			}
		}
	}
	return
}

// 判断两个用户是否等价（用于是否需要 upd）
func userEqual(a, b store.User) bool {
	if a.Proto != b.Proto {
		return false
	}
	if a.UUID != b.UUID || a.Level != b.Level {
		return false
	}
	// VLESS 的 flow 也要比对（VMess 忽略）
	if a.Proto == "vless" && strings.TrimSpace(a.Flow) != strings.TrimSpace(b.Flow) {
		return false
	}
	// Email/UID 做键，通常相同；不作为差异触发字段
	return true
}

// 幂等识别（不同 Xray 版本可能把 not found/exist 塞在 Unknown 里）
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	if st, ok := status.FromError(err); ok {
		if st.Code() == codes.NotFound {
			return true
		}
		msg := strings.ToLower(st.Message())
		if strings.Contains(msg, "not found") {
			return true
		}
	}
	return strings.Contains(strings.ToLower(err.Error()), "not found")
}

func isAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	if st, ok := status.FromError(err); ok {
		if st.Code() == codes.AlreadyExists {
			return true
		}
		msg := strings.ToLower(st.Message())
		if strings.Contains(msg, "already exists") {
			return true
		}
	}
	return strings.Contains(strings.ToLower(err.Error()), "already exists")
}