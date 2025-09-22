package main

import (
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/zionnode/xray-admin/internal/remote"
	"github.com/zionnode/xray-admin/internal/store"
	"github.com/zionnode/xray-admin/internal/syncer"
)

func main() {
	apiURL   := flag.String("api", "https://zioncore.site/apiv2/nodes/server-clients/", "远端 API URL")
	token    := flag.String("token", "", "固定鉴权 token（必填）")
	publicID := flag.String("public-id", "", "该 Xray 服务器的 public_id（必填）")

	xrayAddr := flag.String("xray", "127.0.0.1:1090", "Xray gRPC 地址")
	defaultProto := flag.String("proto", "vless", "默认协议 vless|vmess")
	defaultLevel := flag.Uint("level", 1, "默认 level（建议 1）")
	defaultFlow  := flag.String("flow", "", "默认 VLESS flow（普通 VLESS 留空）")

	mode := flag.String("mode", "replace", "同步模式：replace|upsert（推荐 replace）")
	dbPath := flag.String("db", "data/users.json", "本地清单 DB 路径（JSON）")
	snapDir := flag.String("snap", "data/snapshots", "快照目录")
	interval := flag.Duration("interval", 0, "轮询间隔（>0 则循环同步；例如 1m）")

	concurrency := flag.Int("concurrency", 64, "并发 worker 数（Add/Remove/Update），推荐 64~128")

	flag.Parse()
	if *token == "" || *publicID == "" {
		log.Fatal("缺少必要参数：-token / -public-id")
	}

	db, err := store.Open(*dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}

	runOnce := func() {
		log.Printf("fetching %s ...", *apiURL)
		res, err := remote.Fetch(*apiURL, *token, *publicID, *defaultProto, uint32(*defaultLevel), *defaultFlow, 15*time.Second)
		if err != nil {
			log.Printf("fetch error: %v", err)
			return
		}
		if len(res.Tags) == 0 {
			log.Printf("no tags returned; skip")
			return
		}
		log.Printf("sync to Xray(%s) tags=%v users=%d mode=%s concurrency=%d",
			*xrayAddr, res.Tags, len(res.Users), *mode, *concurrency)

		sum, err := syncer.Sync(*xrayAddr, res.Tags, res.Users, *mode, *concurrency, db, *snapDir, res.Raw)
		if err != nil {
			log.Printf("sync error: %v", err)
			return
		}
		log.Printf("SYNC DONE: added=%d updated=%d removed=%d failed=%d", sum.Added, sum.Updated, sum.Removed, sum.Failed)
	}

	runOnce()
	if *interval > 0 {
		t := time.NewTicker(*interval)
		defer t.Stop()
		for range t.C {
			runOnce()
		}
	}

	fmt.Println("OK")
}