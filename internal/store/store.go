package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// User 是我们在本地保存的“权威用户”结构（以 UID/email 为键）
type User struct {
	UID   string `json:"uid"`   // 你的管理系统里的用户唯一标识
	Email string `json:"email"` // 实际用于 Xray 的 email（我们用 UID 充当）
	UUID  string `json:"uuid"`  // VLESS/VMess 的 Account.Id
	Proto string `json:"proto"` // vless | vmess
	Level uint32 `json:"level"`
	Flow  string `json:"flow"`  // 普通 VLESS 留空；Vision 时为 "xtls-rprx-vision"
}

// DB 是一个简单的 JSON 文件数据库，键为 UID
type DB struct {
	path  string
	mu    sync.Mutex
	Users map[string]User `json:"users"`
}

// Open 打开（或初始化）本地 DB 文件。如果不存在会创建空库。
func Open(path string) (*DB, error) {
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	db := &DB{path: path, Users: map[string]User{}}

	f, err := os.Open(path)
	if err == nil {
		defer f.Close()
		_ = json.NewDecoder(f).Decode(db) // 读失败也不致命，保持空库
	}
	return db, nil
}

func (d *DB) save() error {
	tmp := d.path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(d); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, d.path)
}

// Upsert 写入/更新一个用户（以 UID 为键）
func (d *DB) Upsert(u User) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.Users == nil {
		d.Users = map[string]User{}
	}
	d.Users[u.UID] = u
	return d.save()
}

// Delete 按 UID 删除一个用户
func (d *DB) Delete(uid string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.Users, uid)
	return d.save()
}

// Snapshot 返回当前 Users 的一份拷贝（用于差异计算）
func (d *DB) Snapshot() map[string]User {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(map[string]User, len(d.Users))
	for k, v := range d.Users {
		out[k] = v
	}
	return out
}