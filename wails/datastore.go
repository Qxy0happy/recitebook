package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"recitebook/internal/store"
)

const databaseName = "recitebook.db"

// openEngine opens the database beside the executable and recovers a corrupt
// file by moving it aside before starting from an empty state.
func openEngine(bank *BankService, path string) (*EngineService, *store.SQLite, string, error) {
	warning := ""
	for attempt := 0; ; attempt++ {
		db, err := store.Open(path)
		if err == nil {
			svc, loadErr := NewEngineService(bank, db, warning)
			if loadErr == nil {
				return svc, db, warning, nil
			}
			db.Close()
			err = loadErr
		}
		if attempt > 0 || !errors.Is(err, store.ErrCorrupt) {
			return nil, nil, "", err
		}
		backup, renameErr := backupCorrupt(path)
		if renameErr != nil {
			return nil, nil, "", renameErr
		}
		warning = fmt.Sprintf("数据库文件损坏，已备份为 %s，本次从空白进度开始。", backup)
	}
}

func backupCorrupt(path string) (string, error) {
	backup := path + ".corrupt-" + time.Now().Format("20060102-150405")
	if err := os.Rename(path, backup); err != nil {
		return "", fmt.Errorf("备份损坏的数据库失败：%w", err)
	}
	for _, suffix := range []string{"-journal", "-wal", "-shm"} {
		if err := os.Rename(path+suffix, backup+suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("备份数据库附属文件失败：%w", err)
		}
	}
	return backup, nil
}

// databasePath returns the writable data directory for each platform.
func databasePath() (string, error) {
	if runtime.GOOS == "android" {
		dir := application.Mobile.StoragePath()
		if dir == "" {
			return "", errors.New("无法获取 Android 应用私有目录")
		}
		return filepath.Join(dir, databaseName), nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("定位程序目录失败：%w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Join(filepath.Dir(exe), "data", databaseName), nil
}
