"use strict";
const $ = (id) => document.getElementById(id);
const state = {
  config: null,
  scenarios: [],
  decision: null,
  session: null,
  trace: null,
  tab: "flow",
  busy: false,
  pending: new Set(),
  history: [],
};
const labels = {
  recommended: "可推荐",
  no_candidates: "没有候选",
  constraint_conflict: "约束冲突",
  insufficient_information: "信息不足",
};
const names = {
  prepare_session: "准备会话",
  build_context: "组装候选与记忆",
  evaluate_candidates: "检查硬约束",
  check_facts: "核对人工事实",
  generate_result: "生成推荐",
  validate_result: "校验推荐",
  commit: "保存结果",
  "decision.generate": "决策流程",
  "ollama.generate": "Ollama 请求与响应",
};
function node(tag, text, cls) {
  const n = document.createElement(tag);
  if (text !== undefined) n.textContent = text;
  if (cls) n.className = cls;
  return n;
}
function json(v) {
  return node("pre", JSON.stringify(v, null, 2));
}
function lines(id) {
  return $(id)
    .value.split("\n")
    .map((x) => x.trim())
    .filter(Boolean);
}
function select(el, values, value) {
  el.replaceChildren(
    ...values.map((v) => {
      const o = node("option", v.label ?? v);
      o.value = v.value ?? v;
      return o;
    }),
  );
  if (value !== undefined) {
    el.value = value;
    if (el.selectedIndex < 0 && el.options.length) el.selectedIndex = 0;
  }
}
async function api(path, body) {
  const r = await fetch("/api/" + path, {
    method: body === undefined ? "GET" : "POST",
    headers: body === undefined ? {} : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const data = await r.json();
  if (!r.ok) {
    const e = new Error(data.error || "请求失败");
    e.data = data;
    throw e;
  }
  return data;
}
function error(text) {
  $("error").textContent = text;
  $("error").hidden = !text;
}
function live() {
  return state.session && Date.parse(state.session.expiresAt) > Date.now();
}
function controls() {
  const d = state.decision,
    s = state.session;
  document
    .querySelectorAll("#form input,#form textarea,#form select,#form button")
    .forEach((el) => (el.disabled = state.busy));
  $("model").disabled = state.busy || $("provider").value !== "ollama";
  $("reload").disabled = state.busy || !state.config?.fixturePath || !d;
  $("batch").disabled =
    state.busy || !d || !live() || s.latestDecisionId !== d.id;
  $("retry").disabled = state.busy || !d || !live() || !state.pending.has(s.id);
  $("actor").disabled = state.busy;
  document
    .querySelectorAll(".option-actions button")
    .forEach((el) => (el.disabled = state.busy || !live()));
  $("download").disabled = !state.trace;
  document
    .querySelectorAll("#history button,#refresh,#history-actor")
    .forEach((el) => (el.disabled = state.busy));
}
async function operate(title, path, body) {
  if (state.busy) return;
  state.busy = true;
  error("");
  controls();
  const start = Date.now();
  const tick = () => {
    $("status").textContent =
      title + " · " + ((Date.now() - start) / 1000).toFixed(1) + " 秒";
  };
  tick();
  $("status").classList.add("busy");
  const timer = setInterval(tick, 100);
  try {
    const data = await api(path, body);
    if (data.trace) {
      state.trace = data.trace;
      renderTrace();
    }
    if (data.decision) {
      state.decision = data.decision;
      state.session = data.session;
      renderDecision();
    }
    $("status").textContent =
      title + "完成 · " + ((Date.now() - start) / 1000).toFixed(1) + " 秒";
    if (path.endsWith("/batch") || path.endsWith("/retry"))
      state.pending.delete(state.session.id);
    if (path === "fixture/reload") {
      state.pending.clear();
      await loadConfig();
      state.scenarios = await api("scenarios");
      select(
        $("scenario"),
        state.scenarios.map((s) => ({ value: s.name, label: s.name })),
        $("scenario").value,
      );
      spaceChanged($("requester").value);
    }
    await loadHistory();
  } catch (e) {
    if (e.data?.trace) {
      state.trace = e.data.trace;
      renderTrace();
    }
    if (path.endsWith("/batch")) state.pending.add(state.session.id);
    error(e.message);
    $("status").textContent = title + "失败，上一轮结果保留。";
  } finally {
    clearInterval(timer);
    state.busy = false;
    $("status").classList.remove("busy");
    controls();
  }
}
function spaceChanged(requester) {
  const ids = state.config.members
    .filter((m) => m.spaceId === $("space").value)
    .map((m) => m.userId);
  select($("requester"), ids, ids.includes(requester) ? requester : ids[0]);
  $("participants").textContent = "实际参与人：" + ids.join("、");
}
function applyScenario() {
  const sc = state.scenarios.find((s) => s.name === $("scenario").value);
  if (!sc) return;
  $("description").textContent =
    sc.description + " · 预期：" + (labels[sc.expected] || sc.expected);
  const r = sc.request;
  $("space").value = r.spaceId;
  spaceChanged(r.requesterId);
  $("query").value = r.query;
  $("budget").value =
    r.conditions.budgetMaxCents == null
      ? ""
      : r.conditions.budgetMaxCents / 100;
  $("duration").value = r.conditions.durationMaxMinutes ?? "";
  $("hard").value = (r.conditions.hardConstraints || []).join("\n");
  $("soft").value = (r.conditions.softPreferences || []).join("\n");
  document
    .querySelectorAll("[name=type]")
    .forEach(
      (el) => (el.checked = (r.conditions.noteTypes || []).includes(el.value)),
    );
}
function request() {
  return {
    spaceId: $("space").value,
    requesterId: $("requester").value,
    task: "select",
    query: $("query").value,
    conditions: {
      noteTypes: [...document.querySelectorAll("[name=type]:checked")].map(
        (el) => el.value,
      ),
      budgetMaxCents:
        $("budget").value === ""
          ? null
          : Math.round(Number($("budget").value) * 100),
      durationMaxMinutes:
        $("duration").value === "" ? null : Number($("duration").value),
      hardConstraints: lines("hard"),
      softPreferences: lines("soft"),
    },
  };
}
function renderDecision() {
  const d = state.decision,
    s = state.session;
  if (!d) return;
  $("operations").hidden = false;
  $("outcome").textContent = labels[d.result.outcome] || d.result.outcome;
  const box = $("decision");
  box.replaceChildren();
  const meta = node("div", undefined, "meta");
  meta.append(
    node("strong", d.query),
    node("div", "参与人：" + d.participantIds.join("、")),
    node("div", "Decision: " + d.id),
    node("div", "Session: " + d.sessionId),
  );
  const c = d.conditions;
  meta.append(
    node(
      "div",
      "生效条件：预算 " +
        (c.budgetMaxCents == null ? "不限" : c.budgetMaxCents / 100 + " 元") +
        " / 时长 " +
        (c.durationMaxMinutes == null
          ? "不限"
          : c.durationMaxMinutes + " 分钟") +
        " / " +
        (c.hardConstraints || []).join("、"),
    ),
  );
  box.append(meta, node("p", d.result.explanation, "hint"));
  select(
    $("actor"),
    d.participantIds,
    d.participantIds.includes($("actor").value)
      ? $("actor").value
      : d.requesterId,
  );
  d.result.options.forEach((o, i) => {
    const adopted =
        s.adoptedOption?.optionId === o.optionId &&
        s.adoptedOption?.decisionId === d.id,
      rejected = (s.excludedNoteIds || []).includes(o.selection.noteId);
    const card = node(
        "article",
        undefined,
        "option" + (adopted ? " selected" : "") + (rejected ? " rejected" : ""),
      ),
      heading = node("h2");
    heading.append(
      node("span", String(i + 1).padStart(2, "0"), "number"),
      node("span", o.title),
    );
    if (adopted || rejected)
      heading.append(node("span", adopted ? "已采纳" : "已排除", "badge"));
    card.append(
      heading,
      node("p", o.reason),
      node("div", o.selection.noteId, "hint"),
    );
    for (const m of o.participantMatches) {
      const match = node("div", undefined, "match");
      match.append(node("b", m.userId), node("span", m.explanation));
      card.append(match);
    }
    if (o.unknowns?.length)
      card.append(node("p", "待确认：" + o.unknowns.join("、"), "hint"));
    const actions = node("div", undefined, "option-actions");
    for (const [action, title] of [
      ["adopt", "采纳"],
      ["reject", "拒绝"],
    ]) {
      const b = node("button", title, action);
      b.onclick = () =>
        operate(title, "decisions/" + d.id + "/feedback", {
          actorId: $("actor").value,
          eventId: crypto.randomUUID(),
          action,
          optionId: o.optionId,
        });
      actions.append(b);
    }
    card.append(actions);
    box.append(card);
  });
  const info = $("session");
  info.replaceChildren(
    node(
      "div",
      (live() ? "会话有效至 " : "会话已过期 · ") +
        new Date(s.expiresAt).toLocaleString(),
    ),
    node(
      "div",
      "已推荐 " +
        s.recommendedNoteIds.length +
        " 条 / 已排除 " +
        s.excludedNoteIds.length +
        " 条",
    ),
    node("div", "当前采纳：" + (s.adoptedOption?.noteId || "无")),
  );
  if (s.latestDecisionId !== d.id)
    info.append(node("div", "正在查看旧批次；换批需选择最新批次。"));
  controls();
}
function detail(title, value) {
  const d = node("details", undefined, "stage");
  d.append(node("summary", title), json(value));
  return d;
}
function renderTrace() {
  const t = state.trace;
  if (!t) return;
  const box = $("trace-content");
  box.replaceChildren();
  $("trace-summary").textContent =
    (t.provider === "mock" ? "Mock" : t.model) +
    " · " +
    t.durationMs.toFixed(1) +
    " ms · " +
    (t.error ? "失败" : "完成") +
    " · " +
    t.id.slice(0, 8);
  const find = (name) => t.stages.find((s) => s.name === name);
  if (state.tab === "raw") box.append(json(t));
  if (state.tab === "flow") {
    for (const s of t.stages) {
      const d = node(
          "details",
          undefined,
          "stage" + (s.error ? " failed" : ""),
        ),
        title = node("summary");
      title.append(
        node("span", s.durationMs.toFixed(1) + " ms"),
        node("strong", names[s.name] || s.name),
      );
      d.append(title);
      if (s.error) {
        d.open = true;
        d.append(node("p", s.error));
      }
      if (s.input !== undefined) d.append(node("h3", "输入"), json(s.input));
      if (s.output !== undefined) d.append(node("h3", "输出"), json(s.output));
      d.append(detail("运行属性", s.attributes));
      box.append(d);
    }
  }
  if (state.tab === "context") {
    const c = find("build_context")?.output,
      e = find("evaluate_candidates")?.output,
      pc = find("validate_result")?.input?.context;
    if (!c)
      box.append(
        node(
          "p",
          "此操作未组装推荐上下文。查看流程了解实际执行的步骤。",
          "hint",
        ),
      );
    else {
      const assessments = e?.Candidates || [],
        counts = { satisfied: 0, violated: 0, unknown: 0 };
      for (const a of assessments) counts[a.Status]++;
      const stats = node("div", undefined, "stats");
      for (const [key, label] of [
        ["satisfied", "满足"],
        ["violated", "违反"],
        ["unknown", "未知"],
      ])
        stats.append(node("span", label + " " + counts[key]));
      box.append(stats);
      if (e?.ConditionsConflict)
        box.append(node("p", "已确认条件存在冲突。", "hint"));
      const byID = new Map(c.candidates.map((n) => [n.id, n.title]));
      box.append(node("h3", "完整合法候选池 · " + c.candidates.length + " 条"));
      for (const a of assessments) {
        const row = node("div", undefined, "assessment");
        row.append(
          node("span", byID.get(a.NoteID) + " / " + a.NoteID),
          node(
            "span",
            { satisfied: "满足", violated: "违反", unknown: "未知" }[a.Status],
            a.Status,
          ),
        );
        box.append(row);
      }
      box.append(
        node(
          "p",
          "检查器返回满足、违反或未知的汇总结论；下方人工事实可用于核对费用、时长和食材等依据。",
          "hint",
        ),
      );
      if (pc)
        box.append(
          detail(
            "最终推荐候选 · " + pc.candidates.length + " 条",
            pc.candidates,
          ),
        );
      box.append(
        detail("实际生效条件", c.conditions),
        detail("合法候选的人工事实", find("check_facts")?.input?.facts ?? {}),
        detail("参与人的授权记忆", c.participants),
        detail("候选正文快照", c.candidates),
      );
      const session = find("prepare_session")?.output;
      if (session) box.append(detail("会话排除与已看记录", session));
      box.append(
        node(
          "p",
          "权限、类型和临时排除过滤发生在候选池构建前；这里仅展示已授权内容。",
          "hint",
        ),
      );
    }
  }
  if (state.tab === "model") {
    const llm = find("ollama.generate");
    if (!llm)
      box.append(
        node(
          "p",
          t.provider === "mock"
            ? "当前使用 Mock，没有模型请求。"
            : "本次操作未调用模型，或在调用前已失败。",
          "hint",
        ),
      );
    else {
      const req = llm.input;
      box.append(node("h3", "实际请求 · " + (req?.model || t.model)));
      for (const m of req?.messages || [])
        box.append(
          node("h3", m.role === "system" ? "系统 Prompt" : "用户 Prompt"),
          node("pre", m.content),
        );
      box.append(
        detail("Schema 与生成参数", req),
        node("h3", "原始响应"),
        json(llm.output ?? "未收到响应"),
      );
      if (llm.error) box.append(node("p", llm.error));
      box.append(detail("耗时与 Token 属性", llm.attributes));
    }
  }
  controls();
}
async function loadHistory() {
  state.history = await api(
    "history?actorId=" + encodeURIComponent($("history-actor").value),
  );
  const el = $("history");
  el.replaceChildren();
  if (!state.history.length) {
    el.append(node("p", "当前身份暂无可访问记录。", "hint"));
    return;
  }
  [...state.history].reverse().forEach((item) => {
    const d = item.decision,
      b = node(
        "button",
        undefined,
        state.decision?.id === d.id ? "active" : "",
      );
    b.append(
      node("div", d.query),
      node(
        "small",
        (labels[d.result.outcome] || d.result.outcome) +
          " · " +
          new Date(d.createdAt).toLocaleTimeString() +
          " · " +
          (item.expired
            ? "已过期"
            : item.session.latestDecisionId === d.id
              ? "最新批次"
              : "旧批次"),
      ),
    );
    b.onclick = async () => {
      if (state.busy) return;
      try {
        const trace = await api("traces/" + item.traceId);
        state.trace = trace;
        renderTrace();
        error("");
      } catch (e) {
        state.trace = null;
        $("trace-content").replaceChildren(node("p", e.message, "hint"));
        $("trace-summary").textContent = "历史 Trace 不可用";
      }
      state.decision = d;
      state.session = item.session;
      renderDecision();
      await loadHistory();
    };
    el.append(b);
  });
}
async function loadConfig() {
  state.config = await api("config");
  state.pending = new Set(Object.keys(state.config.pending ?? {}));
  const historyActor = $("history-actor").value;
  select(
    $("history-actor"),
    [...new Set(state.config.members.map((m) => m.userId))],
    historyActor,
  );
  const selected = $("space").value;
  select(
    $("space"),
    state.config.spaces.map((s) => ({ value: s.id, label: s.name })),
    selected,
  );
  $("fixture-path").textContent = state.config.fixturePath || "内置虚构资料";
  $("runtime").textContent =
    "模型超时 " +
    state.config.modelTimeout +
    " · 上下文 " +
    state.config.contextTokens +
    " tokens";
}
$("form").onsubmit = (e) => {
  e.preventDefault();
  const r = request();
  $("history-actor").value = r.requesterId;
  operate("生成推荐", "decisions", {
    request: r,
    provider: $("provider").value,
    model: $("model").value,
  });
};
$("scenario").onchange = applyScenario;
$("space").onchange = () => spaceChanged();
$("provider").onchange = controls;
$("batch").onclick = () =>
  operate("换一批", "sessions/" + state.session.id + "/batch", {
    decisionId: state.decision.id,
    actorId: $("actor").value,
    eventId: crypto.randomUUID(),
  });
$("retry").onclick = () =>
  operate("重试换批", "sessions/" + state.session.id + "/retry", {
    actorId: $("actor").value,
  });
$("reload").onclick = () =>
  operate("重载资料", "fixture/reload", {
    decisionId: state.decision.id,
    actorId: $("actor").value,
  });
$("refresh").onclick = () => loadHistory().catch((e) => error(e.message));
$("history-actor").onchange = $("refresh").onclick;
for (const b of document.querySelectorAll("[data-tab]"))
  b.onclick = () => {
    state.tab = b.dataset.tab;
    document.querySelectorAll("[data-tab]").forEach((el) => {
      el.classList.toggle("active", el === b);
      el.setAttribute("aria-selected", String(el === b));
    });
    renderTrace();
  };
$("download").onclick = () => {
  if (!state.trace) return;
  const a = node("a");
  a.href = "/api/traces/" + state.trace.id + "/download";
  a.download = "jetaime-trace-" + state.trace.id + ".json";
  document.body.append(a);
  a.click();
  a.remove();
};
async function init() {
  try {
    await loadConfig();
    state.scenarios = await api("scenarios");
    select(
      $("scenario"),
      state.scenarios.map((s) => ({ value: s.name, label: s.name })),
    );
    $("model").value = state.config.model;
    $("provider").value = state.config.provider;
    applyScenario();
    const data = await api("current");
    if (data.decision) {
      state.decision = data.decision;
      state.session = data.session;
      $("history-actor").value = data.decision.requesterId;
      renderDecision();
    }
    await loadHistory();
    controls();
  } catch (e) {
    error("初始化失败：" + e.message);
  }
}
init();
