(() => {
  "use strict";

  const tokenStorageKey = "mediadock_token";
  const state = {
    token: sessionStorage.getItem(tokenStorageKey) || "",
    currentView: "dashboard",
    dashboardRequest: 0,
    jobsRequest: 0,
  };

  const $ = (selector) => document.querySelector(selector);
  const $$ = (selector) => Array.from(document.querySelectorAll(selector));

  function setText(selector, value) {
    const element = $(selector);
    if (element) {
      element.textContent = value == null ? "—" : String(value);
    }
  }

  function clear(element) {
    element.replaceChildren();
  }

  function node(tag, className, text) {
    const element = document.createElement(tag);
    if (className) {
      element.className = className;
    }
    if (text != null) {
      element.textContent = text;
    }
    return element;
  }

  function badge(text, className) {
    return node("span", `status-badge ${className || ""}`.trim(), text);
  }

  function kindBadge(text) {
    return node("span", "kind-badge", text);
  }

  function showToast(message, isError) {
    const stack = $("#toast-stack");
    const toast = node("div", `toast${isError ? " error" : ""}`, message);
    stack.append(toast);
    window.setTimeout(() => toast.remove(), 5000);
  }

  function setConnection(label, status) {
    const indicator = $("#connection-indicator");
    indicator.className = `connection-indicator ${status || ""}`.trim();
    indicator.querySelector("span").textContent = label;
  }

  function openTokenModal() {
    const modal = $("#token-modal");
    modal.classList.remove("hidden");
    const input = $("#token-input");
    input.value = state.token;
    window.setTimeout(() => input.focus(), 0);
  }

  function closeTokenModal() {
    $("#token-modal").classList.add("hidden");
  }

  function errorMessage(payload, fallback) {
    if (!payload) {
      return fallback;
    }
    if (typeof payload.error === "string") {
      return payload.error;
    }
    if (payload.error && payload.error.message) {
      return payload.error.message;
    }
    if (payload.message) {
      return payload.message;
    }
    return fallback;
  }

  async function api(path, options) {
    const requestOptions = options || {};
    const headers = new Headers(requestOptions.headers || {});
    headers.set("Accept", "application/json");
    if (requestOptions.body && typeof requestOptions.body !== "string") {
      headers.set("Content-Type", "application/json");
      requestOptions.body = JSON.stringify(requestOptions.body);
    }
    if (requestOptions.auth !== false && state.token) {
      headers.set("Authorization", `Bearer ${state.token}`);
    }

    let response;
    try {
      response = await fetch(path, { ...requestOptions, headers });
    } catch (_error) {
      const error = new Error("无法连接到 MediaDock，请检查服务是否运行。");
      error.network = true;
      throw error;
    }

    const text = await response.text();
    let payload = null;
    if (text) {
      try {
        payload = JSON.parse(text);
      } catch (_error) {
        payload = null;
      }
    }
    if (!response.ok) {
      if (response.status === 401) {
        setConnection("需要认证", "error");
        openTokenModal();
      }
      const error = new Error(errorMessage(payload, `请求失败（${response.status}）`));
      error.status = response.status;
      error.payload = payload;
      throw error;
    }
    return payload;
  }

  function formatMode(mode) {
    return {
      search_only: "仅搜索",
      search_and_acquire: "搜索 + 获取",
    }[mode] || mode || "未知";
  }

  function formatState(stateValue) {
    return {
      ready: "就绪",
      unknown: "未检测",
      unconfigured: "未配置",
      unreachable: "不可达",
      unauthorized: "未授权",
      incompatible: "不兼容",
    }[stateValue] || stateValue || "未知";
  }

  function formatJobStatus(status) {
    return {
      queued: "排队中",
      acquiring: "提交中",
      downloading: "下载中",
      downloaded: "已下载",
      failed: "失败",
      cancelled: "已取消",
      unsupported: "不支持",
    }[status] || status || "未知";
  }

  function formatBytes(bytes) {
    if (!bytes || bytes < 0) {
      return "大小未知";
    }
    const units = ["B", "KB", "MB", "GB", "TB"];
    let value = Number(bytes);
    let unit = 0;
    while (value >= 1024 && unit < units.length - 1) {
      value /= 1024;
      unit += 1;
    }
    return `${value.toFixed(value >= 10 || unit === 0 ? 0 : 1)} ${units[unit]}`;
  }

  function formatDate(value) {
    if (!value) {
      return "时间未知";
    }
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) {
      return String(value);
    }
    return date.toLocaleString("zh-CN", { dateStyle: "medium", timeStyle: "short" });
  }

  function renderProviders(providers) {
    const list = $("#provider-list");
    clear(list);
    if (!providers.length) {
      list.append(node("div", "empty-state", "尚未配置搜索组件"));
      return;
    }
    providers.forEach((provider) => {
      const row = node("div", "component-row");
      const details = node("div");
      details.append(node("span", "component-name", provider.id || provider.type || "未命名组件"));
      const reason = provider.reason ? ` · ${provider.reason}` : "";
      details.append(node("span", "component-type", `${provider.type || "unknown"}${reason}`));
      row.append(details, badge(formatState(provider.state), provider.state));
      list.append(row);
    });
  }

  function renderAcquisition(acquisition, downloaders) {
    const summary = $("#acquisition-summary");
    clear(summary);
    const supported = Array.isArray(acquisition && acquisition.supported_kinds)
      ? acquisition.supported_kinds
      : [];
    const unavailable = Array.isArray(acquisition && acquisition.unavailable_kinds)
      ? acquisition.unavailable_kinds
      : [];

    const supportedLine = node("div", "capability-line");
    supportedLine.append(node("span", "", "支持材料"));
    const supportedValues = node("div", "capability-values");
    if (supported.length) {
      supported.forEach((kind) => supportedValues.append(kindBadge(kind)));
    } else {
      supportedValues.append(node("span", "muted", "无"));
    }
    supportedLine.append(supportedValues);
    summary.append(supportedLine);

    const downloaderLine = node("div", "capability-line");
    downloaderLine.append(node("span", "", "已启用下载器"));
    const downloaderValues = node("div", "capability-values");
    if (downloaders.length) {
      downloaders.forEach((downloader) => downloaderValues.append(kindBadge(downloader)));
    } else {
      downloaderValues.append(node("span", "muted", "未配置"));
    }
    downloaderLine.append(downloaderValues);
    summary.append(downloaderLine);

    const defaultLine = node("div", "capability-line");
    defaultLine.append(node("span", "", "默认下载器"));
    defaultLine.append(node("strong", "", acquisition && acquisition.default_downloader_id || "未设置"));
    summary.append(defaultLine);

    if (unavailable.length) {
      const unavailableLine = node("div", "capability-line");
      unavailableLine.append(node("span", "", "暂不可获取"));
      const unavailableValues = node("div", "capability-values");
      unavailable.forEach((item) => unavailableValues.append(kindBadge(`${item.kind}: ${item.reason}`)));
      unavailableLine.append(unavailableValues);
      summary.append(unavailableLine);
    }
  }

  function renderPolicy(policy) {
    const list = $("#policy-list");
    clear(list);
    const values = [
      [policy && policy.confirmation_required, "获取前需要确认"],
      [!(policy && policy.manage_existing_tasks), "不接管外部任务"],
      [!(policy && policy.delete_files), "不删除文件"],
    ];
    values.forEach(([enabled, label]) => {
      const item = node("div", "policy-item");
      item.append(node("i", "", enabled ? "✓" : "!"));
      item.append(node("span", "", label));
      list.append(item);
    });
  }

  function renderDashboard(capabilities, downloaders) {
    setText("#capability-mode", formatMode(capabilities.mode));
    setText("#provider-count", Array.isArray(capabilities.providers) ? capabilities.providers.length : 0);
    setText("#downloader-count", downloaders.length);
    setText("#confirmation-policy", capabilities.policy && capabilities.policy.confirmation_required ? "需要" : "关闭");
    renderProviders(Array.isArray(capabilities.providers) ? capabilities.providers : []);
    renderAcquisition(capabilities.acquisition || {}, downloaders);
    renderPolicy(capabilities.policy || {});
  }

  async function refreshDashboard() {
    const requestID = ++state.dashboardRequest;
    setConnection("检查中", "");
    setText("#health-detail", "正在检查服务");
    try {
      const health = await api("/healthz", { auth: false });
      const results = await Promise.all([
        api("/api/v1/capabilities"),
        api("/api/v1/downloaders"),
      ]);
      if (requestID !== state.dashboardRequest) {
        return;
      }
      const capabilities = results[0] || {};
      const downloaders = Array.isArray(results[1] && results[1].downloaders)
        ? results[1].downloaders
        : [];
      renderDashboard(capabilities, downloaders);
      setText("#health-detail", health && health.status === "ok" ? "服务运行正常" : "服务状态未知");
      setConnection("已连接", "ready");
    } catch (error) {
      if (requestID !== state.dashboardRequest) {
        return;
      }
      setText("#health-detail", error.status === 401 ? "请输入 Bearer Token" : error.message);
      if (error.status !== 401) {
        setConnection("连接失败", "error");
        showToast(error.message, true);
      }
    }
  }

  function setSearchStatus(message, isError) {
    const status = $("#search-status");
    status.textContent = message || "";
    status.className = `inline-status${isError ? " error" : ""}`;
  }

  function candidateMetadata(candidate) {
    const values = [];
    if (candidate.provider) values.push(`来源：${candidate.provider}`);
    if (candidate.kind) values.push(`类型：${candidate.kind}`);
    if (candidate.size_bytes) values.push(formatBytes(candidate.size_bytes));
    if (candidate.seeders) values.push(`做种 ${candidate.seeders}`);
    if (candidate.quality) values.push(candidate.quality);
    if (candidate.codec) values.push(candidate.codec);
    if (candidate.audio) values.push(candidate.audio);
    if (Array.isArray(candidate.subtitles) && candidate.subtitles.length) {
      values.push(`字幕：${candidate.subtitles.join(", ")}`);
    }
    return values;
  }

  function formatKind(kind) {
    return {
      magnet: "磁力链接",
      torrent: "Torrent 文件",
      http_file: "HTTP 文件",
      cloud_share: "网盘分享",
      unknown: "未知材料",
    }[kind] || kind || "未提供";
  }

  function displayValue(value, fallback) {
    const empty = fallback || "未提供";
    if (Array.isArray(value)) {
      return value.length ? value.join(", ") : empty;
    }
    if (value === null || value === undefined || value === "") {
      return empty;
    }
    return String(value);
  }

  function formatPublishedAt(value) {
    if (!value || String(value).startsWith("0001-01-01")) {
      return "未提供";
    }
    return formatDate(value);
  }

  function formatCandidateScore(value) {
    if (value === null || value === undefined || value === "") {
      return "未提供";
    }
    const number = Number(value);
    return Number.isFinite(number) ? number.toFixed(2) : String(value);
  }

  function acquisitionReason(reason) {
    return {
      unsupported_kind: "当前下载器不支持该材料类型",
      missing_acquirer: "没有匹配的获取组件",
      candidate_expired: "候选已过期，请重新搜索",
      downloader_unavailable: "下载器当前不可用",
    }[reason] || reason || "当前不可获取";
  }

  function candidateDetail(label, value) {
    const item = node("div", "candidate-detail");
    item.append(node("dt", "", label), node("dd", "", value));
    return item;
  }

  function renderSearchOverview(result) {
    const overview = $("#search-overview");
    clear(overview);
    overview.classList.remove("hidden");

    const heading = node("div", "search-overview-heading");
    heading.append(node("strong", "", "本次搜索执行情况"));
    heading.append(
      node(
        "span",
        "muted",
        `候选 ${Array.isArray(result && result.candidates) ? result.candidates.length : 0} 条 · 会话 ${displayValue(result && result.search_id)} ` +
          `· 有效期至 ${formatDate(result && result.expires_at)}`,
      ),
    );
    overview.append(heading);

    const healthGrid = node("div", "provider-health-grid");
    const health = Array.isArray(result && result.provider_health)
      ? result.provider_health
      : [];
    if (!health.length) {
      healthGrid.append(node("div", "empty-state", "没有 provider 执行记录"));
    }
    health.forEach((item) => {
      const count = Number(item.count || 0);
      const stateClass = !item.available
        ? "unreachable"
        : count > 0
          ? "ready"
          : "unknown";
      const stateLabel = !item.available
        ? "请求失败"
        : count > 0
          ? `返回 ${count} 条`
          : "已连接，但返回 0 条";
      const card = node("article", "provider-health-card");
      const cardHeading = node("div", "provider-health-heading");
      cardHeading.append(
        node("strong", "", item.provider || "未命名 provider"),
      );
      cardHeading.append(badge(stateLabel, stateClass));
      card.append(cardHeading);
      card.append(
        node(
          "p",
          "provider-health-meta",
          `耗时 ${displayValue(item.duration_ms, "0")} ms`,
        ),
      );
      if (item.error) {
        card.append(node("p", "provider-health-error", item.error));
      } else if (
        count === 0 &&
        String(item.provider || "")
          .toLowerCase()
          .includes("prowlarr")
      ) {
        card.append(
          node(
            "p",
            "provider-health-note",
            "Prowlarr 已连接，但当前没有返回结果。请在 Prowlarr → Indexers 中添加并测试至少一个索引器。",
          ),
        );
      } else if (count === 0) {
        card.append(
          node(
            "p",
            "provider-health-note",
            "组件已响应，但本次关键词没有匹配结果。",
          ),
        );
      }
      healthGrid.append(card);
    });
    overview.append(healthGrid);

    const warnings = Array.isArray(result && result.warnings)
      ? result.warnings
      : [];
    if (warnings.length) {
      const warningBox = node("div", "search-warnings");
      warningBox.append(node("strong", "", "搜索提示"));
      warnings.forEach((warning) => warningBox.append(node("p", "", warning)));
      overview.append(warningBox);
    }
  }

  function randomIdempotencyKey() {
    if (window.crypto && typeof window.crypto.randomUUID === "function") {
      return `webui-${window.crypto.randomUUID()}`;
    }
    return `webui-${Date.now()}-${Math.random().toString(16).slice(2)}`;
  }

  async function acquireCandidate(candidate, button) {
    const available = candidate.acquisition && candidate.acquisition.available;
    if (!available) {
      return;
    }
    const confirmed = window.confirm(
      `确认将“${candidate.title || "此候选"}”提交给下载器吗？\n\nMediaDock 只会提交任务，不会删除文件。`,
    );
    if (!confirmed) {
      return;
    }
    button.disabled = true;
    button.textContent = "提交中…";
    try {
      const job = await api("/api/v1/acquisitions", {
        method: "POST",
        body: {
          candidate_id: candidate.id,
          confirmed: true,
          idempotency_key: randomIdempotencyKey(),
        },
      });
      button.textContent = "已提交";
      showToast(`任务已创建：${job && job.id ? job.id : "请到任务页查看"}`);
      if (state.currentView === "jobs") {
        refreshJobs();
      }
    } catch (error) {
      button.disabled = false;
      button.textContent = "确认获取";
      showToast(error.message, true);
    }
  }

  function renderCandidates(result) {
    renderSearchOverview(result);
    const list = $("#search-results");
    clear(list);
    const candidates = Array.isArray(result && result.candidates) ? result.candidates : [];
    if (!candidates.length) {
      list.append(node("div", "empty-state large-empty", "没有找到候选资源。"));
      return;
    }

    candidates.forEach((candidate) => {
      const card = node("article", "candidate-card");
      const main = node("div", "candidate-main");
      const details = node("div");
      details.append(node("h3", "candidate-title", candidate.title || "未命名候选"));
      details.append(node("p", "candidate-subtitle", `${candidate.source_name || candidate.provider || "未知来源"} · 排名 ${candidate.rank || "—"}`));
      main.append(details);

      const actions = node("div", "candidate-actions");
      if (candidate.acquisition && candidate.acquisition.available) {
        const button = node("button", "button button-primary button-small", "确认获取");
        button.type = "button";
        button.addEventListener("click", () => acquireCandidate(candidate, button));
        actions.append(button);
      } else {
        actions.append(node("span", "status-badge unreachable", "暂不可获取"));
      }
      main.append(actions);
      card.append(main);

      const meta = node("div", "candidate-meta");
      candidateMetadata(candidate).forEach((value) => meta.append(node("span", "", value)));
      card.append(meta);

      const acquisition = candidate.acquisition || {};
      const detailPanel = document.createElement("details");
      detailPanel.className = "candidate-details";
      detailPanel.open = true;
      detailPanel.append(node("summary", "", "查看完整搜索数据"));
      const detailGrid = node("dl", "candidate-detail-grid");
      const size = Number(candidate.size_bytes || 0);
      const acquisitionValue = acquisition.available
        ? "可获取（提交前仍需确认）"
        : "不可获取";
      detailGrid.append(
        candidateDetail("候选 ID", displayValue(candidate.id)),
        candidateDetail("搜索来源", displayValue(candidate.provider)),
        candidateDetail("来源索引器 / 频道", displayValue(candidate.source_name)),
        candidateDetail("资源类型", formatKind(candidate.kind)),
        candidateDetail("文件大小", size > 0 ? formatBytes(size) : "未提供"),
        candidateDetail("画质", displayValue(candidate.quality)),
        candidateDetail("编码", displayValue(candidate.codec)),
        candidateDetail("音频", displayValue(candidate.audio)),
        candidateDetail("字幕", displayValue(candidate.subtitles)),
        candidateDetail("做种数", displayValue(candidate.seeders)),
        candidateDetail("下载数", displayValue(candidate.leechers)),
        candidateDetail("完整性", displayValue(candidate.completeness)),
        candidateDetail("发布时间", formatPublishedAt(candidate.published_at)),
        candidateDetail("标签", displayValue(candidate.tags)),
        candidateDetail("搜索排名", displayValue(candidate.rank)),
        candidateDetail("匹配评分", formatCandidateScore(candidate.score)),
        candidateDetail("获取能力", acquisitionValue),
        candidateDetail(
          "获取说明",
          acquisition.available ? "已匹配当前下载器" : acquisitionReason(acquisition.reason),
        ),
        candidateDetail("需要确认", candidate.requires_confirmation ? "是" : "否"),
      );
      detailPanel.append(detailGrid);
      detailPanel.append(
        node(
          "p",
          "candidate-safe-note",
          "原始下载地址、分享密码和上游私有数据不会展示到 WebUI。",
        ),
      );
      card.append(detailPanel);

      if (!acquisition.available) {
        card.append(node("p", "candidate-note", `暂不可获取：${acquisitionReason(acquisition.reason)}`));
      }
      list.append(card);
    });
  }

  async function submitSearch(event) {
    event.preventDefault();
    const form = event.currentTarget;
    const subtitles = $("#subtitles").value
      .split(",")
      .map((value) => value.trim())
      .filter(Boolean);
    const body = {
      query: $("#query").value.trim(),
      media_type: $("#media-type").value,
      quality: $("#quality").value.trim(),
      subtitles,
      limit: Number($("#limit").value),
    };
    if (!body.query) {
      setSearchStatus("请输入搜索内容。", true);
      return;
    }

    const submitButton = form.querySelector("button[type=submit]");
    submitButton.disabled = true;
    submitButton.textContent = "搜索中…";
    setSearchStatus("正在查询已配置的搜索组件…");
    try {
      const result = await api("/api/v1/search", { method: "POST", body });
      renderCandidates(result);
      const count = Array.isArray(result && result.candidates) ? result.candidates.length : 0;
      setSearchStatus(`找到 ${count} 个候选，搜索会话有效期至 ${formatDate(result && result.expires_at)}。`);
    } catch (error) {
      setSearchStatus(error.message, true);
      showToast(error.message, true);
    } finally {
      submitButton.disabled = false;
      submitButton.textContent = "开始搜索";
    }
  }

  function renderJobs(payload) {
    const list = $("#jobs-list");
    clear(list);
    const jobs = Array.isArray(payload && payload.jobs) ? payload.jobs : [];
    if (!jobs.length) {
      list.append(node("div", "empty-state", "没有符合条件的任务。"));
      return;
    }

    jobs.forEach((job) => {
      const row = node("div", "job-row");
      const identity = node("div");
      identity.append(node("span", "job-id", job.id || "未知任务"));
      identity.append(node("span", "job-message", `${job.provider || "未知来源"} · 候选 ${job.candidate_id || "—"}`));
      row.append(identity);

      const status = node("div");
      status.append(badge(formatJobStatus(job.status), job.status));
      if (job.status_stale) {
        status.append(node("span", "job-date", "状态待刷新"));
      }
      row.append(status);

      const progress = node("div");
      const progressValue = Math.max(0, Math.min(100, Number(job.progress || 0) * 100));
      progress.append(node("span", "job-message", job.message || job.error || "暂无状态说明"));
      if (["queued", "acquiring", "downloading"].includes(job.status)) {
        const bar = node("div", "job-progress");
        const fill = node("span");
        fill.style.width = `${progressValue}%`;
        bar.append(fill);
        progress.append(bar);
      }
      progress.append(node("span", "job-date", `更新时间：${formatDate(job.updated_at)}`));
      row.append(progress);

      const actions = node("div");
      const canCancel = job.ownership === "managed"
        && !["downloaded", "failed", "cancelled", "unsupported"].includes(job.status);
      if (canCancel) {
        const button = node("button", "button button-ghost button-small", "取消");
        button.type = "button";
        button.addEventListener("click", () => cancelJob(job, button));
        actions.append(button);
      } else if (job.ownership === "external") {
        actions.append(node("span", "muted", "外部任务"));
      }
      row.append(actions);
      list.append(row);
    });
  }

  async function refreshJobs(quiet) {
    const requestID = ++state.jobsRequest;
    const status = $("#job-status-filter").value;
    const query = status ? `?limit=50&status=${encodeURIComponent(status)}` : "?limit=50";
    try {
      const payload = await api(`/api/v1/jobs${query}`);
      if (requestID !== state.jobsRequest) {
        return;
      }
      renderJobs(payload);
      setText("#jobs-updated", `更新于 ${new Date().toLocaleTimeString("zh-CN")}`);
    } catch (error) {
      if (!quiet && error.status !== 401) {
        showToast(error.message, true);
      }
      if (error.status !== 401) {
        const list = $("#jobs-list");
        clear(list);
        list.append(node("div", "empty-state", error.message));
      }
    }
  }

  async function cancelJob(job, button) {
    if (!window.confirm(`确认取消任务 ${job.id} 吗？\n\n已存在于下载器中的外部任务不会被 MediaDock 取消。`)) {
      return;
    }
    button.disabled = true;
    button.textContent = "取消中…";
    try {
      await api(`/api/v1/jobs/${encodeURIComponent(job.id)}/cancel`, { method: "POST" });
      showToast("任务取消请求已完成");
      await refreshJobs();
    } catch (error) {
      button.disabled = false;
      button.textContent = "取消";
      showToast(error.message, true);
    }
  }

  function setView(view) {
    const knownViews = ["dashboard", "search", "jobs", "access"];
    state.currentView = knownViews.includes(view) ? view : "dashboard";
    knownViews.forEach((name) => {
      const section = $(`#view-${name}`);
      const active = name === state.currentView;
      section.classList.toggle("active", active);
      section.setAttribute("aria-hidden", active ? "false" : "true");
    });
    $$(`[data-view-link]`).forEach((link) => {
      link.classList.toggle("active", link.dataset.viewLink === state.currentView);
    });
    if (window.location.hash !== `#${state.currentView}`) {
      window.history.replaceState(null, "", `#${state.currentView}`);
    }
    if (state.currentView === "dashboard") {
      refreshDashboard();
    } else if (state.currentView === "jobs") {
      refreshJobs();
    }
  }

  function setupAccessView() {
    setText("#web-url", window.location.origin);
    setText("#api-url", `${window.location.origin}/api/v1`);
    setText("#mcp-url", `${window.location.origin}/mcp`);
  }

  $$("[data-view-link]").forEach((link) => {
    link.addEventListener("click", () => setView(link.dataset.viewLink));
  });
  $("#search-form").addEventListener("submit", submitSearch);
  $("#refresh-dashboard").addEventListener("click", refreshDashboard);
  $("#refresh-jobs").addEventListener("click", () => refreshJobs());
  $("#job-status-filter").addEventListener("change", () => refreshJobs());
  $("#token-button").addEventListener("click", openTokenModal);
  $("#access-token-button").addEventListener("click", openTokenModal);
  $("#close-token").addEventListener("click", closeTokenModal);
  $("#token-modal").addEventListener("click", (event) => {
    if (event.target === event.currentTarget) {
      closeTokenModal();
    }
  });
  $("#save-token").addEventListener("click", () => {
    state.token = $("#token-input").value.trim();
    if (state.token) {
      sessionStorage.setItem(tokenStorageKey, state.token);
    } else {
      sessionStorage.removeItem(tokenStorageKey);
    }
    closeTokenModal();
    showToast(state.token ? "Token 已保存到当前会话" : "已切换到无 Token 请求");
    refreshDashboard();
    if (state.currentView === "jobs") {
      refreshJobs();
    }
  });
  $("#clear-token").addEventListener("click", () => {
    state.token = "";
    sessionStorage.removeItem(tokenStorageKey);
    $("#token-input").value = "";
    setConnection("未认证", "error");
    showToast("已清除当前会话 Token");
  });
  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape") {
      closeTokenModal();
    }
  });
  window.addEventListener("hashchange", () => setView(window.location.hash.slice(1)));

  setupAccessView();
  setView(window.location.hash.slice(1) || "dashboard");
  window.setInterval(() => {
    if (state.currentView === "jobs" && document.visibilityState === "visible") {
      refreshJobs(true);
    }
  }, 10000);
})();
