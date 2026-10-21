package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	configFileName = "ytdlp_gui_config.json"
	appDisplayName = "yt-dlp 下载器"
)

// Config mirrors the on-disk settings file. Keys are kept identical to the
// original Python GUI so an existing ytdlp_gui_config.json keeps working.
type Config struct {
	OutputDir    string `json:"output_dir"`
	Resolution   string `json:"resolution"`
	Format       string `json:"format"`
	FilenameTmpl string `json:"filename_tmpl"`
	Proxy        string `json:"proxy"`
	Cookies      string `json:"cookies"`
	NoPlaylist   bool   `json:"no_playlist"`
	Thumbnail    bool   `json:"thumbnail"`
	YtdlpPath    string `json:"ytdlp_path,omitempty"`
	FfmpegPath   string `json:"ffmpeg_path,omitempty"`
	Author       string `json:"author,omitempty"`
	Version      string `json:"version,omitempty"`
	Help         string `json:"help,omitempty"`
}

// Deps reports the runtime dependencies discovered on this machine.
type Deps struct {
	YtdlpPath     string `json:"ytdlpPath"`
	YtdlpVersion  string `json:"ytdlpVersion"`
	FfmpegPath    string `json:"ffmpegPath"`
	FfmpegVersion string `json:"ffmpegVersion"`
	JSRuntime     string `json:"jsRuntime"`
	JSVersion     string `json:"jsVersion"`
	ConfigPath    string `json:"configPath"`
	ExeDir        string `json:"exeDir"`
	DefaultDir    string `json:"defaultDir"`
}

// DownloadOptions is what the frontend sends when the user hits download.
type DownloadOptions struct {
	URLs         []string `json:"urls"`
	OutputDir    string   `json:"outputDir"`
	Resolution   string   `json:"resolution"`
	Format       string   `json:"format"`
	FilenameTmpl string   `json:"filenameTmpl"`
	NoPlaylist   bool     `json:"noPlaylist"`
	Thumbnail    bool     `json:"thumbnail"`
	Proxy        string   `json:"proxy"`
	Cookies      string   `json:"cookies"`
	CookiesFile  string   `json:"cookiesFile"`
	YtdlpPath    string   `json:"ytdlpPath"`
	FfmpegPath   string   `json:"ffmpegPath"`
}

// Progress is pushed to the UI while yt-dlp streams its output.
type Progress struct {
	Percent float64 `json:"percent"`
	Size    string  `json:"size"`
	Speed   string  `json:"speed"`
	ETA     string  `json:"eta"`
	File    string  `json:"file"`
	Item    int     `json:"item"`
	Total   int     `json:"total"`
}

type Result struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// App is the bound backend.
type App struct {
	ctx context.Context

	mu      sync.Mutex
	proc    *exec.Cmd
	running bool
	cancel  bool

	cfgPath string
	cfg     Config
}

func NewApp() *App {
	return &App{}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.cfgPath = resolveConfigPath()
	a.cfg = loadConfig(a.cfgPath)
}

func (a *App) domReady(ctx context.Context) {}

func (a *App) beforeClose(ctx context.Context) bool {
	a.mu.Lock()
	running := a.running
	a.mu.Unlock()
	if !running {
		return false
	}
	choice, err := wruntime.MessageDialog(ctx, wruntime.MessageDialogOptions{
		Type:          wruntime.QuestionDialog,
		Title:         appDisplayName,
		Message:       "下载仍在进行中，确定要退出吗？",
		Buttons:       []string{"退出", "继续下载"},
		DefaultButton: "继续下载",
		CancelButton:  "继续下载",
	})
	if err != nil || choice != "退出" {
		return true
	}
	a.killCurrent()
	return false
}

func (a *App) shutdown(ctx context.Context) {
	a.killCurrent()
}

// ---------------------------------------------------------------- paths

func exeDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

func appDataDir() string {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "ytdlp-wails")
}

func isWritableDir(dir string) bool {
	f, err := os.CreateTemp(dir, ".wtest-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

// resolveConfigPath prefers a portable config next to the exe (same behaviour
// as the Python build) and falls back to %APPDATA% when the exe lives in a
// read-only location such as Program Files.
func resolveConfigPath() string {
	dir := exeDir()
	if isWritableDir(dir) {
		return filepath.Join(dir, configFileName)
	}
	d := appDataDir()
	_ = os.MkdirAll(d, 0o755)
	return filepath.Join(d, configFileName)
}

func defaultOutputDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, "Downloads")
}

func defaultConfig() Config {
	return Config{
		OutputDir:  defaultOutputDir(),
		Resolution: "best",
		Format:     "mp4",
		NoPlaylist: true,
		Cookies:    "none",
		Author:     "fjnpzh",
		Version:    "2.0.1",
	}
}

// normalizeConfig upgrades values written by the old Tkinter GUI (Chinese
// display labels) into the stable keys used by this app.
func normalizeConfig(c *Config) {
	resMap := map[string]string{
		"最佳画质 (自动)": "best", "最佳画质（自动）": "best", "最佳画质": "best",
		"4K (2160p)": "2160", "1440p": "1440", "1080p": "1080", "720p": "720",
		"480p": "480", "360p": "360",
	}
	if v, ok := resMap[c.Resolution]; ok {
		c.Resolution = v
	}
	fmtMap := map[string]string{
		"视频+音频 (MP4)": "mp4", "视频+音频（MP4）": "mp4",
		"仅视频 (MP4)": "video", "仅视频（MP4）": "video",
		"仅音频 (MP3)": "mp3", "仅音频（MP3）": "mp3",
	}
	if v, ok := fmtMap[c.Format]; ok {
		c.Format = v
	}
	switch {
	case c.Resolution == "":
		c.Resolution = "best"
	}
	switch {
	case c.Format == "":
		c.Format = "mp4"
	}
	if c.Cookies == "" {
		c.Cookies = "none"
	}
	// 旧版配置里是 "Firefox" / "Chrome" 这类首字母大写的标签，统一转小写再匹配
	c.Cookies = strings.ToLower(strings.TrimSpace(c.Cookies))
	if strings.HasPrefix(c.Cookies, "无") {
		c.Cookies = "none"
	}
	if strings.HasPrefix(c.Cookies, "cookies 文件") {
		c.Cookies = "file"
	}
	switch c.Cookies {
	case "chrome", "edge", "firefox", "file", "none":
	default:
		c.Cookies = "none"
	}
	if c.OutputDir == "" {
		c.OutputDir = defaultOutputDir()
	}
}

func loadConfig(path string) Config {
	cfg := defaultConfig()
	raw, err := os.ReadFile(path)
	if err != nil {
		// try a legacy config sitting in %APPDATA%\ytdlp-wails
		alt := filepath.Join(appDataDir(), configFileName)
		if raw, err = os.ReadFile(alt); err == nil {
			path = alt
		} else {
			return cfg
		}
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return defaultConfig()
	}
	normalizeConfig(&cfg)
	return cfg
}

func (a *App) saveConfigLocked() error {
	cfg := a.cfg
	if cfg.Author == "" {
		cfg.Author = "fjnpzh"
	}
	if cfg.Version == "" {
		cfg.Version = "2.0.1"
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(a.cfgPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(a.cfgPath, raw, 0o644)
}

// ---------------------------------------------------------------- bindings

// GetConfig returns the persisted settings.
func (a *App) GetConfig() Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg
}

// SaveConfig persists settings and returns the normalized config that was
// actually written. Returning the config (not the file path) keeps the
// frontend's cached copy in sync — it used to receive a string, which made the
// next save lose ytdlp_path / ffmpeg_path.
func (a *App) SaveConfig(cfg Config) (Config, error) {
	normalizeConfig(&cfg)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cfg = cfg
	if err := a.saveConfigLocked(); err != nil {
		return a.cfg, err
	}
	return a.cfg, nil
}

// ResetConfig restores factory defaults and saves them.
func (a *App) ResetConfig() (Config, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cfg = defaultConfig()
	if err := a.saveConfigLocked(); err != nil {
		return a.cfg, err
	}
	return a.cfg, nil
}

// GetHelp returns the help text stored in the config file.
func (a *App) GetHelp() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if strings.TrimSpace(a.cfg.Help) != "" {
		return a.cfg.Help
	}
	return defaultHelp
}

// GetDeps inspects yt-dlp / ffmpeg / JS runtime availability.
func (a *App) GetDeps() Deps {
	a.mu.Lock()
	cfg := a.cfg
	path := a.cfgPath
	a.mu.Unlock()

	d := Deps{ConfigPath: path, ExeDir: exeDir(), DefaultDir: defaultOutputDir()}
	d.YtdlpPath = findYtdlp(cfg.YtdlpPath)
	if d.YtdlpPath != "" {
		d.YtdlpVersion = firstLine(runCapture(d.YtdlpPath, []string{"--version"}, 12*time.Second))
	}
	d.FfmpegPath = findFfmpeg(cfg.FfmpegPath)
	if d.FfmpegPath != "" {
		out := runCapture(d.FfmpegPath, []string{"-version"}, 8*time.Second)
		d.FfmpegVersion = firstLine(out)
	}
	if p, err := exec.LookPath("node"); err == nil {
		d.JSRuntime = "node"
		d.JSVersion = firstLine(runCapture(p, []string{"--version"}, 8*time.Second))
	} else if p, err := exec.LookPath("deno"); err == nil {
		d.JSRuntime = "deno"
		d.JSVersion = firstLine(runCapture(p, []string{"--version"}, 8*time.Second))
	}
	return d
}

// BrowseDirectory opens a folder picker.
func (a *App) BrowseDirectory(current string) (string, error) {
	if current == "" {
		current = defaultOutputDir()
	}
	return wruntime.OpenDirectoryDialog(a.ctx, wruntime.OpenDialogOptions{
		Title:            "选择输出目录",
		DefaultDirectory: current,
	})
}

// BrowseFile opens a file picker. kind is one of: exe, cookies, ffmpeg.
func (a *App) BrowseFile(kind string) (string, error) {
	opts := wruntime.OpenDialogOptions{Title: "选择文件"}
	switch kind {
	case "exe":
		opts.Title = "选择 yt-dlp.exe"
		opts.Filters = []wruntime.FileFilter{{DisplayName: "可执行文件 (*.exe)", Pattern: "*.exe"}}
	case "ffmpeg":
		opts.Title = "选择 ffmpeg.exe"
		opts.Filters = []wruntime.FileFilter{{DisplayName: "可执行文件 (*.exe)", Pattern: "*.exe"}}
	case "cookies":
		opts.Title = "选择 Cookies 文件"
		opts.Filters = []wruntime.FileFilter{{DisplayName: "Cookies (*.txt;*.cookies)", Pattern: "*.txt;*.cookies"}}
	}
	return wruntime.OpenFileDialog(a.ctx, opts)
}

// OpenFolder reveals a folder in Explorer.
func (a *App) OpenFolder(path string) error {
	if path == "" {
		return errors.New("目录为空")
	}
	if st, err := os.Stat(path); err != nil || !st.IsDir() {
		return fmt.Errorf("目录不存在: %s", path)
	}
	return exec.Command("explorer", path).Start()
}

// OpenURL opens a link in the default browser.
func (a *App) OpenURL(url string) {
	if url != "" {
		wruntime.BrowserOpenURL(a.ctx, url)
	}
}

// CheckBrowser tells whether a browser process is running (cookie DB lock).
func (a *App) CheckBrowser(browser string) bool {
	name := map[string]string{"chrome": "chrome.exe", "edge": "msedge.exe", "firefox": "firefox.exe"}[browser]
	if name == "" {
		return false
	}
	out := runCapture("tasklist", []string{"/FI", "IMAGENAME eq " + name, "/NH"}, 10*time.Second)
	return strings.Contains(strings.ToLower(out), name)
}

// ChromeProfile returns the Chrome profile that most likely holds a logged-in
// session ("chrome" or "chrome:Profile N").
func (a *App) ChromeProfile() string {
	base := filepath.Join(os.Getenv("LOCALAPPDATA"), "Google", "Chrome", "User Data")
	entries, err := os.ReadDir(base)
	if err != nil {
		return "chrome"
	}
	best, bestMod := "", time.Time{}
	check := func(dir, label string) {
		ck := filepath.Join(dir, "Network", "Cookies")
		if st, err := os.Stat(ck); err == nil && st.ModTime().After(bestMod) {
			bestMod, best = st.ModTime(), label
		}
	}
	for _, e := range entries {
		if e.IsDir() && (e.Name() == "Default" || strings.HasPrefix(e.Name(), "Profile ")) {
			check(filepath.Join(base, e.Name()), e.Name())
		}
	}
	if best == "" || best == "Default" {
		return "chrome"
	}
	return "chrome:" + best
}

// StartDownload launches yt-dlp and streams progress to the UI.
func (a *App) StartDownload(opts DownloadOptions) (string, error) {
	a.mu.Lock()
	if a.running {
		a.mu.Unlock()
		return "", errors.New("已有下载任务进行中")
	}
	cfg := a.cfg
	a.mu.Unlock()

	urls := make([]string, 0, len(opts.URLs))
	for _, u := range opts.URLs {
		if u = strings.TrimSpace(u); u != "" {
			urls = append(urls, u)
		}
	}
	if len(urls) == 0 {
		return "", errors.New("请至少输入一个视频链接")
	}
	outDir := strings.TrimSpace(opts.OutputDir)
	if outDir == "" {
		outDir = cfg.OutputDir
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", fmt.Errorf("无法创建输出目录: %v", err)
	}

	ytdlp := findYtdlp(firstNonEmpty(opts.YtdlpPath, cfg.YtdlpPath))
	if ytdlp == "" {
		return "", errors.New("未找到 yt-dlp，请先指定 yt-dlp.exe 的位置")
	}
	ffmpeg := findFfmpeg(firstNonEmpty(opts.FfmpegPath, cfg.FfmpegPath))

	cookies := opts.Cookies
	if cookies == "" {
		cookies = "none"
	}
	var cookieArgs []string
	switch cookies {
	case "none":
	case "file":
		if opts.CookiesFile == "" {
			return "", errors.New("未选择 Cookies 文件")
		}
		if _, err := os.Stat(opts.CookiesFile); err != nil {
			return "", fmt.Errorf("Cookies 文件不存在: %s", opts.CookiesFile)
		}
		cookieArgs = []string{"--cookies", opts.CookiesFile}
	case "chrome":
		if a.CheckBrowser("chrome") {
			return "", errors.New("Chrome 正在运行，请先完全退出后再试；或改用 Cookies 文件方式")
		}
		cookieArgs = []string{"--cookies-from-browser", a.ChromeProfile()}
	case "edge", "firefox":
		if a.CheckBrowser(cookies) {
			return "", fmt.Errorf("%s 正在运行，请先完全退出后再试；或改用 Cookies 文件方式", strings.Title(cookies))
		}
		cookieArgs = []string{"--cookies-from-browser", cookies}
	}

	args := buildArgs(opts, urls, outDir, ffmpeg, cookieArgs)

	cmd := exec.Command(ytdlp, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("启动 yt-dlp 失败: %v", err)
	}

	a.mu.Lock()
	a.proc = cmd
	a.running = true
	a.cancel = false
	a.mu.Unlock()

	wruntime.EventsEmit(a.ctx, "state", "downloading")
	wruntime.EventsEmit(a.ctx, "progress", Progress{Percent: 0, Total: len(urls), Item: 1})

	total := len(urls)
	item := 1
	currentFile := ""
	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			line := strings.TrimRight(sc.Text(), "\r\n\t ")
			if line == "" {
				continue
			}
			if p, ok := parseProgress(line); ok {
				p.Total = total
				p.Item = item
				p.File = currentFile
				wruntime.EventsEmit(a.ctx, "progress", p)
				continue
			}
			if strings.HasPrefix(line, "[download] Destination: ") {
				currentFile = filepath.Base(strings.TrimSpace(strings.TrimPrefix(line, "[download] Destination: ")))
				item++
				if item > total {
					item = total
				}
			}
			wruntime.EventsEmit(a.ctx, "log", line)
		}
	}()

	err := cmd.Wait()
	pw.Close()
	<-scanDone

	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			code = -1
		}
	}
	a.mu.Lock()
	cancelled := a.cancel
	a.running = false
	a.cancel = false
	a.proc = nil
	a.mu.Unlock()

	msg := "下载完成"
	switch {
	case cancelled:
		msg = "已取消"
	case code != 0:
		msg = fmt.Sprintf("下载失败（退出码 %d）", code)
	}
	wruntime.EventsEmit(a.ctx, "state", map[bool]string{true: "cancelled", false: "done"}[cancelled])
	wruntime.EventsEmit(a.ctx, "result", Result{Code: code, Message: msg})
	return cmdLine(ytdlp, args), nil
}

// Cancel stops the running download (kills the whole process tree).
func (a *App) Cancel() {
	a.mu.Lock()
	a.cancel = true
	a.mu.Unlock()
	a.killCurrent()
}

func (a *App) killCurrent() {
	a.mu.Lock()
	cmd := a.proc
	a.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	kill := exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F")
	kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	_ = kill.Run()
	_ = cmd.Process.Kill()
}

// UpgradeYtdlp runs yt-dlp -U and streams its output to the log.
func (a *App) UpgradeYtdlp() (string, error) {
	a.mu.Lock()
	if a.running {
		a.mu.Unlock()
		return "", errors.New("下载进行中，无法升级")
	}
	cfg := a.cfg
	a.mu.Unlock()

	ytdlp := findYtdlp(cfg.YtdlpPath)
	if ytdlp == "" {
		return "", errors.New("未找到 yt-dlp")
	}
	wruntime.EventsEmit(a.ctx, "log", "== 正在升级 yt-dlp ==")
	cmd := exec.Command(ytdlp, "-U")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	out, err := cmd.CombinedOutput()
	for _, line := range strings.Split(strings.TrimRight(string(out), "\r\n"), "\n") {
		if line = strings.TrimRight(line, "\r"); line != "" {
			wruntime.EventsEmit(a.ctx, "log", line)
		}
	}
	if err != nil {
		return "", fmt.Errorf("升级失败: %v", err)
	}
	return firstLine(runCapture(ytdlp, []string{"--version"}, 12*time.Second)), nil
}

// ---------------------------------------------------------------- helpers

var (
	rePercent = regexp.MustCompile(`\[download\]\s+([\d.]+)%`)
	reSize    = regexp.MustCompile(`of\s+~?\s*([\d.]+\s*[KMGTP]?i?B)`)
	reSpeed   = regexp.MustCompile(`at\s+([\d.]+\s*[KMGTP]?i?B/s|Unknown\s+speed)`)
	reETA     = regexp.MustCompile(`ETA\s+([\d:]+)`)
)

func parseProgress(line string) (Progress, bool) {
	m := rePercent.FindStringSubmatch(line)
	if m == nil {
		return Progress{}, false
	}
	pct, _ := strconv.ParseFloat(m[1], 64)
	p := Progress{Percent: pct}
	if s := reSize.FindStringSubmatch(line); s != nil {
		p.Size = strings.ReplaceAll(s[1], " ", "")
	}
	if s := reSpeed.FindStringSubmatch(line); s != nil {
		p.Speed = strings.ReplaceAll(s[1], " ", "")
	}
	if s := reETA.FindStringSubmatch(line); s != nil {
		p.ETA = s[1]
	}
	return p, true
}

func buildArgs(o DownloadOptions, urls []string, outDir, ffmpeg string, cookieArgs []string) []string {
	height := ""
	switch o.Resolution {
	case "2160", "1440", "1080", "720", "480", "360":
		height = "[height<=" + o.Resolution + "]"
	}
	var args []string
	switch o.Format {
	case "mp3":
		args = append(args, "-f", "bestaudio/best",
			"--extract-audio", "--audio-format", "mp3", "--audio-quality", "0")
	case "video":
		args = append(args, "-f",
			fmt.Sprintf("bestvideo[ext=mp4]%s/bestvideo/best", height),
			"--merge-output-format", "mp4")
	default:
		args = append(args, "-f",
			fmt.Sprintf("bestvideo[ext=mp4]%s+bestaudio[ext=m4a]/bestvideo[ext=mp4]+bestaudio[ext=m4a]/best", height),
			"--merge-output-format", "mp4")
	}
	args = append(args, "--paths", outDir)
	if strings.TrimSpace(o.FilenameTmpl) != "" {
		args = append(args, "-o", strings.TrimSpace(o.FilenameTmpl))
	}
	if o.NoPlaylist {
		args = append(args, "--no-playlist")
	} else {
		args = append(args, "--yes-playlist")
	}
	if o.Thumbnail {
		args = append(args, "--write-thumbnail")
	}
	if strings.TrimSpace(o.Proxy) != "" {
		args = append(args, "--proxy", strings.TrimSpace(o.Proxy))
	}
	args = append(args, cookieArgs...)
	args = append(args, "--concurrent-fragments", "4", "--newline", "--encoding", "utf-8")
	if rt := findJSRuntime(); rt != "" {
		args = append(args, "--js-runtimes", rt)
	}
	if ffmpeg != "" {
		args = append(args, "--ffmpeg-location", ffmpeg)
	}
	args = append(args, urls...)
	return args
}

func findYtdlp(override string) string {
	candidates := []string{}
	if override != "" {
		candidates = append(candidates, override)
	}
	dir := exeDir()
	candidates = append(candidates,
		filepath.Join(dir, "yt-dlp.exe"),
		filepath.Join(dir, "yt-dlp_x86.exe"),
		filepath.Join(dir, "bin", "yt-dlp.exe"),
	)
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	if p, err := exec.LookPath("yt-dlp.exe"); err == nil {
		return p
	}
	if p, err := exec.LookPath("yt-dlp"); err == nil {
		return p
	}
	return ""
}

func findFfmpeg(override string) string {
	candidates := []string{}
	if override != "" {
		candidates = append(candidates, override)
	}
	dir := exeDir()
	candidates = append(candidates,
		filepath.Join(dir, "ffmpeg.exe"),
		filepath.Join(dir, "bin", "ffmpeg.exe"),
	)
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	if p, err := exec.LookPath("ffmpeg.exe"); err == nil {
		return p
	}
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p
	}
	return ""
}

func findJSRuntime() string {
	if _, err := exec.LookPath("node"); err == nil {
		return "node"
	}
	if _, err := exec.LookPath("deno"); err == nil {
		return "deno"
	}
	return ""
}

func runCapture(exe string, args []string, timeout time.Duration) string {
	if exe == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		return ""
	}
	return string(out)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func cmdLine(exe string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, quoteArg(exe))
	for _, a := range args {
		parts = append(parts, quoteArg(a))
	}
	return strings.Join(parts, " ")
}

func quoteArg(s string) string {
	if strings.ContainsAny(s, " \t") {
		return `"` + s + `"`
	}
	return s
}

const defaultHelp = `本工具是 yt-dlp 的图形外壳，界面选项对应下列命令行参数：

【格式】-f / --format
    视频+音频(MP4)：分别下载视频流与音频流，再合并为 MP4
    仅视频(MP4)   ：只取视频流，无声音，体积更小
    仅音频(MP3)   ：只取音频流并转码为 MP3
【分辨率】[height<=N]：限制视频最大高度，如 1080 表示不超过 1080 行
【合并格式】--merge-output-format mp4
【输出目录】--paths
【文件名模板】-o：支持 %(title)s、%(id)s、%(ext)s 等占位符
【仅当前视频】--no-playlist：只下载链接指向的单个视频
【封面】--write-thumbnail：同时保存封面图
【代理】--proxy：http://主机:端口 或 socks5://主机:端口
【浏览器 Cookies】--cookies-from-browser：复用浏览器登录态以绕过机器人验证；
    Chrome / Edge 需先完全退出浏览器，Firefox 通常无需退出
【JS 运行时】--js-runtimes：自动检测本机 Node / Deno，用于 YouTube 格式解析
【并发分片】--concurrent-fragments 4
【ffmpeg】--ffmpeg-location：用于合并与转码

设置会自动保存到配置文件，下次启动自动恢复。`
