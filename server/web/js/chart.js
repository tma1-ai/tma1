// chart.js — uPlot chart helpers + theme color utilities
// Depends on: core.js (fmtNum, tsToMs)

var chartInstances = {};

var chartColors = ['#79c0ff', '#f0883e', '#3fb950', '#d2a8ff', '#f778ba', '#ffa657'];

function getThemeColors() {
  var cs = getComputedStyle(document.documentElement);
  return {
    axisStroke: cs.getPropertyValue('--text-dim').trim(),
    gridStroke: cs.getPropertyValue('--border').trim(),
    blue: cs.getPropertyValue('--blue').trim(),
    orange: cs.getPropertyValue('--orange').trim(),
    green: cs.getPropertyValue('--green').trim(),
    purple: cs.getPropertyValue('--purple').trim(),
    red: cs.getPropertyValue('--red').trim(),
    yellow: cs.getPropertyValue('--yellow').trim(),
    heatmap: [
      cs.getPropertyValue('--heatmap-0').trim(),
      cs.getPropertyValue('--heatmap-1').trim(),
      cs.getPropertyValue('--heatmap-2').trim(),
      cs.getPropertyValue('--heatmap-3').trim(),
      cs.getPropertyValue('--heatmap-4').trim(),
    ],
  };
}

function makeUPlotOpts(title, series, width, yFormatter) {
  var tc = getThemeColors();
  return {
    width: width,
    height: 220,
    cursor: { show: true },
    scales: { x: { time: true } },
    axes: [
      { stroke: tc.axisStroke, grid: { stroke: tc.gridStroke } },
      { stroke: tc.axisStroke, grid: { stroke: tc.gridStroke },
        values: function(u, vals) { return vals.map(function(v) { return yFormatter ? yFormatter(v) : String(v); }); } },
    ],
    series: series,
  };
}

function parseBucketSeconds(bucketStr) {
  var m = bucketStr.match(/^(\d+)\s+(minutes?|hours?)/);
  if (!m) return 300;
  var n = Number(m[1]);
  return m[2][0] === 'h' ? n * 3600 : n * 60;
}

function renderChart(containerId, data, seriesDefs, yFmt, onClickBucket) {
  var container = document.getElementById(containerId);
  container.innerHTML = '';
  if (onClickBucket) closeCostDrilldown();

  function doRender() {
    var baseWidth = container.clientWidth ||
      container.parentElement?.clientWidth ||
      container.closest('.chart-container')?.clientWidth ||
      0;
    var width = Math.max(baseWidth - 32, 320);
    var tc = getThemeColors();

    var timestamps = data.map(function(d) { return tsToMs(d.t) / 1000; });
    var uData = [timestamps];
    var uSeries = [{}];

    var colorMap = {
      '#79c0ff': tc.blue,
      '#f0883e': tc.orange,
      '#3fb950': tc.green,
      '#d2a8ff': tc.purple,
      '#f85149': tc.red,
    };

    seriesDefs.forEach(function(s) {
      uData.push(data.map(function(d) { return d[s.key] != null ? Number(d[s.key]) : null; }));
      var color = colorMap[s.color] || s.color;
      uSeries.push({
        label: s.label,
        stroke: color,
        width: 2,
        fill: color + '1a',
      });
    });

    var opts = makeUPlotOpts('', uSeries, width, yFmt);
    if (chartInstances[containerId]) {
      chartInstances[containerId].destroy();
    }
    try {
      chartInstances[containerId] = new uPlot(opts, uData, container);
    } catch (err) {
      console.error('chart render failed', containerId, err);
      container.innerHTML = '<div class="chart-empty">' + t('error.render_chart') + '</div>';
      return;
    }

    if (onClickBucket) {
      container.style.cursor = 'pointer';
      var cc = container.closest('.chart-container');
      if (cc && !cc.querySelector('.drilldown-hint')) {
        var h3 = cc.querySelector('h3');
        if (h3) {
          var hint = document.createElement('span');
          hint.className = 'drilldown-hint';
          hint.textContent = t('drilldown.hint');
          h3.appendChild(hint);
        }
      }
      chartInstances[containerId].over.addEventListener('click', function(e) {
        var u = chartInstances[containerId];
        var left = e.clientX - u.over.getBoundingClientRect().left;
        var idx = u.valToIdx(u.posToVal(left, 'x'));
        if (idx == null || idx < 0 || idx >= uData[0].length) return;
        var tsSec = uData[0][idx];
        var bucketSec = parseBucketSeconds(chartBucket());
        onClickBucket(container, tsSec, bucketSec);
      });
    }
  }

  // Wait for container to be laid out before measuring width.
  // On tab/view switch the container may still be display:none.
  if (container.clientWidth > 100) {
    doRender();
  } else {
    var ro = new ResizeObserver(function() {
      if (container.clientWidth > 100) {
        ro.disconnect();
        doRender();
      }
    });
    ro.observe(container);
  }
}

// ===================================================================
// Shared Activity Heatmap (GitHub-style contribution graph)
// ===================================================================

function heatmapConfig() {
  var config = {
    '1h':  { bucket: '5 minutes',  interval: '1 hour',   mode: 'strip',    range: '1h',  cols: 12, slotMs: 5 * 60 * 1000,  rangeMs: 60 * 60 * 1000 },
    '6h':  { bucket: '15 minutes', interval: '6 hours',  mode: 'strip',    range: '6h',  cols: 24, slotMs: 15 * 60 * 1000, rangeMs: 6 * 3600 * 1000 },
    '24h': { bucket: '1 hour',     interval: '1 day',    mode: 'strip',    range: '24h', cols: 24, slotMs: 3600 * 1000,    rangeMs: 24 * 3600 * 1000 },
    '7d':  { bucket: '1 hour',     interval: '7 days',   mode: 'calendar', range: '7d',  days: 7 },
    '30d': { bucket: '1 hour',     interval: '30 days',  mode: 'calendar', range: '30d', days: 30 },
    '60d': { bucket: '2 hours',    interval: '60 days',  mode: 'calendar', range: '60d', days: 60 },
  };
  return config[currentTimeRange] || config['24h'];
}

function ahCellColor(cnt, max, palette) {
  if (!cnt || max === 0) return palette[0];
  var ratio = cnt / max;
  if (ratio < 0.25) return palette[1];
  if (ratio < 0.50) return palette[2];
  if (ratio < 0.75) return palette[3];
  return palette[4];
}

function ahLocale() {
  return currentLocale === 'zh' ? 'zh-CN' : currentLocale === 'es' ? 'es' : 'en';
}

// Sunday-first for en, Monday-first for zh/es
function ahWeekStart() { return currentLocale === 'en' ? 0 : 1; }

function ahStartOfDay(ms) {
  var d = new Date(ms);
  return new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
}

function ahDayKey(ms) {
  var d = new Date(ms);
  return d.getFullYear() + '-' + (d.getMonth() + 1) + '-' + d.getDate();
}

function ahFormatDate(ms, locale) {
  return new Date(ms).toLocaleDateString(locale, { month: 'short', day: 'numeric' });
}

function ahLegendHTML(palette) {
  var html = '<div class="ah-legend">' + t('heatmap.less');
  palette.forEach(function(c) { html += '<div class="ah-legend-cell" style="background:' + c + '"></div>'; });
  html += t('heatmap.more') + '</div>';
  return html;
}

function ahSubtitleHTML(range, extra) {
  var s = t('activity.range.' + range);
  if (extra) s += ' \u00b7 ' + extra;
  return '<div class="ah-subtitle">' + s + '</div>';
}

function renderHeatmap(elementId, data) {
  var el = document.getElementById(elementId);
  if (!el) return;
  if (!data.length) {
    el.innerHTML = '<div class="chart-empty">' + t('empty.no_activity') + '</div>';
    return;
  }
  var cfg = heatmapConfig();
  if (cfg.mode === 'calendar') {
    renderCalendarHeatmap(el, data, cfg);
  } else {
    renderStripHeatmap(el, data, cfg);
  }
}

function renderCalendarHeatmap(el, data, cfg) {
  var palette = getThemeColors().heatmap;
  var locale = ahLocale();
  var weekStart = ahWeekStart();
  var days = cfg.days;
  var DAY_MS = 86400000;

  // Build day list (last N days, ending today, anchored to local midnight)
  var todayStart = ahStartOfDay(Date.now());
  var firstDay = todayStart - (days - 1) * DAY_MS;

  // Aggregate hourly buckets to local-day counts
  var counts = {};
  for (var i = 0; i < days; i++) {
    counts[ahDayKey(firstDay + i * DAY_MS)] = 0;
  }
  data.forEach(function(b) {
    var dStart = ahStartOfDay(tsToMs(b.t));
    var key = ahDayKey(dStart);
    if (!(key in counts)) return;
    counts[key] += Number(b.cnt) || 0;
  });

  // Stats
  var max = 0, total = 0, active = 0, peakDayKey = null, peakDayMs = 0, peakCount = 0;
  Object.keys(counts).forEach(function(k) {
    var c = counts[k];
    total += c;
    if (c > 0) active++;
    if (c > max) max = c;
    if (c > peakCount) { peakCount = c; peakDayKey = k; }
  });
  if (peakDayKey) {
    // Convert YYYY-M-D back to ms (local) for formatting
    var parts = peakDayKey.split('-');
    peakDayMs = new Date(Number(parts[0]), Number(parts[1]) - 1, Number(parts[2])).getTime();
  }

  // Traditional month-calendar layout: 7 cols (weekdays horizontal) × N rows (weeks).
  // 7d → 1 week row; 30d → ~5 rows; 60d → ~9 rows.
  var refSun = new Date(2024, 0, 7); // a known Sunday
  var dayNames = [];
  for (var wi = 0; wi < 7; wi++) {
    var wd = new Date(refSun.getTime() + ((wi + weekStart) % 7) * DAY_MS);
    dayNames.push(wd.toLocaleDateString(locale, { weekday: 'short' }));
  }

  var dowAligned = function(ms) {
    var dow = new Date(ms).getDay();
    return (dow - weekStart + 7) % 7;
  };
  var firstDow = dowAligned(firstDay);
  var totalCells = firstDow + days;
  var weeks = Math.ceil(totalCells / 7);

  var headerRow = '';
  for (var hi = 0; hi < 7; hi++) {
    headerRow += '<div class="ah-time-label" style="grid-column:' + (hi + 1) + ';grid-row:1;text-align:center">' + dayNames[hi] + '</div>';
  }

  var cellHtml = '';
  for (var weekIdx = 0; weekIdx < weeks; weekIdx++) {
    for (var dayIdx = 0; dayIdx < 7; dayIdx++) {
      var dayOffset = weekIdx * 7 + dayIdx - firstDow;
      var gridCol = dayIdx + 1;
      var gridRow = weekIdx + 2; // header is row 1
      if (dayOffset < 0 || dayOffset >= days) {
        cellHtml += '<div class="ah-cell empty" style="grid-column:' + gridCol + ';grid-row:' + gridRow + '"></div>';
        continue;
      }
      var ddMs = firstDay + dayOffset * DAY_MS;
      var ddCnt = counts[ahDayKey(ddMs)] || 0;
      var ddTip = ahFormatDate(ddMs, locale) + ' \u2014 ' + fmtNum(ddCnt);
      cellHtml += '<div class="ah-cell" style="background:' + ahCellColor(ddCnt, max, palette) +
        ';grid-column:' + gridCol + ';grid-row:' + gridRow + '" title="' + ddTip + '"></div>';
    }
  }

  var grid = '<div class="ah-grid" style="grid-template-columns:repeat(7, 22px);grid-template-rows:auto repeat(' + weeks + ', 22px)">' +
    headerRow + cellHtml + '</div>';

  // Date range string for subtitle (e.g., "3/31 - 4/29" or "Mar 31 - Apr 29")
  var rangeStr = ahFormatDate(firstDay, locale) + ' \u2013 ' + ahFormatDate(todayStart, locale);

  var peakStr = peakDayKey
    ? ahFormatDate(peakDayMs, locale) + ' (' + fmtNum(peakCount) + ')'
    : '\u2014';
  var summary = '<div class="ah-summary">' +
    '<span class="ah-stat-num">' + fmtNum(total) + '</span> ' + t('activity.events') +
    '<span class="ah-sep">\u00b7</span>' +
    '<span class="ah-stat-num">' + active + ' / ' + days + '</span> ' + t('activity.active_days') +
    '<span class="ah-sep">\u00b7</span>' +
    t('activity.peak_day') + ' <span class="ah-stat-num">' + peakStr + '</span>' +
    '</div>';

  el.innerHTML = '<div class="activity-heatmap ah-calendar">' +
    summary +
    ahSubtitleHTML(cfg.range, rangeStr) +
    grid +
    ahLegendHTML(palette) +
    '</div>';
}

function renderStripHeatmap(el, data, cfg) {
  var palette = getThemeColors().heatmap;
  var slots = cfg.cols;
  var slotMs = cfg.slotMs;
  var rangeMs = cfg.rangeMs;

  var now = Date.now();
  var rangeStart = now - rangeMs;
  var counts = new Array(slots);
  for (var z = 0; z < slots; z++) counts[z] = 0;

  data.forEach(function(b) {
    var ts = tsToMs(b.t);
    var idx = Math.floor((ts - rangeStart) / slotMs);
    if (idx < 0 || idx >= slots) return;
    counts[idx] += Number(b.cnt) || 0;
  });

  var max = 0, total = 0, active = 0, peakIdx = -1, peakCount = 0;
  for (var i = 0; i < slots; i++) {
    var c = counts[i];
    total += c;
    if (c > 0) active++;
    if (c > max) max = c;
    if (c > peakCount) { peakCount = c; peakIdx = i; }
  }

  var labelEvery = cfg.range === '24h' ? 3 : cfg.range === '6h' ? 4 : 2;

  function slotLabel(idx) {
    var dt = new Date(rangeStart + idx * slotMs);
    if (cfg.range === '24h') return dt.getHours() + 'h';
    return dt.getHours() + ':' + String(dt.getMinutes()).padStart(2, '0');
  }

  var labelRow = '';
  for (var li = 0; li < slots; li++) {
    labelRow += '<div class="ah-time-label" style="grid-column:' + (li + 1) + ';grid-row:1;text-align:center">' +
      (li % labelEvery === 0 ? slotLabel(li) : '') + '</div>';
  }

  var cellRow = '';
  for (var ci = 0; ci < slots; ci++) {
    var tip = slotLabel(ci) + ' \u2014 ' + fmtNum(counts[ci]);
    cellRow += '<div class="ah-cell" style="background:' + ahCellColor(counts[ci], max, palette) +
      ';grid-column:' + (ci + 1) + ';grid-row:2" title="' + tip + '"></div>';
  }

  var grid = '<div class="ah-grid" style="grid-template-columns:repeat(' + slots + ', 22px);grid-template-rows:auto 22px;align-items:center">' +
    labelRow + cellRow + '</div>';

  var activeWord = cfg.range === '24h' ? t('activity.active_hours') : t('activity.active_slots');
  var peakWord = cfg.range === '24h' ? t('activity.peak_hour') : t('activity.peak_slot');
  var peakValue = peakIdx >= 0 ? slotLabel(peakIdx) + ' (' + fmtNum(peakCount) + ')' : '\u2014';
  var summary = '<div class="ah-summary">' +
    '<span class="ah-stat-num">' + fmtNum(total) + '</span> ' + t('activity.events') +
    '<span class="ah-sep">\u00b7</span>' +
    '<span class="ah-stat-num">' + active + ' / ' + slots + '</span> ' + activeWord +
    '<span class="ah-sep">\u00b7</span>' +
    peakWord + ' <span class="ah-stat-num">' + peakValue + '</span>' +
    '</div>';

  el.innerHTML = '<div class="activity-heatmap ah-strip">' +
    summary +
    ahSubtitleHTML(cfg.range) +
    grid +
    ahLegendHTML(palette) +
    '</div>';
}
