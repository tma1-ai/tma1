/* Agent Canvas — real-time + replay agent orchestration animation. */
/* globals: query, rows, rowsToObjects, tsToMs, escapeSQLString, escapeHTML, t, sessTimelineData, sessCurrentStats, fmtCost, loadPricing, modelPricing */

var AgentCanvas = (function () {
  // ── Constants ────────────────────────────────────────────────
  var MAIN_R = 28, SUB_R = 20;
  var DAMPING = 0.92, CENTER_K = 0.005, CHARGE_K = 800;
  var PARTICLE_SPEED = 1.2;
  var BUBBLE_TTL = 4.0, MAX_BUBBLES = 4;
  var TOOL_W = 140, TOOL_H = 30;
  var GRID_SPACING = 30;
  var BG_COLOR = '#050510';

  // Holographic color palette.
  var STATE_COLORS = {
    idle: '#8b949e', thinking: '#cc88ff', tool_calling: '#66ccff',
    complete: '#66ffaa', error: '#ff5566',
  };
  var ROLE_COLORS = { user: '#ffbb44', assistant: '#66ccff', thinking: '#cc88ff' };

  // ── State ────────────────────────────────────────────────────
  var canvas, ctx, dpr;
  var agents = {}, edges = {}, toolCalls = {}, particles = [], bubbles = [];
  var selectedId = null, selectedToolId = null;
  var animFrame = null, lastTime = 0, globalTime = 0;
  var mode = null;

  // Camera (pan + zoom).
  var cam = { x: 0, y: 0, zoom: 1 };

  // Drag state.
  var drag = { active: false, target: null, startX: 0, startY: 0, isPan: false };

  // Session info.
  var canvasSessionId = '', canvasMode = '';

  // Live mode.
  var eventSource = null;

  // Cost ticker.
  var costTotal = 0, costFinal = 0, costPollTimer = null;

  // Replay mode.
  var replayEvents = [], replayIdx = 0, replayTimer = null;
  var replaySpeed = 1, replayPaused = false;
  var replayStartTs = 0, replayEndTs = 0, replayCurrentTs = 0;

  var agentToolCounts = {};

  // ── Scene Graph ──────────────────────────────────────────────

  function addAgent(id, label, isMain) {
    if (agents[id]) return agents[id];
    var cx = canvas.width / dpr / 2, cy = canvas.height / dpr / 2;
    agents[id] = {
      id: id, x: cx + (Math.random() - 0.5) * 120, y: cy + (Math.random() - 0.5) * 80,
      vx: 0, vy: 0, r: isMain ? MAIN_R : SUB_R,
      label: label || (isMain ? 'main' : id.slice(0, 8)),
      state: 'idle', breath: Math.random() * 6.28, isMain: isMain, pinned: false,
      opacity: 0, lastActive: globalTime,
    };
    agentToolCounts[id] = 0;
    return agents[id];
  }

  function addEdge(fromId, toId) {
    var key = fromId + '>' + toId;
    if (!edges[key]) edges[key] = { key: key, from: fromId, to: toId, active: true };
    return key;
  }

  function spawnParticle(edgeKey) {
    particles.push({ edge: edgeKey, t: 0, wobble: Math.random() * 6.28, trail: [] });
  }

  function addBubble(agentId, text, role) {
    if (text.length > 80) text = text.slice(0, 77) + '...';
    bubbles.push({ agentId: agentId, text: text, role: role, ttl: BUBBLE_TTL, opacity: 1 });
    if (bubbles.length > MAX_BUBBLES) bubbles.shift();
  }

  // ── Force Simulation ─────────────────────────────────────────

  function simulate(dt) {
    var cx = canvas.width / dpr / 2, cy = canvas.height / dpr / 2;
    var ids = Object.keys(agents);
    var i, j, a, b, dx, dy, distSq, force, fx, fy, dist, diff, nx, ny;

    for (i = 0; i < ids.length; i++) {
      a = agents[ids[i]];
      // Auto-complete idle subagents (handles missing SubagentStop, e.g. Codex).
      // Only 'thinking' state: agent finished tools but no SubagentStop arrived.
      if (!a.isMain && a.state === 'thinking' && globalTime - a.lastActive > 10) {
        a.state = 'complete';
      }
      // Fade out completed subagents, then remove.
      if (a.state === 'complete' && !a.isMain) {
        a.opacity -= dt * 0.8;
        if (a.opacity <= 0) { delete agents[ids[i]]; continue; }
      } else if (a.opacity < 1) {
        // Fade in.
        a.opacity = Math.min(1, a.opacity + dt * 3);
      }
      if (a.pinned) continue;
      a.vx += (cx - a.x) * CENTER_K;
      a.vy += (cy - a.y) * CENTER_K;
      for (j = i + 1; j < ids.length; j++) {
        b = agents[ids[j]];
        if (!b) continue;
        dx = b.x - a.x; dy = b.y - a.y;
        distSq = dx * dx + dy * dy + 1;
        force = CHARGE_K / distSq;
        fx = dx / Math.sqrt(distSq) * force;
        fy = dy / Math.sqrt(distSq) * force;
        if (!a.pinned) { a.vx -= fx; a.vy -= fy; }
        if (!b.pinned) { b.vx += fx; b.vy += fy; }
      }
    }

    for (var ek in edges) {
      var e = edges[ek], from = agents[e.from], to = agents[e.to];
      if (!from || !to) continue;
      dx = to.x - from.x; dy = to.y - from.y;
      dist = Math.sqrt(dx * dx + dy * dy) || 1;
      diff = (dist - (from.r + to.r) * 3) * 0.01;
      nx = dx / dist; ny = dy / dist;
      if (!from.pinned) { from.vx += nx * diff; from.vy += ny * diff; }
      if (!to.pinned) { to.vx -= nx * diff; to.vy -= ny * diff; }
    }

    for (i = 0; i < ids.length; i++) {
      a = agents[ids[i]];
      if (a.pinned) continue;
      a.vx *= DAMPING; a.vy *= DAMPING;
      a.x += a.vx; a.y += a.vy;
      a.breath += dt * 1.5;
    }
  }

  function updateParticles(dt) {
    for (var i = particles.length - 1; i >= 0; i--) {
      var p = particles[i];
      p.t += PARTICLE_SPEED * dt;
      p.wobble += dt * 3;
      var pos = particlePos(p);
      if (pos) { p.trail.push(pos); if (p.trail.length > 8) p.trail.shift(); }
      if (p.t >= 1) particles.splice(i, 1);
    }
  }

  function particlePos(p) {
    var e = edges[p.edge]; if (!e) return null;
    var f = agents[e.from], to = agents[e.to]; if (!f || !to) return null;
    var dx = to.x - f.x, dy = to.y - f.y, dist = Math.sqrt(dx * dx + dy * dy) || 1;
    var perpX = -dy / dist, perpY = dx / dist;
    var cpx = (f.x + to.x) / 2 + perpX * dist * 0.15;
    var cpy = (f.y + to.y) / 2 + perpY * dist * 0.15;
    var mt = 1 - p.t;
    var x = mt * mt * f.x + 2 * mt * p.t * cpx + p.t * p.t * to.x;
    var y = mt * mt * f.y + 2 * mt * p.t * cpy + p.t * p.t * to.y;
    var w = Math.sin(p.wobble) * 3 * Math.sin(p.t * Math.PI);
    return { x: x + perpX * w, y: y + perpY * w };
  }

  function updateBubbles(dt) {
    for (var i = bubbles.length - 1; i >= 0; i--) {
      bubbles[i].ttl -= dt;
      bubbles[i].opacity = Math.min(1, bubbles[i].ttl / 0.5);
      if (bubbles[i].ttl <= 0) bubbles.splice(i, 1);
    }
  }

  // Tool fade-out and stale-running cleanup.
  function updateToolFades(dt) {
    for (var tid in toolCalls) {
      var tc = toolCalls[tid];
      if (tc.state === 'done' || tc.state === 'error') {
        tc.fadeOut = (tc.fadeOut || 0) + dt;
        if (tc.fadeOut > 0.6) delete toolCalls[tid];
      } else if (tc.state === 'running') {
        // Auto-expire running tools after 30s (handles rejected/interrupted tool calls).
        tc.age = (tc.age || 0) + dt;
        if (tc.age > 30) { tc.state = 'done'; }
      }
    }
  }

  // ── Rendering ────────────────────────────────────────────────

  function render() {
    var w = canvas.width / dpr, h = canvas.height / dpr;

    // C1: Deep space background.
    ctx.fillStyle = BG_COLOR;
    ctx.fillRect(0, 0, w, h);

    // Subtle center vignette.
    var vg = ctx.createRadialGradient(w / 2, h / 2, 0, w / 2, h / 2, w * 0.6);
    vg.addColorStop(0, 'rgba(20,30,60,0.15)');
    vg.addColorStop(1, 'rgba(0,0,0,0)');
    ctx.fillStyle = vg;
    ctx.fillRect(0, 0, w, h);

    // Dot grid (batched into single path for performance).
    ctx.fillStyle = 'rgba(100,200,255,0.04)';
    ctx.beginPath();
    var gs = GRID_SPACING;
    for (var gx = gs; gx < w; gx += gs) {
      for (var gy = gs; gy < h; gy += gs) {
        ctx.rect(gx, gy, 1, 1);
      }
    }
    ctx.fill();

    // Apply camera transform.
    ctx.save();
    ctx.translate(w / 2, h / 2);
    ctx.scale(cam.zoom, cam.zoom);
    ctx.translate(-w / 2 + cam.x, -h / 2 + cam.y);

    // C6: Edges with glow.
    for (var ek in edges) {
      var e = edges[ek], f = agents[e.from], to = agents[e.to];
      if (!f || !to) continue;
      var dx = to.x - f.x, dy = to.y - f.y, dist = Math.sqrt(dx * dx + dy * dy) || 1;
      var perpX = -dy / dist, perpY = dx / dist;
      var cpx = (f.x + to.x) / 2 + perpX * dist * 0.15;
      var cpy = (f.y + to.y) / 2 + perpY * dist * 0.15;
      if (e.active) {
        // Glow layer.
        ctx.beginPath(); ctx.moveTo(f.x, f.y); ctx.quadraticCurveTo(cpx, cpy, to.x, to.y);
        ctx.strokeStyle = 'rgba(102,204,255,0.12)'; ctx.lineWidth = 6; ctx.stroke();
        // Core.
        ctx.beginPath(); ctx.moveTo(f.x, f.y); ctx.quadraticCurveTo(cpx, cpy, to.x, to.y);
        ctx.strokeStyle = 'rgba(102,204,255,0.4)'; ctx.lineWidth = 1.5; ctx.stroke();
      } else {
        ctx.beginPath(); ctx.moveTo(f.x, f.y); ctx.quadraticCurveTo(cpx, cpy, to.x, to.y);
        ctx.setLineDash([4, 4]);
        ctx.strokeStyle = 'rgba(139,148,158,0.15)'; ctx.lineWidth = 1; ctx.stroke();
        ctx.setLineDash([]);
      }
    }

    // C3: Particles with glow.
    ctx.save();
    ctx.shadowColor = '#66ccff';
    ctx.shadowBlur = 8;
    for (var pi = 0; pi < particles.length; pi++) {
      var pos = particlePos(particles[pi]); if (!pos) continue;
      var trail = particles[pi].trail;
      ctx.shadowBlur = 0;
      for (var ti = 0; ti < trail.length; ti++) {
        var tAlpha = (ti + 1) / trail.length * 0.4;
        var tSize = 1 + (ti / trail.length) * 2;
        ctx.globalAlpha = tAlpha;
        ctx.beginPath(); ctx.arc(trail[ti].x, trail[ti].y, tSize, 0, 6.28);
        ctx.fillStyle = '#66ccff'; ctx.fill();
      }
      ctx.globalAlpha = 1;
      ctx.shadowBlur = 10;
      ctx.beginPath(); ctx.arc(pos.x, pos.y, 3.5, 0, 6.28);
      ctx.fillStyle = '#aaeeff'; ctx.fill();
    }
    ctx.restore();
    ctx.globalAlpha = 1;

    // C2: Agent hexagons with glow + gradient.
    for (var aid in agents) {
      var a = agents[aid];
      var color = STATE_COLORS[a.state] || STATE_COLORS.idle;
      var s = a.r * (1 + Math.sin(a.breath) * 0.04);
      ctx.globalAlpha = a.opacity;

      // Radial glow for active states.
      if (a.state === 'thinking' || a.state === 'tool_calling') {
        var glowR = s + 20;
        var pulse = 0.12 + Math.sin(globalTime * 2) * 0.05;
        var glow = ctx.createRadialGradient(a.x, a.y, s * 0.5, a.x, a.y, glowR);
        glow.addColorStop(0, color.slice(0, 7) + '40');
        glow.addColorStop(1, color.slice(0, 7) + '00');
        ctx.globalAlpha = a.opacity * pulse * 3;
        ctx.fillStyle = glow;
        ctx.fillRect(a.x - glowR, a.y - glowR, glowR * 2, glowR * 2);
        ctx.globalAlpha = a.opacity;
      }

      // Selected ring.
      if (aid === selectedId) {
        drawHex(ctx, a.x, a.y, s + 4);
        ctx.strokeStyle = '#e6edf3'; ctx.lineWidth = 2; ctx.stroke();
      }

      // Hex fill (gradient).
      drawHex(ctx, a.x, a.y, s);
      var hg = ctx.createRadialGradient(a.x, a.y - s * 0.3, 0, a.x, a.y, s);
      hg.addColorStop(0, color + '30');
      hg.addColorStop(1, color + '10');
      ctx.fillStyle = hg; ctx.fill();
      ctx.strokeStyle = color; ctx.lineWidth = 1.5; ctx.globalAlpha = a.opacity * 0.9; ctx.stroke();
      ctx.globalAlpha = a.opacity;

      // Label with shadow.
      ctx.save();
      ctx.shadowColor = 'rgba(0,0,0,0.5)'; ctx.shadowBlur = 4;
      ctx.fillStyle = '#e6edf3'; ctx.font = (a.r > 22 ? 'bold 11' : '9') + 'px system-ui,sans-serif';
      ctx.textAlign = 'center'; ctx.textBaseline = 'middle';
      ctx.fillText(a.label, a.x, a.y);
      ctx.restore();
    }
    ctx.globalAlpha = 1;

    // C4: Tool call cards with glow border.
    var toolIdx = {};
    for (var tid in toolCalls) {
      var tc = toolCalls[tid], ag = agents[tc.agentId]; if (!ag) continue;
      if (!toolIdx[tc.agentId]) toolIdx[tc.agentId] = 0;
      var idx = toolIdx[tc.agentId]++;
      var tx = ag.x - TOOL_W / 2, ty = ag.y + ag.r + 12 + idx * (TOOL_H + 5);
      var tcAlpha = tc.fadeOut ? Math.max(0, 1 - tc.fadeOut / 0.6) : 1;

      ctx.globalAlpha = tcAlpha * (ag.opacity || 1);
      var tcColor = tc.state === 'running' ? '#66ccff' : tc.state === 'error' ? '#ff5566' : '#66ffaa';

      // Card glow.
      ctx.save();
      ctx.shadowColor = tcColor; ctx.shadowBlur = tc.state === 'running' ? 8 : 4;
      ctx.fillStyle = 'rgba(8,12,24,0.88)';
      ctx.strokeStyle = tcColor; ctx.lineWidth = 1;
      ctx.beginPath(); ctx.roundRect(tx, ty, TOOL_W, TOOL_H, 6); ctx.fill(); ctx.stroke();
      ctx.restore();

      // Error shake.
      var shakeX = tc.state === 'error' ? Math.sin(globalTime * 30) * 2 : 0;

      ctx.fillStyle = '#e6edf3'; ctx.font = '10px system-ui,sans-serif';
      ctx.textAlign = 'left'; ctx.textBaseline = 'middle';
      ctx.fillText(tc.toolName, tx + 10 + shakeX, ty + TOOL_H / 2);

      // Spinning ring.
      if (tc.state === 'running') {
        tc.spin = (tc.spin || 0) + 0.06;
        ctx.beginPath(); ctx.arc(tx + TOOL_W - 16, ty + TOOL_H / 2, 6, tc.spin, tc.spin + 4.7);
        ctx.strokeStyle = '#66ccff'; ctx.lineWidth = 1.5; ctx.stroke();
      }
    }
    ctx.globalAlpha = 1;

    // Bubbles.
    for (var bi = 0; bi < bubbles.length; bi++) {
      var bub = bubbles[bi], ba = agents[bub.agentId]; if (!ba) continue;
      ctx.globalAlpha = Math.max(0, bub.opacity) * (ba.opacity || 1);
      var bx = ba.x + ba.r + 18, by = ba.y - 24 - bi * 28;
      var bcolor = ROLE_COLORS[bub.role] || '#8b949e';
      ctx.save();
      ctx.shadowColor = bcolor; ctx.shadowBlur = 6;
      ctx.fillStyle = 'rgba(8,12,24,0.85)';
      ctx.strokeStyle = bcolor; ctx.lineWidth = 1;
      var tw = Math.min(ctx.measureText(bub.text).width + 18, 300);
      ctx.beginPath(); ctx.roundRect(bx, by - 10, tw, 22, 4); ctx.fill(); ctx.stroke();
      ctx.restore();
      ctx.fillStyle = bcolor; ctx.font = '10px system-ui,sans-serif';
      ctx.textAlign = 'left'; ctx.textBaseline = 'middle';
      ctx.fillText(bub.text, bx + 9, by + 1);
      ctx.globalAlpha = 1;
    }

    // Selected agent info card (C5 glass morphism via canvas).
    if (selectedId && agents[selectedId]) {
      var sa = agents[selectedId];
      var ix = sa.x + sa.r + 18, iy = sa.y - sa.r;
      ctx.save();
      ctx.shadowColor = 'rgba(102,204,255,0.15)'; ctx.shadowBlur = 12;
      ctx.fillStyle = 'rgba(8,12,24,0.88)';
      ctx.strokeStyle = 'rgba(102,204,255,0.15)'; ctx.lineWidth = 1;
      ctx.beginPath(); ctx.roundRect(ix, iy, 160, 76, 8); ctx.fill(); ctx.stroke();
      ctx.restore();
      ctx.fillStyle = '#e6edf3'; ctx.font = 'bold 11px system-ui,sans-serif';
      ctx.textAlign = 'left'; ctx.textBaseline = 'top';
      ctx.fillText(sa.label, ix + 10, iy + 10);
      ctx.font = '10px system-ui,sans-serif'; ctx.fillStyle = '#8b949e';
      ctx.fillText(t('canvas.state') + ': ' + sa.state, ix + 10, iy + 26);
      ctx.fillText(t('canvas.tools') + ': ' + (agentToolCounts[selectedId] || 0), ix + 10, iy + 40);
      ctx.fillText(sa.isMain ? t('canvas.main_agent') : t('canvas.subagent'), ix + 10, iy + 54);
    }

    // Tool detail popup.
    if (selectedToolId && toolCalls[selectedToolId]) {
      var st = toolCalls[selectedToolId], stAg = agents[st.agentId];
      if (stAg) {
        var px = stAg.x + TOOL_W / 2 + 12, py = stAg.y + stAg.r + 12;
        ctx.save();
        ctx.shadowColor = 'rgba(102,204,255,0.1)'; ctx.shadowBlur = 10;
        ctx.fillStyle = 'rgba(8,12,24,0.92)';
        ctx.strokeStyle = 'rgba(102,204,255,0.12)'; ctx.lineWidth = 1;
        ctx.beginPath(); ctx.roundRect(px, py, 220, 80, 8); ctx.fill(); ctx.stroke();
        ctx.restore();
        ctx.fillStyle = '#66ccff'; ctx.font = 'bold 10px system-ui,sans-serif';
        ctx.textAlign = 'left'; ctx.textBaseline = 'top';
        ctx.fillText(st.toolName, px + 10, py + 10);
        ctx.fillStyle = '#8b949e'; ctx.font = '9px system-ui,sans-serif';
        var argText = String(typeof st.toolInput === 'object' ? JSON.stringify(st.toolInput) : st.toolInput || '').slice(0, 80);
        ctx.fillText(argText || t('canvas.no_args'), px + 10, py + 26);
        ctx.fillText(t('canvas.state') + ': ' + st.state, px + 10, py + 42);
        var resText = st.toolResult ? String(typeof st.toolResult === 'object' ? JSON.stringify(st.toolResult) : st.toolResult).slice(0, 60) : '';
        if (resText) ctx.fillText(t('canvas.result') + ': ' + resText, px + 10, py + 56);
      }
    }

    ctx.restore(); // camera transform

    // ── HUD (screen-space, not camera-space) ───────────────────

    // Replay scrubber.
    if (mode === 'replay' && replayEndTs > replayStartTs) {
      var barY = h - 36, barX = 60, barW = w - 120;
      ctx.fillStyle = 'rgba(139,148,158,0.12)';
      ctx.beginPath(); ctx.roundRect(barX, barY - 1, barW, 6, 3); ctx.fill();
      ctx.fillStyle = 'rgba(102,204,255,0.35)';
      var maxDots = Math.min(replayEvents.length, 200);
      var step2 = Math.max(1, Math.floor(replayEvents.length / maxDots));
      for (var di = 0; di < replayEvents.length; di += step2) {
        var dp = (replayEvents[di].ts - replayStartTs) / (replayEndTs - replayStartTs);
        ctx.beginPath(); ctx.arc(barX + dp * barW, barY + 2, 2, 0, 6.28); ctx.fill();
      }
      var pp = Math.max(0, Math.min(1, (replayCurrentTs - replayStartTs) / (replayEndTs - replayStartTs)));
      ctx.fillStyle = '#66ccff';
      ctx.beginPath(); ctx.roundRect(barX + pp * barW - 2, barY - 4, 4, 12, 2); ctx.fill();
      ctx.fillStyle = '#8b949e'; ctx.font = '9px system-ui,sans-serif';
      ctx.textAlign = 'left'; ctx.fillText(fmtTime(0), barX, barY + 18);
      ctx.textAlign = 'right'; ctx.fillText(fmtTime((replayEndTs - replayStartTs) / 1000), barX + barW, barY + 18);
      // Only show playhead time when not too close to start/end labels.
      var playheadX = barX + pp * barW;
      if (playheadX > barX + 40 && playheadX < barX + barW - 40) {
        ctx.textAlign = 'center';
        ctx.fillStyle = '#66ccff';
        ctx.fillText(fmtTime((replayCurrentTs - replayStartTs) / 1000), playheadX, barY + 18);
      }
    }

    // Session info (top-left).
    ctx.fillStyle = 'rgba(139,148,158,0.5)'; ctx.font = '10px system-ui,sans-serif';
    ctx.textAlign = 'left'; ctx.textBaseline = 'top';
    var infoLabel = canvasMode === 'live' ? t('canvas.live') : t('canvas.replay');
    if (canvasSessionId && canvasSessionId !== 'replay') infoLabel += '  \u00B7  ' + canvasSessionId;
    ctx.fillText(infoLabel, 16, 16);

    // B1: Cost ticker (top-left, below session info).
    var displayCost = costTotal;
    if (mode === 'replay' && costFinal > 0 && replayEndTs > replayStartTs) {
      var progress = Math.max(0, Math.min(1, (replayCurrentTs - replayStartTs) / (replayEndTs - replayStartTs)));
      displayCost = costFinal * progress;
    }
    if (displayCost > 0) {
      ctx.fillStyle = '#66ffaa'; ctx.font = 'bold 14px system-ui,sans-serif';
      ctx.fillText('$' + displayCost.toFixed(4), 16, 34);
    }
  }

  function drawHex(c, x, y, r) {
    c.beginPath();
    for (var i = 0; i < 6; i++) {
      var angle = Math.PI / 3 * i - Math.PI / 6;
      if (i === 0) c.moveTo(x + r * Math.cos(angle), y + r * Math.sin(angle));
      else c.lineTo(x + r * Math.cos(angle), y + r * Math.sin(angle));
    }
    c.closePath();
  }

  function fmtTime(sec) {
    var h = Math.floor(sec / 3600);
    var m = Math.floor((sec % 3600) / 60);
    var s = Math.floor(sec % 60);
    if (h > 0) return h + ':' + (m < 10 ? '0' : '') + m + ':' + (s < 10 ? '0' : '') + s;
    return m + ':' + (s < 10 ? '0' : '') + s;
  }

  // ── Animation Loop ───────────────────────────────────────────

  function tick(timestamp) {
    var dt = Math.min((timestamp - lastTime) / 1000, 0.05);
    lastTime = timestamp;
    globalTime += dt;
    simulate(dt);
    updateParticles(dt);
    updateBubbles(dt);
    updateToolFades(dt);
    render();
    animFrame = requestAnimationFrame(tick);
  }

  // ── Event Processing ─────────────────────────────────────────

  function processEvent(ev) {
    var mainId = '_main';
    if (!agents[mainId]) addAgent(mainId, 'main', true);
    var name = ev.hook_event_name || ev.event_type || '';

    switch (name) {
    case 'SessionStart':
      agents[mainId].state = 'thinking'; break;
    case 'PreToolUse': {
      var aId = ev.agent_id || mainId;
      if (aId && aId !== mainId && !agents[aId]) {
        addAgent(aId, ev.agent_type || aId.slice(0, 8), false);
        addEdge(mainId, aId);
      }
      var agNode = agents[aId] || agents[mainId];
      if (agNode) { agNode.state = 'tool_calling'; agNode.lastActive = globalTime; }
      agentToolCounts[aId || mainId] = (agentToolCounts[aId || mainId] || 0) + 1;
      toolCalls[ev.tool_use_id] = {
        toolUseId: ev.tool_use_id, agentId: aId || mainId,
        toolName: ev.tool_name || '?', state: 'running', spin: 0,
        toolInput: typeof ev.tool_input === 'object' ? JSON.stringify(ev.tool_input) : (ev.tool_input || ''),
      };
      if (aId && aId !== mainId) spawnParticle(mainId + '>' + aId);
      break;
    }
    case 'PostToolUse':
    case 'PostToolUseFailure': {
      var tc = toolCalls[ev.tool_use_id];
      if (tc) {
        tc.state = name === 'PostToolUseFailure' ? 'error' : 'done';
        var rawResult = ev.tool_response || ev.tool_result || '';
        tc.toolResult = typeof rawResult === 'object' ? JSON.stringify(rawResult) : rawResult;
      }
      var aId2 = ev.agent_id || mainId;
      if (agents[aId2]) { agents[aId2].state = 'thinking'; agents[aId2].lastActive = globalTime; }
      break;
    }
    case 'SubagentStart': {
      var subId = ev.agent_id || ('sub_' + Date.now());
      addAgent(subId, ev.agent_type || 'subagent', false);
      var ek = addEdge(mainId, subId);
      agents[subId].state = 'thinking';
      spawnParticle(ek);
      break;
    }
    case 'SubagentStop': {
      if (ev.agent_id && agents[ev.agent_id]) agents[ev.agent_id].state = 'complete';
      var stopEdge = mainId + '>' + ev.agent_id;
      if (edges[stopEdge]) edges[stopEdge].active = false;
      break;
    }
    case 'SessionEnd': case 'Stop':
      agents[mainId].state = 'complete'; break;
    }

    if (ev._msgType) addBubble(ev._agentId || '_main', ev._text || '', ev._msgType);
  }

  // ── Live Mode ────────────────────────────────────────────────

  function startLive(sessionId) {
    var url = '/api/hooks/stream';
    if (sessionId) url += '?session_id=' + encodeURIComponent(sessionId);
    eventSource = new EventSource(url);
    eventSource.onmessage = function (e) {
      try {
        var ev = JSON.parse(e.data);
        // Update session info from first event + start cost polling.
        if (ev.session_id && !canvasSessionId) {
          canvasSessionId = ev.session_id;
          if (!costPollTimer) startCostPoll(ev.session_id);
        }
        processEvent(ev);
        if (ev.hook_event_name === 'PreToolUse' && ev.tool_name) {
          addBubble(ev.agent_id || '_main', ev.tool_name, 'assistant');
        }
      } catch (err) { /* ignore */ }
    };
    // Cost ticker polling.
    startCostPoll(sessionId);
  }

  function stopLive() {
    if (eventSource) { eventSource.close(); eventSource = null; }
    if (costPollTimer) { clearInterval(costPollTimer); costPollTimer = null; }
  }

  // B1: Cost ticker.
  function startCostPoll(sessionId) {
    costTotal = 0;
    // For global live (no sessionId), we'll start polling once we see a session from SSE.
    if (!sessionId) return;
    function updateCost() {
      var sql = "SELECT " +
        "SUM(CASE WHEN message_type IN ('user','tool_result','tool_use') THEN LENGTH(COALESCE(content,''))/4 ELSE 0 END) AS input_tok, " +
        "SUM(CASE WHEN message_type IN ('assistant','thinking') THEN LENGTH(COALESCE(content,''))/4 ELSE 0 END) AS output_tok, " +
        "MAX(model) AS model " +
        "FROM tma1_messages WHERE session_id = '" + escapeSQLString(sessionId) + "'";
      Promise.resolve(typeof loadPricing === 'function' ? loadPricing() : null).then(function () {
        return query(sql);
      }).then(function (res) {
        var r = rows(res);
        if (!r.length) return;
        var inputTok = Number(r[0][0]) || 0;
        var outputTok = Number(r[0][1]) || 0;
        var price = canvasLookupPrice(r[0][2] || '');
        costTotal = inputTok * price.input / 1000000 + outputTok * price.output / 1000000;
      }).catch(function () { /* ignore */ });
    }
    updateCost();
    costPollTimer = setInterval(updateCost, 5000);
  }

  function canvasLookupPrice(model) {
    if (!model || typeof modelPricing === 'undefined' || !modelPricing || !modelPricing.length) {
      return { input: 3, output: 15 };
    }
    for (var i = 0; i < modelPricing.length; i++) {
      if (model.indexOf(modelPricing[i].p) !== -1) {
        return { input: modelPricing[i].i, output: modelPricing[i].o };
      }
    }
    return { input: 3, output: 15 };
  }

  // ── Replay Mode ──────────────────────────────────────────────

  function startReplay(timelineData, speed) {
    replayEvents = buildReplayEvents(timelineData);
    if (!replayEvents.length) return;
    replayIdx = 0; replaySpeed = speed || 1; replayPaused = false;
    replayStartTs = replayEvents[0].ts;
    replayEndTs = replayEvents[replayEvents.length - 1].ts;
    replayCurrentTs = replayStartTs;
    scheduleNext();
  }

  function buildReplayEvents(timeline) {
    var evs = [];
    for (var i = 0; i < timeline.length; i++) {
      var item = timeline[i];
      if (item.source === 'tool_pair') {
        evs.push({ ts: item.data.start_ts, hook_event_name: 'PreToolUse',
          tool_name: item.data.tool_name, tool_use_id: item.data.tool_use_id,
          tool_input: item.data.tool_input || '',
          agent_id: item.data.agent_id || '', agent_type: item.data.agent_type || '' });
        evs.push({ ts: item.data.end_ts,
          hook_event_name: item.data.failed ? 'PostToolUseFailure' : 'PostToolUse',
          tool_use_id: item.data.tool_use_id, agent_id: item.data.agent_id || '',
          tool_result: item.data.tool_result || '' });
      } else if (item.source === 'hook') {
        evs.push({ ts: item.ts, hook_event_name: item.data.event_type,
          tool_name: item.data.tool_name, tool_use_id: item.data.tool_use_id,
          tool_input: item.data.tool_input || '',
          agent_id: item.data.agent_id || '', agent_type: item.data.agent_type || '' });
      } else if (item.source === 'message') {
        var mt = item.data.message_type;
        if (mt === 'user' || mt === 'assistant' || mt === 'thinking') {
          evs.push({ ts: item.ts, hook_event_name: '_msg',
            _msgType: mt, _text: item.data.content || '', _agentId: '_main' });
        }
      }
    }
    evs.sort(function (a, b) { return a.ts - b.ts; });
    return evs;
  }

  function scheduleNext() {
    if (replayIdx >= replayEvents.length || replayPaused) return;
    var ev = replayEvents[replayIdx];
    processEvent(ev);
    replayCurrentTs = ev.ts;
    replayIdx++;
    if (replayIdx < replayEvents.length) {
      var delay = (replayEvents[replayIdx].ts - ev.ts) / replaySpeed;
      delay = Math.max(16, Math.min(delay, 2000));
      replayTimer = setTimeout(scheduleNext, delay);
    }
  }

  function seekTo(fraction) {
    var targetTs = replayStartTs + fraction * (replayEndTs - replayStartTs);
    clearTimeout(replayTimer);
    agents = {}; edges = {}; toolCalls = {}; particles = []; bubbles = [];
    agentToolCounts = {}; selectedId = null; selectedToolId = null;
    replayIdx = 0;
    while (replayIdx < replayEvents.length && replayEvents[replayIdx].ts <= targetTs) {
      processEvent(replayEvents[replayIdx]);
      replayIdx++;
    }
    replayCurrentTs = targetTs;
    if (!replayPaused) scheduleNext();
  }

  // ── Mouse Interaction ────────────────────────────────────────

  function screenToWorld(sx, sy) {
    var w = canvas.width / dpr, h = canvas.height / dpr;
    return {
      x: (sx - w / 2) / cam.zoom + w / 2 - cam.x,
      y: (sy - h / 2) / cam.zoom + h / 2 - cam.y,
    };
  }

  function hitTestAgent(wx, wy) {
    var closest = null, closestDist = Infinity;
    for (var aid in agents) {
      var a = agents[aid];
      var dx = wx - a.x, dy = wy - a.y;
      var dist = Math.sqrt(dx * dx + dy * dy);
      if (dist < a.r + 5 && dist < closestDist) { closest = aid; closestDist = dist; }
    }
    return closest;
  }

  function hitTestTool(wx, wy) {
    // Track per-agent index to match the stacked rendering offsets.
    var idxMap = {};
    for (var tid in toolCalls) {
      var tc = toolCalls[tid], ag = agents[tc.agentId]; if (!ag) continue;
      if (!idxMap[tc.agentId]) idxMap[tc.agentId] = 0;
      var idx = idxMap[tc.agentId]++;
      var tx = ag.x - TOOL_W / 2, ty = ag.y + ag.r + 12 + idx * (TOOL_H + 5);
      if (wx >= tx && wx <= tx + TOOL_W && wy >= ty && wy <= ty + TOOL_H) return tid;
    }
    return null;
  }

  function onMouseDown(e) {
    var rect = canvas.getBoundingClientRect();
    var sx = e.clientX - rect.left, sy = e.clientY - rect.top;
    var world = screenToWorld(sx, sy);

    var agentHit = hitTestAgent(world.x, world.y);
    if (agentHit) {
      drag.active = true; drag.target = agentHit; drag.isPan = false;
      agents[agentHit].pinned = true;
      canvas.style.cursor = 'grabbing';
      return;
    }

    // Pan on empty area.
    drag.active = true; drag.target = null; drag.isPan = true;
    drag.startX = sx; drag.startY = sy;
    canvas.style.cursor = 'move';
  }

  function onMouseMove(e) {
    if (!drag.active) {
      var rect2 = canvas.getBoundingClientRect();
      var sx2 = e.clientX - rect2.left, sy2 = e.clientY - rect2.top;
      var w2 = screenToWorld(sx2, sy2);
      canvas.style.cursor = hitTestAgent(w2.x, w2.y) ? 'grab' : 'default';
      return;
    }
    var rect = canvas.getBoundingClientRect();
    var sx = e.clientX - rect.left, sy = e.clientY - rect.top;

    if (drag.isPan) {
      cam.x += (sx - drag.startX) / cam.zoom;
      cam.y += (sy - drag.startY) / cam.zoom;
      drag.startX = sx; drag.startY = sy;
    } else if (drag.target && agents[drag.target]) {
      var world = screenToWorld(sx, sy);
      agents[drag.target].x = world.x;
      agents[drag.target].y = world.y;
    }
  }

  function onMouseUp() {
    // Unpin dragged agent so it returns to force simulation.
    if (drag.active && drag.target && agents[drag.target]) {
      agents[drag.target].pinned = false;
    }
    drag.active = false;
    canvas.style.cursor = 'default';
  }

  function onClick(e) {
    var rect = canvas.getBoundingClientRect();
    var sx = e.clientX - rect.left, sy = e.clientY - rect.top;

    // Scrubber click.
    if (mode === 'replay' && replayEndTs > replayStartTs) {
      var h = canvas.height / dpr;
      var barY = h - 36, barX = 60, barW = canvas.width / dpr - 120;
      if (sy >= barY - 8 && sy <= barY + 20 && sx >= barX && sx <= barX + barW) {
        seekTo((sx - barX) / barW);
        return;
      }
    }

    var world = screenToWorld(sx, sy);

    // Tool click.
    var toolHit = hitTestTool(world.x, world.y);
    if (toolHit) { selectedToolId = (selectedToolId === toolHit) ? null : toolHit; selectedId = null; return; }

    // Agent click.
    var agentHit = hitTestAgent(world.x, world.y);
    selectedId = agentHit; selectedToolId = null;
  }

  function onWheel(e) {
    e.preventDefault();
    var factor = e.deltaY > 0 ? 0.9 : 1.1;
    cam.zoom = Math.max(0.2, Math.min(5, cam.zoom * factor));
  }

  // A3: Zoom to fit.
  function zoomToFit() {
    var ids = Object.keys(agents);
    if (!ids.length) return;
    var minX = Infinity, maxX = -Infinity, minY = Infinity, maxY = -Infinity;
    for (var i = 0; i < ids.length; i++) {
      var a = agents[ids[i]];
      minX = Math.min(minX, a.x - a.r); maxX = Math.max(maxX, a.x + a.r);
      minY = Math.min(minY, a.y - a.r); maxY = Math.max(maxY, a.y + a.r);
    }
    var w = canvas.width / dpr, h = canvas.height / dpr;
    var padding = 80;
    var bw = maxX - minX + padding * 2, bh = maxY - minY + padding * 2;
    cam.zoom = Math.min(w / bw, h / bh, 2);
    cam.x = w / 2 - (minX + maxX) / 2;
    cam.y = h / 2 - (minY + maxY) / 2;
  }

  // ── Keyboard ─────────────────────────────────────────────────

  function onKey(e) {
    if (e.key === 'Escape') close();
    if (e.key === ' ' && mode === 'replay') { e.preventDefault(); togglePause(); }
    if (e.key === 'f' || e.key === 'F') zoomToFit();
    if (e.key >= '1' && e.key <= '5') {
      var speeds = [0.5, 1, 2, 5, 10];
      setSpeed(speeds[e.key - '1']);
      var sel = document.querySelector('#agent-canvas-controls select');
      if (sel) sel.value = String(speeds[e.key - '1']);
    }
  }

  // ── Public API ───────────────────────────────────────────────

  function open(m, opts) {
    mode = m; globalTime = 0; costTotal = 0;
    cam = { x: 0, y: 0, zoom: 1 };
    var overlay = document.getElementById('agent-canvas-overlay');
    overlay.style.display = 'flex';
    canvas = document.getElementById('agent-canvas');
    ctx = canvas.getContext('2d');
    resize();
    agents = {}; edges = {}; toolCalls = {}; particles = []; bubbles = [];
    agentToolCounts = {}; selectedId = null; selectedToolId = null;
    drag = { active: false, target: null, startX: 0, startY: 0, isPan: false };
    lastTime = performance.now();
    animFrame = requestAnimationFrame(tick);
    canvas.addEventListener('click', onClick);
    canvas.addEventListener('mousedown', onMouseDown);
    canvas.addEventListener('mousemove', onMouseMove);
    canvas.addEventListener('mouseup', onMouseUp);
    canvas.addEventListener('wheel', onWheel, { passive: false });
    window.addEventListener('resize', resize);
    window.addEventListener('keydown', onKey);

    canvasSessionId = opts.sessionId || (opts.timelineData && opts.timelineData.length > 0 ? 'replay' : '');
    canvasMode = m;
    renderControls();

    addAgent('_main', 'main', true);
    agents['_main'].state = 'thinking';

    if (m === 'live') {
      startLive(opts.sessionId);
    } else {
      // Replay: accumulate cost proportionally as timeline progresses.
      costFinal = (typeof sessCurrentStats !== 'undefined' && sessCurrentStats) ? sessCurrentStats.cost || 0 : 0;
      startReplay(opts.timelineData || [], opts.speed);
    }
  }

  function close() {
    stopLive();
    clearTimeout(replayTimer);
    if (animFrame) cancelAnimationFrame(animFrame);
    animFrame = null;
    canvas.removeEventListener('click', onClick);
    canvas.removeEventListener('mousedown', onMouseDown);
    canvas.removeEventListener('mousemove', onMouseMove);
    canvas.removeEventListener('mouseup', onMouseUp);
    canvas.removeEventListener('wheel', onWheel);
    window.removeEventListener('resize', resize);
    window.removeEventListener('keydown', onKey);
    document.getElementById('agent-canvas-overlay').style.display = 'none';
    mode = null;
  }

  function resize() {
    if (!canvas) return;
    dpr = window.devicePixelRatio || 1;
    canvas.width = window.innerWidth * dpr;
    canvas.height = window.innerHeight * dpr;
    canvas.style.width = window.innerWidth + 'px';
    canvas.style.height = window.innerHeight + 'px';
    ctx.setTransform(1, 0, 0, 1, 0, 0);
    ctx.scale(dpr, dpr);
  }

  function renderControls() {
    var bar = document.getElementById('agent-canvas-controls');
    var html = '';
    if (mode === 'replay') {
      html += '<button onclick="AgentCanvas.togglePause()" id="ac-pause-btn" aria-label="' + t('canvas.pause_play') + '">\u23F8</button>';
      html += '<select onchange="AgentCanvas.setSpeed(Number(this.value))">';
      html += '<option value="0.5">0.5x</option><option value="1" selected>1x</option>';
      html += '<option value="2">2x</option><option value="5">5x</option>';
      html += '<option value="10">10x</option><option value="20">20x</option></select>';
    } else {
      html += '<span class="ac-live-dot"></span> ' + t('canvas.live_label');
    }
    html += '<button onclick="AgentCanvas.zoomToFit()" title="' + t('canvas.zoom_to_fit') + '" aria-label="' + t('canvas.zoom_to_fit') + '">&#x2922;</button>';
    html += '<button onclick="AgentCanvas.close()" class="ac-close" aria-label="' + t('ui.close') + '">\u2715</button>';
    bar.innerHTML = html;
  }

  function togglePause() {
    replayPaused = !replayPaused;
    var btn = document.getElementById('ac-pause-btn');
    if (btn) btn.textContent = replayPaused ? '\u25B6' : '\u23F8';
    if (!replayPaused) scheduleNext();
  }

  function setSpeed(s) { replaySpeed = s || 1; }

  return { open: open, close: close, togglePause: togglePause, setSpeed: setSpeed, zoomToFit: zoomToFit };
})();
