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
	ID    string `json:"id"`
	Email string `json:"email"`
}

type FetchResult struct {
	TagsVLESS []string
	TagsVMESS []string
	Clients   []ClientLite
	Raw       []byte
}

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
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		preview, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("remote status=%s; body=%.200q", resp.Status, preview)
	}

	// 2xx：读完整体（不要限 1MB，避免大 JSON 被截断）
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body failed: %v", err)
	}

	var envelope struct {
		Tags    json.RawMessage `json:"tags"`
		Clients []ClientLite    `json:"clients"`
	}
	if err := json.Unmarshal(b, &envelope); err != nil {
		// 报错时也给一点正文预览，便于排查
		preview := string(b)
		if len(preview) > 200 {
			preview = preview[:200]
		}
		return nil, fmt.Errorf("decode json failed: %v; body=%.200q", err, preview)
	}

	var tagsVLESS, tagsVMESS []string

	// tags 可能是数组（旧格式）或对象（新格式）
	var arr []string
	if len(envelope.Tags) > 0 && json.Unmarshal(envelope.Tags, &arr) == nil {
		tagsVLESS = nonEmpty(arr)
	} else {
		var obj map[string][]string
		if len(envelope.Tags) > 0 && json.Unmarshal(envelope.Tags, &obj) == nil {
			tagsVLESS = nonEmpty(append(obj["vless"], obj["VLESS"]...))
			tagsVMESS = nonEmpty(append(obj["vmess"], obj["VMESS"]...))
		}
	}

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