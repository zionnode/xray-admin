package store

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// 如果 DB 有互斥锁（mu）建议加锁；没有也可先不加。
func (db *DB) Save(m map[string]User) error {
	// ——以下假设 DB 里有 unexported 的 `path string` 字段——
	// 大多数文件型 DB 都会这样命名。如果你不是叫 path，而是 file/filename等，
	// 把 db.path 按你的字段名改一下即可。
	if err := os.MkdirAll(filepath.Dir(db.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := db.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, db.path)
}