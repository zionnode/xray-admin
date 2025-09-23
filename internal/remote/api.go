package remote

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type ClientLite struct {
	ID    string `json:"id"`    // UUID
	Email string `json:"email"` // 作为 UID/email
}

type FetchResult struct {
	TagsVLESS []string
	TagsVMESS []string
	Clients   []ClientLite
	Raw       []byte // 原始 JSON（标准化后）用于快照
}

// Fetch 拉取服务端数据；兼容两种返回：
// 1) { "tags": ["t1","t2"], "clients":[...] }           // 旧版（默认当作 vless）
// 2) { "tags": { "vless":[...], "vmess":[...] }, ... }  // 新版（区分协议）
func Fetch(apiURL, token, publicID string, timeout time.Duration) (*FetchResult, error) {
	body, _ := json.Marshal(map[string]string{
		"token":     token,
		"public_id": publicID,
	})
	req, err := http.NewRequest(http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	c := &http.Client{
		Timeout: timeout,
		// 避免 POST 被 302 改成 GET，方便排错
		CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 最多 1MB
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("remote status=%s; body=%.200q", resp.Status, b)
	}
	// 容忍部分服务没设置正确 Content-Type，这里不强校验

	// 先解成通用结构
	var envelope struct {
		Tags    json.RawMessage   `json:"tags"`
		Clients []ClientLite      `json:"clients"`
	}
	if err := json.Unmarshal(b, &envelope); err != nil {
		return nil, fmt.Errorf("decode json failed: %v; body=%.200q", err, b)
	}

	var tagsVLESS, tagsVMESS []string

	// tags 可能是数组或对象
	var arr []string
	if len(envelope.Tags) > 0 && json.Unmarshal(envelope.Tags, &arr) == nil {
		// 老格式：数组，默认归到 vless
		tagsVLESS = nonEmpty(arr)
	} else {
		var obj map[string][]string
		if len(envelope.Tags) > 0 && json.Unmarshal(envelope.Tags, &obj) == nil {
			// 新格式：对象
			tagsVLESS = nonEmpty(append(obj["vless"], obj["VLESS"]...))
			tagsVMESS = nonEmpty(append(obj["vmess"], obj["VMESS"]...))
		}
	}

	// 标准化一份原始快照
	raw, _ := json.Marshal(struct {
		Tags struct {
			VLESS []string `json:"vless,omitempty"`
			VMESS []string `json:"vmess,omitempty"`
		} `json:"tags"`
		Clients []ClientLite `json:"clients"`
	}{
		Tags: struct {
			VLESS []string `json:"vless,omitempty"`
			VMESS []string `json:"vmess,omitempty"`
		}{
			VLESS: tagsVLESS,
			VMESS: tagsVMESS,
		},
		Clients: envelope.Clients,
	})

	return &FetchResult{
		TagsVLESS: tagsVLESS,
		TagsVMESS: tagsVMESS,
		Clients:   envelope.Clients,
		Raw:       raw,
	}, nil
}

func nonEmpty(in []string) []string {
	var out []string
	for _, s := range in {
		if q := strings.TrimSpace(s); q != "" {
			out = append(out, q)
		}
	}
	return out
}