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

function renderChart(containerId, data, seriesDefs, yFmt) {
  var container = document.getElementById(containerId);
  container.innerHTML = '';

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
// Shared Activity Heatmap
// ===================================================================

function heatmapConfig() {
  var config = {
    '1h':  { bucket: '5 minutes',  interval: '1 hour',  cols: 12, rowCount: 1 },
    '6h':  { bucket: '15 minutes', interval: '6 hours', cols: 24, rowCount: 1 },
    '24h': { bucket: '1 hour',     interval: '1 day',   cols: 24, rowCount: 1 },
    '7d':  { bucket: '1 hour',     interval: '7 days',  cols: 24, rowCount: 7 },
    '30d': { bucket: '1 hour',     interval: '30 days', cols: 24, rowCount: 30 },
  };
  return config[currentTimeRange] || config['24h'];
}

function renderHeatmap(elementId, data) {
  var el = document.getElementById(elementId);
  if (!el) return;
  if (!data.length) {
    el.innerHTML = '<div class="chart-empty">' + t('empty.no_activity') + '</div>';
    return;
  }

  var tc = getThemeColors();
  var heatColors = tc.heatmap;
  var counts = {};
  var maxCnt = 0;
  var now = new Date();
  var locale = currentLocale === 'zh' ? 'zh-CN' : currentLocale === 'es' ? 'es' : 'en';

  function cellColor(cnt) {
    if (!cnt) return heatColors[0];
    var ratio = cnt / maxCnt;
    if (ratio < 0.25) return heatColors[1];
    if (ratio < 0.50) return heatColors[2];
    if (ratio < 0.75) return heatColors[3];
    return heatColors[4];
  }

  var html;

  if (currentTimeRange === '7d' || currentTimeRange === '30d') {
    var numDays = currentTimeRange === '30d' ? 30 : 7;
    var rangeAgo = new Date(now.getTime() - numDays * 24 * 3600 * 1000);
    data.forEach(function(d) {
      var dt = new Date(tsToMs(d.t));
      var dayIdx = Math.floor((dt.getTime() - rangeAgo.getTime()) / (24 * 3600 * 1000));
      var hour = dt.getHours();
      var key = dayIdx + ':' + hour;
      var cnt = Number(d.cnt) || 0;
      counts[key] = (counts[key] || 0) + cnt;
      if (counts[key] > maxCnt) maxCnt = counts[key];
    });

    var dayLabels = [];
    for (var i = 0; i < numDays; i++) {
      var d = new Date(rangeAgo.getTime() + i * 24 * 3600 * 1000);
      dayLabels.push(currentTimeRange === '30d'
        ? (d.getMonth() + 1) + '/' + d.getDate()
        : d.toLocaleDateString(locale, { weekday: 'short' }));
    }

    html = '<div class="heatmap-grid" style="grid-template-columns:40px repeat(24, 1fr)">';
    html += '<div class="heatmap-label"></div>';
    for (var h = 0; h < 24; h++) {
      html += '<div class="heatmap-label" style="justify-content:center">' + (h % 3 === 0 ? h : '') + '</div>';
    }
    for (var day = 0; day < numDays; day++) {
      html += '<div class="heatmap-label">' + dayLabels[day] + '</div>';
      for (var h2 = 0; h2 < 24; h2++) {
        var cnt = counts[day + ':' + h2] || 0;
        html += '<div class="heatmap-cell" style="background:' + cellColor(cnt) + '" title="' + dayLabels[day] + ' ' + h2 + ':00 \u2014 ' + cnt + '"></div>';
      }
    }
    html += '</div>';

  } else if (currentTimeRange === '24h') {
    var dayStart = new Date(now.getTime() - 24 * 3600 * 1000);
    data.forEach(function(d) {
      var dt = new Date(tsToMs(d.t));
      var hour = Math.floor((dt.getTime() - dayStart.getTime()) / (3600 * 1000));
      if (hour < 0 || hour >= 24) return;
      var key = '0:' + hour;
      var cnt = Number(d.cnt) || 0;
      counts[key] = (counts[key] || 0) + cnt;
      if (counts[key] > maxCnt) maxCnt = counts[key];
    });

    html = '<div class="heatmap-grid" style="grid-template-columns:40px repeat(24, 1fr)">';
    html += '<div class="heatmap-label"></div>';
    for (var dh = 0; dh < 24; dh++) {
      var dhLabel = new Date(dayStart.getTime() + dh * 3600 * 1000);
      html += '<div class="heatmap-label" style="justify-content:center">' + (dh % 3 === 0 ? dhLabel.getHours() + 'h' : '') + '</div>';
    }
    html += '<div class="heatmap-label"></div>';
    for (var dh2 = 0; dh2 < 24; dh2++) {
      var dhCnt = counts['0:' + dh2] || 0;
      var dhLabel2 = new Date(dayStart.getTime() + dh2 * 3600 * 1000);
      html += '<div class="heatmap-cell" style="background:' + cellColor(dhCnt) + '" title="' + dhLabel2.getHours() + ':00 \u2014 ' + dhCnt + '"></div>';
    }
    html += '</div>';

  } else if (currentTimeRange === '6h') {
    var rangeStart6h = new Date(now.getTime() - 6 * 3600 * 1000);
    data.forEach(function(d) {
      var dt = new Date(tsToMs(d.t));
      var slot = Math.floor((dt.getTime() - rangeStart6h.getTime()) / (15 * 60 * 1000));
      if (slot < 0 || slot >= 24) return;
      var key = '0:' + slot;
      var cnt = Number(d.cnt) || 0;
      counts[key] = (counts[key] || 0) + cnt;
      if (counts[key] > maxCnt) maxCnt = counts[key];
    });

    html = '<div class="heatmap-grid" style="grid-template-columns:40px repeat(24, 1fr)">';
    html += '<div class="heatmap-label"></div>';
    for (var q = 0; q < 24; q++) {
      var qTime = new Date(rangeStart6h.getTime() + q * 15 * 60 * 1000);
      var qLabel = q % 4 === 0;
      html += '<div class="heatmap-label" style="justify-content:center;font-size:9px">' +
        (qLabel ? qTime.getHours() + ':' + String(qTime.getMinutes()).padStart(2, '0') : '') + '</div>';
    }
    html += '<div class="heatmap-label"></div>';
    for (var q2 = 0; q2 < 24; q2++) {
      var qCnt = counts['0:' + q2] || 0;
      var qTime2 = new Date(rangeStart6h.getTime() + q2 * 15 * 60 * 1000);
      html += '<div class="heatmap-cell" style="background:' + cellColor(qCnt) + '" title="' +
        qTime2.getHours() + ':' + String(qTime2.getMinutes()).padStart(2, '0') + ' \u2014 ' + qCnt + '"></div>';
    }
    html += '</div>';

  } else {
    var rangeStart1h = new Date(now.getTime() - 3600 * 1000);
    data.forEach(function(d) {
      var dt = new Date(tsToMs(d.t));
      var slot = Math.floor((dt.getTime() - rangeStart1h.getTime()) / (5 * 60 * 1000));
      if (slot < 0 || slot >= 12) return;
      var key = '0:' + slot;
      var cnt = Number(d.cnt) || 0;
      counts[key] = (counts[key] || 0) + cnt;
      if (counts[key] > maxCnt) maxCnt = counts[key];
    });

    html = '<div class="heatmap-grid" style="grid-template-columns:40px repeat(12, 1fr)">';
    html += '<div class="heatmap-label"></div>';
    for (var m = 0; m < 12; m++) {
      var mTime = new Date(rangeStart1h.getTime() + m * 5 * 60 * 1000);
      var mLabel = m % 2 === 0;
      html += '<div class="heatmap-label" style="justify-content:center;font-size:9px">' +
        (mLabel ? ':' + String(mTime.getMinutes()).padStart(2, '0') : '') + '</div>';
    }
    html += '<div class="heatmap-label"></div>';
    for (var m2 = 0; m2 < 12; m2++) {
      var mCnt = counts['0:' + m2] || 0;
      var mTime2 = new Date(rangeStart1h.getTime() + m2 * 5 * 60 * 1000);
      html += '<div class="heatmap-cell" style="background:' + cellColor(mCnt) + '" title="' +
        mTime2.getHours() + ':' + String(mTime2.getMinutes()).padStart(2, '0') + ' \u2014 ' + mCnt + '"></div>';
    }
    html += '</div>';
  }

  html += '<div class="heatmap-legend">' + t('heatmap.less') + ' ';
  heatColors.forEach(function(c) { html += '<div class="heatmap-legend-cell" style="background:' + c + '"></div>'; });
  html += ' ' + t('heatmap.more') + '</div>';
  el.innerHTML = html;
}
