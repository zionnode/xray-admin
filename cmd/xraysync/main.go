package main

import (
	"flag"
	"fmt"
	"log"
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

	// Xray gRPC 与用户默认属性
	xrayAddr     := flag.String("xray", "127.0.0.1:1090", "Xray gRPC 地址（host:port）")
	defaultProto := flag.String("proto", "vless", "默认协议：vless | vmess")
	defaultLevel := flag.Uint("level", 1, "默认 level（建议 1）")
	defaultFlow  := flag.String("flow", "", "默认 VLESS flow（普通 VLESS 请留空；Vision 用 xtls-rprx-vision）")

	// 同步模式与本地存储
	mode    := flag.String("mode", "replace", "同步模式：replace | upsert（replace 会删除目标外的用户）")
	dbPath  := flag.String("db", "data/users.json", "本地清单 DB 路径（JSON）")
	snapDir := flag.String("snap", "data/snapshots", "快照目录（保存远端原始 JSON）")

	// 运行控制
	interval    := flag.Duration("interval", 0, "轮询间隔（>0 则循环同步，如 1m）")
	concurrency := flag.Int("concurrency", 64, "并发 worker 数（Add/Update/Delete），建议大机型 128~192，小机型 4~16")
	reseed      := flag.Bool("reseed", false, "自愈模式：对所有目标用户执行 Add（已存在则跳过），修复 Xray 内存态丢失")

	// 覆盖远端 tags（可选）
	overrideTags := flag.String("tags", "", "用逗号分隔的 inbound tags（若设置则覆盖远端返回的 tags）")

	flag.Parse()

	// 基本校验
	if *token == "" || *publicID == "" {
		log.Fatal("缺少必要参数：-token / -public-id")
	}

	// 打开本地“权威清单”DB
	db, err := store.Open(*dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}

	splitCSV := func(s string) []string {
		if strings.TrimSpace(s) == "" {
			return nil
		}
		parts := strings.Split(s, ",")
		var out []string
		for _, p := range parts {
			if q := strings.TrimSpace(p); q != "" {
				out = append(out, q)
			}
		}
		return out
	}

	runOnce := func() {
		log.Printf("fetching %s ...", *apiURL)
		res, err := remote.Fetch(
			*apiURL,
			*token,
			*publicID,
			*defaultProto,
			uint32(*defaultLevel),
			*defaultFlow,
			15*time.Second,
		)
		if err != nil {
			log.Printf("fetch error: %v", err)
			return
		}

		// tags 决定：优先使用本地覆盖；否则用远端；二者都没有则跳过
		useTags := splitCSV(*overrideTags)
		if len(useTags) == 0 {
			useTags = res.Tags
		}
		if len(useTags) == 0 {
			log.Printf("no tags provided (neither local -tags nor remote tags). skip this round")
			return
		}

		log.Printf("sync to Xray(%s) tags=%v users=%d mode=%s concurrency=%d reseed=%v",
			*xrayAddr, useTags, len(res.Users), *mode, *concurrency, *reseed)

		sum, err := syncer.Sync(
			*xrayAddr,
			useTags,
			res.Users,
			*mode,
			*concurrency,
			*reseed,
			db,
			*snapDir,
			res.Raw,
		)
		if err != nil {
			log.Printf("sync error: %v", err)
			return
		}
		log.Printf("SYNC DONE: added=%d updated=%d removed=%d failed=%d",
			sum.Added, sum.Updated, sum.Removed, sum.Failed)
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

	fmt.Println("OK")
}