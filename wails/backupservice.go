package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"recitebook/internal/engine"
)

// BackupEvent reports the outcome of a dialog-driven backup action.
type BackupEvent struct {
	Path     string        `json:"path"`
	State    *engine.State `json:"state,omitempty"`
	Canceled bool          `json:"canceled"`
}

// BackupService exports and imports JSON backups of the recitation progress.
// The path-based methods are the testable core; the dialog methods are thin
// wrappers around the platform file pickers.
type BackupService struct {
	engine *EngineService
}

// NewBackupService builds the backup service on top of the engine state.
func NewBackupService(engine *EngineService) *BackupService {
	return &BackupService{engine: engine}
}

// Export writes a JSON backup to path. The file is replaced atomically, so a
// failed write never leaves a truncated backup behind.
func (s *BackupService) Export(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("导出失败：未指定文件")
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".recitebook-backup-*.json")
	if err != nil {
		return fmt.Errorf("导出失败：无法写入文件（%v）", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	exportErr := s.engine.exportTo(temp)
	closeErr := temp.Close()
	if exportErr != nil {
		return fmt.Errorf("导出失败：%w", exportErr)
	}
	if closeErr != nil {
		return fmt.Errorf("导出失败：无法写入文件（%v）", closeErr)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("导出失败：无法保存文件（%v）", err)
	}
	return nil
}

// Import validates a backup file and replaces the saved progress with it. An
// invalid file leaves the existing progress and in-memory state untouched.
func (s *BackupService) Import(path string) (engine.State, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return s.engine.State(), errors.New("导入失败：未指定文件")
	}
	file, err := os.Open(path)
	if err != nil {
		return s.engine.State(), fmt.Errorf("导入失败：无法打开文件（%v）", err)
	}
	defer file.Close()
	state, err := s.engine.importFrom(file)
	if err != nil {
		return s.engine.State(), fmt.Errorf("导入失败：文件格式不正确或缺少字段，原有进度未改动（%v）", err)
	}
	return state, nil
}

// ExportFile asks for a destination and writes a backup there.
func (s *BackupService) ExportFile() (BackupEvent, error) {
	app := application.Get()
	if app == nil {
		return BackupEvent{}, errors.New("导出失败：文件对话框不可用")
	}
	dialog := app.Dialog.SaveFile()
	dialog.SetOptions(&application.SaveFileDialogOptions{
		Title:    "导出背诵备份",
		Filename: "recitebook-backup-" + time.Now().Format("20060102-150405") + ".json",
	})
	path, err := dialog.
		AddFilter("JSON 备份", "*.json").
		PromptForSingleSelection()
	if err != nil {
		return BackupEvent{}, fmt.Errorf("导出失败：无法打开保存对话框（%v）", err)
	}
	if path == "" {
		return BackupEvent{Canceled: true}, nil
	}
	if err := s.Export(path); err != nil {
		return BackupEvent{}, err
	}
	return BackupEvent{Path: path}, nil
}

// ImportFile asks for a backup file and restores it.
func (s *BackupService) ImportFile() (BackupEvent, error) {
	app := application.Get()
	if app == nil {
		return BackupEvent{}, errors.New("导入失败：文件对话框不可用")
	}
	path, err := app.Dialog.OpenFile().
		SetTitle("选择背诵备份").
		CanChooseFiles(true).
		CanChooseDirectories(false).
		AddFilter("JSON 备份", "*.json").
		PromptForSingleSelection()
	if err != nil {
		return BackupEvent{}, fmt.Errorf("导入失败：无法打开选择对话框（%v）", err)
	}
	if path == "" {
		return BackupEvent{Canceled: true}, nil
	}
	state, err := s.Import(path)
	if err != nil {
		return BackupEvent{}, err
	}
	return BackupEvent{Path: path, State: &state}, nil
}
