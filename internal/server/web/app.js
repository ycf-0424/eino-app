const state = {
  sessionId: "",
  socket: null,
  generating: false,
  assistantBody: null,
  assistantNode: null,
  pendingApproval: null,
  deleteTarget: "",
  // 用户主动上滑后暂停自动滚底；回到底部时自动恢复跟随。
  followOutput: true,
  // 本轮实时执行进度：run_id、已渲染的最大 sequence 与去重集合。
  activeRun: null,
  // 后端调试模式：只有它为 true 时才显示技能入口与技能事件。
  debug: false,
  // 正在进行的「申请新会话」请求：连点新对话时复用同一次签发，
  // 避免在服务端留下多条空会话。
  newChatRequest: null,
};

const ui = Object.fromEntries([
  "sidebar", "sidebarScrim", "openSidebar", "closeSidebar", "newChat", "sessionList",
  "statusDot", "statusText", "conversationState", "skillControl", "skillSelect", "clearChat", "welcome",
  "messages", "conversation", "composer", "promptInput", "sendButton", "requestStatus",
  "approvalPanel", "approvalDescription", "approveAction", "rejectApproval", "toast",
  "deleteDialog", "confirmDelete",
].map((id) => [id, document.getElementById(id)]));

// 工具与事件的中文标签；未知名称直接展示原名，不伪造语义。
const TOOL_LABELS = {
  current_time: "查询当前时间",
  knowledge_search: "知识库检索",
  load_skills: "加载技能",
  local_file_read: "读取本地文件",
  write_note: "写入笔记",
};

const ERROR_LABELS = {
  timeout: "执行超时",
  outside_roots: "路径越权或文件不存在",
  too_large: "文件超出大小限制",
  not_text: "文件不是 UTF-8 文本",
  not_regular_file: "目标不是普通文件",
  not_found: "文件不存在",
  tool_error: "执行失败",
  cancelled: "已取消",
  disconnected: "连接中断",
  interrupted: "进程中断",
  error: "执行失败",
};

function el(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

function toolLabel(name) {
  if (!name) return "工具";
  return TOOL_LABELS[name] || name;
}

function errorLabel(code) {
  if (!code) return "执行失败";
  return ERROR_LABELS[code] || code;
}

async function api(path, options = {}) {
  const response = await fetch(path, {
    ...options,
    headers: { "Content-Type": "application/json", ...(options.headers || {}) },
  });
  const payload = await response.json().catch(() => ({}));
  if (!response.ok || payload.error) throw new Error(payload.error || `请求失败 (${response.status})`);
  return payload.data;
}

function showToast(message) {
  ui.toast.textContent = message;
  ui.toast.classList.add("visible");
  clearTimeout(showToast.timer);
  showToast.timer = setTimeout(() => ui.toast.classList.remove("visible"), 2800);
}

function setHealth(online) {
  ui.statusDot.className = `status-dot ${online ? "online" : "offline"}`;
  ui.statusText.textContent = online ? "服务已就绪" : "服务不可用";
}

function setGenerating(value) {
  state.generating = value;
  ui.promptInput.disabled = value;
  ui.sendButton.disabled = !value && !ui.promptInput.value.trim();
  ui.sendButton.classList.toggle("stop", value);
  ui.sendButton.querySelector("span").textContent = value ? "" : "↑";
  ui.sendButton.title = value ? "停止生成" : "发送消息";
  ui.requestStatus.textContent = value ? "正在生成" : "本地运行";
}

function resizeInput() {
  ui.promptInput.style.height = "auto";
  ui.promptInput.style.height = `${Math.min(ui.promptInput.scrollHeight, 180)}px`;
  if (!state.generating) ui.sendButton.disabled = !ui.promptInput.value.trim();
}

function normalizeText(value) {
  return String(value || "").replace(/\r\n/g, "\n").replace(/\r/g, "\n");
}

// 模型正文作为不可信输入处理：使用 DOM 节点和 textContent 构建 Markdown，绝不把模型输出当 HTML 执行。
function appendInline(parent, value) {
  const text = String(value || "");
  const token = /`([^`\n]+)`|\*\*([^*\n]+)\*\*|__([^_\n]+)__|\*([^*\n]+)\*|_([^_\n]+)_|\[([^\]\n]+)\]\((https?:\/\/[^\s)]+)\)/g;
  let cursor = 0;
  let match;
  while ((match = token.exec(text)) !== null) {
    if (match.index > cursor) parent.append(document.createTextNode(text.slice(cursor, match.index)));
    if (match[1] !== undefined) {
      const code = document.createElement("code");
      code.textContent = match[1];
      parent.append(code);
    } else if (match[2] !== undefined || match[3] !== undefined) {
      const strong = document.createElement("strong");
      strong.textContent = match[2] ?? match[3];
      parent.append(strong);
    } else if (match[4] !== undefined || match[5] !== undefined) {
      const em = document.createElement("em");
      em.textContent = match[4] ?? match[5];
      parent.append(em);
    } else {
      const link = document.createElement("a");
      link.href = match[7];
      link.target = "_blank";
      link.rel = "noopener noreferrer";
      link.textContent = match[6];
      parent.append(link);
    }
    cursor = token.lastIndex;
  }
  if (cursor < text.length) parent.append(document.createTextNode(text.slice(cursor)));
}

function renderMarkdownInto(container, value) {
  container.replaceChildren();
  const source = normalizeText(value).trim();
  if (!source) return;
  const lines = source.split("\n");
  let paragraph = [];
  let list = null;
  let codeLanguage = "";
  let codeLines = [];

  const flushParagraph = () => {
    if (!paragraph.length) return;
    const node = document.createElement("p");
    paragraph.forEach((line, index) => {
      if (index) node.append(document.createElement("br"));
      appendInline(node, line);
    });
    container.append(node);
    paragraph = [];
  };
  const closeList = () => { list = null; };
  const closeCode = () => {
    if (!codeLanguage) return;
    const pre = document.createElement("pre");
    const code = document.createElement("code");
    code.className = `language-${codeLanguage}`;
    code.textContent = codeLines.join("\n");
    pre.append(code);
    container.append(pre);
    codeLanguage = "";
    codeLines = [];
  };

  for (const line of lines) {
    const fence = line.match(/^\s*```\s*([\w-]*)\s*$/);
    if (fence) {
      flushParagraph();
      closeList();
      if (codeLanguage) closeCode();
      else { codeLanguage = fence[1] || "text"; codeLines = []; }
      continue;
    }
    if (codeLanguage) { codeLines.push(line); continue; }
    if (!line.trim()) { flushParagraph(); closeList(); continue; }
    const heading = line.match(/^\s*(#{1,6})\s+(.+?)\s*#*\s*$/);
    if (heading) {
      flushParagraph(); closeList();
      const node = document.createElement(`h${heading[1].length}`);
      appendInline(node, heading[2]);
      container.append(node);
      continue;
    }
    const unordered = line.match(/^\s*[-*+]\s+(.+)$/);
    const ordered = line.match(/^\s*\d+[.)]\s+(.+)$/);
    if (unordered || ordered) {
      flushParagraph();
      const kind = unordered ? "ul" : "ol";
      if (!list || list.tagName.toLowerCase() !== kind) {
        closeList();
        list = document.createElement(kind);
        container.append(list);
      }
      const item = document.createElement("li");
      appendInline(item, (unordered || ordered)[1]);
      list.append(item);
      continue;
    }
    closeList();
    paragraph.push(line);
  }
  flushParagraph();
  closeList();
  closeCode();
}

// qwen 等模型可能返回 <think>，流式阶段允许标签尚未闭合；思考内容始终单独展示。
function parseThink(value) {
  const text = normalizeText(value);
  const thinkParts = [];
  let rest = text;
  const closed = /<think(?:ing)?>\s*([\s\S]*?)<\/think(?:ing)?>/gi;
  let match;
  while ((match = closed.exec(rest)) !== null) {
    if (match[1]?.trim()) thinkParts.push(match[1].trim());
  }
  rest = rest.replace(/<think(?:ing)?>\s*[\s\S]*?<\/think(?:ing)?>/gi, "");
  const open = rest.match(/<think(?:ing)?>\s*([\s\S]*)$/i);
  if (open) {
    if (open[1]?.trim()) thinkParts.push(open[1].trim());
    rest = rest.replace(/<think(?:ing)?>\s*[\s\S]*$/i, "");
  }
  return { think: thinkParts.join("\n\n").trim(), content: rest.trim() };
}

function parseStructuredAnswer(value) {
  const text = normalizeText(value).trim();
  if (!text) return null;
  const unwrapped = text.replace(/^```(?:json)?\s*/i, "").replace(/\s*```$/i, "").trim();
  if (!unwrapped.startsWith("{") || !unwrapped.endsWith("}")) return null;
  try {
    const parsed = JSON.parse(unwrapped);
    if (typeof parsed?.answer !== "string") return null;
    return {
      answer: parsed.answer,
      sources: Array.isArray(parsed.sources) ? parsed.sources.filter((item) => typeof item === "string" && item.trim()) : [],
      confidence: typeof parsed.confidence === "number" ? parsed.confidence : null,
    };
  } catch {
    return null;
  }
}

function parseAssistantOutput(value) {
  const parsedThink = parseThink(value);
  const structured = parseStructuredAnswer(parsedThink.content);
  if (structured) {
    return { ...parsedThink, content: structured.answer, sources: structured.sources, confidence: structured.confidence };
  }
  return { ...parsedThink, sources: [], confidence: null };
}

function renderSources(node, sources, confidence) {
  if (!node.sources) return;
  const mergedSources = [...new Set([...(node.eventSources || []), ...(sources || [])])];
  node.sources.replaceChildren();
  if (mergedSources.length) {
    const label = el("span", "source-label", "参考来源");
    node.sources.append(label);
    mergedSources.forEach((source) => node.sources.append(el("span", "source-chip", source)));
  }
  if (typeof confidence === "number" && Number.isFinite(confidence)) {
    node.sources.append(el("span", "confidence", `置信度 ${(Math.max(0, Math.min(1, confidence)) * 100).toFixed(0)}%`));
  }
  node.sources.hidden = node.sources.childElementCount === 0;
}

function setStreamStatus(node, text) {
  if (!node?.streamStatus) return;
  node.streamStatus.hidden = false;
  node.streamStatus.querySelector(".stream-status-text").textContent = text;
}

function renderAssistant(node, rawText, streaming = false) {
  if (!node?.body) return;
  node.rawText = normalizeText(rawText);
  const parsed = parseAssistantOutput(node.rawText);
  node.answerText = parsed.content;
  const thinkParts = [node.reasoningText, parsed.think].filter((part) => part && part.trim());
  node.thinkText = thinkParts.join("\n\n").trim();
  renderMarkdownInto(node.body, parsed.content);
  node.body.classList.toggle("empty", !parsed.content);
  if (node.think) {
    node.think.textContent = node.thinkText;
    node.thinkBlock.hidden = !node.thinkText;
    if (node.thinkText && streaming && !node.hasThink) {
      node.thinkBlock.classList.add("live");
      node.thinkCollapsed = false;
    }
    node.hasThink = Boolean(node.thinkText);
    node.thinkBlock.classList.toggle("collapsed", node.thinkCollapsed);
    node.thinkToggle.setAttribute("aria-expanded", node.thinkCollapsed ? "false" : "true");
    node.thinkToggle.querySelector(".think-toggle-label").textContent = streaming ? "思考中（实时）" : "思考过程";
  }
  if (node.streamStatus) {
    const hasAnswer = Boolean(parsed.content);
    node.streamStatus.hidden = !streaming;
    node.streamStatus.querySelector(".stream-status-text").textContent = node.thinkText && !hasAnswer ? "正在思考…" : "正在生成回答…";
  }
  renderSources(node, parsed.sources, parsed.confidence);
}

// execPanel 创建可折叠的「执行过程」面板。live=true 时默认展开（实时进度）。
function execPanel(live) {
  const panel = el("div", "exec-panel");
  const toggle = el("button", "exec-toggle");
  toggle.type = "button";
  toggle.setAttribute("aria-expanded", live ? "true" : "false");
  toggle.append(el("span", "exec-toggle-icon", "▸"), el("span", "exec-toggle-label", "执行过程"), el("span", "exec-toggle-meta", ""));
  const list = el("ol", "exec-list");
  panel.append(toggle, list);
  panel.classList.toggle("collapsed", !live);
  panel._items = new Map();
  toggle.onclick = () => {
    const collapsed = panel.classList.toggle("collapsed");
    toggle.setAttribute("aria-expanded", collapsed ? "false" : "true");
  };
  return panel;
}

function messageNode(role, content = "", reasoning = "") {
  const row = document.createElement("article");
  row.className = `message ${role}`;
  const inner = document.createElement("div");
  inner.className = "message-inner";
  const avatar = document.createElement("div");
  avatar.className = "avatar";
  avatar.textContent = role === "assistant" ? "E" : "你";
  const body = document.createElement("div");
  body.className = "message-body";
  const contentBox = document.createElement("div");
  contentBox.className = "message-content";
  let panel = null;
  const node = { row, body, panel, rawText: "", reasoningText: normalizeText(reasoning), answerText: "", thinkText: "", hasThink: false, thinkCollapsed: true };
  if (role === "assistant") {
    panel = execPanel(false);
    panel.hidden = true;
    node.panel = panel;
    panel._messageNode = node;

    const streamStatus = el("div", "stream-status");
    streamStatus.append(el("span", "stream-status-dot"), el("span", "stream-status-text", "正在生成回答…"));
    streamStatus.hidden = true;
    node.streamStatus = streamStatus;

    const thinkBlock = el("section", "think-block");
    const thinkToggle = el("button", "think-toggle");
    thinkToggle.type = "button";
    thinkToggle.setAttribute("aria-expanded", "false");
    thinkToggle.append(el("span", "think-toggle-icon", "▸"), el("span", "think-toggle-label", "思考过程"), el("span", "think-toggle-hint", "展开"));
    const thinkText = el("pre", "think-text");
    thinkBlock.append(thinkToggle, thinkText);
    thinkBlock.hidden = true;
    node.thinkBlock = thinkBlock;
    node.thinkToggle = thinkToggle;
    node.think = thinkText;
    thinkToggle.onclick = () => {
      node.thinkCollapsed = !node.thinkCollapsed;
      thinkBlock.classList.toggle("collapsed", node.thinkCollapsed);
      thinkToggle.setAttribute("aria-expanded", node.thinkCollapsed ? "false" : "true");
      thinkToggle.querySelector(".think-toggle-icon").textContent = node.thinkCollapsed ? "▸" : "▾";
      thinkToggle.querySelector(".think-toggle-hint").textContent = node.thinkCollapsed ? "展开" : "折叠";
    };

    const sources = el("div", "sources");
    sources.hidden = true;
    node.sources = sources;
    contentBox.append(panel, streamStatus, thinkBlock);
  }
  contentBox.append(body);
  if (role === "assistant") {
    const actions = document.createElement("div");
    actions.className = "message-actions";
    const copy = document.createElement("button");
    copy.type = "button";
    copy.className = "copy-button";
    copy.textContent = "复制";
    copy.onclick = async () => {
      try {
        await navigator.clipboard.writeText(node.answerText || body.textContent || "");
        showToast("回答已复制");
      } catch {
        showToast("浏览器未允许访问剪贴板");
      }
    };
    actions.append(copy);
    contentBox.append(node.sources, actions);
  }
  inner.append(avatar, contentBox);
  row.append(inner);
  body._messageNode = node;
  if (role === "assistant") renderAssistant(node, content, false);
  else body.textContent = content;
  return node;
}

function addMessage(role, content = "", pending = false, reasoning = "") {
  ui.welcome.hidden = true;
  ui.conversation.classList.add("has-messages");
  const node = messageNode(role, content, reasoning);
  node.row.classList.toggle("pending", pending);
  ui.messages.append(node.row);
  state.followOutput = true;
  scrollToBottom();
  return node;
}

function scrollToBottom() {
  if (!state.followOutput) return;
  requestAnimationFrame(() => { ui.conversation.scrollTop = ui.conversation.scrollHeight; });
}

function closeSocket() {
  if (state.socket) {
    state.socket.onclose = null;
    state.socket.close();
    state.socket = null;
  }
}

function finishGeneration() {
  if (state.assistantNode) {
    renderAssistant(state.assistantNode, state.assistantNode.rawText, false);
    state.assistantNode.thinkBlock?.classList.remove("live");
    state.assistantNode.row.classList.remove("pending");
  } else {
    state.assistantBody?.closest(".message")?.classList.remove("pending");
  }
  state.assistantBody = null;
  state.assistantNode = null;
  state.socket = null;
  state.activeRun = null;
  setGenerating(false);
  loadSessions();
  ui.promptInput.focus();
}

// startNewChat 先向服务端申请一个新的 session id，再进入空会话。
//
// id 不再由前端生成：客户端自造 id 正是「知道别人的 id 就能读他的审批内容、
// 恢复他的执行」的来源。现在一律由 POST /sessions 签发并登记归属，
// 因此这里必须异步等待，`makeSessionId()` 已移除。
async function startNewChat() {
  closeSocket();
  if (!state.newChatRequest) {
    state.newChatRequest = api("/sessions", { method: "POST" })
      .then((created) => created.id)
      .finally(() => {
        state.newChatRequest = null;
      });
  }
  try {
    state.sessionId = await state.newChatRequest;
  } catch (error) {
    showToast(error.message);
    return;
  }
  state.assistantBody = null;
  state.assistantNode = null;
  state.pendingApproval = null;
  state.activeRun = null;
  state.followOutput = true;
  ui.approvalPanel.hidden = true;
  ui.messages.replaceChildren();
  ui.welcome.hidden = false;
  ui.conversation.classList.remove("has-messages");
  ui.conversationState.textContent = "新对话";
  setGenerating(false);
  highlightSession();
  closeSidebar();
  ui.promptInput.focus();
}

function socketURL() {
  const protocol = location.protocol === "https:" ? "wss:" : "ws:";
  return `${protocol}//${location.host}/ws?session_id=${encodeURIComponent(state.sessionId)}`;
}

// updateExecSummary 汇总步数与工具耗时，只统计结构化字段。
function updateExecSummary(panel) {
  const items = panel.querySelectorAll(".exec-item");
  let ms = 0;
  items.forEach((item) => {
    const value = Number.parseInt(item.dataset.ms || "0", 10);
    if (!Number.isNaN(value)) ms += value;
  });
  panel.querySelector(".exec-toggle-meta").textContent = `${items.length} 步${ms ? ` · 工具 ${(ms / 1000).toFixed(1)}s` : ""}`;
}

// describeEvent 从 payload 的结构化字段生成展示文本；summary 只作为兜底。
function describeEvent(event) {
  const payload = event.payload || {};
  const name = payload.tool_name;
  switch (event.type) {
    case "tool_completed":
      if (name === "knowledge_search" && typeof payload.hit_count === "number") {
        return `知识库检索：${payload.hit_count === 0 ? "无命中" : `命中 ${payload.hit_count} 条`}`;
      }
      return `${toolLabel(name)}已完成`;
    case "skill_loaded": {
      const names = Array.isArray(payload.skill_names) ? payload.skill_names : [];
      return names.length ? `加载技能：${names.join("、")}` : "加载技能";
    }
    case "file_read_completed": {
      const file = payload.file_name || "本地文件";
      const format = payload.file_format ? `（${payload.file_format}）` : "";
      return `读取 ${file}${format}`;
    }
    case "tool_failed":
      return `${toolLabel(name)}失败：${errorLabel(payload.error_code)}`;
    default:
      return event.summary || toolLabel(name);
  }
}

// renderEvent 把一条事件渲染进面板；同一 tool_call_id 复用同一条目。
function renderEvent(panel, event) {
  if (!panel || !event || !event.type) return;
  if (event.type === "chunk") return;
  const payload = event.payload || {};
  // 技能属于内部实现：非调试模式下不展示任何技能相关事件，
  // 包括 load_skills 的工具调用本身，否则面板会露出「加载技能」。
  if (!state.debug && (event.type === "skill_preloaded" || event.type === "skill_loaded" || payload.tool_name === "load_skills")) return;
  const message = panel._messageNode;
  panel.hidden = false;
  const list = panel.querySelector(".exec-list");
  const key = payload.tool_call_id || `event:${event.event_id}`;
  let item = panel._items.get(key);
  if (!item) {
    item = el("li", "exec-item");
    item.append(el("span", "exec-icon"), el("span", "exec-text"), el("span", "exec-time"));
    panel._items.set(key, item);
    list.append(item);
  }
  const [icon, text, time] = item.children;
  switch (event.type) {
    case "run_started":
      item.dataset.state = "info";
      icon.textContent = "▶";
      text.textContent = "开始处理本轮请求";
      setStreamStatus(message, "正在准备本轮请求…");
      break;
    case "model_waiting":
      item.dataset.state = "pending";
      icon.textContent = "…";
      text.textContent = payload.phase === "resume" ? "等待模型继续生成" : "等待模型响应";
      setStreamStatus(message, "正在思考…");
      break;
    case "skill_preloaded": {
      const names = Array.isArray(payload.skill_names) ? payload.skill_names : [];
      item.dataset.state = "info";
      icon.textContent = "✦";
      text.textContent = names.length ? `预加载技能：${names.join("、")}` : "预加载技能";
      break;
    }
    case "tool_started":
      item.dataset.state = "pending";
      icon.textContent = "…";
      text.textContent = `调用 ${toolLabel(payload.tool_name)}`;
      setStreamStatus(message, `正在调用 ${toolLabel(payload.tool_name)}…`);
      break;
    case "tool_completed":
    case "skill_loaded":
    case "file_read_completed":
      item.dataset.state = "done";
      icon.textContent = "✓";
      text.textContent = describeEvent(event);
      setStreamStatus(message, "正在整理回答…");
      if (payload.tool_name === "knowledge_search" && Array.isArray(payload.sources)) {
        const message = panel._messageNode;
        if (message) {
          message.eventSources = payload.sources.filter((source) => typeof source === "string" && source.trim());
          renderSources(message, [], null);
        }
      }
      if (typeof payload.duration_ms === "number") {
        item.dataset.ms = String(payload.duration_ms);
        time.textContent = `${payload.duration_ms}ms`;
      }
      break;
    case "tool_failed":
      item.dataset.state = "failed";
      icon.textContent = "✗";
      text.textContent = describeEvent(event);
      time.textContent = "";
      setStreamStatus(message, "工具执行失败，正在处理结果…");
      break;
    case "approval_required":
      item.dataset.state = "pending";
      icon.textContent = "!";
      text.textContent = `等待审批：${toolLabel(payload.tool_name)}`;
      setStreamStatus(message, "等待你的批准…");
      break;
    case "run_completed":
      item.dataset.state = "done";
      icon.textContent = "✓";
      text.textContent = "本轮完成";
      break;
    case "run_failed":
      item.dataset.state = "failed";
      icon.textContent = "✗";
      text.textContent = `本轮失败：${errorLabel(payload.error_code)}`;
      break;
    case "run_cancelled":
      item.dataset.state = "failed";
      icon.textContent = "×";
      text.textContent = `本轮已取消${payload.reason ? `（${errorLabel(payload.reason)}）` : ""}`;
      break;
    default:
      if (!event.type || !String(event.type).length) return;
      item.dataset.state = "info";
      icon.textContent = "•";
      text.textContent = event.summary || event.type;
      break;
  }
  updateExecSummary(panel);
  if (panel.classList.contains("collapsed")) {
    panel.classList.remove("collapsed");
    panel.querySelector(".exec-toggle").setAttribute("aria-expanded", "true");
  }
  scrollToBottom();
}

// handleStreamEvent 负责按 run 去重、按 sequence 排序，并在发现丢帧时补取。
function handleStreamEvent(event) {
  const run = state.activeRun;
  if (!run || !event) return;
  if (run.runId && event.run_id && event.run_id !== run.runId) return;
  if (!run.runId && event.run_id) run.runId = event.run_id;
  const key = `${event.run_id || ""}#${event.sequence ?? ""}`;
  if (run.seen.has(key)) return;
  run.seen.add(key);
  if (typeof event.sequence === "number" && event.sequence > run.lastSeq) run.lastSeq = event.sequence;
  renderEvent(run.panel, event);
  if (event.dropped && run.runId) backfillRun(run.runId);
}

// backfillRun 按 after_sequence 补取被背压丢弃的执行事件，避免面板缺步。
async function backfillRun(runId) {
  if (!state.activeRun || state.activeRun.runId !== runId) return;
  const after = state.activeRun.lastSeq;
  try {
    const snapshot = await api(`/sessions/${encodeURIComponent(state.sessionId)}/execution?run_id=${encodeURIComponent(runId)}&after_sequence=${after}`);
    for (const event of snapshot?.events || []) handleStreamEvent(event);
  } catch {
    // 补取失败不影响主要回答，保留已有进度即可。
  }
}

function sendMessage(query) {
  if (!query || state.generating) return;
  state.pendingApproval = null;
  ui.approvalPanel.hidden = true;
  if (ui.messages.children.length === 0) ui.conversationState.textContent = query.replace(/\s+/g, " ").slice(0, 42);
  addMessage("user", query);
  const assistant = addMessage("assistant", "", true);
  state.assistantBody = assistant.body;
  state.assistantNode = assistant;
  renderAssistant(assistant, "", true);
  state.activeRun = { runId: "", lastSeq: 0, seen: new Set(), panel: assistant.panel };
  setGenerating(true);
  ui.promptInput.value = "";
  resizeInput();

  const socket = new WebSocket(socketURL());
  state.socket = socket;
  socket.onmessage = (event) => {
    const message = JSON.parse(event.data);
    if (message.type === "ready") {
      // 会话 id 以服务端为准：连接建立时可能已经签发或校正过它。
      if (message.session_id) {
        state.sessionId = message.session_id;
        highlightSession();
      }
      applyDebugMode(message.debug === true);
      // 帧内不再携带 session_id —— 会话由连接本身确定，服务端一律忽略该字段。
      // 非调试模式不带 skill：按名指定技能属于内部能力，交给模型自主路由。
      socket.send(JSON.stringify({ type: "chat", query, skill: state.debug ? ui.skillSelect.value : "" }));
    } else if (message.type === "chunk") {
      const run = state.activeRun;
      if (run) {
        if (run.runId && message.run_id && message.run_id !== run.runId) return;
        if (message.run_id) run.runId = message.run_id;
        if (typeof message.sequence === "number") {
          const key = `${message.run_id || ""}#${message.sequence}`;
          if (run.seen.has(key)) return;
          run.seen.add(key);
          run.lastSeq = Math.max(run.lastSeq, message.sequence);
        }
      }
      assistant.rawText += message.content || "";
      assistant.reasoningText += message.reasoning_content || message.reasoning || "";
      renderAssistant(assistant, assistant.rawText, true);
      scrollToBottom();
      if (message.dropped && run?.runId) backfillRun(run.runId);
    } else if (message.type === "reasoning") {
      const run = state.activeRun;
      if (run) {
        if (run.runId && message.run_id && message.run_id !== run.runId) return;
        if (message.run_id) run.runId = message.run_id;
        if (typeof message.sequence === "number") {
          const key = `${message.run_id || ""}#${message.sequence}`;
          if (run.seen.has(key)) return;
          run.seen.add(key);
          run.lastSeq = Math.max(run.lastSeq, message.sequence);
        }
      }
      assistant.reasoningText += message.reasoning_content || message.content || message.reasoning || "";
      renderAssistant(assistant, assistant.rawText, true);
      scrollToBottom();
      if (message.dropped && run?.runId) backfillRun(run.runId);
    } else if (message.type === "event") {
      handleStreamEvent(message.event);
    } else if (message.type === "approval") {
      state.pendingApproval = message.approval;
      ui.approvalDescription.textContent = `${message.approval.tool_name}: ${message.approval.arguments}`;
      ui.approvalPanel.hidden = false;
      finishGeneration();
    } else if (message.type === "cancelled") {
      showToast("已停止生成");
      finishGeneration();
    } else if (message.type === "done") {
      finishGeneration();
    } else if (message.type === "error") {
      if (!assistant.rawText) {
        assistant.rawText = `请求失败：${message.error}`;
        renderAssistant(assistant, assistant.rawText, false);
      }
      showToast(message.error || "生成失败");
      finishGeneration();
    }
  };
  socket.onerror = () => {
    if (!assistant.rawText) {
      assistant.rawText = "连接中断，请确认服务是否正常运行。";
      renderAssistant(assistant, assistant.rawText, false);
    }
    showToast("WebSocket 连接失败");
  };
  socket.onclose = () => {
    if (state.generating) finishGeneration();
  };
}

function stopGeneration() {
  if (!state.generating) return;
  // 优先通知后端取消本轮执行；连接已断开时才退回关闭连接。
  if (state.socket && state.socket.readyState === WebSocket.OPEN) {
    state.socket.send(JSON.stringify({ type: "cancel" }));
    showToast("正在停止…");
    return;
  }
  closeSocket();
  finishGeneration();
}

async function submitApproval(approved) {
  if (!state.pendingApproval || state.generating) return;
  ui.approvalPanel.hidden = true;
  const assistant = addMessage("assistant", approved ? "正在执行已批准的操作…" : "正在取消该操作…", true);
  state.assistantBody = assistant.body;
  state.assistantNode = assistant;
  assistant.rawText = "";
  renderAssistant(assistant, "", true);
  state.activeRun = { runId: "", lastSeq: 0, seen: new Set(), panel: assistant.panel };
  setGenerating(true);
  try {
    const result = await api(`/sessions/${encodeURIComponent(state.sessionId)}/approval`, {
      method: "POST",
      body: JSON.stringify({ approved }),
    });
    assistant.rawText = result.answer || (approved ? "操作已完成。" : "操作已取消。");
    renderAssistant(assistant, assistant.rawText, false);
    for (const event of result.events || []) {
      if (event.type === "chunk") continue;
      const key = `${event.run_id || ""}#${event.sequence ?? ""}`;
      if (state.activeRun.seen.has(key)) continue;
      state.activeRun.seen.add(key);
      renderEvent(state.activeRun.panel, event);
    }
    if (result.approval) {
      state.pendingApproval = result.approval;
      ui.approvalDescription.textContent = `${result.approval.tool_name}: ${result.approval.arguments}`;
      ui.approvalPanel.hidden = false;
    } else {
      state.pendingApproval = null;
    }
  } catch (error) {
    assistant.rawText = `审批处理失败：${error.message}`;
    renderAssistant(assistant, assistant.rawText, false);
  } finally {
    finishGeneration();
  }
}

function roleOf(message) {
  const role = String(message.role || "").toLowerCase();
  if (role.includes("assistant")) return "assistant";
  if (role.includes("user")) return "user";
  return "";
}

// replayExecution 按 run 的 assistant_sequence 把历史事件挂回对应助手消息。
function replayExecution(model, snapshot) {
  const grouped = new Map();
  for (const event of snapshot?.events || []) {
    if (event.type === "chunk") continue;
    const list = grouped.get(event.run_id) || [];
    list.push(event);
    grouped.set(event.run_id, list);
  }
  for (const run of snapshot?.runs || []) {
    if (run.assistant_sequence === null || run.assistant_sequence === undefined) continue;
    const node = model[run.assistant_sequence];
    if (!node?.panel) continue;
    for (const event of grouped.get(run.run_id) || []) renderEvent(node.panel, event);
  }
}

async function openSession(id) {
  if (state.generating) return showToast("请先停止当前回答");
  try {
    const [messages, snapshot] = await Promise.all([
      api(`/sessions/${encodeURIComponent(id)}`),
      api(`/sessions/${encodeURIComponent(id)}/execution`).catch(() => null),
    ]);
    state.sessionId = id;
    state.pendingApproval = null;
    state.activeRun = null;
    ui.approvalPanel.hidden = true;
    ui.messages.replaceChildren();
    ui.conversation.classList.remove("has-messages");
    const model = [];
    for (const message of messages || []) {
      const role = roleOf(message);
      const content = String(message.content || "");
      const reasoning = String(message.reasoning_content || "");
      const visible = role === "assistant" ? content || reasoning : content;
      model.push(role && visible ? addMessage(role, content, false, reasoning) : null);
    }
    replayExecution(model, snapshot);
    ui.welcome.hidden = ui.messages.children.length > 0;
    ui.conversationState.textContent = firstUserText(messages) || "历史对话";
    highlightSession();
    closeSidebar();
    scrollToBottom();
  } catch (error) {
    showToast(error.message);
  }
}

function firstUserText(messages) {
  const item = (messages || []).find((message) => roleOf(message) === "user" && message.content);
  return item?.content?.replace(/\s+/g, " ").slice(0, 42) || "";
}

async function deleteSession(id) {
  try {
    await api(`/sessions/${encodeURIComponent(id)}`, { method: "DELETE" });
    if (id === state.sessionId) startNewChat();
    await loadSessions();
  } catch (error) {
    showToast(error.message);
  }
}

function requestDelete(id) {
  if (!id || state.generating) return;
  // 尚未产生消息的新会话没有持久化文件，直接回到一个新的空会话即可。
  if (id === state.sessionId && ui.messages.children.length === 0) {
    startNewChat();
    return;
  }
  state.deleteTarget = id;
  ui.deleteDialog.showModal();
}

function highlightSession() {
  document.querySelectorAll(".session-row").forEach((row) => row.classList.toggle("active", row.dataset.id === state.sessionId));
}

async function loadSessions() {
  try {
    const sessions = await api("/sessions");
    ui.sessionList.replaceChildren();
    if (!sessions?.length) {
      const empty = document.createElement("div");
      empty.className = "session-empty";
      empty.textContent = "暂无历史对话";
      ui.sessionList.append(empty);
      return;
    }
    for (const session of sessions) {
      const row = document.createElement("div");
      row.className = "session-row";
      row.dataset.id = session.id;
      const open = document.createElement("button");
      open.type = "button";
      open.className = "session-open";
      open.textContent = session.id;
      open.title = session.id;
      open.onclick = () => openSession(session.id);
      const remove = document.createElement("button");
      remove.type = "button";
      remove.className = "icon-button session-delete";
      remove.textContent = "×";
      remove.title = "删除会话";
      remove.setAttribute("aria-label", `删除会话 ${session.id}`);
      remove.onclick = (event) => { event.stopPropagation(); requestDelete(session.id); };
      row.append(open, remove);
      ui.sessionList.append(row);
      api(`/sessions/${encodeURIComponent(session.id)}`).then((messages) => {
        const title = firstUserText(messages);
        if (title) { open.textContent = title; open.title = title; }
      }).catch(() => {});
    }
    highlightSession();
  } catch (error) {
    showToast(`会话加载失败：${error.message}`);
  }
}

// 技能名与技能目录属于内部实现：只有后端处于 debug 模式时，
// 才显示技能选择器并渲染技能相关事件，普通用户面不出现任何技能入口。
function applyDebugMode(enabled) {
  if (state.debug === enabled) return;
  state.debug = enabled;
  ui.skillControl.hidden = !enabled;
  if (enabled) loadSkills();
}

async function loadSkills() {
  if (ui.skillSelect.options.length > 1) return;
  try {
    const skills = await api("/skills");
    for (const skill of skills || []) {
      const option = document.createElement("option");
      option.value = skill;
      option.textContent = skill.replaceAll("_", " ");
      ui.skillSelect.append(option);
    }
  } catch (error) {
    // 非调试模式下该接口未注册，属预期结果，不必打扰用户。
    if (state.debug) showToast(`Skill 加载失败：${error.message}`);
  }
}

function openSidebar() { document.body.classList.add("sidebar-open"); }
function closeSidebar() { document.body.classList.remove("sidebar-open"); }

ui.composer.addEventListener("submit", (event) => {
  event.preventDefault();
  if (state.generating) {
    stopGeneration();
    return;
  }
  sendMessage(ui.promptInput.value.trim());
});
ui.promptInput.addEventListener("input", resizeInput);
ui.promptInput.addEventListener("keydown", (event) => {
  if (event.key === "Enter" && !event.shiftKey && !event.isComposing) {
    event.preventDefault();
    ui.composer.requestSubmit();
  }
});
ui.newChat.onclick = startNewChat;
ui.clearChat.onclick = () => requestDelete(state.sessionId);
ui.openSidebar.onclick = openSidebar;
ui.closeSidebar.onclick = closeSidebar;
ui.sidebarScrim.onclick = closeSidebar;
ui.approveAction.onclick = () => submitApproval(true);
ui.rejectApproval.onclick = () => submitApproval(false);
ui.deleteDialog.addEventListener("close", () => {
  if (ui.deleteDialog.returnValue === "confirm" && state.deleteTarget) deleteSession(state.deleteTarget);
  state.deleteTarget = "";
});
document.querySelectorAll("[data-prompt]").forEach((button) => button.onclick = () => sendMessage(button.dataset.prompt));

async function bootstrap() {
  startNewChat();
  try {
    const health = await api("/health");
    setHealth(true);
    // 技能入口的显隐完全由后端 debug 状态决定，前端不做任何默认开启。
    applyDebugMode(health?.debug === true);
  } catch {
    setHealth(false);
  }
  await loadSessions();
}

// 流式输出期间允许用户自由滚动：只有距离底部较近时才继续自动跟随。
ui.conversation.addEventListener("scroll", () => {
  const distance = ui.conversation.scrollHeight - ui.conversation.clientHeight - ui.conversation.scrollTop;
  state.followOutput = distance <= 80;
});

bootstrap();
