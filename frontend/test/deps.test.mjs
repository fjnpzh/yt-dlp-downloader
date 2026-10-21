/* Regression test for the "两个依赖路径互相覆盖" bug.
 *
 * main.js is a plain browser script (no ESM exports), so instead of importing it
 * we evaluate it inside a tiny fake DOM and drive the real functions. This keeps
 * the test honest: it exercises the shipped code, not a copy of it.
 *
 * Run: node frontend/test/deps.test.mjs
 */
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));
const src = readFileSync(join(here, '..', 'src', 'main.js'), 'utf8');

/* ---------------------------------------------------------------- fake DOM */
function el(id) {
    return {
        id,
        value: '',
        checked: false,
        hidden: true,
        textContent: '',
        title: '',
        innerHTML: '',
        dataset: {},
        style: {},
        classList: { toggle() {}, add() {}, remove() {}, contains: () => false },
        appendChild() {},
        removeChild() {},
        removeAttribute() {},
        addEventListener() {},
        focus() {},
        scrollTop: 0,
        scrollHeight: 0,
    };
}

const nodes = new Map();
const document = {
    querySelector(sel) {
        if (!nodes.has(sel)) nodes.set(sel, el(sel));
        return nodes.get(sel);
    },
    querySelectorAll: () => [],
    createElement: () => el('created'),
    addEventListener() {},
};

const window = { addEventListener() {} };
const navigator = { clipboard: { readText: async () => '' } };
const confirm = () => true;

/* --------------------------------------------------------------- the app */
const factory = new Function(
    'document', 'window', 'navigator', 'confirm', 'setTimeout', 'clearTimeout',
    src + '\nreturn { state, applyConfig, collectConfig, pickDependency, saveConfig, setApi: (a) => { api = a; } };'
);
const app = factory(document, window, navigator, confirm, setTimeout, clearTimeout);

/* -------------------------------------------------- fake Go backend */
let stored = {
    output_dir: 'C:\\Users\\me\\Downloads',
    resolution: '1080',
    format: 'mp4',
    filename_tmpl: '',
    proxy: 'http://127.0.0.1:1180',
    cookies: 'none',
    no_playlist: true,
    thumbnail: false,
    author: 'fjnpzh',
    version: '2.0',
};

const backend = {
    GetConfig: async () => ({ ...stored }),
    // mimics app.go: merge + normalize + return the config (not a path)
    SaveConfig: async (c) => {
        stored = { ...stored, ...c };
        if (stored.cookies === 'Firefox') stored.cookies = 'firefox';
        return { ...stored };
    },
    GetDeps: async () => ({ ytdlpPath: stored.ytdlp_path || '', ffmpegPath: stored.ffmpeg_path || '' }),
    BrowseFile: async () => globalThis.__nextPick,
    ResetConfig: async () => ({ ...stored }),
};
app.setApi(backend);

/* ------------------------------------------------------------------ test */
let failures = 0;
function check(name, cond, detail) {
    if (cond) {
        console.log('  ok   ' + name);
    } else {
        failures++;
        console.log('  FAIL ' + name + (detail ? '  → ' + detail : ''));
    }
}

async function run() {
    // boot state: no dependency paths yet
    app.applyConfig(await backend.GetConfig());

    // 1) 指定 yt-dlp
    globalThis.__nextPick = 'C:\\Soft\\ffmpeg\\bin\\yt-dlp.exe';
    await app.pickDependency('ytdlp_path', 'exe');

    // 2) 指定 ffmpeg —— 不能把上一步的 yt-dlp 路径冲掉
    globalThis.__nextPick = 'C:\\Soft\\ffmpeg\\bin\\ffmpeg.exe';
    await app.pickDependency('ffmpeg_path', 'ffmpeg');

    // 3) 改一个无关字段（触发自动保存）
    document.querySelector('#proxy').value = 'http://127.0.0.1:7890';
    await app.saveConfig();

    console.log('backend 最终保存的配置:');
    console.log('  ytdlp_path  = ' + stored.ytdlp_path);
    console.log('  ffmpeg_path = ' + stored.ffmpeg_path);

    check('yt-dlp 路径未被覆盖', stored.ytdlp_path === 'C:\\Soft\\ffmpeg\\bin\\yt-dlp.exe', stored.ytdlp_path);
    check('ffmpeg 路径已保存', stored.ffmpeg_path === 'C:\\Soft\\ffmpeg\\bin\\ffmpeg.exe', stored.ffmpeg_path);
    check('无关字段保存后两路径仍在', !!stored.ytdlp_path && !!stored.ffmpeg_path);

    // 4) 再次保存（模拟连续自动保存）后仍然稳定
    await app.saveConfig();
    await app.saveConfig();
    check('多次保存后依然稳定', stored.ytdlp_path === 'C:\\Soft\\ffmpeg\\bin\\yt-dlp.exe' && stored.ffmpeg_path === 'C:\\Soft\\ffmpeg\\bin\\ffmpeg.exe');

    // 5) 重新加载配置后仍能读到两条路径
    app.applyConfig(await backend.GetConfig());
    const reloaded = app.collectConfig();
    check('重新加载后 collectConfig 仍带两条路径',
        reloaded.ytdlp_path === 'C:\\Soft\\ffmpeg\\bin\\yt-dlp.exe' &&
        reloaded.ffmpeg_path === 'C:\\Soft\\ffmpeg\\bin\\ffmpeg.exe',
        JSON.stringify([reloaded.ytdlp_path, reloaded.ffmpeg_path]));

    if (failures) {
        console.log('\n' + failures + ' 个用例失败');
        process.exit(1);
    }
    console.log('\n全部用例通过');
}

run().catch((e) => { console.error(e); process.exit(1); });
