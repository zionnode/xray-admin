package remote

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/zionnode/xray-admin/internal/store"
)

type reqBody struct {
	Token    string `json:"token"`
	PublicID string `json:"public_id"`
}

type respBody struct {
	Tags    []string `json:"tags"`
	Clients []struct {
		ID    string `json:"id"`    // UUID
		Email string `json:"email"` // 作为 UID/Email
	} `json:"clients"`
}

type FetchResult struct {
	Tags  []string
	Users map[string]store.User // key = UID(email)
	Raw   []byte                // 原始 JSON，用于快照
}

// Fetch 拉取服务端 tags + clients，并转换为本地 User 结构（proto=vless, level=1, flow="" 可由调用方覆盖）
func Fetch(apiURL, token, publicID, proto string, level uint32, flow string, timeout time.Duration) (*FetchResult, error) {
	body, _ := json.Marshal(reqBody{Token: token, PublicID: publicID})

	req, err := http.NewRequest(http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	c := &http.Client{Timeout: timeout}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("remote status: %s", resp.Status)
	}

	var rb respBody
	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(&rb); err != nil {
		return nil, err
	}

	users := make(map[string]store.User, len(rb.Clients))
	for _, cli := range rb.Clients {
		if cli.Email == "" || cli.ID == "" {
			continue
		}
		// email 字段就用 UID（你的设计）
		users[cli.Email] = store.User{
			UID:   cli.Email,
			Email: cli.Email,
			UUID:  cli.ID,
			Proto: proto, // 默认 vless
			Level: level, // 默认 1
			Flow:  flow,  // 默认 ""
		}
	}

	// 重新序列化一份“标准化原始快照”，便于落盘
	raw, _ := json.Marshal(struct {
		Tags    []string               `json:"tags"`
		Clients []map[string]string    `json:"clients"`
	}{
		Tags: rb.Tags,
		Clients: func() []map[string]string {
			out := make([]map[string]string, 0, len(rb.Clients))
			for _, c := range rb.Clients {
				out = append(out, map[string]string{"id": c.ID, "email": c.Email})
			}
			return out
		}(),
	})

	return &FetchResult{Tags: rb.Tags, Users: users, Raw: raw}, nil
}