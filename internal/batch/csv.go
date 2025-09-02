package batch

import (
	"encoding/csv"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zionnode/xray-admin/internal/xray"
)

type Row struct {
	Proto string // vless | vmess
	Email string
	UUID  string
	Level uint32
	Flow  string // vless only
}

func parseRecord(rec []string, defProto string, defLevel uint32, defFlow string) (Row, error) {
	if len(rec) < 2 {
		return Row{}, fmt.Errorf("need at least email,uuid")
	}
	r := Row{
		Email: strings.TrimSpace(rec[0]),
		UUID:  strings.TrimSpace(rec[1]),
		Proto: strings.ToLower(strings.TrimSpace(defProto)),
		Level: defLevel,
		Flow:  defFlow,
	}
	if len(rec) >= 3 && strings.TrimSpace(rec[2]) != "" {
		r.Proto = strings.ToLower(strings.TrimSpace(rec[2]))
	}
	if len(rec) >= 4 && strings.TrimSpace(rec[3]) != "" {
		var lv uint32
		_, _ = fmt.Sscanf(strings.TrimSpace(rec[3]), "%d", &lv)
		r.Level = lv
	}
	if len(rec) >= 5 && strings.TrimSpace(rec[4]) != "" {
		r.Flow = strings.TrimSpace(rec[4])
	}
	if r.Proto != "vless" && r.Proto != "vmess" {
		return Row{}, fmt.Errorf("unsupported proto: %s", r.Proto)
	}
	return r, nil
}

func LoadRows(r *csv.Reader, defProto string, defLevel uint32, defFlow string) ([]Row, error) {
	r.FieldsPerRecord = -1
	var rows []Row
	for {
		rec, err := r.Read()
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return nil, err
		}
		if len(rec) == 0 {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(rec[0]), "#") {
			continue
		}
		row, err := parseRecord(rec, defProto, defLevel, defFlow)
		if err != nil {
			log.Printf("skip: %v", err)
			continue
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func RunBulk(cli *xray.Client, rows []Row, concurrency, retries int) (ok, failed int) {
	type job struct{ r Row }
	jobs := make(chan job, concurrency*2)

	var done int64
	var fail int64
	var wg sync.WaitGroup

	worker := func(id int) {
		defer wg.Done()
		for j := range jobs {
			var err error
			for attempt := 0; attempt <= retries; attempt++ {
				switch j.r.Proto {
				case "vless":
					err = cli.AddVLESS(j.r.Email, j.r.UUID, j.r.Level, j.r.Flow)
				case "vmess":
					err = cli.AddVMess(j.r.Email, j.r.UUID, j.r.Level)
				default:
					err = fmt.Errorf("unknown proto: %s", j.r.Proto)
				}
				if err == nil {
					break
				}
				time.Sleep(time.Duration(200*(attempt+1)) * time.Millisecond)
			}
			if err != nil {
				atomic.AddInt64(&fail, 1)
				log.Printf("[W%02d] %s failed: %v", id, j.r.Email, err)
			} else {
				n := atomic.AddInt64(&done, 1)
				if n%100 == 0 {
					log.Printf("progress: %d ok, %d failed", n, atomic.LoadInt64(&fail))
				}
			}
		}
	}

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go worker(i + 1)
	}
	for _, r := range rows {
		jobs <- job{r: r}
	}
	close(jobs)
	wg.Wait()

	return int(done), int(fail)
}
