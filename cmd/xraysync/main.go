package main

import (
	"flag"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/zionnode/xray-admin/internal/remote"
	"github.com/zionnode/xray-admin/internal/store"
	"github.com/zionnode/xray-admin/internal/syncer"
)

func main() {
	// 远端 API
	apiURL   := flag.String("api", "http://127.0.0.1:8080/apiv2/nodes/server-clients/", "远端 API URL")
	token    := flag.String("token", "", "固定鉴权 token（必填）")
	publicID := flag.String("public-id", "", "该 Xray 服务器的 public_id（必填）")

	// Xray gRPC 与默认
	xrayAddr := flag.String("xray", "127.0.0.1:1090", "Xray gRPC 地址（host:port）")
	defLevel := flag.Uint("level", 1, "默认 level（建议 1）")
	defFlow  := flag.String("flow", "", "默认 VLESS flow（普通 VLESS 留空；Vision 用 xtls-rprx-vision）")

	// 同步模式与存储
	mode    := flag.String("mode", "replace", "同步模式：replace | upsert（replace 会删除目标外的用户）")
	dbPath  := flag.String("db", "data/users.json", "本地清单 DB 路径（基名；会自动拆分为 .vless/.vmess）")
	snapDir := flag.String("snap", "data/snapshots", "快照目录（保存远端原始 JSON）")

	// 运行控制
	interval    := flag.Duration("interval", 0, "轮询间隔（>0 则循环同步，如 1m）")
	concurrency := flag.Int("concurrency", 64, "并发 worker 数（Add/Update/Delete）")
	reseed      := flag.Bool("reseed", false, "自愈模式：对目标集合执行 Add（已存在跳过），修复 Xray 内存态丢失")

	flag.Parse()
	if *token == "" || *publicID == "" {
		log.Fatal("缺少必要参数：-token / -public-id")
	}

	// helper：从基路径派生 .vless/.vmess 两个文件
	suff := func(base, suffix string) string {
		if strings.HasSuffix(base, ".json") {
			return strings.TrimSuffix(base, ".json") + "." + suffix + ".json"
		}
		return base + "." + suffix + ".json"
	}
	dbPathV := suff(*dbPath, "vless")
	dbPathM := suff(*dbPath, "vmess")

	// 打开两个 DB（分别记录两套权威清单，互不覆盖）
	dbV, err := store.Open(dbPathV)
	if err != nil { log.Fatalf("open db vless: %v", err) }
	dbM, err := store.Open(dbPathM)
	if err != nil { log.Fatalf("open db vmess: %v", err) }

	buildUsers := func(clients []remote.ClientLite, proto string) map[string]store.User {
		out := make(map[string]store.User, len(clients))
		for _, c := range clients {
			if c.Email == "" || c.ID == "" { continue }
			u := store.User{
				UID:   c.Email,
				Email: c.Email,
				UUID:  c.ID,
				Proto: proto,
				Level: uint32(*defLevel),
				Flow:  "",
			}
			if proto == "vless" {
				u.Flow = *defFlow // 仅 vless 有 flow 概念
			}
			out[c.Email] = u
		}
		return out
	}

	runOnce := func() {
		log.Printf("fetching %s ...", *apiURL)
		res, err := remote.Fetch(*apiURL, *token, *publicID, 15*time.Second)
		if err != nil {
			log.Printf("fetch error: %v", err)
			return
		}
		// 快速提示返回了什么 tags
		log.Printf("remote tags: vless=%v vmess=%v (clients=%d)", res.TagsVLESS, res.TagsVMESS, len(res.Clients))

		// VLESS 同步
		if len(res.TagsVLESS) > 0 {
			usersV := buildUsers(res.Clients, "vless")
			log.Printf("sync VLESS → Xray(%s), tags=%v, users=%d, mode=%s, concurrency=%d, reseed=%v",
				*xrayAddr, res.TagsVLESS, len(usersV), *mode, *concurrency, *reseed)

			sum, err := syncer.Sync(*xrayAddr, res.TagsVLESS, usersV, *mode, *concurrency, *reseed, dbV, *snapDir, res.Raw)
			if err != nil {
				log.Printf("sync VLESS error: %v", err)
			} else {
				log.Printf("SYNC VLESS DONE: added=%d updated=%d removed=%d failed=%d",
					sum.Added, sum.Updated, sum.Removed, sum.Failed)
			}
		}

		// VMess 同步
		if len(res.TagsVMESS) > 0 {
			usersM := buildUsers(res.Clients, "vmess")
			log.Printf("sync VMESS → Xray(%s), tags=%v, users=%d, mode=%s, concurrency=%d, reseed=%v",
				*xrayAddr, res.TagsVMESS, len(usersM), *mode, *concurrency, *reseed)

			sum, err := syncer.Sync(*xrayAddr, res.TagsVMESS, usersM, *mode, *concurrency, *reseed, dbM, *snapDir, res.Raw)
			if err != nil {
				log.Printf("sync VMESS error: %v", err)
			} else {
				log.Printf("SYNC VMESS DONE: added=%d updated=%d removed=%d failed=%d",
					sum.Added, sum.Updated, sum.Removed, sum.Failed)
			}
		}

		if len(res.TagsVLESS) == 0 && len(res.TagsVMESS) == 0 {
			log.Printf("no tags in remote response; nothing to do")
		}
	}

	// 先跑一次
	runOnce()

	// 周期轮询
	if *interval > 0 {
		t := time.NewTicker(*interval)
		defer t.Stop()
		for range t.C {
			runOnce()
		}
	}

	fmt.Println("OK (snapshots →", filepath.Clean(*snapDir)+")")
}