package store

// 如果你的包里已经有未导出的方法 `load()` / `save(...)`，
// 直接用两个 tiny wrapper 暴露出去即可。

func (db *DB) Load() (map[string]User, error) { // 与 syncer.go 对齐
	return db.load()
}

func (db *DB) Save(m map[string]User) error { // 与 syncer.go 对齐
	return db.save(m)
}