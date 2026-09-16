// cloud-sync control panel. Vanilla ES5+/DOM, no build step, no external
// dependencies. All server-provided strings are inserted with textContent or
// setAttribute so they can never be interpreted as HTML.
"use strict";

(function () {
  var STATUS_POLL_MS = 5000;
  var SEARCH_DEBOUNCE_MS = 300;
  var DEFAULT_PAGE_SIZE = 50;

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
  var statusTimer = null;
  var searchTimer = null;
  var currentTab = "dashboard";

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
      return h + "h " + m + "m " + sec + "s";
    }
    if (m > 0) {
      return m + "m " + sec + "s";
    }
    return sec + "s";
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
    close.setAttribute("aria-label", "Dismiss");
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

  function setConn(state) {
    var map = {
      ok: { text: "Connected", className: "ok" },
      degraded: { text: "Degraded", className: "degraded" },
      down: { text: "Unreachable", className: "down" },
    };
    var info = map[state] || map.down;
    var dot = byId("conn-dot");
    var text = byId("conn-text");
    if (dot) {
      dot.className = "conn-dot " + info.className;
    }
    if (text) {
      text.textContent = info.text;
    }
    if (state === "down" && connState !== "down") {
      toast("Cannot reach the cloud-sync backend.", "error");
    }
    connState = state;
  }

  // ---- status / dashboard ---------------------------------------------

  function renderStatus(st) {
    if (!st) {
      return;
    }
    var counts = st.counts || {};
    setText("count-synced", counts.synced != null ? counts.synced : 0);
    setText("count-failed", counts.failed != null ? counts.failed : 0);
    setText("count-cleaned", counts.cleaned != null ? counts.cleaned : 0);
    setText("count-unsynced", counts.unsynced != null ? counts.unsynced : 0);

    var banner = byId("degraded-banner");
    if (st.ok) {
      hide(banner);
    } else {
      setText("degraded-error", st.error || "supervisor not running");
      show(banner);
    }

    var badge = byId("openlist-badge");
    if (badge) {
      badge.textContent = st.openlist_ping ? "OK" : "down";
      badge.className = "badge " + (st.openlist_ping ? "ok" : "down");
    }

    setText("uptime", formatUptime(st.uptime_seconds));
    setText("started-at", formatTime(st.started_at));

    var cfg = st.config || {};
    setText("ui-listen", cfg.ui_listen || "\u2013");
    setText(
      "cleanup-mode",
      cfg.cleanup_dry_run
        ? "Cleanup is in dry-run mode (no files will be deleted)."
        : "Cleanup is live (files may be deleted)."
    );

    var dirs = byId("watch-dirs");
    if (dirs) {
      dirs.textContent = "";
      var list = st.watch_dirs || [];
      if (!list.length) {
        dirs.appendChild(el("li", "muted", "No watch directories configured."));
      } else {
        list.forEach(function (dir) {
          dirs.appendChild(el("li", "dir", dir));
        });
      }
    }

    renderEffectiveConfig(cfg);
  }

  var CONFIG_LABELS = {
    openlist_url: "OpenList URL",
    openlist_overwrite: "Overwrite existing",
    cleanup_dry_run: "Cleanup dry-run",
    upload_concurrency: "Upload concurrency",
    ui_listen: "UI listen",
  };

  function renderEffectiveConfig(cfg) {
    var dl = byId("effective-config");
    if (!dl) {
      return;
    }
    dl.textContent = "";
    if (!cfg) {
      dl.appendChild(el("dd", "muted", "No effective config available."));
      return;
    }
    Object.keys(CONFIG_LABELS).forEach(function (key) {
      if (!(key in cfg)) {
        return;
      }
      var value = cfg[key];
      if (typeof value === "boolean") {
        value = value ? "on" : "off";
      } else if (value === "" || value === null || value === undefined) {
        value = "\u2013";
      }
      dl.appendChild(el("dt", null, CONFIG_LABELS[key]));
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
      toast("Cleanup processed " + n + " item(s).", "success");
      await loadStatus();
    } catch (e) {
      toast("Cleanup failed: " + e.message, "error");
    } finally {
      if (btn) {
        btn.disabled = false;
      }
    }
  }

  // ---- files -----------------------------------------------------------

  function stateBadge(state) {
    var label = state || "unknown";
    return el("span", "badge state-" + label, label);
  }

  function fileRow(item) {
    var tr = el("tr", "state-row-" + (item.state || "unknown"));

    var checkCell = el("td", "col-check");
    var check = el("input", null);
    check.type = "checkbox";
    check.checked = selected.has(item.key);
    check.setAttribute("aria-label", "Select " + item.key);
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
    stateCell.appendChild(stateBadge(item.state));
    tr.appendChild(stateCell);

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
    var retry = el("button", "btn small", "Retry");
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
      var emptyCell = el("td", "empty", "No files match this filter.");
      emptyCell.colSpan = 9;
      emptyRow.appendChild(emptyCell);
      body.appendChild(emptyRow);
    } else {
      items.forEach(function (item) {
        body.appendChild(fileRow(item));
      });
    }

    var totalPages = Math.max(1, Math.ceil(filesState.total / filesState.pageSize));
    setText("page-info", "Page " + filesState.page + " of " + totalPages);
    setText("total-info", filesState.total + " file(s)");

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
      toast("Failed to load files: " + e.message, "error");
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

  async function retryKeys(keys) {
    keys = (keys || []).filter(Boolean);
    if (!keys.length) {
      return;
    }
    var btn = byId("retry-selected");
    if (btn) {
      btn.disabled = true;
    }
    try {
      var data = await postJSON("/api/retry", { keys: keys });
      var results = data && data.results ? data.results : [];
      if (!results.length) {
        toast("No matching files to retry.", "info");
      }
      var okCount = 0;
      var failCount = 0;
      results.forEach(function (r) {
        if (r.ok) {
          okCount++;
          toast("Retry queued: " + r.key, "success");
        } else {
          failCount++;
          toast("Retry failed: " + r.key + " \u2014 " + (r.error || "unknown"), "error");
        }
      });
      if (results.length) {
        toast(
          "Retry: " + okCount + " queued, " + failCount + " failed.",
          failCount ? "error" : "success"
        );
      }
      selected.clear();
      await loadFiles();
      loadStatus();
    } catch (e) {
      toast("Retry failed: " + e.message, "error");
    } finally {
      updateRetrySelected();
    }
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
      showConfigError("Failed to load config: " + e.message);
      toast("Failed to load config: " + e.message, "error");
    }
  }

  async function saveConfig() {
    var area = byId("config-yaml");
    var yaml = area ? area.value : "";
    if (!yaml.trim()) {
      showConfigError("Config is empty; refusing to save.");
      toast("Config is empty; not saved.", "error");
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
      toast("Config saved and reloaded.", "success");
      if (data && data.status) {
        renderStatus(data.status);
      }
      await loadStatus();
      await loadConfig();
    } catch (e) {
      showConfigError(e.message);
      toast("Save failed: " + e.message, "error");
    } finally {
      if (btn) {
        btn.disabled = false;
      }
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
    } else if (name === "files") {
      loadFiles();
    } else if (name === "config") {
      loadStatus();
      loadConfig();
    }
  }

  // ---- wire up ---------------------------------------------------------

  function bindEvents() {
    Array.prototype.forEach.call(document.querySelectorAll(".tab"), function (tab) {
      tab.addEventListener("click", function () {
        showTab(tab.getAttribute("data-tab"));
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

    var cleanupBtn = byId("cleanup-btn");
    if (cleanupBtn) {
      cleanupBtn.addEventListener("click", runCleanup);
    }

    var saveBtn = byId("config-save");
    if (saveBtn) {
      saveBtn.addEventListener("click", saveConfig);
    }

    var reloadBtn = byId("config-reload");
    if (reloadBtn) {
      reloadBtn.addEventListener("click", loadConfig);
    }

    document.addEventListener("visibilitychange", onVisibilityChange);
  }

  function init() {
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
