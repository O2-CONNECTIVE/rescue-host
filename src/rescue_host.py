"""Windows 向け Upterm レスキュー GUI。Python で実行するか、単一の EXE にまとめる。"""
from __future__ import annotations

import json
import os
import queue
import re
import sys
import threading
import time
import tkinter as tk
from tkinter import filedialog, messagebox, scrolledtext, ttk
from pathlib import Path
from uuid import uuid4

from rescue_core import (
    RescueError, Settings, APP_VERSION, START_BLOCKED_REASON, require_secure_session_backend, build_mac_forward_command, check_local_sshd,
    check_upterm_version, data_dir, get_session_info, is_session_active,
    load_session_state, load_settings, parse_host_json, prepare_authorized_keys,
    record_session_state, remove_session_state_if_name, resolve_known_hosts,
    resolve_upterm, run_upterm, save_settings, stop_session, upterm_cmd_start,
)
from rescue_watchdog import launch_watchdog, watchdog_main

APP_TITLE = "Rescue Host v" + APP_VERSION + " | 新規接続停止中"
INK = "#172836"
MUTED = "#63758A"
BLUE = "#155A92"
GREEN = "#007F60"
RED = "#B42337"
CANVAS = "#F4F6F9"
WHITE = "#FFFFFF"


class RescueApp:
    def __init__(self, root: tk.Tk):
        self.root = root
        self.root.title(APP_TITLE)
        self.root.geometry("850x665")
        self.root.minsize(750, 580)
        self.root.configure(bg=CANVAS)
        self.events: queue.Queue[tuple[str, object]] = queue.Queue()
        self.busy = False
        self.poll_busy = False
        self.expiry_stop_attempted = False
        self.session: dict | None = load_session_state()
        try:
            self.config = load_settings()
        except RescueError as exc:
            self.config = Settings()
            messagebox.showwarning("設定の読み込み", str(exc) + "\n既定設定で起動します。")
        self._styles()
        self._layout()
        self._load_into_form()
        self._refresh_state()
        self.root.after(150, self._drain_events)
        self.root.after(1000, self._tick)
        self.root.after(400, self._recover)
        self.root.protocol("WM_DELETE_WINDOW", self._request_close)

    def _styles(self):
        style = ttk.Style(self.root)
        if "clam" in style.theme_names():
            style.theme_use("clam")
        style.configure("TFrame", background=CANVAS)
        style.configure("Panel.TFrame", background=WHITE)
        style.configure("TLabel", background=CANVAS, foreground=INK, font=("Yu Gothic UI", 10))
        style.configure("Panel.TLabel", background=WHITE, foreground=INK, font=("Yu Gothic UI", 10))
        style.configure("Sub.Panel.TLabel", background=WHITE, foreground=MUTED, font=("Yu Gothic UI", 9))
        style.configure("Title.TLabel", background=CANVAS, foreground=INK, font=("Yu Gothic UI", 19, "bold"))
        style.configure("Heading.Panel.TLabel", background=WHITE, foreground=INK, font=("Yu Gothic UI", 12, "bold"))
        style.configure("TButton", font=("Yu Gothic UI", 10), padding=(12, 9))
        style.configure("TEntry", padding=6)
        style.configure("TCombobox", padding=6)
        style.configure("Primary.TButton", background=GREEN, foreground="white", borderwidth=0)
        style.map("Primary.TButton", background=[("disabled", "#B1C2C0"), ("active", "#006D54")])
        style.configure("Stop.TButton", background=RED, foreground="white", borderwidth=0)
        style.map("Stop.TButton", background=[("disabled", "#E8BBC3"), ("active", "#921D30")])
        style.configure("TNotebook", background=CANVAS, borderwidth=0)
        style.configure("TNotebook.Tab", font=("Yu Gothic UI", 10), padding=(18, 10))

    def _panel(self, parent, pad=(20, 16)):
        frame = ttk.Frame(parent, style="Panel.TFrame", padding=pad)
        frame.columnconfigure(0, weight=1)
        return frame

    def _layout(self):
        shell = ttk.Frame(self.root, padding=(24, 17, 24, 12))
        shell.pack(fill="both", expand=True)
        ttk.Label(shell, text="Rescue Host", style="Title.TLabel").pack(anchor="w")
        ttk.Label(shell, text="開発版：操作範囲の設定と旧セッションの停止要求のみ。新規接続は無効です。",
                  foreground=MUTED).pack(anchor="w", pady=(3, 12))

        self.tabs = ttk.Notebook(shell)
        self.tabs.pack(fill="both", expand=True)
        self.tab_control = ttk.Frame(self.tabs, padding=(0, 12, 0, 0))
        self.tab_settings = ttk.Frame(self.tabs, padding=(0, 12, 0, 0))
        self.tabs.add(self.tab_control, text="接続管理")
        self.tabs.add(self.tab_settings, text="環境設定")
        self._create_control_tab()
        self._create_settings_tab()
        foot = ttk.Frame(shell)
        foot.pack(fill="x", pady=(8, 0))
        ttk.Label(foot, text="権限制御・強制失効は実環境未検証 / 新規接続・共有は無効", foreground=MUTED).pack(side="left")
        ttk.Label(foot, text=APP_VERSION, foreground=MUTED).pack(side="right")

    def _create_control_tab(self):
        panel = self._panel(self.tab_control)
        panel.pack(fill="x", pady=(0, 12))
        row = ttk.Frame(panel, style="Panel.TFrame")
        row.pack(fill="x")
        ttk.Label(row, text="接続状態", style="Heading.Panel.TLabel").pack(side="left")
        self.state_tag = ttk.Label(row, text="待機中", style="Panel.TLabel", foreground=GREEN)
        self.state_tag.pack(side="right")
        self.countdown = ttk.Label(panel, text="未接続", style="Panel.TLabel", font=("Yu Gothic UI", 26, "bold"))
        self.countdown.pack(anchor="w", pady=(12, 3))
        self.summary = ttk.Label(panel, text="環境設定で公開鍵を登録してから開始してください。",
                                 style="Sub.Panel.TLabel")
        self.summary.pack(anchor="w")
        action = ttk.Frame(panel, style="Panel.TFrame")
        action.pack(anchor="w", pady=(16, 0))
        self.start_btn = ttk.Button(action, text="このPCへの接続を許可", style="Primary.TButton", command=self._start)
        self.start_btn.pack(side="left", padx=(0, 9))
        self.stop_btn = ttk.Button(action, text="停止を要求", style="Stop.TButton", command=self._stop)
        self.stop_btn.pack(side="left")
        self.close_unconfirmed_btn = ttk.Button(action, text="停止未確認のまま画面だけ閉じる",
                                               command=self._close_unconfirmed)
        self.close_unconfirmed_btn.pack(side="left", padx=(9, 0))

        details = self._panel(self.tab_control)
        details.pack(fill="x", pady=(0, 12))
        ttk.Label(details, text="Mac側へ渡す接続コマンド", style="Heading.Panel.TLabel").pack(anchor="w")
        ttk.Label(details, text="新規接続とコマンド共有は無効です。旧セッションの権限制限・終了は保証できません。",
                  style="Sub.Panel.TLabel").pack(anchor="w", pady=(4, 10))
        self.ssh_command = tk.StringVar(value="新規接続・接続コマンドの共有は無効です")
        self.join_entry = ttk.Entry(details, textvariable=self.ssh_command, state="readonly", font=("Consolas", 9))
        self.join_entry.pack(fill="x")
        copy_row = ttk.Frame(details, style="Panel.TFrame")
        copy_row.pack(fill="x", pady=(10, 0))
        self.copy_btn = ttk.Button(copy_row, text="接続コマンドをコピー", command=self._copy_join)
        self.copy_btn.pack(side="left")
        self.copy_forward_btn = ttk.Button(copy_row, text="MacのSSH転送コマンドをコピー", command=self._copy_forward)
        self.copy_forward_btn.pack(side="left", padx=(8, 0))

        notes = self._panel(self.tab_control, (20, 14))
        notes.pack(fill="both", expand=True)
        ttk.Label(notes, text="動作ログ", style="Heading.Panel.TLabel").pack(anchor="w", pady=(0, 7))
        self.logs = scrolledtext.ScrolledText(notes, height=7, wrap="word", state="disabled", font=("Consolas", 9),
                                             bg="#F8FAFC", fg=INK, borderwidth=0)
        self.logs.pack(fill="both", expand=True)

    def _create_settings_tab(self):
        # 保存操作は常に表示する。長い設定や大きな文字の設定領域は独立してスクロールする。
        footer = self._panel(self.tab_settings, (20, 8))
        footer.pack(side="bottom", fill="x")
        scroll_holder = ttk.Frame(self.tab_settings, style="Panel.TFrame")
        scroll_holder.pack(side="top", fill="both", expand=True)
        scroll = tk.Canvas(scroll_holder, bg=WHITE, highlightthickness=0)
        scroll_bar = ttk.Scrollbar(scroll_holder, orient="vertical", command=scroll.yview)
        scroll.configure(yscrollcommand=scroll_bar.set)
        scroll.pack(side="left", fill="both", expand=True)
        scroll_bar.pack(side="right", fill="y")
        panel = self._panel(scroll, (20, 16))
        scroll_win = scroll.create_window((0, 0), window=panel, anchor="nw")
        panel.bind("<Configure>", lambda e: scroll.configure(scrollregion=scroll.bbox("all")))
        scroll.bind("<Configure>", lambda e: scroll.itemconfigure(scroll_win, width=e.width))
        # ポインターが設定内容の上にある間だけマウスホイールを有効にする。
        def on_wheel(e):
            if e.delta:
                scroll.yview_scroll(int(-e.delta / 120), "units")
        scroll.bind("<Enter>", lambda e: scroll.bind_all("<MouseWheel>", on_wheel))
        scroll.bind("<Leave>", lambda e: scroll.unbind_all("<MouseWheel>"))
        ttk.Label(panel, text="接続方法と環境設定", style="Heading.Panel.TLabel").pack(anchor="w", pady=(0, 9))

        grid = ttk.Frame(panel, style="Panel.TFrame")
        grid.columnconfigure(1, weight=1)

        def row(label: str, index: int, variable, width=None):
            ttk.Label(grid, text=label, style="Panel.TLabel").grid(row=index, column=0, sticky="w", pady=6, padx=(0, 12))
            entry = ttk.Entry(grid, textvariable=variable, width=width or 42)
            entry.grid(row=index, column=1, sticky="ew", pady=6)
            return entry

        self.operation_var = tk.StringVar(value="restricted")
        ttk.Label(panel, text="操作範囲（設定のみ・現在は両方利用不可）", style="Heading.Panel.TLabel").pack(anchor="w")
        for label, value in (("制限付き診断・修復（既定）", "restricted"), ("フル操作（高リスク・管理者昇格なし）", "full")):
            ttk.Radiobutton(panel, text=label, variable=self.operation_var, value=value,
                            command=self._operation_changed).pack(anchor="w", pady=3)
        self.operation_note = ttk.Label(panel, text="", style="Sub.Panel.TLabel", wraplength=650)
        self.operation_note.pack(anchor="w", pady=(4, 12))
        grid.pack(fill="x")
        # 旧接続方式と要求された操作範囲を区別して保持する。
        self.mode_var = tk.StringVar()
        ttk.Label(grid, text="旧接続方式", style="Panel.TLabel").grid(row=0, column=0, sticky="w", pady=6)
        self.mode_box = ttk.Combobox(grid, textvariable=self.mode_var, values=["SSH転送（Claude向け）", "PowerShell共有（手動操作）"], state="readonly")
        self.mode_box.grid(row=0, column=1, sticky="ew", pady=6)
        self.mode_box.bind("<<ComboboxSelected>>", lambda e: self._mode_note())
        self.mode_note = ttk.Label(grid, text="", style="Sub.Panel.TLabel", wraplength=520)
        self.mode_note.grid(row=1, column=1, sticky="w", pady=(0, 5))

        self.duration_var = tk.StringVar()
        row("公開時間（分）", 2, self.duration_var)
        self.relay_var = tk.StringVar()
        row("Upterm中継先", 3, self.relay_var)
        self.external_var = tk.StringVar()
        up_frame = ttk.Frame(grid, style="Panel.TFrame")
        up_frame.grid(row=4, column=1, sticky="ew", pady=6)
        up_frame.columnconfigure(0, weight=1)
        ttk.Label(grid, text="upterm.exe（任意）", style="Panel.TLabel").grid(row=4, column=0, sticky="w")
        ttk.Entry(up_frame, textvariable=self.external_var).grid(row=0, column=0, sticky="ew")
        ttk.Button(up_frame, text="参照", command=lambda: self._browse_file(self.external_var, "*.exe")).grid(row=0, column=1, padx=(8, 0))
        self.known_var = tk.StringVar()
        host_frame = ttk.Frame(grid, style="Panel.TFrame")
        host_frame.grid(row=5, column=1, sticky="ew", pady=6)
        host_frame.columnconfigure(0, weight=1)
        ttk.Label(grid, text="known_hosts（任意）", style="Panel.TLabel").grid(row=5, column=0, sticky="w")
        ttk.Entry(host_frame, textvariable=self.known_var).grid(row=0, column=0, sticky="ew")
        ttk.Button(host_frame, text="参照", command=lambda: self._browse_file(self.known_var, "*.*")).grid(row=0, column=1, padx=(8, 0))

        key_header = ttk.Frame(panel, style="Panel.TFrame")
        key_header.pack(fill="x", pady=(14, 5))
        ttk.Label(key_header, text="MacのSSH公開鍵", style="Heading.Panel.TLabel").pack(side="left")
        ttk.Button(key_header, text=".pubファイルを読み込む", command=self._import_key).pack(side="right")
        ttk.Label(panel, text="Macの ~/.ssh/claude_rescue.pub の内容を貼り付けます。秘密鍵は絶対に入力しないでください。",
                  style="Sub.Panel.TLabel").pack(anchor="w", pady=(0, 5))
        self.pubkeys = scrolledtext.ScrolledText(panel, height=4, wrap="word", font=("Consolas", 9), bg="#F8FAFC", fg=INK)
        self.pubkeys.pack(fill="x")

        actions = ttk.Frame(footer, style="Panel.TFrame")
        actions.pack(fill="x", pady=(0, 0))
        self.save_btn = ttk.Button(actions, text="設定を保存", style="Primary.TButton", command=self._save_from_form)
        self.save_btn.pack(side="left")
        ttk.Button(actions, text="接続管理へ戻る", command=lambda: self.tabs.select(self.tab_control)).pack(side="left", padx=(10, 0))
        ttk.Label(footer, text="※ Windows OpenSSH Serverは自動インストール・起動しません。SSH転送には別途セットアップが必要です。",
                  style="Sub.Panel.TLabel", wraplength=700).pack(anchor="w", pady=(7, 0))

    def _operation_changed(self):
        if self.operation_var.get() == "full":
            if not messagebox.askyesno("フル操作の設定", "フル操作では通常ユーザー権限の任意操作が可能になります。変更・外部通信・永続化の影響は接続終了後も残り得ます。\n管理者昇格は含みません（別途承認する機構は未実装）。\n\nこれは設定の保存のみです。新規接続は許可されません。選択しますか？", icon="warning"):
                self.operation_var.set("restricted")
        self._operation_note()

    def _operation_note(self):
        if self.operation_var.get() == "full":
            note = "フル操作：高リスク。管理者昇格は含みません。接続終了は操作結果を取り消しません。"
        else:
            note = "制限付き：許可された診断・修復のみを行う予定です。権限制御は未実装・未検証です。"
        self.operation_note.configure(text=note + " 両モードともバックエンド未検証のため利用不可。設定しても接続は開始されません。")

    def _mode_note(self):
        if self.mode_var.get().startswith("SSH"):
            note = "無効: Upterm単体では転送先を127.0.0.1:22に限定できません。公開鍵認証だけでは制限できません。"
        else:
            note = "無効: PowerShellは現在のユーザーの全権限を持ちます。強制失効を検証するまで新規接続は停止します。"
        self.mode_note.config(text=note)

    def _load_into_form(self):
        self.operation_var.set(self.config.operation_mode)
        self._operation_note()
        self.mode_var.set("SSH転送（Claude向け）" if self.config.mode == "ssh" else "PowerShell共有（手動操作）")
        self.duration_var.set(str(self.config.duration_minutes))
        self.relay_var.set(self.config.relay)
        self.external_var.set(self.config.external_upterm)
        self.known_var.set(self.config.custom_known_hosts)
        self.pubkeys.delete("1.0", "end")
        self.pubkeys.insert("1.0", self.config.public_keys)
        self._mode_note()

    def _settings_from_form(self) -> Settings:
        try:
            minutes = int(self.duration_var.get().strip())
        except ValueError as exc:
            raise RescueError("公開時間は整数の分数で指定してください。") from exc
        settings = Settings(
            relay=self.relay_var.get().strip(),
            duration_minutes=minutes,
            operation_mode=self.operation_var.get(),
            mode="ssh" if self.mode_var.get().startswith("SSH") else "terminal",
            public_keys=self.pubkeys.get("1.0", "end").strip(),
            external_upterm=self.external_var.get().strip(),
            custom_known_hosts=self.known_var.get().strip(),
        )
        settings.validate()
        return settings

    def _save_from_form(self):
        try:
            settings = self._settings_from_form()
            save_settings(settings)
            self.config = settings
        except (RescueError, OSError) as exc:
            messagebox.showerror("設定エラー", str(exc))
            return False
        self._log("接続設定を保存しました。")
        messagebox.showinfo("保存完了", "設定を保存しました。")
        return True

    def _browse_file(self, variable, filetype):
        path = filedialog.askopenfilename(title="ファイルを選択", filetypes=[("対象ファイル", filetype), ("すべて", "*.*")])
        if path:
            variable.set(path)

    def _import_key(self):
        path = filedialog.askopenfilename(filetypes=[("SSH公開鍵", "*.pub"), ("すべて", "*.*")])
        if path:
            try:
                content = Path(path).read_text(encoding="ascii")
                self.pubkeys.delete("1.0", "end")
                self.pubkeys.insert("1.0", content)
            except (OSError, UnicodeError) as exc:
                messagebox.showerror("読み込み失敗", str(exc))

    def _log(self, msg):
        import datetime
        stamp = datetime.datetime.now().strftime("%H:%M:%S")
        self.logs.configure(state="normal")
        self.logs.insert("end", f"[{stamp}] {msg}\n")
        self.logs.see("end")
        self.logs.configure(state="disabled")

    def _run_async(self, event_name, fn):
        self.busy = True
        self._refresh_state()
        def runner():
            try:
                self.events.put((event_name + "_ok", fn()))
            except Exception as exc:
                self.events.put((event_name + "_error", str(exc)))
        threading.Thread(target=runner, daemon=True).start()

    def _start(self):
        if self.busy or self.session:
            return
        try:
            require_secure_session_backend()
            settings = self._settings_from_form()
            exe = resolve_upterm(settings.external_upterm)
            if settings.mode == "ssh" and not check_local_sshd():
                raise RescueError("127.0.0.1:22 にOpenSSH Serverが見つかりません。サービスとループバック待ち受けを確認してください。")
        except (RescueError, OSError) as exc:
            messagebox.showerror("開始できません", str(exc))
            self.tabs.select(self.tab_settings)
            return
        if sys.platform == "win32":
            import ctypes
            if ctypes.windll.shell32.IsUserAnAdmin():
                messagebox.showerror("管理者権限で実行中", "Rescue Hostは通常ユーザーで起動してください。PowerShell共有が管理者権限を継承する危険があります。")
                return
        warning = (
            f"このPCへの外部接続を {settings.duration_minutes} 分間許可します。\n\n"
            "許可したSSH公開鍵の所有者は、その時間中は再接続できます。\n"
            "毎回の接続要求ごとの確認ではありません。\n\n"
            "操作は現在のWindowsユーザー権限で実行されます。\n"
        )
        if settings.mode == "ssh":
            warning += "\n【注意】SSH転送は127.0.0.1:22以外のTCP宛先も指定可能です。"
        if not messagebox.askyesno("外部接続を許可しますか？", warning, icon="warning"):
            return
        # 検証とローカルでの明示的な同意が完了してから設定を保存する。
        save_settings(settings)
        self.config = settings
        self._log("セッションの起動処理を開始しました。")
        self._run_async("start", lambda: self._start_worker(settings, exe))

    def _start_worker(self, settings, exe):
        require_secure_session_backend()
        settings.validate()
        name = "rescue-" + uuid4().hex[:12]
        key_path = prepare_authorized_keys(settings, name)
        known_hosts = resolve_known_hosts(settings)
        deadline = time.time() + settings.duration_minutes * 60
        info = {
            "name": name, "deadline": deadline,
            "mode": settings.mode, "external_upterm": str(exe.resolve()),
            "relay": settings.relay,
        }
        try:
            check_upterm_version(exe)
            # 切り離された Upterm デーモンを作成できるようになる前に、必ず監視プロセスを起動する。
            record_session_state(info)
            launch_watchdog(name, deadline, str(exe.resolve()))
            result = run_upterm(exe, upterm_cmd_start(settings, name, known_hosts, key_path), timeout=50)
            if result.returncode != 0:
                raise RescueError("Uptermを開始できませんでした: " + (result.stderr or result.stdout)[:500])
            join_info = parse_host_json(result.stdout)
            info.update(join_info)
            record_session_state({k: v for k, v in info.items() if k != "ssh_command"})
            return info
        except Exception:
            # 起動後に host が利用できない JSON を返した場合は、こちらで付けた一意の名前を使って停止する。
            # 停止に失敗した場合は、復旧や手動対応のために状態を保持する。
            try:
                stop_session(exe, name)
            except RescueError:
                pass  # 監視プロセスがセッションの期限に停止を試みる。
            else:
                remove_session_state_if_name(name)
            raise

    def _stop(self):
        if self.busy or not self.session:
            return
        if not messagebox.askyesno("セッション終了", "旧セッションに停止を要求します。終了を検証できない場合は回復情報と鍵を保持します。続行しますか？"):
            return
        state = dict(self.session)
        self._log("セッション停止を要求しました。")
        self._run_async("stop", lambda: self._stop_worker(state))

    def _stop_worker(self, state):
        exe = resolve_upterm(state.get("external_upterm", ""))
        stop_session(exe, state["name"])
        remove_session_state_if_name(state["name"])
        return state["name"]

    def _copy_text(self, text):
        self.root.clipboard_clear()
        self.root.clipboard_append(text)
        self.root.update_idletasks()
        self._log("接続コマンドをクリップボードにコピーしました。")

    def _copy_join(self):
        messagebox.showwarning("接続共有は無効", "v0.2では既存セッションの停止・確認だけを提供します。")

    def _copy_forward(self):
        messagebox.showwarning("SSH転送は無効", START_BLOCKED_REASON)

    def _drain_events(self):
        try:
            while True:
                event, payload = self.events.get_nowait()
                if event == "start_ok":
                    self.session = payload
                    self.expiry_stop_attempted = False
                    self.busy = False
                    self._log("Uptermを開始しました。接続先をMacに共有できます。")
                elif event == "start_error":
                    self.session = load_session_state()
                    self.busy = False
                    self._log("開始失敗: " + str(payload))
                    messagebox.showerror("Uptermの起動失敗", str(payload))
                elif event == "stop_ok":
                    if self.session and self.session.get("name") == payload:
                        self.session = None
                    self.busy = False
                    self._log("対象セッションの終了確認が完了しました。")
                elif event == "stop_error":
                    self.busy = False
                    self._log("停止失敗: " + str(payload))
                    messagebox.showerror("停止失敗", str(payload) + "\n\n手動停止: upterm session stop <セッション名>")
                elif event == "info_ok":
                    self.poll_busy = False
                    self._apply_info(payload)
                elif event == "info_error":
                    self.poll_busy = False
                    self._log("状態確認に失敗: " + str(payload))
                self._refresh_state()
        except queue.Empty:
            pass
        except (OSError, RescueError, ValueError, TypeError) as exc:
            # イベント処理の失敗でポーリングや期限処理を停止させたり、証跡を消したりしない。
            self.busy = False
            self.poll_busy = False
            self.expiry_stop_attempted = False
            self._log("状態処理に失敗しました。回復情報を保持して再試行します: " + str(exc))
        finally:
            self.root.after(150, self._drain_events)

    def _apply_info(self, info):
        if not self.session or info.get("name") != self.session.get("name"):
            return  # 以前のセッションに属する古いポーリング応答は無視する。
        if info.get("status") == "ended":
            # CLI の名前と状態は観測結果であり、起動単位に結び付いた失効の証明ではない。
            self._log("終了応答を受信しましたが旧セッションの同一性は未確認です。回復情報を保持します。")
        else:
            self.session["guest_count"] = info.get("guestCount", 0)
            # 復旧処理で、安全でない旧セッションの接続コマンドを公開してはならない。
            self.session.pop("ssh_command", None)

    def _recover(self):
        if not self.session:
            return
        self._log("前回セッションの状態を確認しています。")
        state = dict(self.session)
        if time.time() >= state.get("deadline", 0):
            self._log("有効期限を過ぎた前回セッションを停止します。")
            self._run_async("stop", lambda: self._stop_worker(state))
        else:
            self._poll_session()

    def _poll_session(self):
        if self.poll_busy or self.busy or not self.session:
            return
        state = dict(self.session)
        self.poll_busy = True
        def worker():
            try:
                exe = resolve_upterm(state.get("external_upterm", ""))
                info = get_session_info(exe, state["name"])
                self.events.put(("info_ok", info))
            except Exception as exc:
                self.events.put(("info_error", str(exc)))
        threading.Thread(target=worker, daemon=True).start()

    def _tick(self):
        if self.session and not self.busy and not self.expiry_stop_attempted and time.time() >= self.session.get("deadline", float("inf")):
            self.expiry_stop_attempted = True
            state = dict(self.session)
            self._log("公開期限を過ぎたため停止要求を送信します。")
            self._run_async("stop", lambda: self._stop_worker(state))
        self._refresh_state()
        self.root.after(1000, self._tick)
        if self.session and int(time.time()) % 16 == 0:
            self._poll_session()

    def _refresh_state(self):
        active = bool(self.session)
        self.start_btn.configure(state="normal", text="新規接続停止中（理由を表示）")
        self.stop_btn.configure(state="disabled" if self.busy or not active else "normal")
        self.copy_btn.configure(state="disabled")
        self.copy_forward_btn.configure(state="disabled")
        if self.busy:
            self.state_tag.configure(text="処理中", foreground=BLUE)
        elif active:
            self.state_tag.configure(text="停止未確認（旧セッション）", foreground=RED)
        else:
            self.state_tag.configure(text="新規接続停止中", foreground=MUTED)
        if active:
            remaining = max(0, int(self.session["deadline"] - time.time()))
            self.countdown.configure(text=f"残り {remaining // 60:02d}:{remaining % 60:02d}")
            mode = "SSH転送" if self.session.get("mode") == "ssh" else "PowerShell共有"
            self.summary.configure(text=f"{mode}  |  接続数: {self.session.get('guest_count', '?')}  |  {self.session['name']}")
            self.ssh_command.set("旧セッションの共有は無効です。停止して終了を確認してください。")
        else:
            self.countdown.configure(text="未接続")
            self.summary.configure(text="v0.2開発版：安全なバックエンドが未実装のため新規接続はできません。")
            self.ssh_command.set("新規接続・接続コマンドの共有は無効です")

    def _close_unconfirmed(self):
        if self.busy:
            messagebox.showwarning("処理中", "停止要求の処理が完了してから閉じてください。")
            return
        if not self.session:
            self.root.destroy()
            return
        if messagebox.askyesno("停止未確認のまま閉じますか？",
                "画面を閉じるだけです。リモート接続が継続している可能性があります。\n"
                "この操作は接続の停止・終了確認を行いません。監視プロセスによる失効も保証できません。\n"
                "回復情報と鍵は削除せず保持します。外部で対象セッションの終了を確認してください。\n\n"
                "停止未確認のまま画面だけ閉じますか？", icon="warning", default="no"):
            self.root.destroy()

    def _request_close(self):
        if self.busy:
            messagebox.showwarning("処理中", "開始／停止処理が完了してから閉じてください。")
            return
        if not self.session:
            self.root.destroy()
            return
        if messagebox.askyesno("終了確認", "GUIを閉じる前に停止を要求します。この開発版では旧セッションの終了を検証できないため、自動では閉じられません。画面だけ閉じる場合は専用ボタンを選んでください。停止を要求しますか？"):
            state = dict(self.session)
            self._run_async("stop", lambda: self._stop_worker(state))
            self._close_after_stop = True
            self.root.after(200, self._close_when_stopped)

    def _close_when_stopped(self):
        if not self.busy and not self.session:
            self.root.destroy()
        elif not self.busy and self.session:
            messagebox.showwarning("停止未完了", "セッションの終了を確認できません。手動で対象を確認してください。画面だけ閉じる場合は専用ボタンを選んでください。")
        else:
            self.root.after(200, self._close_when_stopped)


def main():
    if len(sys.argv) in (5, 6) and sys.argv[1] == "--watchdog":
        sys.exit(watchdog_main(sys.argv[2], sys.argv[3], sys.argv[4], sys.argv[5] if len(sys.argv) == 6 else None))
    if len(sys.argv) > 1:
        raise SystemExit("Usage: RescueHost.exe [--watchdog INTERNAL_ARGS]")
    mutex = None
    if os.name == "nt":
        import ctypes
        # 別の GUI インスタンスが稼働中セッションの復旧記録を上書きしないようにする。
        mutex = ctypes.windll.kernel32.CreateMutexW(None, False, "Local\\RescueHostUptermGui")
        if not mutex:
            raise SystemExit("GUIの単一インスタンス制御に失敗しました。")
        if ctypes.windll.kernel32.GetLastError() == 183:  # ERROR_ALREADY_EXISTS
            ctypes.windll.kernel32.CloseHandle(mutex)
            raise SystemExit("Rescue Host は既に起動しています。")
    try:
        root = tk.Tk()
        try:
            app = RescueApp(root)
        except RescueError as exc:
            root.withdraw()
            messagebox.showerror("安全のため起動を停止", str(exc))
            root.destroy()
            return
        root.mainloop()
    finally:
        if mutex:
            import ctypes
            ctypes.windll.kernel32.CloseHandle(mutex)


if __name__ == "__main__":
    main()
