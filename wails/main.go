package main

import (
	"embed"
	"log"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// Wails embeds the built frontend into the binary.
//
//go:embed all:frontend/dist
var assets embed.FS

func main() {
	bank, err := NewBankService()
	if err != nil {
		log.Fatal(err)
	}
	path, err := databasePath()
	if err != nil {
		log.Fatal(err)
	}
	engineService, db, warning, err := openEngine(bank, path)
	if err != nil {
		log.Fatal(err)
	}
	if warning != "" {
		log.Print(warning)
	}
	gradeService := NewGradeService(db)
	backupService := NewBackupService(engineService)
	syncService := NewSyncService(engineService, db)
	chatService := NewChatService(db, bank)

	app := application.New(application.Options{
		Name:        "循环背诵",
		Description: "考研英语循环背诵",
		Services: []application.Service{
			application.NewService(bank),
			application.NewService(engineService),
			application.NewService(gradeService),
			application.NewService(backupService),
			application.NewService(syncService),
			application.NewService(chatService),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "循环背诵",
		Width:            980,
		Height:           760,
		MinWidth:         360,
		MinHeight:        520,
		BackgroundColour: application.NewRGB(255, 255, 255),
		URL:              "/",
	})

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
