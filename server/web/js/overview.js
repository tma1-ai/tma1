var overviewVersion = 0;
var AGENT_LABELS = {
  claude_code: "Claude Code",
  codex: "Codex",
  copilot_cli: "Copilot CLI",
};
var AGENT_COLORS = {
  claude_code: "#de9876",
  codex: "#72b7a4",
  copilot_cli: "#a798d6",
};

function overviewTotals(data) {
  return data.reduce(
    function (total, row) {
      total.sessions += Number(row.sessions) || 0;
      total.tools += Number(row.tools) || 0;
      total.failures += Number(row.failures) || 0;
      total.active += Number(row.active) || 0;
      return total;
    },
    { sessions: 0, tools: 0, failures: 0, active: 0 },
  );
}

function overviewActivity(data) {
  var buckets = new Map();
  data.forEach(function (row) {
    var ts = tsToMs(row.hour);
    if (!Number.isFinite(ts)) return;
    if (!buckets.has(ts)) buckets.set(ts, {});
    buckets.get(ts)[row.agent_source] = Number(row.events) || 0;
  });
  var entries = Array.from(buckets.entries()).sort(function (a, b) {
    return a[0] - b[0];
  });
  if (!entries.length)
    return (
      '<div class="overview-empty">' + t("overview.no_activity") + "</div>"
    );
  var bucketMs =
    currentTimeRange === "30d" || currentTimeRange === "60d"
      ? 86400000
      : 3600000;
  var first = entries[0][0],
    last = entries[entries.length - 1][0];
  entries = [];
  for (var time = first; time <= last; time += bucketMs)
    entries.push([time, buckets.get(time) || {}]);
  var peak = Math.max.apply(
    null,
    entries.map(function (e) {
      return Object.values(e[1]).reduce(function (a, b) {
        return a + b;
      }, 0);
    }),
  );
  var width = 920,
    height = 190;
  var step = width / entries.length;
  var svg =
    '<svg viewBox="0 0 ' +
    width +
    " " +
    (height + 28) +
    '" role="img" aria-label="' +
    t("overview.activity") +
    '">';
  [0, 0.5, 1].forEach(function (f) {
    svg +=
      '<line x1="0" x2="' +
      width +
      '" y1="' +
      (height - f * height) +
      '" y2="' +
      (height - f * height) +
      '" stroke="var(--border)" stroke-dasharray="3 5"/>';
  });
  entries.forEach(function (entry, i) {
    var y = height;
    Object.keys(AGENT_LABELS).forEach(function (source) {
      var count = entry[1][source] || 0;
      var h = peak ? (count / peak) * (height - 10) : 0;
      y -= h;
      if (h)
        svg +=
          '<rect x="' +
          (i * step + 2) +
          '" y="' +
          y +
          '" width="' +
          Math.max(1, step - 4) +
          '" height="' +
          h +
          '" rx="2" fill="' +
          AGENT_COLORS[source] +
          '"><title>' +
          escapeHTML(
            AGENT_LABELS[source] +
              " · " +
              new Date(entry[0]).toLocaleString() +
              " · " +
              count,
          ) +
          "</title></rect>";
    });
    if (
      i === 0 ||
      i === entries.length - 1 ||
      (entries.length > 5 && i === Math.floor(entries.length / 2))
    )
      svg +=
        '<text x="' +
        (i * step + step / 2) +
        '" y="' +
        (height + 23) +
        '" text-anchor="middle" fill="var(--text-muted)" font-size="11">' +
        escapeHTML(
          bucketMs === 86400000
            ? new Date(entry[0]).toLocaleDateString([], {
                month: "short",
                day: "numeric",
              })
            : new Date(entry[0]).toLocaleTimeString([], {
                hour: "2-digit",
                minute: "2-digit",
              }),
        ) +
        "</text>";
  });
  return svg + "</svg>";
}

async function overviewLoad() {
  var version = ++overviewVersion;
  var root = document.getElementById("overview-content");
  if (!dataSources.hasHookEvents) {
    root.innerHTML =
      '<div class="overview-empty">' + t("overview.connect") + "</div>";
    return;
  }
  var bucket =
    currentTimeRange === "30d" || currentTimeRange === "60d" ? "day" : "hour";
  var where =
    supportedAgentWhere() +
    " AND ts > NOW() - INTERVAL '" +
    intervalSQL() +
    "'";
  try {
    var result = await Promise.all([
      query(
        "SELECT agent_source, COUNT(DISTINCT session_id) AS sessions, SUM(CASE WHEN event_type='PreToolUse' THEN 1 ELSE 0 END) AS tools, SUM(CASE WHEN event_type='PostToolUseFailure' OR tool_success=false THEN 1 ELSE 0 END) AS failures, COUNT(DISTINCT CASE WHEN ts > NOW() - INTERVAL '5 minutes' THEN session_id ELSE NULL END) AS active FROM tma1_hook_events WHERE " +
          where +
          " GROUP BY agent_source",
      ),
      query(
        "SELECT DATE_TRUNC('" +
          bucket +
          "', ts) AS hour, agent_source, COUNT(*) AS events FROM tma1_hook_events WHERE " +
          where +
          " GROUP BY DATE_TRUNC('" +
          bucket +
          "', ts), agent_source ORDER BY hour LIMIT 4323",
      ),
      query(
        "SELECT tool_name, COUNT(*) AS completed, SUM(CASE WHEN event_type='PostToolUseFailure' OR tool_success=false THEN 1 ELSE 0 END) AS failures FROM tma1_hook_events WHERE " +
          where +
          " AND event_type IN ('PostToolUse','PostToolUseFailure') AND tool_name <> '' GROUP BY tool_name ORDER BY failures DESC, completed DESC LIMIT 8",
      ),
      query(
        "SELECT session_id, agent_source, MAX(cwd) AS cwd, MAX(ts) AS last_ts, SUM(CASE WHEN event_type='PreToolUse' THEN 1 ELSE 0 END) AS tools FROM tma1_hook_events WHERE " +
          where +
          " GROUP BY session_id, agent_source HAVING SUM(CASE WHEN event_type IN ('PreToolUse','UserPromptSubmit') THEN 1 ELSE 0 END)>0 ORDER BY last_ts DESC LIMIT 8",
      ),
    ]);
    if (version !== overviewVersion) return;
    var agents = rowsToObjects(result[0]),
      total = overviewTotals(agents),
      tools = rowsToObjects(result[2]),
      sessions = rowsToObjects(result[3]);
    var html = '<div class="overview-kpis">';
    [
      ["sessions", total.sessions],
      ["tools", total.tools],
      ["active", total.active],
      ["failures", total.failures],
    ].forEach(function (item, i) {
      html +=
        '<div class="overview-kpi"><div class="overview-kpi-label"><span>' +
        t("overview." + item[0]) +
        '</span><span class="kpi-index">0' +
        (i + 1) +
        '</span></div><div class="overview-kpi-value' +
        (item[0] === "failures" && item[1] ? " has-failures" : "") +
        '">' +
        fmtNum(item[1]) +
        '</div><div class="overview-kpi-caption">' +
        t(
          item[0] === "active"
            ? "overview.last_five"
            : "overview.selected_window",
        ) +
        "</div></div>";
    });
    html +=
      '</div><div class="overview-grid"><section class="overview-panel activity-panel"><div class="panel-heading"><div><span class="eyebrow">' +
      t("overview.telemetry") +
      "</span><h2>" +
      t("overview.activity") +
      '</h2></div><div class="agent-legend">';
    agents.forEach(function (a) {
      html +=
        '<span><i style="background:' +
        AGENT_COLORS[a.agent_source] +
        '"></i>' +
        escapeHTML(AGENT_LABELS[a.agent_source] || a.agent_source) +
        "</span>";
    });
    html +=
      '</div></div><div class="activity-chart">' +
      overviewActivity(rowsToObjects(result[1])) +
      '</div><p class="panel-caption">' +
      t("overview.activity_note") +
      "</p></section>";
    html +=
      '<section class="overview-panel agent-panel"><div class="panel-heading"><div><span class="eyebrow">' +
      t("overview.sources") +
      "</span><h2>" +
      t("overview.agent_mix") +
      '</h2></div></div><div class="agent-stack">';
    var maxTools = Math.max(
      1,
      ...agents.map(function (a) {
        return Number(a.tools) || 0;
      }),
    );
    agents.forEach(function (a) {
      html +=
        '<div class="agent-summary"><div class="agent-summary-title"><span><i style="background:' +
        AGENT_COLORS[a.agent_source] +
        '"></i>' +
        escapeHTML(AGENT_LABELS[a.agent_source] || a.agent_source) +
        "</span><strong>" +
        fmtNum(Number(a.tools)) +
        '</strong></div><div class="agent-meter"><span style="width:' +
        (100 * Number(a.tools)) / maxTools +
        "%;background:" +
        AGENT_COLORS[a.agent_source] +
        '"></span></div><p>' +
        fmtNum(Number(a.sessions)) +
        " " +
        t("overview.sessions") +
        " · " +
        fmtNum(Number(a.failures)) +
        " " +
        t("overview.failures") +
        "</p></div>";
    });
    html +=
      '</div></section><section class="overview-panel recent-panel"><div class="panel-heading"><div><span class="eyebrow">' +
      t("overview.investigate") +
      "</span><h2>" +
      t("overview.recent") +
      '</h2></div><button class="text-button" onclick="switchView(\'sessions\')">' +
      t("overview.all_sessions") +
      ' ↗</button></div><div class="recent-sessions">';
    sessions.forEach(function (s) {
      var name =
        (s.cwd || "").split(/[\\/]/).filter(Boolean).pop() ||
        s.session_id.slice(0, 8);
      html +=
        '<button class="recent-session" data-session="' +
        escapeHTML(s.session_id) +
        '" data-source="' +
        escapeHTML(s.agent_source) +
        '"><span class="session-glyph" style="color:' +
        AGENT_COLORS[s.agent_source] +
        '">↗</span><span class="recent-session-name"><strong>' +
        escapeHTML(name) +
        "</strong><span>" +
        escapeHTML(AGENT_LABELS[s.agent_source] || s.agent_source) +
        ' <span class="mono">' +
        escapeHTML(s.session_id.slice(0, 8)) +
        '</span></span></span><span class="recent-session-meta">' +
        fmtNum(Number(s.tools)) +
        " " +
        t("overview.tools") +
        "<span>" +
        escapeHTML(
          new Date(tsToMs(s.last_ts)).toLocaleTimeString([], {
            hour: "2-digit",
            minute: "2-digit",
          }),
        ) +
        "</span></span></button>";
    });
    if (!sessions.length)
      html +=
        '<div class="overview-empty">' + t("overview.no_activity") + "</div>";
    html +=
      '</div></section><section class="overview-panel tool-panel"><div class="panel-heading"><div><span class="eyebrow">' +
      t("overview.diagnostics") +
      "</span><h2>" +
      t("overview.tool_health") +
      '</h2></div></div><div class="tool-health-list">';
    tools.forEach(function (tool) {
      var completed = Number(tool.completed) || 0,
        failures = Number(tool.failures) || 0;
      html +=
        '<div class="tool-health-row"><code title="' +
        escapeHTML(tool.tool_name) +
        '">' +
        escapeHTML(tool.tool_name) +
        "</code><span>" +
        fmtNum(completed) +
        '</span><strong class="' +
        (failures ? "has-failures" : "tool-ok") +
        '">' +
        (failures
          ? fmtNum(failures) + " " + t("overview.failed")
          : t("overview.no_failures")) +
        "</strong></div>";
    });
    if (!tools.length)
      html +=
        '<div class="overview-empty">' + t("overview.no_tools") + "</div>";
    html +=
      '</div><p class="panel-caption">' +
      t("overview.tool_note") +
      "</p></section></div>";
    root.innerHTML = html;
    root.querySelectorAll(".recent-session").forEach(function (button) {
      button.addEventListener("click", async function () {
        await switchView("sessions");
        sess_openDetail(button.dataset.session, button.dataset.source);
      });
    });
  } catch (error) {
    if (version === overviewVersion)
      root.innerHTML =
        '<div class="overview-error" role="alert">' +
        t("overview.error") +
        ": " +
        escapeHTML(error.message) +
        "</div>";
  }
}
