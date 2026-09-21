// cloud-sync control panel. Vanilla ES5+/DOM, no build step, no external
// dependencies. All server-provided strings are inserted with textContent or
// setAttribute so they can never be interpreted as HTML.
"use strict";

(function () {
  var STATUS_POLL_MS = 5000;
  var SEARCH_DEBOUNCE_MS = 300;
  var DEFAULT_PAGE_SIZE = 50;
  var LANG_KEY = "cloudsync.lang";

  // ---- i18n ------------------------------------------------------------

  var I18N = {
    en: {
      "brand.sub": "control panel",
      "conn.aria": "Backend connection status",
      "conn.connecting": "Connecting\u2026",
      "conn.ok": "Connected",
      "conn.degraded": "Degraded",
      "conn.down": "Unreachable",
      "conn.downToast": "Cannot reach the cloud-sync backend.",
      "tabs.aria": "Sections",
      "tab.dashboard": "Dashboard",
      "tab.files": "Files",
      "tab.config": "Config",
      "dash.degraded": "Degraded:",
      "status.supervisorDown": "supervisor not running",
      "state.all": "All",
      "state.synced": "Synced",
      "state.failed": "Failed",
      "state.cleaned": "Cleaned",
      "state.unsynced": "Unsynced",
      "state.syncing": "Syncing",
      "state.unknown": "Unknown",
      "health.title": "Health",
      "health.openlist": "OpenList",
      "health.badge.unknown": "Unknown",
      "health.uptime": "Uptime",
      "health.started": "Started",
      "health.uilisten": "UI listen",
      "health.version": "Version",
      "watch.title": "Watch directories",
      "watch.none": "No watch directories configured.",
      "maint.title": "Maintenance",
      "maint.modeUnknown": "Cleanup mode unknown.",
      "maint.dryRun": "Cleanup is in dry-run mode (no files will be deleted).",
      "maint.live": "Cleanup is live (files may be deleted).",
      "maint.run": "Run cleanup now",
      "maint.done": "Cleanup processed {n} item(s).",
      "maint.failed": "Cleanup failed: {e}",
      "tasks.title": "Tasks",
      "tasks.running": "Running",
      "tasks.paused": "Paused",
      "tasks.unknown": "Unknown",
      "tasks.start": "Start tasks",
      "tasks.pause": "Pause tasks",
      "tasks.note": "Tasks are off by default; starting them begins watching/uploading and cleanup.",
      "tasks.started": "Tasks started.",
      "tasks.pausedToast": "Tasks paused.",
      "tasks.failed": "Task switch failed: {e}",
      "precheck.title": "Upload pre-check",
      "precheck.run": "Run pre-check",
      "precheck.running": "Scanning\u2026",
      "precheck.none": "No pre-check yet.",
      "precheck.summary": "{n} to upload ({exists} already on cloud, {missing} missing), {size} total.",
      "precheck.clean": "Nothing to upload.",
      "precheck.failed": "Pre-check failed: {e}",
      "precheck.cloud.exists": "on cloud",
      "precheck.cloud.missing": "not on cloud",
      "precheck.cloud.unknown": "cloud unknown",
      "files.search": "Search path\u2026",
      "files.retrySelected": "Retry selected",
      "files.retryFailedAll": "Retry failed",
      "files.retry": "Retry",
      "files.selectAll": "Select all rows",
      "files.select": "Select {key}",
      "files.empty": "No files match this filter.",
      "files.page": "Page {page} of {total}",
      "files.count": "{n} file(s)",
      "files.retryQueued": "Retry queued: {key}",
      "files.retryFailed": "Retry failed: {key} \u2014 {err}",
      "files.retryRequestFailed": "Retry request failed: {e}",
      "files.retrySummary": "Retry: {ok} queued, {fail} failed.",
      "files.retryNone": "No matching files to retry.",
      "files.loadFailed": "Failed to load files: {e}",
      "th.path": "Path",
      "th.size": "Size",
      "th.state": "State",
      "th.cloudStatus": "Cloud",
      "th.cloud": "Cloud path",
      "th.syncedAt": "Synced at",
      "th.cleanupAt": "Cleanup at",
      "th.error": "Error",
      "pager.prev": "Prev",
      "pager.next": "Next",
      "config.title": "Configuration (YAML)",
      "config.reload": "Reload from server",
      "config.save": "Save & Reload",
      "config.effective": "Effective config",
      "config.loading": "Loading\u2026",
      "config.none": "No effective config available.",
      "config.loadFailed": "Failed to load config: {e}",
      "config.empty": "Config is empty; refusing to save.",
      "config.emptyToast": "Config is empty; not saved.",
      "config.saved": "Config saved and reloaded.",
      "config.saveFailed": "Save failed: {e}",
      "config.mode.form": "Form",
      "config.mode.yaml": "Advanced (YAML)",
      "config.form.title": "Configuration",
      "config.regenerate": "Regenerate YAML",
      "config.regenerateConfirm": "Regenerate the config file from current values? Comments and formatting in the existing file will be lost.",
      "config.regenerated": "Config regenerated.",
      "config.regenerateFailed": "Regenerate failed: {e}",
      "config.restart": "restart required",
      "config.minFileSize": "Min file size (read-only): {bytes}",
      "config.tasksRuntime": "Runtime: {state}",
      "config.field.tokenPlaceholder": "leave blank to keep current",
      "config.group.openlist": "OpenList",
      "config.group.watch": "Watching & filtering",
      "config.group.upload": "Tasks & upload",
      "config.group.cleanup": "Cleanup",
      "config.group.logging": "Logging & UI",
      "config.field.openlist_url": "OpenList URL",
      "config.field.openlist_url.help": "e.g. http://openlist:5244",
      "config.field.openlist_token": "OpenList token",
      "config.field.openlist_token.help": "Leave blank to keep the current token.",
      "config.field.openlist_src_storage": "Source storage root",
      "config.field.openlist_dst_storage": "Destination storage root",
      "config.field.openlist_overwrite": "Overwrite existing cloud files",
      "config.field.openlist_overwrite.help": "Off: skip existing (recommended). On: replace.",
      "config.field.watch_dirs": "Watch directories",
      "config.field.watch_dirs.help": "One absolute path per line; paths must exist.",
      "config.field.allowed_source_prefixes": "Allowed source prefixes",
      "config.field.allowed_source_prefixes.help": "Cleanup only deletes under these prefixes.",
      "config.field.sync_status_dir": "Sync status dir",
      "config.field.tasks_enabled": "Start tasks on boot (process start/restart only)",
      "config.field.tasks_enabled.help": "Hot reload does not start or stop tasks; use the Dashboard to start/pause at runtime.",
      "config.field.upload_concurrency": "Upload concurrency",
      "config.field.stabilize_wait_seconds": "Stabilize wait (seconds)",
      "config.field.poll_interval_seconds": "Poll interval (seconds)",
      "config.field.task_timeout_seconds": "Request timeout (seconds)",
      "config.field.cleanup_after_hours": "Cleanup after (hours)",
      "config.field.cleanup_dry_run": "Cleanup dry run",
      "config.field.cleanup_dry_run.help": "On: only log what would be deleted.",
      "config.field.log_level": "Log level",
      "config.field.log_file": "Log file",
      "config.field.log_file.help": "Empty = stdout.",
      "config.field.ui_listen": "Web UI listen",
      "config.field.ui_listen.help": "Empty or \"-\" disables the UI.",
      "cfg.openlist_url": "OpenList URL",
      "cfg.openlist_overwrite": "Overwrite existing",
      "cfg.cleanup_dry_run": "Cleanup dry-run",
      "cfg.upload_concurrency": "Upload concurrency",
      "cfg.ui_listen": "UI listen",
      "bool.on": "on",
      "bool.off": "off",
      "uptime.h": "h",
      "uptime.m": "m",
      "uptime.s": "s",
      "toast.dismiss": "Dismiss",
    },
    zh: {
      "brand.sub": "控制面板",
      "conn.aria": "后端连接状态",
      "conn.connecting": "连接中\u2026",
      "conn.ok": "已连接",
      "conn.degraded": "降级运行",
      "conn.down": "无法连接",
      "conn.downToast": "无法连接 cloud-sync 后端。",
      "tabs.aria": "导航",
      "tab.dashboard": "仪表盘",
      "tab.files": "文件",
      "tab.config": "配置",
      "dash.degraded": "降级运行：",
      "status.supervisorDown": "supervisor 未运行",
      "state.all": "全部",
      "state.synced": "已同步",
      "state.failed": "失败",
      "state.cleaned": "已清理",
      "state.unsynced": "未同步",
      "state.syncing": "上传中",
      "state.unknown": "未知",
      "health.title": "运行状况",
      "health.openlist": "OpenList",
      "health.badge.unknown": "未知",
      "health.uptime": "运行时长",
      "health.started": "启动时间",
      "health.uilisten": "UI 监听",
      "health.version": "版本",
      "watch.title": "监听目录",
      "watch.none": "未配置监听目录。",
      "maint.title": "维护",
      "maint.modeUnknown": "清理模式未知。",
      "maint.dryRun": "清理处于 dry-run 模式（不会删除文件）。",
      "maint.live": "清理已启用（可能删除文件）。",
      "maint.run": "立即清理",
      "maint.done": "清理处理了 {n} 项。",
      "maint.failed": "清理失败：{e}",
      "tasks.title": "任务",
      "tasks.running": "运行中",
      "tasks.paused": "已暂停",
      "tasks.unknown": "未知",
      "tasks.start": "启动任务",
      "tasks.pause": "暂停任务",
      "tasks.note": "任务默认关闭；启动后才开始监听/上传与清理。",
      "tasks.started": "任务已启动。",
      "tasks.pausedToast": "任务已暂停。",
      "tasks.failed": "切换任务失败：{e}",
      "precheck.title": "上传预检查",
      "precheck.run": "运行预检查",
      "precheck.running": "扫描中\u2026",
      "precheck.none": "尚未预检查。",
      "precheck.summary": "待上传 {n} 个（云盘已有 {exists}，缺失 {missing}），共 {size}。",
      "precheck.clean": "没有需要上传的文件。",
      "precheck.failed": "预检查失败：{e}",
      "precheck.cloud.exists": "云盘已有",
      "precheck.cloud.missing": "云盘缺失",
      "precheck.cloud.unknown": "云盘未知",
      "files.search": "搜索路径\u2026",
      "files.retrySelected": "重试选中",
      "files.retryFailedAll": "重试所有失败",
      "files.retry": "重试",
      "files.selectAll": "全选",
      "files.select": "选择 {key}",
      "files.empty": "没有符合条件的文件。",
      "files.page": "第 {page} / {total} 页",
      "files.count": "共 {n} 个文件",
      "files.retryQueued": "已加入重试：{key}",
      "files.retryFailed": "重试失败：{key} \u2014 {err}",
      "files.retryRequestFailed": "重试请求失败:{e}",
      "files.retrySummary": "重试：{ok} 个已入队，{fail} 个失败。",
      "files.retryNone": "没有可重试的文件。",
      "files.loadFailed": "加载文件失败：{e}",
      "th.path": "路径",
      "th.size": "大小",
      "th.state": "状态",
      "th.cloudStatus": "云盘",
      "th.cloud": "云盘路径",
      "th.syncedAt": "同步时间",
      "th.cleanupAt": "清理时间",
      "th.error": "错误",
      "pager.prev": "上一页",
      "pager.next": "下一页",
      "config.title": "配置（YAML）",
      "config.reload": "从服务器重载",
      "config.save": "保存并重载",
      "config.effective": "生效配置",
      "config.loading": "加载中\u2026",
      "config.none": "无生效配置。",
      "config.loadFailed": "加载配置失败：{e}",
      "config.empty": "配置为空，拒绝保存。",
      "config.emptyToast": "配置为空，未保存。",
      "config.saved": "配置已保存并重载。",
      "config.saveFailed": "保存失败：{e}",
      "config.mode.form": "表单",
      "config.mode.yaml": "高级（YAML）",
      "config.form.title": "配置",
      "config.regenerate": "重新生成 YAML",
      "config.regenerateConfirm": "将按当前值重新生成配置文件？现有文件中的注释与格式会丢失。",
      "config.regenerated": "配置已重新生成。",
      "config.regenerateFailed": "重新生成失败：{e}",
      "config.restart": "需重启",
      "config.minFileSize": "最小文件大小（只读）：{bytes}",
      "config.tasksRuntime": "当前：{state}",
      "config.field.tokenPlaceholder": "留空表示不修改",
      "config.group.openlist": "OpenList",
      "config.group.watch": "监听与过滤",
      "config.group.upload": "任务与上传",
      "config.group.cleanup": "清理",
      "config.group.logging": "日志与界面",
      "config.field.openlist_url": "OpenList 地址",
      "config.field.openlist_url.help": "如 http://openlist:5244",
      "config.field.openlist_token": "OpenList Token",
      "config.field.openlist_token.help": "留空表示保留当前 token。",
      "config.field.openlist_src_storage": "源存储根路径",
      "config.field.openlist_dst_storage": "目标存储根路径",
      "config.field.openlist_overwrite": "覆盖云端同名文件",
      "config.field.openlist_overwrite.help": "关闭：跳过已存在（推荐）。开启：覆盖。",
      "config.field.watch_dirs": "监听目录",
      "config.field.watch_dirs.help": "每行一个绝对路径；路径必须存在。",
      "config.field.allowed_source_prefixes": "允许清理的路径前缀",
      "config.field.allowed_source_prefixes.help": "清理只会删除这些前缀下的文件。",
      "config.field.sync_status_dir": "状态目录",
      "config.field.tasks_enabled": "启动时开启任务（仅进程启动/重启生效）",
      "config.field.tasks_enabled.help": "保存并热重载不会启动/暂停任务；运行时请用 Dashboard 的「启动任务/暂停」。",
      "config.field.upload_concurrency": "上传并发",
      "config.field.stabilize_wait_seconds": "稳定等待（秒）",
      "config.field.poll_interval_seconds": "轮询间隔（秒）",
      "config.field.task_timeout_seconds": "单次请求超时（秒）",
      "config.field.cleanup_after_hours": "上传后清理（小时）",
      "config.field.cleanup_dry_run": "清理演练",
      "config.field.cleanup_dry_run.help": "开启时只记录将删除哪些文件。",
      "config.field.log_level": "日志级别",
      "config.field.log_file": "日志文件",
      "config.field.log_file.help": "留空 = 输出到 stdout。",
      "config.field.ui_listen": "Web UI 监听地址",
      "config.field.ui_listen.help": "留空或 \"-\" 表示关闭 UI。",
      "cfg.openlist_url": "OpenList 地址",
      "cfg.openlist_overwrite": "覆盖已有文件",
      "cfg.cleanup_dry_run": "清理 dry-run",
      "cfg.upload_concurrency": "上传并发",
      "cfg.ui_listen": "UI 监听",
      "bool.on": "开",
      "bool.off": "关",
      "uptime.h": "时",
      "uptime.m": "分",
      "uptime.s": "秒",
      "toast.dismiss": "关闭",
    },
  };

  var lang = "en";

  function t(key, vars) {
    var dict = I18N[lang] || I18N.en;
    var s = dict[key];
    if (s === undefined) {
      s = I18N.en[key] !== undefined ? I18N.en[key] : key;
    }
    if (vars) {
      Object.keys(vars).forEach(function (k) {
        s = s.replace(new RegExp("\\{" + k + "\\}", "g"), String(vars[k]));
      });
    }
    return s;
  }

  function detectLang() {
    var saved = null;
    try {
      saved = localStorage.getItem(LANG_KEY);
    } catch (e) {
      saved = null;
    }
    if (saved === "zh" || saved === "en") {
      return saved;
    }
    var nav = (navigator.language || navigator.userLanguage || "en").toLowerCase();
    return nav.indexOf("zh") === 0 ? "zh" : "en";
  }

  function applyStaticI18n() {
    Array.prototype.forEach.call(document.querySelectorAll("[data-i18n]"), function (node) {
      node.textContent = t(node.getAttribute("data-i18n"));
    });
    Array.prototype.forEach.call(
      document.querySelectorAll("[data-i18n-placeholder]"),
      function (node) {
        node.setAttribute("placeholder", t(node.getAttribute("data-i18n-placeholder")));
      }
    );
    Array.prototype.forEach.call(document.querySelectorAll("[data-i18n-aria]"), function (node) {
      node.setAttribute("aria-label", t(node.getAttribute("data-i18n-aria")));
    });
    var conn = byId("conn");
    if (conn) {
      conn.setAttribute("title", t("conn.aria"));
    }
  }

  var filesState = {
    state: "all",
    q: "",
    page: 1,
    pageSize: DEFAULT_PAGE_SIZE,
    total: 0,
    items: [],
  };

  var selected = new Set();
  var connState = null;
  var tasksRunning = false;
  var statusTimer = null;
  var searchTimer = null;
  var currentTab = "dashboard";
  var lastStatus = null;
  var lastConfig = null;
  var lastConfigForm = null;
  var configMode = "form";

  // ---- tiny helpers ----------------------------------------------------

  function byId(id) {
    return document.getElementById(id);
  }

  function el(tag, className, text) {
    var node = document.createElement(tag);
    if (className) {
      node.className = className;
    }
    if (text !== undefined && text !== null) {
      node.textContent = String(text);
    }
    return node;
  }

  function setText(id, text) {
    var node = byId(id);
    if (node) {
      node.textContent = text === undefined || text === null ? "" : String(text);
    }
  }

  function show(node) {
    if (node) {
      node.classList.remove("hidden");
    }
  }

  function hide(node) {
    if (node) {
      node.classList.add("hidden");
    }
  }

  function formatBytes(n) {
    if (n === undefined || n === null || isNaN(n)) {
      return "\u2013";
    }
    if (n < 1024) {
      return n + " B";
    }
    var units = ["KB", "MB", "GB", "TB", "PB"];
    var value = n / 1024;
    var i = 0;
    while (value >= 1024 && i < units.length - 1) {
      value /= 1024;
      i++;
    }
    return value.toFixed(value >= 10 ? 1 : 2) + " " + units[i];
  }

  function formatUptime(seconds) {
    var s = Number(seconds);
    if (!isFinite(s) || s < 0) {
      s = 0;
    }
    s = Math.floor(s);
    var h = Math.floor(s / 3600);
    var m = Math.floor((s % 3600) / 60);
    var sec = s % 60;
    if (h > 0) {
      return h + t("uptime.h") + " " + m + t("uptime.m") + " " + sec + t("uptime.s");
    }
    if (m > 0) {
      return m + t("uptime.m") + " " + sec + t("uptime.s");
    }
    return sec + t("uptime.s");
  }

  function formatTime(iso) {
    if (!iso) {
      return "\u2013";
    }
    var d = new Date(iso);
    if (isNaN(d.getTime())) {
      return iso;
    }
    return d.toLocaleString();
  }

  function toast(message, type) {
    var box = byId("toasts");
    if (!box) {
      return;
    }
    var item = el("div", "toast " + (type || "info"));
    item.appendChild(el("span", "toast-msg", message));

    var close = el("button", "toast-close", "\u00d7");
    close.type = "button";
    close.setAttribute("aria-label", t("toast.dismiss"));
    close.addEventListener("click", function () {
      item.remove();
    });
    item.appendChild(close);

    box.appendChild(item);
    window.setTimeout(function () {
      item.classList.add("toast-hide");
      window.setTimeout(function () {
        item.remove();
      }, 250);
    }, 5000);
  }

  // ---- API -------------------------------------------------------------

  async function api(path, options) {
    var res;
    try {
      res = await fetch(path, options);
    } catch (e) {
      throw new Error("network error: " + e.message);
    }
    var raw = "";
    try {
      raw = await res.text();
    } catch (e) {
      raw = "";
    }
    var data = null;
    if (raw) {
      try {
        data = JSON.parse(raw);
      } catch (e) {
        data = null;
      }
    }
    if (!res.ok) {
      var msg = data && data.error ? data.error : res.status + " " + res.statusText;
      throw new Error(msg);
    }
    return data;
  }

  function postJSON(path, body) {
    return api(path, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
  }

  // ---- connection indicator -------------------------------------------

  function renderConn() {
    var map = {
      ok: { text: t("conn.ok"), className: "ok" },
      degraded: { text: t("conn.degraded"), className: "degraded" },
      down: { text: t("conn.down"), className: "down" },
    };
    var info = map[connState] || map.down;
    var dot = byId("conn-dot");
    var text = byId("conn-text");
    if (dot) {
      dot.className = "conn-dot " + info.className;
    }
    if (text) {
      text.textContent = info.text;
    }
  }

  function setConn(state) {
    if (state === "down" && connState !== "down") {
      toast(t("conn.downToast"), "error");
    }
    connState = state;
    renderConn();
  }

  // ---- status / dashboard ---------------------------------------------

  function renderStatus(st) {
    if (!st) {
      return;
    }
    lastStatus = st;
    var counts = st.counts || {};
    setText("count-synced", counts.synced != null ? counts.synced : 0);
    setText("count-failed", counts.failed != null ? counts.failed : 0);
    setText("count-cleaned", counts.cleaned != null ? counts.cleaned : 0);
    setText("count-unsynced", counts.unsynced != null ? counts.unsynced : 0);
    setText("count-syncing", counts.syncing != null ? counts.syncing : 0);

    var banner = byId("degraded-banner");
    if (st.ok) {
      hide(banner);
    } else {
      setText("degraded-error", st.error || t("status.supervisorDown"));
      show(banner);
    }

    var badge = byId("openlist-badge");
    if (badge) {
      badge.textContent = st.openlist_ping ? "OK" : "down";
      badge.className = "badge " + (st.openlist_ping ? "ok" : "down");
    }

    setText("uptime", formatUptime(st.uptime_seconds));
    setText("started-at", formatTime(st.started_at));

    var build = st.version || "\u2013";
    if (st.build_time) {
      build += "  (" + st.build_time + ")";
    }
    setText("version", build);

    var av = byId("app-version");
    if (av) {
      av.textContent = st.version || "";
      av.title = [st.commit, st.build_time].filter(Boolean).join(" \u00b7 ");
    }

    var cfg = st.config || {};
    setText("ui-listen", cfg.ui_listen || "\u2013");
    setText("cleanup-mode", cfg.cleanup_dry_run ? t("maint.dryRun") : t("maint.live"));

    var dirs = byId("watch-dirs");
    if (dirs) {
      dirs.textContent = "";
      var list = st.watch_dirs || [];
      if (!list.length) {
        dirs.appendChild(el("li", "muted", t("watch.none")));
      } else {
        list.forEach(function (dir) {
          dirs.appendChild(el("li", "dir", dir));
        });
      }
    }

    tasksRunning = !!st.tasks_running;
    renderTasks(st);
    updateControls();

    renderEffectiveConfig(cfg);
  }

  function renderTasks(st) {
    var badge = byId("tasks-badge");
    var btn = byId("tasks-toggle");
    var running = !!(st && st.tasks_running);
    if (badge) {
      badge.textContent = running ? t("tasks.running") : t("tasks.paused");
      badge.className = "badge " + (running ? "ok" : "down");
    }
    if (btn) {
      btn.textContent = running ? t("tasks.pause") : t("tasks.start");
    }
  }

  // Retry and cleanup run on the supervisor's one-off path, so they stay
  // available while tasks are paused; only the selection gates retry-selected.
  function updateControls() {
    var cleanupBtn = byId("cleanup-btn");
    if (cleanupBtn) {
      cleanupBtn.disabled = false;
    }
    var retryFailedBtn = byId("retry-failed");
    if (retryFailedBtn) {
      retryFailedBtn.disabled = false;
    }
    updateRetrySelected();
  }

  var CONFIG_LABELS = {
    openlist_url: "cfg.openlist_url",
    openlist_overwrite: "cfg.openlist_overwrite",
    cleanup_dry_run: "cfg.cleanup_dry_run",
    upload_concurrency: "cfg.upload_concurrency",
    ui_listen: "cfg.ui_listen",
  };

  function renderEffectiveConfig(cfg) {
    var dl = byId("effective-config");
    if (!dl) {
      return;
    }
    lastConfig = cfg;
    dl.textContent = "";
    if (!cfg) {
      dl.appendChild(el("dd", "muted", t("config.none")));
      return;
    }
    Object.keys(CONFIG_LABELS).forEach(function (key) {
      if (!(key in cfg)) {
        return;
      }
      var value = cfg[key];
      if (typeof value === "boolean") {
        value = value ? t("bool.on") : t("bool.off");
      } else if (value === "" || value === null || value === undefined) {
        value = "\u2013";
      }
      dl.appendChild(el("dt", null, t(CONFIG_LABELS[key])));
      dl.appendChild(el("dd", null, value));
    });
  }

  async function loadStatus() {
    try {
      var st = await api("/api/status");
      renderStatus(st);
      setConn(st.ok ? "ok" : "degraded");
      return st;
    } catch (e) {
      setConn("down");
      return null;
    }
  }

  function startStatusPolling() {
    stopStatusPolling();
    statusTimer = window.setInterval(function () {
      if (document.visibilityState === "visible") {
        loadStatus();
      }
    }, STATUS_POLL_MS);
  }

  function stopStatusPolling() {
    if (statusTimer !== null) {
      window.clearInterval(statusTimer);
      statusTimer = null;
    }
  }

  function onVisibilityChange() {
    if (document.visibilityState === "visible") {
      loadStatus();
    }
  }

  async function runCleanup() {
    var btn = byId("cleanup-btn");
    if (btn) {
      btn.disabled = true;
    }
    try {
      var data = await postJSON("/api/cleanup/run", {});
      var n = data && data.processed != null ? data.processed : 0;
      toast(t("maint.done", { n: n }), "success");
      await loadStatus();
    } catch (e) {
      toast(t("maint.failed", { e: e.message }), "error");
    } finally {
      if (btn) {
        btn.disabled = false;
      }
    }
  }

  async function toggleTasks() {
    var btn = byId("tasks-toggle");
    if (btn) {
      btn.disabled = true;
    }
    try {
      var data = await postJSON("/api/tasks", { enabled: !tasksRunning });
      toast(data && data.tasks_running ? t("tasks.started") : t("tasks.pausedToast"), "success");
      if (data) {
        renderStatus(data);
      }
      await loadStatus();
      await loadFiles();
    } catch (e) {
      toast(t("tasks.failed", { e: e.message }), "error");
    } finally {
      if (btn) {
        btn.disabled = false;
      }
    }
  }

  // ---- upload pre-check (read-only) -----------------------------------

  function renderPrecheck(rep) {
    var sum = byId("precheck-summary");
    var list = byId("precheck-list");
    if (list) {
      list.textContent = "";
    }
    if (!rep) {
      if (sum) {
        sum.textContent = t("precheck.none");
      }
      return;
    }
    var n = rep.candidates_total || 0;
    if (sum) {
      sum.textContent = n
        ? t("precheck.summary", {
            n: n,
            exists: rep.cloud_exists || 0,
            missing: rep.cloud_missing || 0,
            size: formatBytes(rep.candidates_bytes || 0),
          })
        : t("precheck.clean");
    }
    if (list) {
      (rep.candidates || []).slice(0, 10).forEach(function (c) {
        var cloud = "precheck.cloud." + (c.cloud || "unknown");
        list.appendChild(
          el("li", "dir", c.key + "  (" + formatBytes(c.size) + ")  \u2013 " + t(cloud))
        );
      });
    }
  }

  async function loadPrecheck() {
    try {
      renderPrecheck(await api("/api/precheck"));
    } catch (e) {
      renderPrecheck(null);
    }
  }

  async function runPrecheck() {
    var btn = byId("precheck-btn");
    if (btn) {
      btn.disabled = true;
    }
    var sum = byId("precheck-summary");
    if (sum) {
      sum.textContent = t("precheck.running");
    }
    try {
      var rep = await postJSON("/api/precheck", {});
      renderPrecheck(rep);
      toast(
        t("precheck.summary", {
          n: rep.candidates_total || 0,
          exists: rep.cloud_exists || 0,
          missing: rep.cloud_missing || 0,
          size: formatBytes(rep.candidates_bytes || 0),
        }),
        "success"
      );
      // Refresh the Files list so the Cloud column reflects the new results.
      await loadFiles();
    } catch (e) {
      toast(t("precheck.failed", { e: e.message }), "error");
      await loadPrecheck();
    } finally {
      if (btn) {
        btn.disabled = false;
      }
    }
  }

  // ---- files -----------------------------------------------------------

  function stateLabel(state) {
    var key = "state." + (state || "unknown");
    var known = ["synced", "failed", "cleaned", "unsynced", "syncing", "all"];
    if (known.indexOf(state) === -1) {
      key = "state.unknown";
    }
    return t(key);
  }

  function stateBadge(item) {
    var state = item && item.state ? item.state : "unknown";
    var label = stateLabel(state);
    if (state === "syncing") {
      var pct = item.progress != null ? Math.round(item.progress) : 0;
      label = label + " " + pct + "%";
    }
    return el("span", "badge state-" + state, label);
  }

  function fileRow(item) {
    var tr = el("tr", "state-row-" + (item.state || "unknown"));

    var checkCell = el("td", "col-check");
    var check = el("input", null);
    check.type = "checkbox";
    check.checked = selected.has(item.key);
    check.setAttribute("aria-label", t("files.select", { key: item.key }));
    check.addEventListener("change", function () {
      if (check.checked) {
        selected.add(item.key);
      } else {
        selected.delete(item.key);
      }
      syncSelectAll();
      updateRetrySelected();
    });
    checkCell.appendChild(check);
    tr.appendChild(checkCell);

    var pathCell = el("td", "cell-path");
    var pathSpan = el("span", "path", item.key);
    pathSpan.title = item.src_path || item.key || "";
    pathCell.appendChild(pathSpan);
    tr.appendChild(pathCell);

    tr.appendChild(el("td", "cell-size", formatBytes(item.size)));

    var stateCell = el("td");
    stateCell.appendChild(stateBadge(item));
    tr.appendChild(stateCell);

    var cloudStatusCell = el("td");
    if (item.cloud) {
      var cloudCls = item.cloud === "exists" ? "ok" : item.cloud === "missing" ? "down" : "";
      cloudStatusCell.appendChild(
        el("span", "badge " + cloudCls, t("precheck.cloud." + item.cloud))
      );
    } else {
      cloudStatusCell.appendChild(el("span", "muted", "\u2013"));
    }
    tr.appendChild(cloudStatusCell);

    var cloudCell = el("td", "cell-path");
    cloudCell.appendChild(el("span", "path muted", item.cloud_path || "\u2013"));
    tr.appendChild(cloudCell);

    tr.appendChild(el("td", "cell-time", formatTime(item.synced_at)));
    tr.appendChild(el("td", "cell-time", formatTime(item.cleanup_at)));

    var errCell = el("td", "cell-error");
    if (item.error) {
      errCell.appendChild(el("span", "error-text", item.error));
      errCell.title = item.error;
    } else {
      errCell.appendChild(el("span", "muted", "\u2013"));
    }
    tr.appendChild(errCell);

    var actionCell = el("td", "col-action");
    var retry = el("button", "btn small", t("files.retry"));
    retry.type = "button";
    retry.addEventListener("click", function () {
      retryKeys([item.key]);
    });
    actionCell.appendChild(retry);
    tr.appendChild(actionCell);

    return tr;
  }

  function renderFiles() {
    var body = byId("files-body");
    if (!body) {
      return;
    }
    body.textContent = "";
    var items = filesState.items || [];

    if (!items.length) {
      var emptyRow = el("tr");
      var emptyCell = el("td", "empty", t("files.empty"));
      emptyCell.colSpan = 10;
      emptyRow.appendChild(emptyCell);
      body.appendChild(emptyRow);
    } else {
      items.forEach(function (item) {
        body.appendChild(fileRow(item));
      });
    }

    var totalPages = Math.max(1, Math.ceil(filesState.total / filesState.pageSize));
    setText("page-info", t("files.page", { page: filesState.page, total: totalPages }));
    setText("total-info", t("files.count", { n: filesState.total }));

    var prev = byId("prev-page");
    var next = byId("next-page");
    if (prev) {
      prev.disabled = filesState.page <= 1;
    }
    if (next) {
      next.disabled = filesState.page >= totalPages;
    }

    syncSelectAll();
    updateRetrySelected();
  }

  function syncSelectAll() {
    var all = byId("check-all");
    if (!all) {
      return;
    }
    var items = filesState.items || [];
    var checkedCount = 0;
    items.forEach(function (item) {
      if (selected.has(item.key)) {
        checkedCount++;
      }
    });
    all.checked = items.length > 0 && checkedCount === items.length;
    all.indeterminate = checkedCount > 0 && checkedCount < items.length;
  }

  function updateRetrySelected() {
    var btn = byId("retry-selected");
    if (btn) {
      btn.disabled = selected.size === 0;
    }
  }

  async function loadFiles() {
    var params = new URLSearchParams();
    params.set("state", filesState.state);
    params.set("page", String(filesState.page));
    params.set("page_size", String(filesState.pageSize));
    if (filesState.q) {
      params.set("q", filesState.q);
    }
    try {
      var data = await api("/api/files?" + params.toString());
      data = data || {};
      filesState.items = data.items || [];
      filesState.total = data.total != null ? data.total : 0;
      filesState.page = data.page != null ? data.page : filesState.page;
      filesState.pageSize = data.page_size != null ? data.page_size : filesState.pageSize;
      pruneSelection();
      renderFiles();
    } catch (e) {
      toast(t("files.loadFailed", { e: e.message }), "error");
      filesState.items = [];
      filesState.total = 0;
      renderFiles();
    }
  }

  function pruneSelection() {
    var visible = new Set();
    (filesState.items || []).forEach(function (item) {
      visible.add(item.key);
    });
    Array.from(selected).forEach(function (key) {
      if (!visible.has(key)) {
        selected.delete(key);
      }
    });
  }

  async function doRetry(body) {
    var selectedBtn = byId("retry-selected");
    var failedBtn = byId("retry-failed");
    if (selectedBtn) {
      selectedBtn.disabled = true;
    }
    if (failedBtn) {
      failedBtn.disabled = true;
    }
    try {
      var data = await postJSON("/api/retry", body);
      var results = data && data.results ? data.results : [];
      if (!results.length) {
        toast(t("files.retryNone"), "info");
      }
      var okCount = 0;
      var failCount = 0;
      results.forEach(function (r) {
        if (r.ok) {
          okCount++;
          toast(t("files.retryQueued", { key: r.key }), "success");
        } else {
          failCount++;
          toast(
            t("files.retryFailed", { key: r.key, err: r.error || t("state.unknown") }),
            "error"
          );
        }
      });
      if (results.length) {
        toast(
          t("files.retrySummary", { ok: okCount, fail: failCount }),
          failCount ? "error" : "success"
        );
      }
      selected.clear();
      await loadFiles();
      loadStatus();
    } catch (e) {
      toast(t("files.retryRequestFailed", { e: e.message }), "error");
    } finally {
      updateControls();
    }
  }

  function retryKeys(keys) {
    keys = (keys || []).filter(Boolean);
    if (!keys.length) {
      return;
    }
    return doRetry({ keys: keys });
  }

  // Retry every failed record (server-side state batch).
  function retryFailed() {
    return doRetry({ state: "failed", all: true });
  }

  function retrySelected() {
    retryKeys(Array.from(selected));
  }

  // ---- config ----------------------------------------------------------

  function showConfigError(message) {
    var node = byId("config-error");
    if (node) {
      node.textContent = message;
      show(node);
    }
  }

  function hideConfigError() {
    var node = byId("config-error");
    if (node) {
      node.textContent = "";
      hide(node);
    }
  }

  async function loadConfig() {
    try {
      var data = await api("/api/config");
      var area = byId("config-yaml");
      if (area) {
        area.value = data && data.yaml ? data.yaml : "";
      }
      hideConfigError();
    } catch (e) {
      showConfigError(t("config.loadFailed", { e: e.message }));
      toast(t("config.loadFailed", { e: e.message }), "error");
    }
  }

  async function saveConfig() {
    var area = byId("config-yaml");
    var yaml = area ? area.value : "";
    if (!yaml.trim()) {
      showConfigError(t("config.empty"));
      toast(t("config.emptyToast"), "error");
      return;
    }
    var btn = byId("config-save");
    if (btn) {
      btn.disabled = true;
    }
    try {
      var data = await api("/api/config", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ yaml: yaml }),
      });
      hideConfigError();
      toast(t("config.saved"), "success");
      if (data && data.status) {
        renderStatus(data.status);
      }
      await loadStatus();
      await loadConfig();
    } catch (e) {
      showConfigError(e.message);
      toast(t("config.saveFailed", { e: e.message }), "error");
    } finally {
      if (btn) {
        btn.disabled = false;
      }
    }
  }

  // ---- config form -----------------------------------------------------

  var CONFIG_FIELDS = [
    { group: "config.group.openlist", fields: [
      { key: "openlist_url", type: "text" },
      { key: "openlist_token", type: "password" },
      { key: "openlist_src_storage", type: "text" },
      { key: "openlist_dst_storage", type: "text" },
      { key: "openlist_overwrite", type: "bool" },
    ] },
    { group: "config.group.watch", fields: [
      { key: "watch_dirs", type: "list" },
      { key: "allowed_source_prefixes", type: "list" },
      { key: "sync_status_dir", type: "text" },
    ] },
    { group: "config.group.upload", fields: [
      { key: "tasks_enabled", type: "bool" },
      { key: "upload_concurrency", type: "number", min: 1 },
      { key: "stabilize_wait_seconds", type: "number", min: 1 },
      { key: "poll_interval_seconds", type: "number", min: 1 },
      { key: "task_timeout_seconds", type: "number", min: 1 },
    ] },
    { group: "config.group.cleanup", fields: [
      { key: "cleanup_after_hours", type: "number", min: 1 },
      { key: "cleanup_dry_run", type: "bool" },
    ] },
    { group: "config.group.logging", fields: [
      { key: "log_level", type: "select", options: ["debug", "info", "warn", "error"] },
      { key: "log_file", type: "text" },
      { key: "ui_listen", type: "text", restart: true },
    ] },
  ];

  function showConfigFormError(message) {
    var node = byId("config-form-error");
    if (node) {
      node.textContent = message;
      show(node);
    }
  }

  function hideConfigFormError() {
    var node = byId("config-form-error");
    if (node) {
      node.textContent = "";
      hide(node);
    }
  }

  function configFormField(f, values) {
    var wrap = el("div", "form-field");
    var id = "cfg-" + f.key;
    var label = el("label", "form-label", t("config.field." + f.key));
    label.setAttribute("for", id);
    if (f.restart) {
      label.appendChild(el("span", "restart-tag", t("config.restart")));
    }
    wrap.appendChild(label);

    var val = values ? values[f.key] : undefined;
    var input;
    if (f.type === "bool") {
      input = el("input", "form-input form-checkbox");
      input.type = "checkbox";
      input.checked = !!val;
    } else if (f.type === "list") {
      input = el("textarea", "form-input form-textarea");
      input.rows = 3;
      input.spellcheck = false;
      input.value = Array.isArray(val) ? val.join("\n") : "";
    } else if (f.type === "select") {
      input = el("select", "form-input");
      (f.options || []).forEach(function (o) {
        var opt = el("option", null, o);
        opt.value = o;
        if (val === o) {
          opt.selected = true;
        }
        input.appendChild(opt);
      });
    } else {
      input = el("input", "form-input");
      if (f.type === "number") {
        input.type = "number";
        if (f.min != null) {
          input.min = String(f.min);
        }
      } else if (f.type === "password") {
        input.type = "password";
        input.autocomplete = "new-password";
        input.placeholder = t("config.field.tokenPlaceholder");
      } else {
        input.type = "text";
      }
      input.value = val != null ? String(val) : "";
    }
    input.id = id;
    input.setAttribute("data-key", f.key);
    input.setAttribute("data-type", f.type);
    wrap.appendChild(input);

    var helpKey = "config.field." + f.key + ".help";
    var help = t(helpKey);
    if (help !== helpKey) {
      wrap.appendChild(el("p", "form-help", help));
    }
    return wrap;
  }

  function renderConfigForm(meta) {
    var root = byId("config-form");
    if (!root) {
      return;
    }
    root.textContent = "";
    var values = meta ? meta.values : {};
    CONFIG_FIELDS.forEach(function (group) {
      var section = el("div", "form-group");
      section.appendChild(el("h3", "form-group-title", t(group.group)));
      var grid = el("div", "form-grid");
      group.fields.forEach(function (f) {
        grid.appendChild(configFormField(f, values));
      });
      section.appendChild(grid);
      root.appendChild(section);
    });
    if (meta) {
      root.appendChild(
        el(
          "p",
          "form-note",
          t("config.tasksRuntime", {
            state: meta.tasks_running ? t("tasks.running") : t("tasks.paused"),
          })
        )
      );
      root.appendChild(
        el(
          "p",
          "form-note",
          t("config.minFileSize", { bytes: formatBytes(meta.min_file_size_bytes) }) +
            " \u00b7 " +
            meta.config_path
        )
      );
    }
  }

  function collectConfigForm() {
    var values = {};
    Array.prototype.forEach.call(
      document.querySelectorAll("#config-form [data-key]"),
      function (input) {
        var key = input.getAttribute("data-key");
        var type = input.getAttribute("data-type");
        if (type === "bool") {
          values[key] = input.checked;
        } else if (type === "number") {
          values[key] = parseInt(input.value, 10) || 0;
        } else if (type === "list") {
          values[key] = input.value
            .split("\n")
            .map(function (s) {
              return s.trim();
            })
            .filter(function (s) {
              return s !== "";
            });
        } else {
          values[key] = input.value;
        }
      }
    );
    return values;
  }

  async function loadConfigForm() {
    try {
      var data = await api("/api/config/form");
      lastConfigForm = data;
      renderConfigForm(data);
      hideConfigFormError();
    } catch (e) {
      showConfigFormError(t("config.loadFailed", { e: e.message }));
      toast(t("config.loadFailed", { e: e.message }), "error");
    }
  }

  async function saveConfigForm() {
    var values = collectConfigForm();
    var btn = byId("config-form-save");
    if (btn) {
      btn.disabled = true;
    }
    try {
      var data = await api("/api/config/form", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ values: values }),
      });
      hideConfigFormError();
      toast(t("config.saved"), "success");
      if (data && data.status) {
        renderStatus(data.status);
      }
      await loadStatus();
      await loadConfigForm();
    } catch (e) {
      showConfigFormError(e.message);
      toast(t("config.saveFailed", { e: e.message }), "error");
    } finally {
      if (btn) {
        btn.disabled = false;
      }
    }
  }

  async function regenerateConfig() {
    if (!window.confirm(t("config.regenerateConfirm"))) {
      return;
    }
    var btn = byId("config-regenerate");
    if (btn) {
      btn.disabled = true;
    }
    try {
      var data = await api("/api/config/regenerate", { method: "POST" });
      toast(t("config.regenerated"), "success");
      if (data && data.status) {
        renderStatus(data.status);
      }
      await loadStatus();
      await loadConfigForm();
    } catch (e) {
      toast(t("config.regenerateFailed", { e: e.message }), "error");
    } finally {
      if (btn) {
        btn.disabled = false;
      }
    }
  }

  function setConfigMode(mode) {
    configMode = mode === "yaml" ? "yaml" : "form";
    Array.prototype.forEach.call(
      document.querySelectorAll("#config-modes .chip"),
      function (c) {
        c.classList.toggle("active", c.getAttribute("data-mode") === configMode);
      }
    );
    var formView = byId("config-form-view");
    var yamlView = byId("config-yaml-view");
    if (formView) {
      formView.classList.toggle("hidden", configMode !== "form");
    }
    if (yamlView) {
      yamlView.classList.toggle("hidden", configMode !== "yaml");
    }
    if (configMode === "form") {
      loadConfigForm();
    } else {
      loadConfig();
    }
  }

  // ---- tabs ------------------------------------------------------------

  function showTab(name) {
    currentTab = name;
    Array.prototype.forEach.call(document.querySelectorAll(".tab"), function (tab) {
      tab.classList.toggle("active", tab.getAttribute("data-tab") === name);
    });
    Array.prototype.forEach.call(document.querySelectorAll(".panel"), function (panel) {
      panel.classList.toggle("active", panel.id === "panel-" + name);
    });
    if (name === "dashboard") {
      loadStatus();
      loadPrecheck();
    } else if (name === "files") {
      loadFiles();
    } else if (name === "config") {
      loadStatus();
      if (configMode === "form") {
        loadConfigForm();
      } else {
        loadConfig();
      }
    }
  }

  // ---- language switching ---------------------------------------------

  function setLang(next) {
    lang = next === "zh" ? "zh" : "en";
    try {
      localStorage.setItem(LANG_KEY, lang);
    } catch (e) {
      /* ignore */
    }
    document.documentElement.lang = lang === "zh" ? "zh-CN" : "en";
    Array.prototype.forEach.call(document.querySelectorAll(".lang-btn"), function (b) {
      b.classList.toggle("active", b.getAttribute("data-lang") === lang);
    });
    applyStaticI18n();
    // re-render dynamic content in the new language
    if (lastStatus) {
      renderStatus(lastStatus);
    }
    renderTasks(lastStatus);
    renderFiles();
    renderEffectiveConfig(lastConfig);
    if (configMode === "form" && lastConfigForm) {
      renderConfigForm(lastConfigForm);
    }
    renderConn();
  }

  // ---- wire up ---------------------------------------------------------

  function bindEvents() {
    Array.prototype.forEach.call(document.querySelectorAll(".tab"), function (tab) {
      tab.addEventListener("click", function () {
        showTab(tab.getAttribute("data-tab"));
      });
    });

    Array.prototype.forEach.call(document.querySelectorAll(".lang-btn"), function (btn) {
      btn.addEventListener("click", function () {
        setLang(btn.getAttribute("data-lang"));
      });
    });

    Array.prototype.forEach.call(document.querySelectorAll("#state-tabs .chip"), function (chip) {
      chip.addEventListener("click", function () {
        var state = chip.getAttribute("data-state");
        if (state === filesState.state) {
          return;
        }
        Array.prototype.forEach.call(
          document.querySelectorAll("#state-tabs .chip"),
          function (c) {
            c.classList.toggle("active", c === chip);
          }
        );
        filesState.state = state;
        filesState.page = 1;
        selected.clear();
        loadFiles();
      });
    });

    var search = byId("search");
    if (search) {
      search.addEventListener("input", function () {
        if (searchTimer !== null) {
          window.clearTimeout(searchTimer);
        }
        searchTimer = window.setTimeout(function () {
          filesState.q = search.value.trim();
          filesState.page = 1;
          selected.clear();
          loadFiles();
        }, SEARCH_DEBOUNCE_MS);
      });
    }

    var checkAll = byId("check-all");
    if (checkAll) {
      checkAll.addEventListener("change", function () {
        (filesState.items || []).forEach(function (item) {
          if (checkAll.checked) {
            selected.add(item.key);
          } else {
            selected.delete(item.key);
          }
        });
        renderFiles();
      });
    }

    var prev = byId("prev-page");
    if (prev) {
      prev.addEventListener("click", function () {
        if (filesState.page > 1) {
          filesState.page -= 1;
          selected.clear();
          loadFiles();
        }
      });
    }

    var next = byId("next-page");
    if (next) {
      next.addEventListener("click", function () {
        var totalPages = Math.max(1, Math.ceil(filesState.total / filesState.pageSize));
        if (filesState.page < totalPages) {
          filesState.page += 1;
          selected.clear();
          loadFiles();
        }
      });
    }

    var retryBtn = byId("retry-selected");
    if (retryBtn) {
      retryBtn.addEventListener("click", retrySelected);
    }

    var retryFailedBtn = byId("retry-failed");
    if (retryFailedBtn) {
      retryFailedBtn.addEventListener("click", retryFailed);
    }

    var cleanupBtn = byId("cleanup-btn");
    if (cleanupBtn) {
      cleanupBtn.addEventListener("click", runCleanup);
    }

    var tasksBtn = byId("tasks-toggle");
    if (tasksBtn) {
      tasksBtn.addEventListener("click", toggleTasks);
    }

    var precheckBtn = byId("precheck-btn");
    if (precheckBtn) {
      precheckBtn.addEventListener("click", runPrecheck);
    }

    var saveBtn = byId("config-save");
    if (saveBtn) {
      saveBtn.addEventListener("click", saveConfig);
    }

    var reloadBtn = byId("config-reload");
    if (reloadBtn) {
      reloadBtn.addEventListener("click", loadConfig);
    }

    Array.prototype.forEach.call(
      document.querySelectorAll("#config-modes .chip"),
      function (chip) {
        chip.addEventListener("click", function () {
          setConfigMode(chip.getAttribute("data-mode"));
        });
      }
    );
    var formSaveBtn = byId("config-form-save");
    if (formSaveBtn) {
      formSaveBtn.addEventListener("click", saveConfigForm);
    }
    var formReloadBtn = byId("config-form-reload");
    if (formReloadBtn) {
      formReloadBtn.addEventListener("click", loadConfigForm);
    }
    var regenBtn = byId("config-regenerate");
    if (regenBtn) {
      regenBtn.addEventListener("click", regenerateConfig);
    }

    document.addEventListener("visibilitychange", onVisibilityChange);
  }

  function init() {
    lang = detectLang();
    document.documentElement.lang = lang === "zh" ? "zh-CN" : "en";
    Array.prototype.forEach.call(document.querySelectorAll(".lang-btn"), function (b) {
      b.classList.toggle("active", b.getAttribute("data-lang") === lang);
    });
    applyStaticI18n();
    bindEvents();
    showTab("dashboard");
    loadStatus();
    startStatusPolling();
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();
