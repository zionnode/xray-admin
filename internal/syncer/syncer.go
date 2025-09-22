package syncer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
// mode: "replace"（推荐）或 "upsert"
func Sync(apiAddr string, tags []string, target map[string]store.User, mode string,
	db *store.DB, snapDir string, rawJSON []byte) (Summary, error) {

	if strings.TrimSpace(mode) == "" {
		mode = "replace"
	}
	_ = os.MkdirAll(snapDir, 0755)
	currentJSON := filepath.Join(snapDir, "current.json")

	prev := db.Snapshot() // 旧权威清单（由我们维护）
	adds, upds, dels := diff(prev, target, mode)

	cli, err := xray.NewClient(apiAddr, tags, 8*time.Second)
	if err != nil {
		return Summary{}, err
	}
	defer cli.Close()

	var sum Summary

	// 新增
	for _, u := range adds {
		if err := addUser(cli, u); err != nil {
			sum.Failed++
			continue
		}
		if err := db.Upsert(u); err != nil {
			sum.Failed++
			continue
		}
		sum.Added++
	}

	// 更新（删后加；失败尝试回滚）
	for _, p := range upds {
		if err := cli.Remove(p.old.Email); err != nil {
			sum.Failed++
			continue
		}
		if err := addUser(cli, p.new); err != nil {
			_ = addUser(cli, p.old) // 回滚
			sum.Failed++
			continue
		}
		_ = db.Upsert(p.new)
		sum.Updated++
	}

	// 删除（replace 模式）
	if strings.EqualFold(mode, "replace") {
		for _, u := range dels {
			if err := cli.Remove(u.Email); err != nil {
				sum.Failed++
				continue
			}
			_ = db.Delete(u.UID)
			sum.Removed++
		}
	}

	// 保存当前请求的原始 JSON（带 revision）
	now := time.Now().Unix()
	wrap := map[string]any{"revision": now, "payload": json.RawMessage(rawJSON)}
	b, _ := json.MarshalIndent(wrap, "", "  ")
	_ = os.WriteFile(currentJSON, b, 0644)

	// 额外保存一份带时间戳的快照
	ts := time.Unix(now, 0).UTC().Format("20060102T150405Z")
	_ = os.WriteFile(filepath.Join(snapDir, fmt.Sprintf("snapshot-%s.json", ts)), rawJSON, 0644)

	return sum, nil
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