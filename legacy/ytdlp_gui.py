# -*- coding: utf-8 -*-
"""yt-dlp GUI downloader (ASCII source; UI text pulled from zh_strings.py)."""
import os
import re
import json
import queue
import sys
import shutil
import sqlite3
import threading
import subprocess
from pathlib import Path

import tkinter as tk
from tkinter import ttk, filedialog, messagebox, scrolledtext

import zh_strings as _zh
L = _zh.L

# ---------- constants ----------
APP_TITLE = L["title"]
# When frozen with PyInstaller (--onefile) __file__ points into the _MEIPASS
# temp cache; sys.executable is the real path of the running exe, so BASE_DIR
# resolves to the folder that actually contains yt-dlp_x86.exe / ffmpeg.exe /
# ytdlp_gui_config.json.
if getattr(sys, "frozen", False):
    BASE_DIR = Path(sys.executable).resolve().parent
else:
    BASE_DIR = Path(__file__).resolve().parent
YTDLP_EXE = str(BASE_DIR / "yt-dlp_x86.exe")
FFMPEG_EXE = str(BASE_DIR / "ffmpeg.exe")
CONFIG_FILE = str(BASE_DIR / "ytdlp_gui_config.json")

RESOLUTION_OPTIONS = {
    L["res_best"]:   "",                            # no height constraint
    "4K (2160p)":    "[height<=2160]",
    "1440p":         "[height<=1440]",
    "1080p":         "[height<=1080]",
    "720p":          "[height<=720]",
    "480p":          "[height<=480]",
    "360p":          "[height<=360]",
}

# Each format: (format_expression, merge_output_format, post_processing_args)
# MP4 formats restrict video stream to mp4 container + m4a audio so the merged
# result is a true .mp4 (yt-dlp would otherwise merge to .webm by default).
FORMAT_OPTIONS = {
    L["fmt_mp4"]:   ("bestvideo[ext=mp4]{h}+bestaudio[ext=m4a]/bestvideo[ext=mp4]+bestaudio[ext=m4a]/best", "mp4", []),
    L["fmt_video"]: ("bestvideo[ext=mp4]{h}/bestvideo/best", "mp4", []),
    L["fmt_mp3"]:   ("bestaudio/best", None, ["--extract-audio", "--audio-format", "mp3", "--audio-quality", "0"]),
}

# Browser choices for --cookies-from-browser (display name -> yt-dlp browser id).
# Chrome locks its cookie DB while the browser is running, so users may prefer
# Firefox/Edge which can usually be read without closing the browser.
COOKIES_MAP = {
    "Chrome": "chrome",
    "Edge":   "edge",
    "Firefox": "firefox",
}
COOKIES_NONE_LABEL = L["cookies_none"]


class YtDlpGUI:
    def __init__(self, root):
        self.root = root
        self.root.title(APP_TITLE)
        self.root.geometry("800x740")
        self.root.minsize(680, 600)

        self.msg_queue = queue.Queue()
        self.current_proc = None
        self.is_downloading = False

        self.output_dir = tk.StringVar(value=str(Path.home() / "Downloads"))
        self.is_dir_valid = False
        self._ensure_default_dir()

        self._setup_style()
        self._build_ui()
        self._load_config()
        self._poll_queue()
        self.root.protocol("WM_DELETE_WINDOW", self._on_close)

    # ---------- UI ----------
    def _setup_style(self):
        """Configure a clean, modern ttk theme."""
        style = ttk.Style(self.root)
        try:
            style.theme_use("clam")
        except tk.TclError:
            pass
        base_font = ("Microsoft YaHei UI", 10)
        small_font = ("Microsoft YaHei UI", 9)
        style.configure(".", font=base_font)
        style.configure("TLabel", font=base_font)
        style.configure("TLabelFrame.Label", font=("Microsoft YaHei UI", 10, "bold"), foreground="#1a73e8")
        style.configure("TEntry", font=base_font)
        style.configure("TCombobox", font=base_font)
        style.configure("TCheckbutton", font=small_font)
        style.configure("TButton", font=("Microsoft YaHei UI", 10), padding=(12, 6))
        style.configure("DL.TButton", font=("Microsoft YaHei UI", 14, "bold"),
                        foreground="white", background="#1a73e8",
                        padding=(20, 10), relief="flat")
        style.map("DL.TButton",
                  background=[("active", "#1668c9"), ("disabled", "#8fb7ea")],
                  foreground=[("disabled", "#e8eef7")])
        style.configure("Accent.TButton", font=("Microsoft YaHei UI", 10, "bold"),
                        foreground="#1a73e8", background="#e8f0fe")
        style.map("Accent.TButton",
                  background=[("active", "#d4e3fb")])
        style.configure("Status.TLabel", font=("Microsoft YaHei UI", 10, "bold"))
        style.configure("Hint.TLabel", font=small_font, foreground="#888888")

    def _build_ui(self):
        main = ttk.Frame(self.root, padding=10)
        main.pack(fill="both", expand=True)
        main.columnconfigure(0, weight=1)

        row = ttk.LabelFrame(main, text=L["url_lbl"], padding=8)
        row.grid(row=0, column=0, sticky="ew", pady=(0, 8))
        row.columnconfigure(0, weight=1)
        self.url_text = scrolledtext.ScrolledText(row, height=4, wrap="word", font=("Consolas", 11))
        self.url_text.grid(row=0, column=0, sticky="ew")

        row = ttk.LabelFrame(main, text=L["outdir"], padding=8)
        row.grid(row=1, column=0, sticky="ew", pady=(0, 8))
        row.columnconfigure(1, weight=1)
        ttk.Entry(row, textvariable=self.output_dir).grid(row=0, column=1, sticky="ew", padx=(8, 0))
        ttk.Button(row, text=L["browse"], command=self._choose_dir).grid(row=0, column=2, padx=(4, 0))
        ttk.Button(row, text=L["opendir"], command=self._open_dir).grid(row=0, column=3, padx=(4, 0))

        row = ttk.LabelFrame(main, text=L["options"], padding=8)
        row.grid(row=2, column=0, sticky="ew", pady=(0, 8))
        row.columnconfigure(1, weight=1)
        row.columnconfigure(3, weight=1)

        ttk.Label(row, text=L["resolution"]).grid(row=0, column=0, sticky="w", padx=(0, 8))
        self.res_var = tk.StringVar(value=list(RESOLUTION_OPTIONS.keys())[0])
        ttk.Combobox(row, textvariable=self.res_var, state="readonly", width=26,
                     values=list(RESOLUTION_OPTIONS.keys())).grid(row=0, column=1, sticky="ew", padx=(0, 20))

        ttk.Label(row, text=L["output_format"]).grid(row=0, column=2, sticky="w", padx=(0, 8))
        self.fmt_var = tk.StringVar(value=list(FORMAT_OPTIONS.keys())[0])
        ttk.Combobox(row, textvariable=self.fmt_var, state="readonly", width=20,
                     values=list(FORMAT_OPTIONS.keys())).grid(row=0, column=3, sticky="ew")

        ttk.Label(row, text=L["filename_tmpl"]).grid(row=1, column=0, sticky="w", padx=(0, 8), pady=(8, 0))
        self.otmpl_var = tk.StringVar(value="")
        ttk.Entry(row, textvariable=self.otmpl_var).grid(row=1, column=1, columnspan=3, sticky="ew", pady=(8, 0))

        self.opt_noplaylist = tk.BooleanVar(value=True)
        ttk.Checkbutton(row, text=L["no_playlist"], variable=self.opt_noplaylist).grid(row=2, column=0, columnspan=2, sticky="w", pady=(8, 0))
        self.opt_thumbnail = tk.BooleanVar(value=False)
        ttk.Checkbutton(row, text=L["thumbnail"], variable=self.opt_thumbnail).grid(row=2, column=2, columnspan=2, sticky="w", pady=(8, 0))

        # proxy row
        ttk.Label(row, text=L["proxy"]).grid(row=3, column=0, sticky="w", padx=(0, 8), pady=(8, 0))
        self.proxy_var = tk.StringVar(value="")
        ttk.Entry(row, textvariable=self.proxy_var).grid(row=3, column=1, columnspan=3, sticky="ew", pady=(8, 0))
        self.proxy_tip = ttk.Label(row, text=L["proxy_ph"], foreground="gray")
        self.proxy_tip.grid(row=4, column=1, columnspan=3, sticky="w")

        # cookies row (auto-read from browser to bypass bot checks)
        ttk.Label(row, text=L["cookies_lbl"]).grid(row=5, column=0, sticky="w", padx=(0, 8), pady=(8, 0))
        self.cookies_var = tk.StringVar(value=COOKIES_NONE_LABEL)
        self.cookies_file_var = tk.StringVar(value="")
        self.cookies_cmb = ttk.Combobox(row, textvariable=self.cookies_var, width=26,
                                        values=[COOKIES_NONE_LABEL] + list(COOKIES_MAP.keys()) + [L["cookies_file"]],
                                        state="readonly")
        self.cookies_cmb.bind("<<ComboboxSelected>>", self._on_cookies_select)
        self.cookies_cmb.grid(row=5, column=1, columnspan=3, sticky="ew", pady=(8, 0))
        self.cookies_tip = ttk.Label(row, text=L["cookies_ph"], foreground="gray")
        self.cookies_tip.grid(row=6, column=1, columnspan=3, sticky="w")

        # version row
        row = ttk.LabelFrame(main, text=L["version"], padding=8)
        row.grid(row=3, column=0, sticky="ew", pady=(0, 8))
        row.columnconfigure(1, weight=1)
        self.ver_var = tk.StringVar(value="--")
        ttk.Label(row, text=L["cur_version"]).grid(row=0, column=0, sticky="w", padx=(0, 8))
        ttk.Label(row, textvariable=self.ver_var).grid(row=0, column=1, sticky="w")
        ttk.Button(row, text=L["btn_check"], command=self._check_version).grid(row=0, column=2, padx=(4, 4))
        self.upgrade_btn = ttk.Button(row, text=L["btn_upgrade"], command=self._upgrade_ytdlp, state="disabled")
        self.upgrade_btn.grid(row=0, column=3, padx=(4, 0))
        ttk.Button(row, text=L["btn_help"], style="Accent.TButton", command=self._show_help).grid(row=0, column=4, padx=(8, 4))
        ttk.Button(row, text=L["btn_save_cfg"], style="Accent.TButton", command=self._save_config).grid(row=0, column=5, padx=(4, 4))
        ttk.Button(row, text=L["btn_reset_cfg"], style="Accent.TButton", command=self._reset_config).grid(row=0, column=6, padx=(4, 0))

        row = ttk.Frame(main)
        row.grid(row=4, column=0, sticky="ew", pady=(4, 8))
        row.columnconfigure(0, weight=1)
        self.dl_btn = ttk.Button(row, text=L["btn_download"], style="DL.TButton", command=self._start_download)
        self.dl_btn.grid(row=0, column=0, sticky="ew", padx=(0, 8))
        self.cancel_btn = ttk.Button(row, text=L["btn_cancel"], command=self._cancel_download, state="disabled")
        self.cancel_btn.grid(row=0, column=1, padx=(8, 0))

        self.progress = ttk.Progressbar(main, mode="determinate", maximum=1000)
        self.progress.grid(row=5, column=0, sticky="ew", pady=(0, 4))
        self.status_lbl = ttk.Label(main, text=L["status_ready"], anchor="w", style="Status.TLabel")
        self.status_lbl.grid(row=6, column=0, sticky="ew", pady=(0, 8))

        row = ttk.LabelFrame(main, text=L["log_title"], padding=4)
        row.grid(row=7, column=0, sticky="nsew")
        main.rowconfigure(7, weight=1)
        self.log_text = scrolledtext.ScrolledText(row, height=8, state="disabled", wrap="word",
                                                  font=("Consolas", 10), background="#1e1e1e", foreground="#d4d4d4")
        self.log_text.pack(fill="both", expand=True)

        # about / copyright bar
        self.about_lbl = ttk.Label(main, text=L["about"], anchor="e", foreground="gray")
        self.about_lbl.grid(row=8, column=0, sticky="ew", pady=(4, 0))

    # ---------- helpers ----------
    def _ensure_default_dir(self):
        try:
            Path(self.output_dir.get()).mkdir(parents=True, exist_ok=True)
            self.is_dir_valid = True
        except OSError:
            self.is_dir_valid = False

    def _choose_dir(self):
        chosen = filedialog.askdirectory(initialdir=self.output_dir.get() or str(Path.home()))
        if chosen:
            self.output_dir.set(chosen)
            self.is_dir_valid = True
            self._log(L["outdir"] + ": " + chosen)

    def _open_dir(self):
        d = self.output_dir.get()
        if d and os.path.isdir(d):
            os.startfile(d)
        else:
            messagebox.showwarning(APP_TITLE, L["warn_bad_dir"])

    def _log(self, message):
        self.log_text.configure(state="normal")
        self.log_text.insert("end", message + "\n")
        self.log_text.see("end")
        self.log_text.configure(state="disabled")

    def _set_status(self, text):
        self.status_lbl.config(text=text)

    # ---------- download ----------
    def _on_cookies_select(self, event=None):
        """When the user picks the 'Cookies file...' option, open a file picker."""
        if self.cookies_var.get() == L["cookies_file"]:
            f = filedialog.askopenfilename(
                title=APP_TITLE,
                filetypes=[("Cookies 文件", "*.txt *.cookies *.txt"), ("所有文件", "*.*")],
            )
            if f:
                self.cookies_file_var.set(f)
            else:
                # user cancelled: revert to the default (no cookies) choice
                self.cookies_var.set(COOKIES_NONE_LABEL)

    def _browser_running(self, browser_id):
        """Return True if the given yt-dlp browser id's process is currently running."""
        proc_names = {"chrome": "chrome", "edge": "msedge", "firefox": "firefox"}
        exe_name = proc_names.get(browser_id, browser_id) + ".exe"
        try:
            out = subprocess.run(
                ["tasklist", "/FI", f"IMAGENAME eq {exe_name}"],
                capture_output=True, text=True, errors="ignore",
                creationflags=subprocess.CREATE_NO_WINDOW,
                timeout=15,
            )
            return exe_name.lower() in out.stdout.lower()
        except Exception:
            # If we cannot inspect, do not block the download.
            return False

    def _detect_chrome_profile(self):
        """Auto-detect the Chrome profile that holds a logged-in YouTube session.

        Chrome keeps cookies per-profile under 'User Data\\Profile N'. yt-dlp
        only reads the 'Default' profile by default, which often does not exist,
        causing 'Failed to decrypt with DPAPI'. Returns a yt-dlp browser spec
        like 'chrome:Profile 2', or plain 'chrome' as a fallback.
        """
        base = os.path.join(os.environ.get("LOCALAPPDATA", ""), "Google", "Chrome", "User Data")
        best = None
        best_mtime = 0
        try:
            for entry in os.scandir(base):
                if not (entry.is_dir() and entry.name.startswith("Profile ")):
                    continue
                ck = os.path.join(entry.path, "Network", "Cookies")
                if not os.path.isfile(ck):
                    continue
                try:
                    con = sqlite3.connect("file:" + ck + "?mode=ro&immutable=1", uri=True)
                    try:
                        cur = con.cursor()
                        cur.execute(
                            "SELECT name FROM cookies WHERE host_key LIKE '%youtube.com' "
                            "AND name IN ('SID','LOGIN_INFO','__Secure-3PAPISID')"
                        )
                        if cur.fetchone():
                            m = os.path.getmtime(ck)
                            if m > best_mtime:
                                best_mtime = m
                                best = entry.name
                    finally:
                        con.close()
                except Exception:
                    continue
        except Exception:
            pass
        return ("chrome:" + best) if best else "chrome"

    def _start_download(self):
        if self.is_downloading:
            messagebox.showinfo(APP_TITLE, L["err_busy"])
            return
        urls = [u.strip() for u in self.url_text.get("1.0", "end").splitlines() if u.strip()]
        if not urls:
            messagebox.showwarning(APP_TITLE, L["err_need_url"])
            return

        out_dir = self.output_dir.get().strip() or str(Path.home() / "Downloads")
        self.output_dir.set(out_dir)
        try:
            os.makedirs(out_dir, exist_ok=True)
        except OSError as e:
            messagebox.showerror(APP_TITLE, L["err_no_dir"] + "\n" + str(e))
            return
        if not os.path.isfile(YTDLP_EXE):
            messagebox.showerror(APP_TITLE, L["err_no_ytdlp"] + "\n" + YTDLP_EXE)
            return

        args = [YTDLP_EXE]
        height = RESOLUTION_OPTIONS[self.res_var.get()]
        fmt_expr, merge_fmt, post_args = FORMAT_OPTIONS[self.fmt_var.get()]

        # fill height constraint placeholder into the format expression
        fmt_expr = fmt_expr.replace("{h}", height)
        args.append("-f")
        args.append(fmt_expr)
        args += post_args
        if merge_fmt:
            args += ["--merge-output-format", merge_fmt]
        args += ["--paths", out_dir]

        if self.otmpl_var.get().strip():
            args += ["-o", self.otmpl_var.get().strip()]

        if self.opt_noplaylist.get():
            args += ["--no-playlist"]
        else:
            args += ["--yes-playlist"]

        if self.opt_thumbnail.get():
            args += ["--write-thumbnail"]

        proxy = self.proxy_var.get().strip()
        if proxy:
            args += ["--proxy", proxy]

        # cookies: none (default) / a cookies.txt file (no browser close needed)
        # / read from a browser (requires that browser to be closed first).
        if self.cookies_var.get() == COOKIES_NONE_LABEL:
            pass  # do not pass any cookies argument
        elif self.cookies_var.get() == L["cookies_file"]:
            cf = self.cookies_file_var.get().strip()
            if cf and os.path.isfile(cf):
                args += ["--cookies", cf]
            else:
                messagebox.showwarning(APP_TITLE, L["err_cookies_file"] + "\n" + cf)
                return
        else:
            cookies_browser = COOKIES_MAP.get(self.cookies_var.get())
            if cookies_browser:
                if self._browser_running(cookies_browser):
                    messagebox.showwarning(APP_TITLE, L["cookies_warn"])
                    return
                # For Chrome, auto-detect the profile that actually holds the
                # logged-in YouTube session (Chrome's 'Default' profile often
                # does not exist, which makes yt-dlp fail with DPAPI errors).
                spec = self._detect_chrome_profile() if cookies_browser == "chrome" else cookies_browser
                if spec != cookies_browser:
                    self._log(L["cookies_profile_log"] + spec)
                args += ["--cookies-from-browser", spec]

        args += ["--concurrent-fragments", "4"]
        # Node.js is used as the JS runtime for full YouTube format extraction
        # (yt-dlp needs a JS runtime like deno/node to parse player responses).
        js_rt = "node" if shutil.which("node") else ("deno" if shutil.which("deno") else None)
        if js_rt:
            args += ["--js-runtimes", js_rt]
        if os.path.isfile(FFMPEG_EXE):
            args += ["--ffmpeg-location", FFMPEG_EXE]
        # Force UTF-8 output from yt-dlp so log lines (paths/titles) display
        # correctly instead of being mangled by the console codepage.
        args += ["--encoding", "utf-8"]
        args += urls

        self.is_downloading = True
        self.dl_btn.config(state="disabled")
        self.cancel_btn.config(state="normal")
        self._set_status(L["status_downloading"])
        self._log("=" * 70)
        self._log(L["cmd"] + " " + " ".join(_quote(a) for a in args))

        threading.Thread(target=self._worker, args=(args,), daemon=True).start()

    def _worker(self, args):
        try:
            self.current_proc = subprocess.Popen(
                args, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                text=True, encoding="utf-8", errors="replace", bufsize=1,
                creationflags=subprocess.CREATE_NO_WINDOW,
            )
            for raw in self.current_proc.stdout:
                line = raw.rstrip()
                if not line:
                    continue
                self.msg_queue.put(("log", line))
                self._parse_progress(line)
            self.current_proc.wait()
            rc = self.current_proc.returncode
            self.current_proc = None
            self.msg_queue.put(("log", L["done_code"] + " " + str(rc)))
            self.msg_queue.put(("done", rc))
        except Exception as e:
            self.msg_queue.put(("log", L["err_log"] + " " + str(e)))
            self.msg_queue.put(("done", -1))

    def _parse_progress(self, line):
        m = re.search(r"\[download\]\s+(\d+(?:\.\d+)?)%", line)
        if m:
            self.msg_queue.put(("progress", float(m.group(1))))
        elif "100%" in line:
            self.msg_queue.put(("progress", 100.0))

    def _cancel_download(self):
        if self.current_proc:
            try:
                self.current_proc.terminate()
                self._log(L["cancel_log"])
            except OSError:
                pass

    # ---------- version check / upgrade ----------
    def _run_silent(self, args):
        """Run yt-dlp command capturing output (used by version/upgrade)."""
        try:
            proc = subprocess.Popen(
                args, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                text=True, encoding="utf-8", errors="replace",
                creationflags=subprocess.CREATE_NO_WINDOW,
            )
            out, _ = proc.communicate()
            return proc.returncode, out.strip()
        except OSError as e:
            return -1, str(e)

    def _check_version(self):
        if self.is_downloading:
            messagebox.showinfo(APP_TITLE, L["upd_busy"])
            return
        self.ver_var.set(L["chk_updating"])
        self._set_status(L["chk_updating"])

        def worker():
            rc, out = self._run_silent([YTDLP_EXE, "--version"])
            if rc == 0 and out:
                self.msg_queue.put(("version_result", out))
            else:
                self.msg_queue.put(("log", L["err_log"] + " " + (out or ("rc=" + str(rc)))))
                self.msg_queue.put(("version_result", "--"))

        threading.Thread(target=worker, daemon=True).start()

    def _upgrade_ytdlp(self):
        if self.is_downloading:
            messagebox.showinfo(APP_TITLE, L["upd_busy"])
            return
        if not messagebox.askyesno(APP_TITLE, L["upd_confirm"]):
            return
        self.upgrade_btn.config(state="disabled")
        self._set_status(L["upd_doing"])
        self._log(L["upd_doing"])

        def worker():
            rc, out = self._run_silent([YTDLP_EXE, "--update", "--verbose"])
            if rc == 0:
                self.msg_queue.put(("log", L["upd_done"]))
                self.msg_queue.put(("version_result", out.splitlines()[-1] if out else ""))
            else:
                self.msg_queue.put(("log", L["upd_fail"] + " rc=" + str(rc)))
                if out:
                    self.msg_queue.put(("log", out))
            self.msg_queue.put(("log", ""))

        threading.Thread(target=worker, daemon=True).start()

    # ---------- poll ----------
    def _poll_queue(self):
        try:
            while True:
                kind, payload = self.msg_queue.get_nowait()
                if kind == "log":
                    self._log(payload)
                elif kind == "progress":
                    self.progress["value"] = payload * 10
                elif kind == "done":
                    self._on_download_finished(payload)
                elif kind == "version_result":
                    ver = str(payload).strip()
                    self.ver_var.set(ver or "--")
                    self.upgrade_btn.config(state="normal")
                    self._set_status(L["version"] + " " + (ver or "--"))
        except queue.Empty:
            pass
        self.root.after(100, self._poll_queue)

    def _on_download_finished(self, rc):
        self.is_downloading = False
        self.current_proc = None
        self.dl_btn.config(state="normal")
        self.cancel_btn.config(state="disabled")
        if rc == 0:
            self.progress["value"] = 1000
            self._set_status(L["status_success"] + " " + self.output_dir.get())
            self._log(L["done_file"] + " " + self.output_dir.get())
        else:
            self._set_status(L["status_fail"])
            self._log(L["status_fail"])

    # ---------- configuration persistence ----------
    def _load_config(self):
        try:
            with open(CONFIG_FILE, "r", encoding="utf-8") as f:
                cfg = json.load(f)
        except (OSError, ValueError):
            return
        if cfg.get("output_dir"):
            self.output_dir.set(cfg["output_dir"])
        res = cfg.get("resolution")
        if res in RESOLUTION_OPTIONS:
            self.res_var.set(res)
        fmt = cfg.get("format")
        if fmt in FORMAT_OPTIONS:
            self.fmt_var.set(fmt)
        self.otmpl_var.set(cfg.get("filename_tmpl", ""))
        self.proxy_var.set(cfg.get("proxy", ""))
        if cfg.get("cookies"):
            self.cookies_var.set(cfg["cookies"])
        else:
            self.cookies_var.set(COOKIES_NONE_LABEL)
        self.opt_noplaylist.set(cfg.get("no_playlist", True))
        self.opt_thumbnail.set(cfg.get("thumbnail", False))
        self._ensure_default_dir()
        self._set_status(L["cfg_loaded"])

    def _save_config(self):
        # preserve the user-editable help/meta fields if the config file already has them
        old = {}
        try:
            with open(CONFIG_FILE, "r", encoding="utf-8") as f:
                old = json.load(f)
        except (OSError, ValueError):
            old = {}
        cfg = {
            "output_dir": self.output_dir.get(),
            "resolution": self.res_var.get(),
            "format": self.fmt_var.get(),
            "filename_tmpl": self.otmpl_var.get(),
            "proxy": self.proxy_var.get(),
            "cookies": self.cookies_var.get(),
            "no_playlist": self.opt_noplaylist.get(),
            "thumbnail": self.opt_thumbnail.get(),
            "author": old.get("author", L.get("about_author", "fjnpzh")),
            "version": old.get("version", "1.0"),
            "help": old.get("help", L.get("help_default", "")),
        }
        try:
            with open(CONFIG_FILE, "w", encoding="utf-8") as f:
                json.dump(cfg, f, ensure_ascii=False, indent=2)
            self._set_status(L["cfg_saved"])
        except OSError as e:
            self._set_status(L["err_log"] + " " + str(e))

    def _reset_config(self):
        self.output_dir.set(str(Path.home() / "Downloads"))
        self.res_var.set(list(RESOLUTION_OPTIONS.keys())[0])
        self.fmt_var.set(list(FORMAT_OPTIONS.keys())[0])
        self.otmpl_var.set("")
        self.proxy_var.set("")
        self.cookies_var.set(COOKIES_NONE_LABEL)
        self.opt_noplaylist.set(True)
        self.opt_thumbnail.set(False)
        self._ensure_default_dir()
        self._set_status(L["cfg_reset"])
        self._save_config()

    def _show_help(self):
        """Show the yt-dlp parameter usage help (editable via config file)."""
        text = ""
        try:
            with open(CONFIG_FILE, "r", encoding="utf-8") as f:
                text = json.load(f).get("help", "")
        except (OSError, ValueError):
            text = ""
        if not text:
            text = L.get("help_default", L["btn_help"])

        win = tk.Toplevel(self.root)
        win.title(L["help_title"])
        win.geometry("620x520")
        win.minsize(480, 360)
        box = scrolledtext.ScrolledText(win, wrap="word", font=("Microsoft YaHei UI", 10),
                                        padx=12, pady=12)
        box.pack(fill="both", expand=True, padx=8, pady=8)
        box.insert("1.0", text)
        box.configure(state="disabled")

    def _on_close(self):
        if self.is_downloading:
            if not messagebox.askyesno(APP_TITLE, L["confirm_exit"]):
                return
            if self.current_proc:
                try:
                    self.current_proc.terminate()
                except OSError:
                    pass
        self._save_config()
        self.root.destroy()


def _quote(s):
    s = str(s)
    if " " in s and not s.startswith('"'):
        return '"' + s.replace('"', '\\"') + '"'
    return s


def main():
    root = tk.Tk()
    try:
        ttk.Style(root).theme_use("clam")
    except Exception:
        pass
    YtDlpGUI(root)
    root.mainloop()


if __name__ == "__main__":
    main()