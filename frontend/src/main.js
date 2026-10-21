/* yt-dlp 下载器 — frontend logic
 * Works against the real Go backend (window.go.main.App) and falls back to an
 * in-page mock so the UI can be previewed in a plain browser. */

const $ = (s) => document.querySelector(s);
const $$ = (s) => Array.from(document.querySelectorAll(s));

const state = {
    backend: false,
    cfg: null,
    deps: null,
    running: false,
    format: 'mp4',
    resolution: 'best',
    cookiesFile: '',
    // 依赖路径独立保存，不从表单推导、也不依赖 state.cfg 的完整性，
    // 这样指定 yt-dlp 和指定 ffmpeg 永远不会互相覆盖。
    ytdlpPath: '',
    ffmpegPath: '',
    logs: 0,
    saveTimer: null,
};

/* ------------------------------------------------------------ mock backend */
const mockCfg = {
    output_dir: 'C:\\Users\\hugua\\Downloads',
    resolution: '1080',
    format: 'mp4',
    filename_tmpl: '',
    proxy: 'http://127.0.0.1:1180',
    cookies: 'firefox',
    no_playlist: true,
    thumbnail: false,
};

const mockApi = {
    GetConfig: async () => ({ ...mockCfg }),
    SaveConfig: async (c) => { Object.assign(mockCfg, c); return { ...mockCfg }; },
    ResetConfig: async () => ({ ...mockCfg }),
    GetHelp: async () => '【预览模式】\n\n当前页面是在普通浏览器中打开的，未连接到 Go 后端，\n所以下载、选目录等操作只是模拟效果。\n\n在 Wails 窗口内运行时会连接真实的 yt-dlp。',
    GetDeps: async () => ({
        ytdlpPath: 'C:\\Tools\\yt-dlp.exe',
        ytdlpVersion: '2026.10.15',
        ffmpegPath: 'C:\\Tools\\ffmpeg.exe',
        ffmpegVersion: 'ffmpeg version 7.1',
        jsRuntime: 'node',
        jsVersion: 'v22.22.2',
        configPath: 'C:\\Tools\\ytdlp_gui_config.json',
        exeDir: 'C:\\Tools',
        defaultDir: 'C:\\Users\\hugua\\Downloads',
    }),
    BrowseDirectory: async () => 'C:\\Users\\hugua\\Videos',
    BrowseFile: async () => '',
    OpenFolder: async () => {},
    OpenURL: async () => {},
    CheckBrowser: async () => false,
    ChromeProfile: async () => 'chrome',
    StartDownload: async () => 'yt-dlp -f "bestvideo[ext=mp4][height<=1080]+bestaudio[ext=m4a]" --paths "C:\\Users\\hugua\\Downloads" <url>',
    Cancel: async () => {},
    UpgradeYtdlp: async () => '2026.10.15',
};

let api = mockApi;

/* ------------------------------------------------------------ helpers */
function toast(msg, kind) {
    const el = $('#toast');
    el.textContent = msg;
    el.className = 'toast' + (kind ? ' ' + kind : '');
    el.hidden = false;
    clearTimeout(el._t);
    el._t = setTimeout(() => { el.hidden = true; }, 2600);
}

function setStatus(text, kind) {
    $('#statusText').textContent = text;
    $('#statusDot').className = 'status-dot' + (kind ? ' ' + kind : '');
}

function stamp() {
    const d = new Date();
    return [d.getHours(), d.getMinutes(), d.getSeconds()].map((n) => String(n).padStart(2, '0')).join(':');
}

function classify(line) {
    const l = line.toLowerCase();
    if (l.includes('error') || l.startsWith('err')) return 'err';
    if (l.includes('warning')) return 'warn';
    if (line.startsWith('==')) return 'ok';
    if (line.startsWith('$ ') || line.startsWith('> ')) return 'cmd';
    if (line.includes('destination:') || line.includes('merger') || line.includes('extractaudio')) return 'hl';
    if (l.includes('downloading') || l.includes('fetching')) return 'dim';
    return '';
}

function addLog(line, cls) {
    const box = $('#log');
    const row = document.createElement('div');
    row.className = 'l ' + (cls || classify(line));
    const t = document.createElement('span');
    t.className = 't';
    t.textContent = stamp();
    row.appendChild(t);
    row.appendChild(document.createTextNode(line));
    box.appendChild(row);
    if (++state.logs > 600) {
        box.removeChild(box.firstChild);
        state.logs--;
    }
    if ($('#autoScroll').checked) box.scrollTop = box.scrollHeight;
}

function updateUrlCount() {
    const n = $('#urls').value.split('\n').map((s) => s.trim()).filter(Boolean).length;
    $('#urlCount').textContent = n + ' 个链接';
    return n;
}

/* ------------------------------------------------------------ config */
function cfgObject() {
    return (state.cfg && typeof state.cfg === 'object') ? state.cfg : {};
}

function applyConfig(cfg) {
    state.cfg = (cfg && typeof cfg === 'object') ? cfg : {};
    const c = state.cfg;
    state.ytdlpPath = c.ytdlp_path || '';
    state.ffmpegPath = c.ffmpeg_path || '';
    $('#outDir').value = c.output_dir || '';
    $('#tmpl').value = c.filename_tmpl || '';
    $('#proxy').value = c.proxy || '';
    $('#cookies').value = c.cookies || 'none';
    $('#noPlaylist').checked = !!c.no_playlist;
    $('#thumb').checked = !!c.thumbnail;
    setFormat(c.format || 'mp4', false);
    setResolution(c.resolution || 'best', false);
    onCookiesChange();
}

function collectConfig() {
    const prev = cfgObject();
    return {
        output_dir: $('#outDir').value.trim(),
        resolution: state.resolution,
        format: state.format,
        filename_tmpl: $('#tmpl').value.trim(),
        proxy: $('#proxy').value.trim(),
        cookies: $('#cookies').value,
        no_playlist: $('#noPlaylist').checked,
        thumbnail: $('#thumb').checked,
        // 两个依赖路径各用各的字段，互不干扰
        ytdlp_path: state.ytdlpPath || '',
        ffmpeg_path: state.ffmpegPath || '',
        author: prev.author || 'fjnpzh',
        version: prev.version || '2.0',
        help: prev.help || '',
    };
}

function scheduleSave() {
    clearTimeout(state.saveTimer);
    state.saveTimer = setTimeout(saveConfig, 400);
}

async function saveConfig() {
    try {
        const saved = await api.SaveConfig(collectConfig());
        // 后端返回的是归一化后的配置对象，缓存它，避免下次保存丢字段
        if (saved && typeof saved === 'object') state.cfg = saved;
    } catch (e) {
        console.error(e);
    }
}

/* ------------------------------------------------------------ controls */
function setFormat(v, persist = true) {
    state.format = v;
    $$('#fmtGroup .seg').forEach((b) => b.classList.toggle('active', b.dataset.value === v));
    if (persist) scheduleSave();
}

function setResolution(v, persist = true) {
    state.resolution = v;
    $$('#resGroup .chip').forEach((b) => b.classList.toggle('active', b.dataset.value === v));
    if (persist) scheduleSave();
}

function onCookiesChange() {
    const v = $('#cookies').value;
    const isFile = v === 'file';
    $('#btnCookiesFile').hidden = !isFile;
    const hints = {
        none: '不使用浏览器登录态。多数公开视频无需 Cookies。',
        chrome: '需先完全退出 Chrome。程序会自动挑选最近使用的配置文件。',
        edge: '需先完全退出 Edge，否则无法读取其 Cookie 数据库。',
        firefox: 'Firefox 的 Cookie 库可在运行时读取，通常无需退出浏览器。',
        file: state.cookiesFile ? '已选择：' + state.cookiesFile : '请选择一个 Netscape 格式的 cookies.txt 文件。',
    };
    $('#cookiesHint').textContent = hints[v] || '';
    scheduleSave();
}

/* ------------------------------------------------------------ deps */
const ICONS = {
    ytdlp: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3v11"/><path d="m7.5 9.5 4.5 4.5 4.5-4.5"/><path d="M5 19h14"/></svg>',
    ffmpeg: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="5" width="18" height="14" rx="2.5"/><path d="M10 9.5v5l4-2.5z"/></svg>',
    node: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="m8 8-4 4 4 4"/><path d="m16 8 4 4-4 4"/></svg>',
};

function depRow(key, name, pillText, pillKind, pathText, action) {
    const li = document.createElement('li');
    li.className = 'dep';
    li.innerHTML = `
        <span class="dep-ico">${ICONS[key]}</span>
        <span class="dep-main">
            <span class="dep-name">${name}<span class="pill ${pillKind}">${pillText}</span></span>
            <span class="dep-path" title="${pathText || ''}">${pathText || ''}</span>
        </span>`;
    if (action) {
        const btn = document.createElement('button');
        btn.className = 'dep-act';
        btn.textContent = action.label;
        btn.onclick = action.onClick;
        li.appendChild(btn);
    }
    return li;
}

async function refreshDeps() {
    const list = $('#deps');
    try {
        const d = await api.GetDeps();
        state.deps = d;
        list.innerHTML = '';

        const hasYtdlp = !!d.ytdlpPath;
        list.appendChild(depRow(
            'ytdlp', 'yt-dlp',
            hasYtdlp ? (d.ytdlpVersion || '已安装') : '未找到',
            hasYtdlp ? 'ok' : 'bad',
            hasYtdlp ? d.ytdlpPath : '把 yt-dlp.exe 放到程序目录，或手动指定',
            hasYtdlp
                ? { label: '升级', onClick: upgradeYtdlp }
                : { label: '指定', onClick: () => pickDependency('ytdlp_path', 'exe') }
        ));

        const hasFf = !!d.ffmpegPath;
        list.appendChild(depRow(
            'ffmpeg', 'ffmpeg',
            hasFf ? '已安装' : '未找到',
            hasFf ? 'ok' : 'warn',
            hasFf ? d.ffmpegPath : '缺少 ffmpeg 时无法合并音视频 / 转 MP3',
            { label: hasFf ? '更换' : '指定', onClick: () => pickDependency('ffmpeg_path', 'ffmpeg') }
        ));

        list.appendChild(depRow(
            'node', 'JS 运行时',
            d.jsRuntime ? d.jsRuntime : '可选 · 未启用',
            d.jsRuntime ? 'ok' : 'mute',
            d.jsRuntime ? ((d.jsVersion ? d.jsVersion + ' · ' : '') + '仅 YouTube 需要') : '仅 YouTube 需要；装 Node.js / Deno 后自动启用',
            null
        ));

        if (!hasYtdlp) {
            setStatus('缺少 yt-dlp，请先指定其位置', 'err');
        }
    } catch (e) {
        console.error(e);
        toast('检测运行环境失败', 'err');
    }
}

async function pickDependency(field, kind) {
    const p = await api.BrowseFile(kind);
    if (!p) return;
    // 先落到独立的 state 字段，再整体保存 —— 两个字段各自独立
    if (field === 'ytdlp_path') state.ytdlpPath = p;
    else state.ffmpegPath = p;

    const cfg = collectConfig();
    try {
        const saved = await api.SaveConfig(cfg);
        if (saved && typeof saved === 'object') {
            state.cfg = saved;
            state.ytdlpPath = saved.ytdlp_path || state.ytdlpPath;
            state.ffmpegPath = saved.ffmpeg_path || state.ffmpegPath;
        }
        toast(field === 'ytdlp_path' ? '已保存 yt-dlp 路径' : '已保存 ffmpeg 路径', 'ok');
    } catch (e) {
        toast(String(e), 'err');
        return;
    }
    refreshDeps();
}

async function upgradeYtdlp() {
    if (state.running) return;
    setStatus('正在升级 yt-dlp…', 'busy');
    try {
        const v = await api.UpgradeYtdlp();
        toast('已升级到 ' + (v || '最新版'), 'ok');
        setStatus('yt-dlp 已升级', 'ok');
        refreshDeps();
    } catch (e) {
        toast(String(e), 'err');
        setStatus('升级失败', 'err');
    }
}

/* ------------------------------------------------------------ download */
function resetProgress() {
    $('#pct').textContent = '0';
    $('#barFill').style.width = '0%';
    $('#speed').textContent = '—';
    $('#eta').textContent = '—';
    $('#size').textContent = '—';
    $('#item').textContent = '—';
    $('#fileLine').textContent = '—';
    $('#fileLine').removeAttribute('title');
}

function setRunning(on) {
    state.running = on;
    $('#btnStart').disabled = on;
    $('#btnCancel').disabled = !on;
    $('#btnStart').textContent = on ? '下载中…' : '开始下载';
    $('.bar').classList.toggle('active', on);
}

async function startDownload() {
    if (state.running) return;
    const urls = $('#urls').value.split('\n').map((s) => s.trim()).filter(Boolean);
    if (!urls.length) {
        toast('请先输入至少一个视频链接', 'err');
        $('#urls').focus();
        return;
    }
    if (!state.deps || !state.deps.ytdlpPath) {
        toast('未找到 yt-dlp，请先在右侧指定路径', 'err');
        return;
    }

    await saveConfig();
    const opts = {
        urls,
        outputDir: $('#outDir').value.trim(),
        resolution: state.resolution,
        format: state.format,
        filenameTmpl: $('#tmpl').value.trim(),
        noPlaylist: $('#noPlaylist').checked,
        thumbnail: $('#thumb').checked,
        proxy: $('#proxy').value.trim(),
        cookies: $('#cookies').value,
        cookiesFile: state.cookiesFile,
    };

    resetProgress();
    setRunning(true);
    setStatus('正在启动下载…', 'busy');
    try {
        const cmd = await api.StartDownload(opts);
        if (cmd) addLog('$ ' + cmd, 'cmd');
        if (!state.backend) simulateDownload(urls.length);
    } catch (e) {
        setRunning(false);
        const msg = String(e).replace(/^Error:\s*/, '');
        addLog('[错误] ' + msg, 'err');
        setStatus(msg, 'err');
        toast(msg, 'err');
    }
}

async function cancelDownload() {
    if (!state.running) return;
    setStatus('正在取消…', 'busy');
    try { await api.Cancel(); } catch (e) { console.error(e); }
    if (!state.backend) {
        setRunning(false);
        setStatus('已取消', '');
        addLog('[取消] 已终止下载', 'warn');
    }
}

function onProgress(p) {
    const pct = Math.max(0, Math.min(100, p.percent || 0));
    $('#pct').textContent = String(Math.round(pct));
    $('#barFill').style.width = pct.toFixed(1) + '%';
    $('#speed').textContent = p.speed || '—';
    $('#eta').textContent = p.eta || '—';
    $('#size').textContent = p.size || '—';
    if (p.total > 1) $('#item').textContent = `${p.item || 1}/${p.total}`;
    else $('#item').textContent = '1/1';
    if (p.file) {
        const el = $('#fileLine');
        el.textContent = p.file;
        el.title = p.file;
    }
    if (state.running) setStatus(`下载中 ${Math.round(pct)}%` + (p.speed ? ` · ${p.speed}` : ''), 'busy');
}

function onState(s) {
    if (s === 'downloading') {
        setRunning(true);
    } else {
        setRunning(false);
    }
}

function onResult(r) {
    setRunning(false);
    if (r.code === 0) {
        $('#barFill').style.width = '100%';
        $('#pct').textContent = '100';
        setStatus('✔ 下载完成 · ' + ($('#outDir').value || ''), 'ok');
        addLog('== ' + r.message + ' ==', 'ok');
        toast('下载完成', 'ok');
    } else {
        setStatus('✘ ' + r.message, 'err');
        addLog('== ' + r.message + ' ==', 'err');
        toast(r.message, 'err');
    }
}

/* ------------------------------------------------------------ mock stream */
function simulateDownload(count) {
    let pct = 0;
    const total = 11.4;
    addLog('[youtube] Extracting URL: https://www.youtube.com/watch?v=preview', 'dim');
    addLog('[info] Downloading 1 format(s): 137+140', 'dim');
    addLog('[download] Destination: ' + ($('#outDir').value || 'C:\\Users\\hugua\\Downloads') + '\\示例视频.f137.mp4', 'hl');
    const timer = setInterval(() => {
        pct = Math.min(100, pct + Math.random() * 9 + 3);
        const done = (total * pct) / 100;
        onProgress({
            percent: pct,
            size: total.toFixed(1) + 'MiB',
            speed: (3.2 + Math.random() * 2).toFixed(2) + 'MiB/s',
            eta: pct >= 100 ? '00:00' : '00:' + String(Math.max(1, Math.round((100 - pct) / 8))).padStart(2, '0'),
            item: 1,
            total: count,
            file: '示例视频.f137.mp4',
        });
        if (pct >= 100) {
            clearInterval(timer);
            addLog('[Merger] Merging formats into "示例视频.mp4"', 'hl');
            addLog('== 下载完成（预览模拟）==', 'ok');
            onResult({ code: 0, message: '下载完成（预览模拟）' });
        }
    }, 420);
    state._mockTimer = timer;
}

/* ------------------------------------------------------------ events */
function bindUI() {
    $('#urls').addEventListener('input', updateUrlCount);

    $('#btnPaste').onclick = async () => {
        try {
            const text = await navigator.clipboard.readText();
            if (!text.trim()) return toast('剪贴板为空');
            const box = $('#urls');
            box.value = (box.value.trim() ? box.value.trim() + '\n' : '') + text.trim();
            updateUrlCount();
        } catch {
            toast('无法读取剪贴板，请手动粘贴', 'err');
        }
    };
    $('#btnClearUrls').onclick = () => { $('#urls').value = ''; updateUrlCount(); };

    $('#btnBrowseDir').onclick = async () => {
        const d = await api.BrowseDirectory($('#outDir').value);
        if (d) { $('#outDir').value = d; scheduleSave(); }
    };
    $('#btnOpenDir').onclick = async () => {
        try { await api.OpenFolder($('#outDir').value); }
        catch (e) { toast(String(e), 'err'); }
    };

    $$('#fmtGroup .seg').forEach((b) => { b.onclick = () => setFormat(b.dataset.value); });
    $$('#resGroup .chip').forEach((b) => { b.onclick = () => setResolution(b.dataset.value); });

    $('#cookies').onchange = onCookiesChange;
    $('#btnCookiesFile').onclick = async () => {
        const p = await api.BrowseFile('cookies');
        if (p) { state.cookiesFile = p; onCookiesChange(); }
    };

    ['#outDir', '#tmpl', '#proxy'].forEach((sel) => $(sel).addEventListener('change', scheduleSave));
    ['#noPlaylist', '#thumb'].forEach((sel) => $(sel).addEventListener('change', scheduleSave));

    $('#btnStart').onclick = startDownload;
    $('#btnCancel').onclick = cancelDownload;
    $('#btnRefreshDeps').onclick = refreshDeps;

    $('#btnClearLog').onclick = () => { $('#log').innerHTML = ''; state.logs = 0; };

    $('#btnHelp').onclick = async () => {
        $('#helpBody').textContent = await api.GetHelp();
        $('#helpModal').hidden = false;
    };
    $('#btnCloseHelp').onclick = () => { $('#helpModal').hidden = true; };
    $('#helpModal').addEventListener('click', (e) => {
        if (e.target.id === 'helpModal') $('#helpModal').hidden = true;
    });

    $('#btnReset').onclick = async () => {
        if (!confirm('恢复默认设置？当前输出目录与选项会被重置。')) return;
        const cfg = await api.ResetConfig();
        applyConfig(cfg);
        toast('已恢复默认设置', 'ok');
    };

    document.addEventListener('keydown', (e) => {
        if (e.ctrlKey && e.key === 'Enter') { e.preventDefault(); startDownload(); }
        if (e.key === 'Escape') {
            if (!$('#helpModal').hidden) { $('#helpModal').hidden = true; return; }
            if (state.running) cancelDownload();
        }
    });
}

/* ------------------------------------------------------------ boot */
async function waitBackend() {
    for (let i = 0; i < 40; i++) {
        if (window.go && window.go.main && window.go.main.App) return true;
        await new Promise((r) => setTimeout(r, 50));
    }
    return false;
}

async function init() {
    bindUI();
    state.backend = await waitBackend();
    if (state.backend) api = window.go.main.App;

    try {
        const cfg = await api.GetConfig();
        applyConfig(cfg);
    } catch (e) {
        console.error('load config failed', e);
    }

    if (state.backend && window.runtime) {
        window.runtime.EventsOn('log', (line) => addLog(line));
        window.runtime.EventsOn('progress', onProgress);
        window.runtime.EventsOn('state', onState);
        window.runtime.EventsOn('result', onResult);
    }

    refreshDeps();
    updateUrlCount();
    setStatus(state.backend ? '就绪 · 等待任务' : '预览模式 · 未连接后端', state.backend ? '' : 'busy');
    addLog(state.backend ? '== 环境已就绪，等待任务 ==' : '== 浏览器预览模式：下载操作为模拟 ==', state.backend ? 'ok' : 'warn');
    if (state.backend && state.cfg && state.cfg.help === undefined) {
        // noop
    }
}

window.addEventListener('DOMContentLoaded', init);
