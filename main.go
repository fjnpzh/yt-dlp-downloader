package main

import (
	"embed"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed all:frontend/src
var assets embed.FS

func main() {
	app := NewApp()

	err := wails.Run(&options.App{
		Title:            appDisplayName,
		Width:            1120,
		Height:           800,
		MinWidth:         940,
		MinHeight:        640,
		BackgroundColour: &options.RGBA{R: 10, G: 12, B: 17, A: 1},
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		OnStartup:     app.startup,
		OnDomReady:    app.domReady,
		OnBeforeClose: app.beforeClose,
		OnShutdown:    app.shutdown,
		Windows: &windows.Options{
			// 只声明深色主题，让 DWM 自己绘制标题栏与右上角的最小化/最大化/关闭按钮。
			// 之前用 CustomTheme 强制把标题栏刷成 #0a0c11，系统处于浅色模式时
			// DWM 仍按浅色绘制按钮字形（深色），叠在近黑标题栏上等于看不见，
			// 这就是「少了关闭按钮」的原因。
			Theme: windows.Dark,
		},
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
