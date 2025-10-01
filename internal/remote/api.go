package remote

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type ClientLite struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

// 程序内部使用的归一化结构
type Response struct {
	TagsVLESS   []string
	TagsVMESS   []string
	TagsREALITY []string
	Clients     []ClientLite
	Raw         []byte // 保存原始 JSON（用于快照）
}

// 向远端拉取 {tags, clients}
func Fetch(api, token, publicID string, timeout time.Duration) (Response, error) {
	body := map[string]string{
		"token":     token,
		"public_id": publicID,
	}
	b, _ := json.Marshal(body)

	req, err := http.NewRequest(http.MethodPost, api, bytes.NewReader(b))
	if err != nil {
		return Response{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	c := &http.Client{Timeout: timeout}
	resp, err := c.Do(req)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return Response{}, err
	}

	// 兼容你的返回格式：
	// {
	//   "tags": { "vless": [...], "vmess": [...], "reality": [...] },
	//   "clients": [ { "id": "...", "email": "..." }, ... ]
	// }
	var wire struct {
		Tags struct {
			VLESS   []string `json:"vless"`
			VMESS   []string `json:"vmess"`
			REALITY []string `json:"reality"`
		} `json:"tags"`
		Clients []ClientLite `json:"clients"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return Response{}, fmt.Errorf("decode json failed: %w; body=%q", err, string(raw))
	}

	return Response{
		TagsVLESS:   wire.Tags.VLESS,
		TagsVMESS:   wire.Tags.VMESS,
		TagsREALITY: wire.Tags.REALITY,
		Clients:     wire.Clients,
		Raw:         raw,
	}, nil
}