package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func newTestApp(t *testing.T) *App {
	t.Helper()
	a := NewApp()
	a.cfgPath = filepath.Join(t.TempDir(), configFileName)
	a.cfg = defaultConfig()
	return a
}

func readCfgFile(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取配置文件失败: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("配置文件不是合法 JSON: %v", err)
	}
	return m
}

// 两个依赖路径必须互不干扰：先设 yt-dlp 再设 ffmpeg（以及反过来），
// 落盘后两个字段都要在。
func TestSaveConfigKeepsBothDependencyPaths(t *testing.T) {
	a := newTestApp(t)

	cfg := a.GetConfig()
	cfg.YtdlpPath = `C:\Tools\yt-dlp.exe`
	saved, err := a.SaveConfig(cfg)
	if err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if saved.YtdlpPath != cfg.YtdlpPath {
		t.Fatalf("返回值里 ytdlp_path 丢了: %q", saved.YtdlpPath)
	}

	// 第二次保存只改 ffmpeg 路径，yt-dlp 路径必须保留
	saved.FfmpegPath = `C:\Tools\ffmpeg.exe`
	saved, err = a.SaveConfig(saved)
	if err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if saved.YtdlpPath != `C:\Tools\yt-dlp.exe` {
		t.Errorf("ytdlp_path 被覆盖: %q", saved.YtdlpPath)
	}
	if saved.FfmpegPath != `C:\Tools\ffmpeg.exe` {
		t.Errorf("ffmpeg_path 未保存: %q", saved.FfmpegPath)
	}

	m := readCfgFile(t, a.cfgPath)
	if m["ytdlp_path"] != `C:\Tools\yt-dlp.exe` {
		t.Errorf("文件里 ytdlp_path = %v", m["ytdlp_path"])
	}
	if m["ffmpeg_path"] != `C:\Tools\ffmpeg.exe` {
		t.Errorf("文件里 ffmpeg_path = %v", m["ffmpeg_path"])
	}

	// 第三次保存只动其它选项，两个路径都不能丢
	saved.Proxy = "http://127.0.0.1:7890"
	if _, err := a.SaveConfig(saved); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	m = readCfgFile(t, a.cfgPath)
	if m["ytdlp_path"] != `C:\Tools\yt-dlp.exe` || m["ffmpeg_path"] != `C:\Tools\ffmpeg.exe` {
		t.Errorf("改其它选项后路径丢失: ytdlp=%v ffmpeg=%v", m["ytdlp_path"], m["ffmpeg_path"])
	}
}

// 旧版 Python GUI 写的中文标签要能归一化成内部 key
func TestNormalizeLegacyLabels(t *testing.T) {
	cfg := Config{
		Resolution: "1080p",
		Format:     "视频+音频 (MP4)",
		Cookies:    "Firefox",
	}
	normalizeConfig(&cfg)
	if cfg.Resolution != "1080" {
		t.Errorf("resolution = %q, 期望 1080", cfg.Resolution)
	}
	if cfg.Format != "mp4" {
		t.Errorf("format = %q, 期望 mp4", cfg.Format)
	}
	if cfg.Cookies != "firefox" {
		t.Errorf("cookies = %q, 期望 firefox", cfg.Cookies)
	}

	legacy := Config{Resolution: "最佳画质 (自动)", Format: "仅音频 (MP3)", Cookies: "无 (不使用 Cookies, 默认)"}
	normalizeConfig(&legacy)
	if legacy.Resolution != "best" || legacy.Format != "mp3" || legacy.Cookies != "none" {
		t.Errorf("归一化结果错误: %+v", legacy)
	}
}

// 配置读回后必须和写进去的一致（往返一致性）
func TestConfigRoundTrip(t *testing.T) {
	a := newTestApp(t)
	cfg := a.GetConfig()
	cfg.YtdlpPath = `C:\A\yt-dlp.exe`
	cfg.FfmpegPath = `C:\B\ffmpeg.exe`
	cfg.Proxy = "socks5://127.0.0.1:1080"
	cfg.Resolution = "720"
	cfg.Format = "mp3"
	cfg.Thumbnail = true
	if _, err := a.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	back := loadConfig(a.cfgPath)
	if back.YtdlpPath != cfg.YtdlpPath || back.FfmpegPath != cfg.FfmpegPath {
		t.Errorf("路径往返不一致: %+v", back)
	}
	if back.Proxy != cfg.Proxy || back.Resolution != cfg.Resolution || back.Format != cfg.Format || back.Thumbnail != cfg.Thumbnail {
		t.Errorf("其它字段往返不一致: %+v", back)
	}
}
