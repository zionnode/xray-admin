package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/zionnode/xray-admin/internal/batch"
	"github.com/zionnode/xray-admin/internal/xray"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		return
	}
	switch os.Args[1] {
	case "add-vless":
		addVLESS()
	case "add-vmess":
		addVMess()
	case "del":
		delUser()
	case "bulk-add":
		bulkAdd()
	default:
		usage()
	}
}

func usage() {
	fmt.Println(`xrayctl - Xray gRPC 管理工具

子命令:
  add-vless   添加 VLESS 用户到指定 inbound(们)
  add-vmess   添加 VMess 用户到指定 inbound(们)
  del         按 email 删除用户（会对所有指定 inbound 同步执行）
  bulk-add    批量添加（CSV）

示例:
  xrayctl add-vless -addr 127.0.0.1:1090 -tags "in-1,in-2" -email a@b -uuid <uuid> -flow ""
  xrayctl del -addr 127.0.0.1:1090 -tags "in-1" -email a@b
  xrayctl bulk-add -addr 127.0.0.1:1090 -tags "in-1,in-2" -file assets/users.example.csv -concurrency 64
`)
}

func mustClient(addr string, tags []string, timeout time.Duration) *xray.Client {
	cli, err := xray.NewClient(addr, tags, timeout)
	if err != nil {
		log.Fatalf("dial: %v", err)
	}
	return cli
}

func parseTags(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if q := strings.TrimSpace(p); q != "" {
			out = append(out, q)
		}
	}
	return out
}

func addVLESS() {
	fs := flag.NewFlagSet("add-vless", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:1090", "Xray gRPC 地址")
	tagsS := fs.String("tags", "", "逗号分隔的 inbound tags（必填，可多个）")
	email := fs.String("email", "", "用户 email")
	uuid := fs.String("uuid", "", "VLESS UUID")
	level := fs.Uint("level", 0, "level")
	flow := fs.String("flow", "", "VLESS flow（普通 VLESS 留空）")
	timeout := fs.Duration("timeout", 8*time.Second, "RPC 超时")
	_ = fs.Parse(os.Args[2:])
	if *tagsS == "" || *email == "" || *uuid == "" {
		log.Fatal("缺少 -tags / -email / -uuid")
	}
	cli := mustClient(*addr, parseTags(*tagsS), *timeout)
	defer cli.Close()
	if err := cli.AddVLESS(*email, *uuid, uint32(*level), *flow); err != nil {
		log.Fatalf("添加失败: %v", err)
	}
	fmt.Println("OK")
}

func addVMess() {
	fs := flag.NewFlagSet("add-vmess", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:1090", "Xray gRPC 地址")
	tagsS := fs.String("tags", "", "逗号分隔的 inbound tags（必填，可多个）")
	email := fs.String("email", "", "用户 email")
	uuid := fs.String("uuid", "", "VMess UUID")
	level := fs.Uint("level", 0, "level")
	timeout := fs.Duration("timeout", 8*time.Second, "RPC 超时")
	_ = fs.Parse(os.Args[2:])
	if *tagsS == "" || *email == "" || *uuid == "" {
		log.Fatal("缺少 -tags / -email / -uuid")
	}
	cli := mustClient(*addr, parseTags(*tagsS), *timeout)
	defer cli.Close()
	if err := cli.AddVMess(*email, *uuid, uint32(*level)); err != nil {
		log.Fatalf("添加失败: %v", err)
	}
	fmt.Println("OK")
}

func delUser() {
	fs := flag.NewFlagSet("del", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:1090", "Xray gRPC 地址")
	tagsS := fs.String("tags", "", "逗号分隔的 inbound tags（必填，可多个）")
	email := fs.String("email", "", "用户 email（作为删除键）")
	timeout := fs.Duration("timeout", 8*time.Second, "RPC 超时")
	_ = fs.Parse(os.Args[2:])
	if *tagsS == "" || *email == "" {
		log.Fatal("缺少 -tags / -email")
	}
	cli := mustClient(*addr, parseTags(*tagsS), *timeout)
	defer cli.Close()
	if err := cli.Remove(*email); err != nil {
		log.Fatalf("删除失败: %v", err)
	}
	fmt.Println("OK")
}

func bulkAdd() {
	fs := flag.NewFlagSet("bulk-add", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:1090", "Xray gRPC 地址")
	tagsS := fs.String("tags", "", "逗号分隔的 inbound tags（必填，可多个）")
	file := fs.String("file", "assets/users.example.csv", "CSV 文件路径")
	proto := fs.String("proto", "vless", "默认协议 vless|vmess（CSV 未写时使用）")
	level := fs.Uint("level", 0, "默认 level（CSV 未写时使用）")
	flow := fs.String("flow", "", "默认 VLESS flow（CSV 未写时使用）")
	concurrency := fs.Int("concurrency", 64, "并发数")
	retries := fs.Int("retries", 3, "重试次数")
	timeout := fs.Duration("timeout", 8*time.Second, "RPC 超时")
	_ = fs.Parse(os.Args[2:])
	if *tagsS == "" {
		log.Fatal("缺少 -tags")
	}
	tags := parseTags(*tagsS)

	f, err := os.Open(*file)
	if err != nil {
		log.Fatalf("open csv: %v", err)
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1

	rows, err := batch.LoadRows(r, *proto, uint32(*level), *flow)
	if err != nil {
		log.Fatalf("parse csv: %v", err)
	}
	if len(rows) == 0 {
		log.Fatal("CSV 无任务")
	}

	cli := mustClient(*addr, tags, *timeout)
	defer cli.Close()

	ok, fail := batch.RunBulk(cli, rows, *concurrency, *retries)
	log.Printf("BULK DONE: ok=%d failed=%d total=%d", ok, fail, len(rows))
	if fail > 0 {
		os.Exit(1)
	}
}
