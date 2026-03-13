// ═══════════════════════════════════════════════════════════
// cagent mind-graph: Poincaré disk hyperbolic work graph
// ═══════════════════════════════════════════════════════════

// ── Data Loading ────────────────────────────────────────────

function normalizeItem(raw) {
  const reqAtt = raw.required_attestations || [];
  const attRecs = (raw.attestation_records || []).filter(a => a.result === "passed");
  return {
    id: raw.work_id,
    t: raw.title || "(untitled)",
    k: raw.kind || "task",
    s: raw.execution_state || "ready",
    approval: raw.approval_state || "none",
    lock: raw.lock_state || "unlocked",
    p: raw.parent_work_id || null,
    pr: raw.priority || 3,
    ch: raw.children || [],
    bb: raw.blocked_by || [],
    att: [Math.max(reqAtt.length, 1), attRecs.length],
    obj: raw.objective || "",
    cr: new Date(raw.created_at).getTime(),
    up: new Date(raw.updated_at).getTime(),
  };
}

async function loadData() {
  try {
    const res = await fetch("/api/work/items", { signal: AbortSignal.timeout(3000) });
    if (res.ok) {
      const raw = await res.json();
      const items = Array.isArray(raw) ? raw : (raw.items || raw.work_items || []);
      if (items.length > 0) {
        const byId = {};
        items.forEach(item => { byId[item.work_id] = item; });
        items.forEach(item => { if (!item.children) item.children = []; });
        items.forEach(item => {
          if (item.parent_work_id && byId[item.parent_work_id]) {
            if (!byId[item.parent_work_id].children) byId[item.parent_work_id].children = [];
            if (!byId[item.parent_work_id].children.includes(item.work_id))
              byId[item.parent_work_id].children.push(item.work_id);
          }
        });
        return items.map(normalizeItem);
      }
    }
  } catch (e) { /* API not available */ }
  return null; // signals: use mock or show empty state
}

// ── Poincaré Disk Math ──────────────────────────────────────

function dot(a, b) { return a[0]*b[0] + a[1]*b[1]; }
function norm(v) { return Math.sqrt(v[0]*v[0] + v[1]*v[1]); }
function scale(v, s) { return [v[0]*s, v[1]*s]; }
function add(a, b) { return [a[0]+b[0], a[1]+b[1]]; }
function sub(a, b) { return [a[0]-b[0], a[1]-b[1]]; }
function clampDisk(p, maxR) {
  const r = norm(p);
  return r < maxR ? p : scale(p, maxR / r);
}

function mobAdd(x, y) {
  const xx = dot(x, x), yy = dot(y, y), xy = dot(x, y);
  const denom = 1 + 2*xy + xx*yy;
  if (denom < 1e-10) return [0, 0];
  return [
    ((1 + 2*xy + yy)*x[0] + (1 - xx)*y[0]) / denom,
    ((1 + 2*xy + yy)*x[1] + (1 - xx)*y[1]) / denom
  ];
}

function mobNeg(x) { return [-x[0], -x[1]]; }

function hypDist(x, y) {
  const diff = sub(x, y);
  const diffSq = dot(diff, diff);
  const xSq = dot(x, x), ySq = dot(y, y);
  const denom = (1 - xSq) * (1 - ySq);
  if (denom < 1e-12) return 10;
  return Math.acosh(Math.max(1, 1 + 2 * diffSq / denom));
}

function conformal(x) { return 2 / (1 - dot(x, x)); }

function logMap(x, y) {
  const mxy = mobAdd(mobNeg(x), y);
  const n = norm(mxy);
  if (n < 1e-10) return [0, 0];
  const lam = conformal(x);
  return scale(mxy, (2 / lam) * Math.atanh(Math.min(n, 0.999)) / n);
}

function expMap(x, v) {
  const nv = norm(v);
  if (nv < 1e-10) return x;
  const lam = conformal(x);
  return clampDisk(mobAdd(x, scale(v, Math.tanh(lam * nv / 2) / nv)), 0.98);
}

function focusTransform(a, z) { return mobAdd(mobNeg(a), z); }

// ── Simulation ──────────────────────────────────────────────

const RHO_STATE = { running: 0.45, blocked: 0.85, ready: 1.35, draft: 2.20, done: 3.20 };
const DAMPING = { running: 1.0, blocked: 1.4, ready: 1.6, draft: 2.0, done: 2.8 };
const K_RHO = 0.8, K_TREE = 1.0, L_TREE = 0.85, K_BLOCK = 1.8, L_BLOCK = 0.35;
const K_REP = 0.04, K_WALL = 3.0, RHO_WALL = 4.0, SIGMA_WALL = 0.3;
const DT = 1/60, V_MAX = 0.06;
const COL = {draft:"#5a6a7a",ready:"#c4a060",running:"#50b888",blocked:"#d06060",done:"#408868",
             failed:"#803030",cancelled:"#555"};

let W = [], byId = {}, BLOCKS = [], nodes = {};
let focusPoint = [0, 0], focusTarget = [0, 0], focusId = null, hoverId = null;

function initSimulation(items) {
  W = items;
  byId = Object.fromEntries(W.map(w => [w.id, w]));
  BLOCKS = [];
  W.forEach(w => {
    if (w.bb) w.bb.forEach(bid => { if (byId[bid]) BLOCKS.push([bid, w.id]); });
  });
  nodes = {};
  W.forEach((w, i) => {
    const angle = (i / W.length) * Math.PI * 2;
    const r = 0.15 + Math.random() * 0.25;
    nodes[w.id] = { pos: [Math.cos(angle) * r, Math.sin(angle) * r], vel: [0, 0], depth: 0 };
  });
  W.forEach(w => {
    let d = 0, cur = w;
    while (cur.p && byId[cur.p]) { d++; cur = byId[cur.p]; }
    nodes[w.id].depth = d;
  });
}

function refreshData(items) {
  const oldById = byId;
  const newById = Object.fromEntries(items.map(w => [w.id, w]));

  // Update existing items in place, preserve simulation state
  items.forEach(w => {
    if (nodes[w.id]) {
      const old = oldById[w.id];
      if (old && old.s !== w.s) {
        // State changed — inject impulse
        const rhoOld = RHO_STATE[old.s] || 1.5;
        const rhoNew = RHO_STATE[w.s] || 1.5;
        const impulse = (rhoOld - rhoNew) * 0.03;
        const ni = nodes[w.id];
        const rhoI = hypDist([0,0], ni.pos);
        if (rhoI > 0.01) {
          const toOrigin = logMap(ni.pos, [0,0]);
          const n = norm(toOrigin);
          if (n > 1e-8) ni.vel = add(ni.vel, scale(toOrigin, impulse / n));
        }
      }
    } else {
      // New item — initialize at a random position
      const angle = Math.random() * Math.PI * 2;
      const r = 0.3 + Math.random() * 0.2;
      nodes[w.id] = { pos: [Math.cos(angle) * r, Math.sin(angle) * r], vel: [0, 0], depth: 0 };
    }
  });

  // Remove deleted items
  Object.keys(nodes).forEach(id => { if (!newById[id]) delete nodes[id]; });

  W = items;
  byId = newById;
  BLOCKS = [];
  W.forEach(w => {
    if (w.bb) w.bb.forEach(bid => { if (byId[bid]) BLOCKS.push([bid, w.id]); });
  });
  W.forEach(w => {
    let d = 0, cur = w;
    while (cur.p && byId[cur.p]) { d++; cur = byId[cur.p]; }
    nodes[w.id].depth = d;
  });
}

function preferredRho(w) {
  const n = nodes[w.id];
  const stale = Math.min(1, Math.max(0, (Date.now() - w.up) / (14 * 86400000)));
  const attDef = w.att ? 1 - w.att[1] / Math.max(w.att[0], 1) : 0;
  return (RHO_STATE[w.s] || 1.5) + 0.18 * n.depth + 0.5 * stale - 0.3 * attDef;
}

function simulate() {
  const N = W.length;
  for (let i = 0; i < N; i++) {
    const w = W[i];
    const ni = nodes[w.id];
    if (!ni) continue;
    const pi = ni.pos;
    const rhoI = hypDist([0,0], pi);
    let F = [0, 0];

    // 1. Attention shell
    const rhoStar = preferredRho(w);
    if (rhoI > 0.01) {
      const toOrigin = logMap(pi, [0, 0]);
      const n = norm(toOrigin);
      if (n > 1e-8) F = add(F, scale(scale(toOrigin, 1/n), K_RHO * (rhoI - rhoStar)));
    }

    // 2. Tree springs
    if (w.p && nodes[w.p]) {
      const pj = nodes[w.p].pos, d = hypDist(pi, pj), lv = logMap(pi, pj), ln = norm(lv);
      if (ln > 1e-8) F = add(F, scale(scale(lv, 1/ln), K_TREE * (d - L_TREE)));
    }
    if (w.ch) for (const cid of w.ch) {
      if (!nodes[cid]) continue;
      const pj = nodes[cid].pos, d = hypDist(pi, pj), lv = logMap(pi, pj), ln = norm(lv);
      if (ln > 1e-8) F = add(F, scale(scale(lv, 1/ln), K_TREE * 0.5 * (d - L_TREE)));
    }

    // 3. Blocker tension
    if (w.bb) for (const bid of w.bb) {
      if (!nodes[bid]) continue;
      const pj = nodes[bid].pos, d = hypDist(pi, pj), lv = logMap(pi, pj), ln = norm(lv);
      if (ln > 1e-8) F = add(F, scale(scale(lv, 1/ln), K_BLOCK * Math.sinh(Math.max(0, d - L_BLOCK))));
    }
    for (const [bid, tid] of BLOCKS) {
      if (bid === w.id && nodes[tid]) {
        const pj = nodes[tid].pos, d = hypDist(pi, pj), lv = logMap(pi, pj), ln = norm(lv);
        if (ln > 1e-8) F = add(F, scale(scale(lv, 1/ln), K_BLOCK * 0.3 * Math.sinh(Math.max(0, d - L_BLOCK))));
      }
    }

    // 4. Repulsion
    for (let j = 0; j < N; j++) {
      if (i === j) continue;
      const pj = nodes[W[j].id].pos, d = hypDist(pi, pj);
      if (d > 4) continue;
      const lv = logMap(pi, pj), ln = norm(lv);
      if (ln > 1e-8) {
        const sinhH = Math.sinh(Math.max(d, 0.1) / 2);
        F = add(F, scale(scale(lv, 1/ln), -K_REP / (sinhH * sinhH)));
      }
    }

    // 5. Wall
    if (rhoI > RHO_WALL - 1) {
      const toOrigin = logMap(pi, [0, 0]), n = norm(toOrigin);
      if (n > 1e-8) F = add(F, scale(scale(toOrigin, 1/n), K_WALL * Math.exp((rhoI - RHO_WALL) / SIGMA_WALL)));
    }

    // 6. Activity noise
    if (w.s === "running") F = add(F, [(Math.random()-0.5)*0.006, (Math.random()-0.5)*0.006]);

    // Integrate
    const gamma = DAMPING[w.s] || 1.0;
    ni.vel = add(scale(ni.vel, Math.exp(-gamma * DT)), scale(F, DT));
    const vn = norm(ni.vel);
    if (vn > V_MAX) ni.vel = scale(ni.vel, V_MAX / vn);
    ni.pos = expMap(pi, scale(ni.vel, DT));
  }
}

// ── Focus ───────────────────────────────────────────────────

function updateFocus() {
  if (focusId && nodes[focusId]) focusTarget = [...nodes[focusId].pos];
  const diff = sub(focusTarget, focusPoint);
  const d = norm(diff);
  if (d > 0.0005) focusPoint = add(focusPoint, scale(diff, 0.1));
  else focusPoint = [...focusTarget];
}

function setFocus(id) {
  focusId = id || null;
  if (!id) focusTarget = [0, 0];
  updateUI();
}

// ── Rendering ───────────────────────────────────────────────

const cv = document.getElementById("cv");
const ctx = cv.getContext("2d");
let W_px, H_px, CX, CY, R;

function resize() {
  const wrap = document.getElementById("canvas-wrap");
  const rect = wrap.getBoundingClientRect();
  const dpr = window.devicePixelRatio || 1;
  const size = Math.min(rect.width, rect.height);
  W_px = H_px = size;
  cv.width = size * dpr; cv.height = size * dpr;
  cv.style.width = size + "px"; cv.style.height = size + "px";
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  CX = CY = size / 2;
  R = size / 2 * 0.92;
}

function diskToScreen(p) {
  const fp = focusTransform(focusPoint, p);
  return [CX + fp[0] * R, CY + fp[1] * R];
}

function textSizeForNode(w) {
  const fp = focusTransform(focusPoint, nodes[w.id].pos);
  const r = norm(fp);
  const soft = 0.15 + 0.85 * (1 - r * r) / 2;
  const baseSize = !w.p ? 26 : (w.ch && w.ch.length > 0 ? 18 : 14);
  return Math.max(5, baseSize * soft * 1.8);
}

function nodeAlpha(w) {
  const fp = focusTransform(focusPoint, nodes[w.id].pos);
  const cf = (1 - norm(fp) ** 2) / 2;
  return Math.max(0.18, Math.min(1, cf * 2.2 + 0.1));
}

function draw() {
  if (W.length === 0) { requestAnimationFrame(draw); return; }
  simulate();
  updateFocus();
  ctx.clearRect(0, 0, W_px, H_px);

  // Disk boundary
  ctx.save();
  ctx.beginPath(); ctx.arc(CX, CY, R, 0, Math.PI * 2);
  ctx.strokeStyle = "rgba(255,255,255,0.07)"; ctx.lineWidth = 1; ctx.stroke();
  ctx.restore();

  // Shell rings
  ctx.save(); ctx.globalAlpha = 0.015; ctx.strokeStyle = "#888"; ctx.lineWidth = 0.5;
  for (const rho of [0.45, 0.85, 1.35, 2.20, 3.20]) {
    ctx.beginPath(); ctx.arc(CX, CY, Math.tanh(rho / 2) * R, 0, Math.PI * 2); ctx.stroke();
  }
  ctx.restore();

  const screenPos = {};
  W.forEach(w => { screenPos[w.id] = diskToScreen(nodes[w.id].pos); });

  // Parent-child edges
  W.forEach(w => {
    if (!w.p || !screenPos[w.p]) return;
    const [x1, y1] = screenPos[w.p], [x2, y2] = screenPos[w.id];
    const fp1 = focusTransform(focusPoint, nodes[w.p].pos);
    const fp2 = focusTransform(focusPoint, nodes[w.id].pos);
    const ea = Math.min(Math.max(0.15, (1-norm(fp1)**2)/2), Math.max(0.15, (1-norm(fp2)**2)/2)) * 0.5;
    if (ea < 0.02) return;
    ctx.save(); ctx.strokeStyle = COL[w.s] || "#444"; ctx.globalAlpha = Math.min(0.35, ea);
    ctx.lineWidth = 0.8; ctx.beginPath(); ctx.moveTo(x1, y1); ctx.lineTo(x2, y2); ctx.stroke();
    ctx.restore();
  });

  // Blocking edges
  BLOCKS.forEach(([bid, tid]) => {
    if (!screenPos[bid] || !screenPos[tid]) return;
    const [x1, y1] = screenPos[bid], [x2, y2] = screenPos[tid];
    const d = hypDist(nodes[bid].pos, nodes[tid].pos);
    const tension = Math.max(0, (d - L_BLOCK) / L_BLOCK);
    ctx.save(); ctx.strokeStyle = "#d06060"; ctx.globalAlpha = 0.3;
    ctx.lineWidth = 0.8 + tension * 2;
    const mx = (x1+x2)/2, my = (y1+y2)/2, dx = x2-x1, dy = y2-y1;
    const dashLen = Math.max(2, 6 - tension * 2);
    ctx.setLineDash([dashLen, dashLen]);
    ctx.lineDashOffset = -(Date.now() / (200 - tension * 80)) % (dashLen * 2);
    ctx.beginPath(); ctx.moveTo(x1, y1);
    ctx.quadraticCurveTo(mx - dy*0.2, my + dx*0.2, x2, y2); ctx.stroke();
    ctx.setLineDash([]); ctx.restore();
  });

  // Nodes (back-to-front)
  const sorted = [...W].sort((a, b) =>
    norm(focusTransform(focusPoint, nodes[b.id].pos)) - norm(focusTransform(focusPoint, nodes[a.id].pos))
  );

  sorted.forEach(w => {
    const [sx, sy] = screenPos[w.id];
    const fp = focusTransform(focusPoint, nodes[w.id].pos);
    const cf = (1 - norm(fp) ** 2) / 2;
    const sz = textSizeForNode(w);
    const col = COL[w.s] || "#666";
    const isH = hoverId === w.id;
    if (sz < 2) return;

    ctx.save();
    const alpha = nodeAlpha(w);

    if (sz >= 11) {
      ctx.globalAlpha = isH ? Math.min(1, alpha + 0.3) : alpha;
      ctx.fillStyle = isH ? "#e8e0d8" : col;
      ctx.font = (cf > 0.2 ? "500 " : "400 ") + Math.round(sz) + "px 'SF Pro Display', 'Helvetica Neue', system-ui, sans-serif";
      ctx.textAlign = "center"; ctx.textBaseline = "middle";
      ctx.fillText(w.t, sx, sy);

      if (w.att && w.att[0] > 0) {
        const [req, sat] = w.att, bw = Math.max(20, sz * 2.5);
        ctx.globalAlpha *= 0.5;
        ctx.fillStyle = "rgba(128,128,128,0.15)";
        ctx.fillRect(sx - bw/2, sy + sz*0.55 + 3, bw, 2);
        ctx.fillStyle = sat >= req ? "#408868" : "#c4a060";
        ctx.fillRect(sx - bw/2, sy + sz*0.55 + 3, bw * Math.min(1, sat/req), 2);
      }

      if (sz >= 16) {
        ctx.globalAlpha = alpha * 0.35;
        ctx.font = "400 9px 'SF Mono', monospace"; ctx.fillStyle = "#888";
        ctx.fillText(w.k, sx, sy - sz * 0.55 - 6);
      }

      if (w.s === "running" && cf > 0.1) {
        ctx.globalAlpha = cf * 0.06; ctx.fillStyle = col;
        ctx.beginPath();
        ctx.arc(sx, sy, sz * 1.8 + Math.sin(Date.now()/600 + sx) * 3, 0, Math.PI*2);
        ctx.fill();
      }

    } else if (sz >= 7) {
      ctx.globalAlpha = isH ? Math.min(1, alpha + 0.3) : alpha * 0.85;
      ctx.fillStyle = col;
      ctx.font = "400 " + Math.round(sz) + "px 'SF Mono', monospace";
      ctx.textAlign = "center"; ctx.textBaseline = "middle";
      ctx.fillText(w.t.split(" ")[0], sx, sy);

    } else if (sz >= 4) {
      ctx.globalAlpha = alpha * 0.7; ctx.fillStyle = col;
      ctx.beginPath(); ctx.arc(sx, sy, 2, 0, Math.PI*2); ctx.fill();
      ctx.font = "400 " + Math.round(sz) + "px monospace";
      ctx.textAlign = "center"; ctx.textBaseline = "middle";
      ctx.fillText(w.t.split(" ").map(s => s[0]).join(""), sx, sy + 5);

    } else {
      ctx.globalAlpha = Math.max(0.2, alpha * 0.6);
      ctx.strokeStyle = col; ctx.lineWidth = 1;
      const halfLen = sz * 2;
      let angle = 0;
      if (w.p && screenPos[w.p]) angle = Math.atan2(sy - screenPos[w.p][1], sx - screenPos[w.p][0]);
      ctx.beginPath();
      ctx.moveTo(sx - Math.cos(angle)*halfLen, sy - Math.sin(angle)*halfLen);
      ctx.lineTo(sx + Math.cos(angle)*halfLen, sy + Math.sin(angle)*halfLen);
      ctx.stroke();
    }
    ctx.restore();

    // Hover detail
    if (isH && sz >= 7) {
      ctx.save();
      ctx.font = "400 10px 'SF Mono', monospace";
      ctx.textAlign = "center"; ctx.textBaseline = "middle";
      ctx.fillStyle = "#888"; ctx.globalAlpha = 0.7;
      const day = 86400000;
      const lines = [`${w.s} · ${w.att[1]}/${w.att[0]} attested · ${w.k} · ${Math.round((Date.now()-w.cr)/day)}d old`];
      if (w.obj) lines.push(w.obj.slice(0, 80));
      if (w.bb && w.bb.length) lines.push("blocked by: " + w.bb.map(id => byId[id]?.t || id).join(", "));
      lines.forEach((line, li) => {
        ctx.fillStyle = li === 0 ? "#888" : li === 1 ? "#777" : "#d06060";
        ctx.fillText(line, sx, sy + sz*0.55 + 16 + li * 13);
      });
      ctx.restore();
    }
  });

  requestAnimationFrame(draw);
}

// ── Interaction ─────────────────────────────────────────────

function hitTest(mx, my) {
  let best = null, bd = Infinity;
  W.forEach(w => {
    const [sx, sy] = diskToScreen(nodes[w.id].pos);
    const sz = textSizeForNode(w);
    if (sz < 5) return;
    const d = Math.sqrt((mx-sx)**2 + (my-sy)**2);
    if (d < Math.max(sz*2, 15) && d < bd) { bd = d; best = w.id; }
  });
  return best;
}

cv.addEventListener("mousemove", ev => {
  const rect = cv.getBoundingClientRect();
  hoverId = hitTest(ev.clientX - rect.left, ev.clientY - rect.top);
  cv.style.cursor = hoverId ? "pointer" : "default";
});

cv.addEventListener("click", ev => {
  const rect = cv.getBoundingClientRect();
  const h = hitTest(ev.clientX - rect.left, ev.clientY - rect.top);
  if (h) setFocus(focusId === h ? (byId[h].p || null) : h);
});

document.addEventListener("keydown", ev => {
  if (ev.key === "Escape" && focusId) setFocus(byId[focusId]?.p || null);
});

document.getElementById("btn-back").addEventListener("click", () => setFocus(null));

cv.addEventListener("touchstart", ev => {
  ev.preventDefault();
  const t = ev.touches[0], rect = cv.getBoundingClientRect();
  const h = hitTest(t.clientX - rect.left, t.clientY - rect.top);
  if (h) setFocus(focusId === h ? (byId[h].p || null) : h);
}, { passive: false });

// ── UI + Init ───────────────────────────────────────────────

function updateUI() {
  const counts = {};
  W.forEach(w => { counts[w.s] = (counts[w.s] || 0) + 1; });
  const parts = [];
  if (counts.running) parts.push(`${counts.running} running`);
  if (counts.done) parts.push(`${counts.done} done`);
  if (counts.blocked) parts.push(`${counts.blocked} blocked`);
  parts.push(`${W.length} total`);
  document.getElementById("stats").textContent = parts.join(" · ");
  document.getElementById("focus-label").textContent = focusId ? ("→ " + byId[focusId].t) : "";
  document.getElementById("btn-back").style.display = focusId ? "inline" : "none";
}

async function refresh() {
  const items = await loadData();
  if (!items) return;
  if (W.length === 0) {
    initSimulation(items);
  } else {
    refreshData(items);
  }
  updateUI();
}

async function boot() {
  const items = await loadData();
  if (items && items.length > 0) {
    initSimulation(items);
  } else {
    // No data — show empty state
    document.getElementById("loading").textContent = "no work items — create some with: cagent inbox \"your idea\"";
    return;
  }
  document.getElementById("loading").style.display = "none";
  document.getElementById("top").style.display = "flex";
  document.getElementById("canvas-wrap").style.display = "flex";
  document.getElementById("bottom").style.display = "flex";
  window.addEventListener("resize", resize);
  resize();
  updateUI();
  draw();
  setInterval(refresh, 15000);
}

boot();
