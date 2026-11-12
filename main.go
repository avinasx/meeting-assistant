package main

import (
	"embed"
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"

	"github.com/avinasx/meeting-assistant/internal/config"
)

//go:embed all:frontend
var assets embed.FS

func main() {
	path, err := config.DefaultPath()
	if err != nil {
		log.Fatalf("meeting-assistant: %v", err)
	}
	store, err := config.Open(path)
	if err != nil {
		log.Fatalf("meeting-assistant: %v", err)
	}
	app := NewApp(store)
	settings := store.Get()

	err = wails.Run(&options.App{
		Title:            "Meeting Assistant",
		Width:            460,
		Height:           620,
		MinWidth:         340,
		MinHeight:        220,
		Frameless:        true,
		AlwaysOnTop:      true,
		BackgroundColour: &options.RGBA{R: 0, G: 0, B: 0, A: 0},
		AssetServer:      &assetserver.Options{Assets: assets},
		OnStartup:        app.startup,
		OnDomReady:       app.domReady,
		OnShutdown:       app.shutdown,
		Bind:             []interface{}{app},
		Mac: &mac.Options{
			// Excluded from screen capture from the first frame; the rest of
			// the overlay behaviour is applied once the window exists.
			ContentProtection:    settings.Stealth,
			WebviewIsTransparent: true,
			WindowIsTranslucent:  false,
			DisableZoom:          true,
		},
	})
	if err != nil {
		log.Fatalf("meeting-assistant: %v", err)
	}
}
