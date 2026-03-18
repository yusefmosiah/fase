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
    att: reqAtt.length > 0 ? [reqAtt.length, attRecs.length] : [attRecs.length, attRecs.length],
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

async function loadEdges() {
  try {
    const res = await fetch("/api/work/edges", { signal: AbortSignal.timeout(3000) });
    if (res.ok) {
      const data = await res.json();
      dagEdges = Array.isArray(data) ? data : (data.edges || []);
      dagLayout = null; // invalidate cached layout
    }
  } catch (e) { /* optional */ }
}

// ── Detail Loading ──────────────────────────────────────────

let detailCache = {}; // work_id → { data, fetchedAt }
let currentDetail = null; // the currently displayed detail

async function loadDetail(workId) {
  // Return cached if fresh (< 30s)
  const cached = detailCache[workId];
  if (cached && Date.now() - cached.fetchedAt < 30000) return cached.data;

  try {
    const detail = {};

    // Fetch work item details
    const showRes = await fetch(`/api/work/${workId}`, { signal: AbortSignal.timeout(3000) });
    if (showRes.ok) {
      const showData = await showRes.json();
      const w = showData.work || showData;
      detail.objective = w.objective || "";
      detail.kind = w.kind || "";
      detail.state = w.execution_state || "";
      detail.approval = w.approval_state || "";
      detail.notes = (showData.notes || []).slice(0, 5);
      detail.updates = (showData.updates || []).slice(0, 5);
      detail.attestations = (showData.attestations || []).slice(0, 5);
      detail.children = (showData.children || []).slice(0, 10);
      detail.docs = showData.docs || [];
    }

    // Fetch hydration (optional, may fail)
    try {
      const hydrateRes = await fetch(`/api/work/${workId}/hydrate?mode=thin`, { signal: AbortSignal.timeout(5000) });
      if (hydrateRes.ok) {
        const hydration = await hydrateRes.json();
        detail.openQuestions = hydration.open_questions || [];
        detail.nextActions = hydration.next_actions || hydration.recommended_next_actions || [];
        detail.summary = hydration.hydration_summary || hydration.summary || "";
      }
    } catch (e) { /* hydration is optional */ }

    detailCache[workId] = { data: detail, fetchedAt: Date.now() };
    return detail;
  } catch (e) {
    return null;
  }
}

async function loadRuns() {
  try {
    const res = await fetch("/api/runs?limit=80", { signal: AbortSignal.timeout(4000) });
    if (res.ok) {
      const raw = await res.json();
      return Array.isArray(raw) ? raw : (raw.items || raw.runs || []);
    }
  } catch (e) { /* optional */ }
  return [];
}

async function loadRunDetail(jobId) {
  const cached = runCache[jobId];
  if (cached && Date.now() - cached.fetchedAt < 30000) return cached.data;

  try {
    const res = await fetch(`/api/runs/${jobId}`, { signal: AbortSignal.timeout(5000) });
    if (!res.ok) return null;
    const data = await res.json();
    runCache[jobId] = { data, fetchedAt: Date.now() };
    return data;
  } catch (e) {
    return null;
  }
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

// ── DAG Layout ───────────────────────────────────────────────

function computeDagLayout(items, edges) {
  if (!items.length) return null;
  const nodeSet = new Set(items.map(w => w.id));
  const outAdj = new Map(items.map(w => [w.id, []]));
  const inAdj  = new Map(items.map(w => [w.id, []]));

  function addEdge(from, to) {
    if (!nodeSet.has(from) || !nodeSet.has(to) || from === to) return;
    if (!outAdj.get(from).includes(to)) {
      outAdj.get(from).push(to);
      inAdj.get(to).push(from);
    }
  }

  // Parent-child relationships
  items.forEach(w => { if (w.p) addEdge(w.p, w.id); });
  // Edges from API
  edges.forEach(e => {
    if (e.edge_type === "blocks" || e.edge_type === "parent_child" || e.edge_type === "depends_on")
      addEdge(e.from_work_id, e.to_work_id);
  });

  // Kahn's BFS: topological sort + longest-path layer assignment
  const inDeg = new Map(items.map(w => [w.id, inAdj.get(w.id).length]));
  const layer = new Map(items.map(w => [w.id, 0]));
  const queue = items.filter(w => inAdj.get(w.id).length === 0).map(w => w.id);
  let head = 0;
  while (head < queue.length) {
    const id = queue[head++];
    const myLayer = layer.get(id);
    for (const toId of outAdj.get(id)) {
      if (myLayer + 1 > layer.get(toId)) layer.set(toId, myLayer + 1);
      const d = inDeg.get(toId) - 1;
      inDeg.set(toId, d);
      if (d <= 0) queue.push(toId);
    }
  }

  const maxLayer = Math.max(0, ...layer.values());
  const layers = Array.from({ length: maxLayer + 1 }, () => []);
  items.forEach(w => layers[layer.get(w.id) || 0].push(w));
  layers.forEach(l => l.sort((a, b) => a.t.localeCompare(b.t)));

  const NODE_W = 200, NODE_H = 44, H_GAP = 20, V_GAP = 60;
  // Wrap layers to fit available width (prevent horizontal overflow)
  const maxCols = Math.max(2, Math.floor((W_px - 80) / (NODE_W + H_GAP)));
  const positions = new Map();
  let currentY = 0;
  layers.forEach((layerItems) => {
    // Split layer into rows of maxCols
    for (let rowStart = 0; rowStart < layerItems.length; rowStart += maxCols) {
      const row = layerItems.slice(rowStart, rowStart + maxCols);
      const totalW = row.length * (NODE_W + H_GAP) - H_GAP;
      row.forEach((w, idx) => {
        positions.set(w.id, {
          x: idx * (NODE_W + H_GAP) - totalW / 2,
          y: currentY,
        });
      });
      currentY += NODE_H + V_GAP;
    }
  });
  return { positions, outAdj, inAdj, layer, layers, NODE_W, NODE_H };
}

function initDagTransform() {
  if (!dagLayout || !dagLayout.positions.size) return;
  const { positions, NODE_W, NODE_H } = dagLayout;
  let minX = Infinity, maxX = -Infinity, minY = Infinity, maxY = -Infinity;
  positions.forEach(p => {
    minX = Math.min(minX, p.x);     maxX = Math.max(maxX, p.x + NODE_W);
    minY = Math.min(minY, p.y);     maxY = Math.max(maxY, p.y + NODE_H);
  });
  const PAD = 40;
  const dagW = maxX - minX, dagH = maxY - minY;
  const s = Math.min((W_px - PAD * 2) / Math.max(dagW, 1), (H_px - PAD * 2) / Math.max(dagH, 1), 1.4);
  dagTransform.s = s;
  dagTransform.x = W_px / 2 - (minX + dagW / 2) * s;
  dagTransform.y = PAD - minY * s;
}

function rrPath(x, y, w, h, r) {
  ctx.beginPath();
  ctx.moveTo(x + r, y); ctx.lineTo(x + w - r, y);
  ctx.quadraticCurveTo(x + w, y, x + w, y + r);
  ctx.lineTo(x + w, y + h - r);
  ctx.quadraticCurveTo(x + w, y + h, x + w - r, y + h);
  ctx.lineTo(x + r, y + h);
  ctx.quadraticCurveTo(x, y + h, x, y + h - r);
  ctx.lineTo(x, y + r);
  ctx.quadraticCurveTo(x, y, x + r, y);
  ctx.closePath();
}

function dagHitTest(mx, my) {
  if (!dagLayout) return null;
  const { positions, NODE_W, NODE_H } = dagLayout;
  const { x: ox, y: oy, s } = dagTransform;
  const dx = (mx - ox) / s, dy = (my - oy) / s;
  for (const w of W) {
    const pos = positions.get(w.id);
    if (pos && dx >= pos.x && dx <= pos.x + NODE_W && dy >= pos.y && dy <= pos.y + NODE_H)
      return w.id;
  }
  return null;
}

function drawDagView() {
  ctx.clearRect(0, 0, W_px, H_px);
  if (!W.length) return;

  if (!dagLayout) {
    dagLayout = computeDagLayout(W, dagEdges);
    initDagTransform();
  }
  if (!dagLayout) return;

  const { positions, outAdj, NODE_W, NODE_H } = dagLayout;
  const { x: ox, y: oy, s } = dagTransform;

  ctx.save();
  ctx.translate(ox, oy);
  ctx.scale(s, s);

  // Draw edges
  ctx.lineWidth = 1.5;
  W.forEach(w => {
    const from = positions.get(w.id);
    if (!from) return;
    const sx = from.x + NODE_W / 2, sy = from.y + NODE_H;
    for (const toId of (outAdj.get(w.id) || [])) {
      const to = positions.get(toId);
      if (!to) continue;
      const dx2 = to.x + NODE_W / 2, dy2 = to.y;
      const isBlock = dagEdges.some(e => e.from_work_id === w.id && e.to_work_id === toId && e.edge_type === "blocks");
      ctx.beginPath();
      ctx.moveTo(sx, sy);
      ctx.bezierCurveTo(sx, sy + (dy2 - sy) * 0.55, dx2, sy + (dy2 - sy) * 0.45, dx2, dy2 - 7);
      ctx.strokeStyle = isBlock ? "rgba(208,96,96,0.4)" : "rgba(80,80,110,0.4)";
      ctx.stroke();
      // Arrowhead
      ctx.fillStyle = isBlock ? "rgba(208,96,96,0.6)" : "rgba(80,80,110,0.6)";
      ctx.beginPath();
      ctx.moveTo(dx2, dy2); ctx.lineTo(dx2 - 5, dy2 - 10); ctx.lineTo(dx2 + 5, dy2 - 10);
      ctx.closePath(); ctx.fill();
    }
  });

  // Draw nodes
  W.forEach(w => {
    const pos = positions.get(w.id);
    if (!pos) return;
    const { x, y } = pos;
    const col = DAG_COL[w.s] || "#888";
    const isSel = w.id === focusId;
    const isHov = w.id === dagHoverId;
    const filtOut = !stateMatchesFilter(w.s, activeFilter);
    ctx.globalAlpha = filtOut ? 0.15 : 1.0;

    // Background
    rrPath(x, y, NODE_W, NODE_H, 5);
    ctx.fillStyle = isSel ? "rgba(255,255,255,0.07)" : "rgba(26,26,32,0.92)";
    ctx.fill();
    ctx.strokeStyle = isSel ? col : (isHov ? col + "99" : "#252530");
    ctx.lineWidth = isSel ? 1.5 : 1;
    ctx.stroke();

    // Left color bar
    rrPath(x, y, 3, NODE_H, 2);
    ctx.fillStyle = filtOut ? col + "55" : col + "cc";
    ctx.fill();

    // Title
    ctx.fillStyle = isSel ? "#e0d8d0" : (isHov ? "#b0a898" : "#7a7a88");
    ctx.font = `400 11px 'SF Pro Display', 'Helvetica Neue', sans-serif`;
    ctx.textAlign = "left"; ctx.textBaseline = "middle";
    let title = w.t;
    const maxTW = NODE_W - 16;
    while (ctx.measureText(title).width > maxTW && title.length > 5)
      title = title.slice(0, -4) + "…";
    ctx.fillText(title, x + 10, y + NODE_H * 0.43);

    // Kind + state tags
    ctx.font = `400 9px 'SF Mono', monospace`;
    ctx.fillStyle = "#383840";
    ctx.fillText(w.k.toUpperCase().slice(0, 4), x + 10, y + NODE_H - 8);
    ctx.textAlign = "right";
    ctx.fillStyle = col + "88";
    ctx.fillText(w.s, x + NODE_W - 6, y + NODE_H - 8);

    ctx.globalAlpha = 1.0;
    ctx.textAlign = "left";
  });

  ctx.restore();
}

// ── Simulation ──────────────────────────────────────────────

const RHO_STATE = { claimed: 0.45, in_progress: 0.45, blocked: 0.85, ready: 1.35, failed: 2.80, cancelled: 2.80, done: 3.20 };
const DAMPING = { claimed: 2.0, in_progress: 2.0, blocked: 2.5, ready: 3.0, failed: 3.5, cancelled: 3.5, done: 4.0 };
const K_RHO = 0.8, K_TREE = 1.0, L_TREE = 0.85, K_BLOCK = 1.8, L_BLOCK = 0.35;
const K_REP = 0.04, K_WALL = 3.0, RHO_WALL = 4.0, SIGMA_WALL = 0.3;
const DT = 1/60, V_MAX = 0.12;
const COL = {ready:"#c4a060",claimed:"#50b888",in_progress:"#4db884",blocked:"#d06060",done:"#408868",
             failed:"#803030",cancelled:"#555"};

let W = [], byId = {}, BLOCKS = [], nodes = {};
let focusPoint = [0, 0], focusTarget = [0, 0], focusId = null, hoverId = null;
const nodeBBoxes = {}; // work_id → { x1, y1, x2, y2 } screen-space bounding box
let activeFilter = "all"; // "all", "ready", "active", "blocked", "done", "failed"
let appMode = "graph"; // "graph" | "runs"
let runItems = [], runById = {}, currentRunId = null;
let runCache = {}; // job_id → { data, fetchedAt }

// ── View Mode ────────────────────────────────────────────────
let viewMode = "hyperbolic"; // "hyperbolic" | "dag"

const DAG_COL = {
  ready: "#c4a060", in_progress: "#4a90d9", claimed: "#4a90d9",
  done: "#408868", completed: "#408868",
  failed: "#d06060", cancelled: "#803030", blocked: "#888",
};
let dagEdges = [], dagLayout = null;
let dagTransform = { x: 0, y: 0, s: 1.0 };
let dagDrag = null, dagDragMoved = false, dagHoverId = null;

function initSimulation(items) {
  W = items;
  byId = Object.fromEntries(W.map(w => [w.id, w]));
  BLOCKS = [];
  W.forEach(w => {
    if (w.bb) w.bb.forEach(bid => { if (byId[bid]) BLOCKS.push([bid, w.id]); });
  });
  nodes = {};
  // Sort by creation time for temporal ordering
  const sorted = [...W].sort((a, b) => a.cr - b.cr);
  const timeMin = sorted.length ? sorted[0].cr : 0;
  const timeMax = sorted.length ? sorted[sorted.length - 1].cr : 1;
  const timeRange = Math.max(timeMax - timeMin, 1);

  W.forEach((w) => {
    // Temporal angle: oldest at top (12 o'clock), newest clockwise
    const timeFrac = (w.cr - timeMin) / timeRange;
    const angle = -Math.PI / 2 + timeFrac * Math.PI * 2; // start at top, go clockwise
    const r = 0.15 + Math.random() * 0.2;
    nodes[w.id] = {
      pos: [Math.cos(angle) * r, Math.sin(angle) * r],
      vel: [0, 0],
      depth: 0,
      temporalAngle: angle, // preferred angular position
    };
  });
  W.forEach(w => {
    let d = 0, cur = w;
    while (cur.p && byId[cur.p]) { d++; cur = byId[cur.p]; }
    nodes[w.id].depth = d;
  });
  computeDagDepths();
}

function computeDagDepths() {
  // Kahn's algorithm: compute topological depth from blocking edges + API edges
  const nodeSet = new Set(W.map(w => w.id));
  const outAdj = new Map(W.map(w => [w.id, []]));
  const inAdj  = new Map(W.map(w => [w.id, []]));
  function addE(from, to) {
    if (!nodeSet.has(from) || !nodeSet.has(to) || from === to) return;
    if (!outAdj.get(from).includes(to)) { outAdj.get(from).push(to); inAdj.get(to).push(from); }
  }
  W.forEach(w => { if (w.bb) w.bb.forEach(bid => addE(bid, w.id)); });
  dagEdges.forEach(e => {
    if (e.edge_type === "blocks" || e.edge_type === "parent_child" || e.edge_type === "depends_on")
      addE(e.from_work_id, e.to_work_id);
  });
  const inDeg = new Map(W.map(w => [w.id, inAdj.get(w.id).length]));
  const layer = new Map(W.map(w => [w.id, 0]));
  const queue = W.filter(w => inAdj.get(w.id).length === 0).map(w => w.id);
  let head = 0;
  while (head < queue.length) {
    const id = queue[head++];
    const myLayer = layer.get(id);
    for (const toId of outAdj.get(id)) {
      if (myLayer + 1 > layer.get(toId)) layer.set(toId, myLayer + 1);
      const d = inDeg.get(toId) - 1; inDeg.set(toId, d);
      if (d <= 0) queue.push(toId);
    }
  }
  // Also compute fan-out (how many items transitively depend on this)
  const maxDepth = Math.max(1, ...layer.values());
  W.forEach(w => {
    if (nodes[w.id]) {
      nodes[w.id].dagDepth = layer.get(w.id) || 0;
      nodes[w.id].dagMaxDepth = maxDepth;
      nodes[w.id].dagFanOut = outAdj.get(w.id).length;
      // Assign angular sector by dependency chain root
      let root = w.id, cur = w.id;
      while (inAdj.get(cur) && inAdj.get(cur).length > 0) { root = inAdj.get(cur)[0]; cur = root; }
      nodes[w.id].dagChainRoot = root;
    }
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
  computeDagDepths();
}

function preferredRho(w) {
  const n = nodes[w.id];
  const stale = Math.min(1, Math.max(0, (Date.now() - w.up) / (14 * 86400000)));
  const attDef = w.att && w.att[0] > 0 ? 1 - w.att[1] / w.att[0] : 0;
  const stateRho = (RHO_STATE[w.s] || 1.5);
  if (n.dagDepth !== undefined && n.dagMaxDepth > 0) {
    // Has DAG structure: depth drives radial position
    const dagRho = 0.4 + (n.dagDepth / n.dagMaxDepth) * 2.5;
    return 0.6 * dagRho + 0.4 * stateRho + 0.2 * stale - 0.15 * attDef;
  }
  // No DAG edges: fall back to state-based placement
  return stateRho + 0.1 * n.depth + 0.4 * stale - 0.2 * attDef;
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

    // 6. Angular spring — fixed target angles, no chasing moving points
    if (rhoI > 0.05) {
      const currentAngle = Math.atan2(pi[1], pi[0]);
      // Use a stable target: chain root's TEMPORAL angle (fixed at init), not its current position
      let targetAngle = ni.temporalAngle || 0;
      if (ni.dagChainRoot && ni.dagChainRoot !== w.id && nodes[ni.dagChainRoot]) {
        targetAngle = nodes[ni.dagChainRoot].temporalAngle || targetAngle;
      }
      let angleDiff = targetAngle - currentAngle;
      while (angleDiff > Math.PI) angleDiff -= 2 * Math.PI;
      while (angleDiff < -Math.PI) angleDiff += 2 * Math.PI;
      const tangent = [-Math.sin(currentAngle), Math.cos(currentAngle)];
      const K_ANGLE = 0.25;
      F = add(F, scale(tangent, K_ANGLE * angleDiff));
    }

    // 7. Activity noise
    if (w.s === "claimed" || w.s === "in_progress") F = add(F, [(Math.random()-0.5)*0.006, (Math.random()-0.5)*0.006]);

    // Integrate — fast initial snap, then decelerate
    const baseGamma = DAMPING[w.s] || 2.0;
    const vn0 = norm(ni.vel);
    // Adaptive damping: increases as velocity drops (fast start, gentle settle)
    const gamma = baseGamma + 4.0 * (1.0 - Math.min(vn0 / V_MAX, 1.0));
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
  const panel = document.getElementById("detail-panel");
  const canvasWrap = document.getElementById("canvas-wrap");

  if (!id) {
    focusTarget = [0, 0];
    currentDetail = null;
    panel.classList.remove("open");
    canvasWrap.classList.remove("has-detail");
    resize();
  } else {
    panel.classList.add("open");
    canvasWrap.classList.add("has-detail");
    resize();
    // Show title immediately
    const w = byId[id];
    if (w) {
      document.getElementById("detail-kind").textContent = w.k;
      document.getElementById("detail-title").textContent = w.t;
      document.getElementById("detail-meta").innerHTML =
        `<span class="state state-${w.s}">${w.s}</span>` +
        (w.approval !== "none" ? `<span class="state">${w.approval}</span>` : "") +
        `<span style="color:#555">${w.att[1]}/${w.att[0]} attested</span>`;
      document.getElementById("detail-body").innerHTML = '<div style="color:#555;padding:20px 0">loading...</div>';
    }
    // Fetch full detail + live diff
    loadDetail(id).then(async detail => {
      if (focusId === id && detail) {
        currentDetail = detail;
        renderDetailPanel(detail, byId[id]);
        // Append live diff if available
        try {
          const diffRes = await fetch("/api/diff", { signal: AbortSignal.timeout(3000) });
          if (diffRes.ok) {
            const { diff } = await diffRes.json();
            if (diff && focusId === id) appendDiffToPanel(diff);
          }
        } catch (e) { /* diff is optional */ }
      }
    });
  }
  updateUI();
}

function setAppMode(mode) {
  appMode = mode;
  const body = document.body;
  const runsMode = appMode === "runs";
  body.classList.toggle("runs-mode", runsMode);

  const buttons = ["btn-dag-view", "btn-hyp-view", "btn-diff", "btn-bash"];
  buttons.forEach(id => {
    const el = document.getElementById(id);
    if (el) el.style.display = runsMode ? "none" : "";
  });
  const runsBtn = document.getElementById("btn-runs-view");
  if (runsBtn) runsBtn.classList.toggle("view-btn-active", runsMode);
  const dagBtn = document.getElementById("btn-dag-view");
  const hypBtn = document.getElementById("btn-hyp-view");
  if (dagBtn) dagBtn.classList.toggle("view-btn-active", !runsMode && viewMode === "dag");
  if (hypBtn) hypBtn.classList.toggle("view-btn-active", !runsMode && viewMode === "hyperbolic");
  document.getElementById("bottom").style.display = runsMode ? "none" : "flex";
  document.getElementById("btn-back").textContent = runsMode ? "← list" : "← overview";

  if (runsMode) {
    focusId = null;
    focusTarget = [0, 0];
    currentDetail = null;
    const panel = document.getElementById("detail-panel");
    const canvasWrap = document.getElementById("canvas-wrap");
    panel.classList.add("open");
    canvasWrap.classList.add("has-detail");
    resize();
    renderRunSidebar();
    updateRunStats();
  } else {
    const panel = document.getElementById("detail-panel");
    const canvasWrap = document.getElementById("canvas-wrap");
    panel.classList.remove("open");
    canvasWrap.classList.remove("has-detail");
    currentDetail = null;
    currentRunId = null;
    renderSidebar();
    updateUI();
    resize();
  }
}

function openRun(jobId) {
  currentRunId = jobId || null;
  const panel = document.getElementById("detail-panel");
  const canvasWrap = document.getElementById("canvas-wrap");

  if (!currentRunId) {
    panel.classList.remove("open");
    canvasWrap.classList.remove("has-detail");
    document.getElementById("detail-kind").textContent = "";
    document.getElementById("detail-title").textContent = "";
    document.getElementById("detail-meta").innerHTML = "";
    document.getElementById("detail-body").innerHTML = '<div style="color:#555;padding:20px 0">Select a run to see details.</div>';
    resize();
    renderRunSidebar();
    updateRunStats();
    return;
  }

  panel.classList.add("open");
  canvasWrap.classList.add("has-detail");
  resize();
  const item = runById[currentRunId];
  if (item) {
    document.getElementById("detail-kind").textContent = "RUN";
    document.getElementById("detail-title").textContent = item.work_title || item.work_id || item.job_id;
    document.getElementById("detail-meta").innerHTML = `<span class="state state-${statusClass(item.status)}">${escapeHtml(item.status || item.job_state || "unknown")}</span>` +
      (item.adapter ? `<span class="state">${escapeHtml(item.adapter)}${item.model ? ` / ${escapeHtml(item.model)}` : ""}</span>` : "") +
      `<span style="color:#555">${formatDuration(item.duration_ms)}</span>`;
    document.getElementById("detail-body").innerHTML = '<div style="color:#555;padding:20px 0">loading...</div>';
  }

  loadRunDetail(currentRunId).then(detail => {
    if (!detail || currentRunId !== jobId) return;
    renderRunDetailPanel(detail);
  });
}

// ── Rendering ───────────────────────────────────────────────

const cv = document.getElementById("cv");
const ctx = cv.getContext("2d");
let W_px, H_px, CX, CY, R;

function resize() {
  const wrap = document.getElementById("canvas-wrap");
  const rect = wrap.getBoundingClientRect();
  const dpr = window.devicePixelRatio || 1;
  // Canvas fills the full viewport rectangle
  W_px = rect.width;
  H_px = rect.height;
  cv.width = W_px * dpr; cv.height = H_px * dpr;
  cv.style.width = W_px + "px"; cv.style.height = H_px + "px";
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  // Center of canvas
  CX = W_px / 2;
  CY = H_px / 2;
  // Disk radius uses the smaller dimension (stays circular)
  R = Math.min(W_px, H_px) / 2 * 0.85;
}

function diskToScreen(p) {
  const fp = focusTransform(focusPoint, p);
  return [CX + fp[0] * R, CY + fp[1] * R];
}

function textSizeForNode(w) {
  const fp = focusTransform(focusPoint, nodes[w.id].pos);
  const r = norm(fp);
  const soft = 0.15 + 0.85 * (1 - r * r) / 2;
  const n = nodes[w.id];
  // Size by DAG importance: items blocking many others are larger
  const fanOut = n.dagFanOut || 0;
  const dagBoost = 1 + Math.min(fanOut * 0.15, 0.6); // up to 60% larger
  const baseSize = n.dagDepth === 0 ? 24 : (fanOut > 0 ? 18 : 14);
  return Math.max(5, baseSize * soft * 1.8 * dagBoost);
}

function stateMatchesFilter(state, filter) {
  if (filter === "all") return true;
  if (filter === "active") return state === "claimed" || state === "in_progress";
  if (filter === "done") return state === "done" || state === "completed";
  if (filter === "failed") return state === "failed" || state === "cancelled";
  return state === filter;
}

function nodeAlpha(w) {
  const fp = focusTransform(focusPoint, nodes[w.id].pos);
  const cf = (1 - norm(fp) ** 2) / 2;
  const base = Math.max(0.18, Math.min(1, cf * 2.2 + 0.1));
  if (!stateMatchesFilter(w.s, activeFilter)) return base * 0.12;
  return base;
}

function draw() {
  if (appMode === "runs") {
    requestAnimationFrame(draw);
    return;
  }
  if (viewMode === "dag") { drawDagView(); requestAnimationFrame(draw); return; }
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
      { const tw = ctx.measureText(w.t).width; const pad = Math.max(6, sz * 0.3);
        nodeBBoxes[w.id] = { x1: sx - tw/2 - pad, y1: sy - sz/2 - pad, x2: sx + tw/2 + pad, y2: sy + sz/2 + pad }; }

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

      if ((w.s === "claimed" || w.s === "in_progress") && cf > 0.1) {
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
      { const tw = ctx.measureText(w.t.split(" ")[0]).width; const pad = Math.max(6, sz * 0.3);
        nodeBBoxes[w.id] = { x1: sx - tw/2 - pad, y1: sy - sz/2 - pad, x2: sx + tw/2 + pad, y2: sy + sz/2 + pad }; }

    } else if (sz >= 4) {
      ctx.globalAlpha = alpha * 0.7; ctx.fillStyle = col;
      ctx.beginPath(); ctx.arc(sx, sy, 2, 0, Math.PI*2); ctx.fill();
      ctx.font = "400 " + Math.round(sz) + "px monospace";
      ctx.textAlign = "center"; ctx.textBaseline = "middle";
      const abbr = w.t.split(" ").map(s => s[0]).join("");
      ctx.fillText(abbr, sx, sy + 5);
      { const tw = ctx.measureText(abbr).width; const pad = Math.max(8, sz);
        nodeBBoxes[w.id] = { x1: sx - tw/2 - pad, y1: sy - pad, x2: sx + tw/2 + pad, y2: sy + sz + pad }; }

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
    const bb = nodeBBoxes[w.id];
    if (!bb) return;
    if (mx < bb.x1 || mx > bb.x2 || my < bb.y1 || my > bb.y2) return;
    // Use distance to center for tie-breaking among overlapping labels
    const [sx, sy] = diskToScreen(nodes[w.id].pos);
    const d = Math.sqrt((mx-sx)**2 + (my-sy)**2);
    if (d < bd) { bd = d; best = w.id; }
  });
  return best;
}

cv.addEventListener("mousemove", ev => {
  const rect = cv.getBoundingClientRect();
  const mx = ev.clientX - rect.left, my = ev.clientY - rect.top;
  if (viewMode === "dag") {
    if (dagDrag) {
      const dx = ev.clientX - dagDrag.sx, dy = ev.clientY - dagDrag.sy;
      if (Math.abs(dx) + Math.abs(dy) > 3) dagDragMoved = true;
      dagTransform.x = dagDrag.ox + dx;
      dagTransform.y = dagDrag.oy + dy;
    }
    dagHoverId = dagHitTest(mx, my);
    cv.style.cursor = dagDrag ? "grabbing" : (dagHoverId ? "pointer" : "grab");
  } else {
    hoverId = hitTest(mx, my);
    cv.style.cursor = hoverId ? "pointer" : "default";
  }
});

cv.addEventListener("mousedown", ev => {
  if (viewMode !== "dag") return;
  dagDrag = { sx: ev.clientX, sy: ev.clientY, ox: dagTransform.x, oy: dagTransform.y };
  dagDragMoved = false;
  cv.style.cursor = "grabbing";
});

cv.addEventListener("mouseup", () => { dagDrag = null; });
cv.addEventListener("mouseleave", () => { dagDrag = null; });

cv.addEventListener("wheel", ev => {
  if (viewMode !== "dag") return;
  ev.preventDefault();
  const rect = cv.getBoundingClientRect();
  const mx = ev.clientX - rect.left, my = ev.clientY - rect.top;
  const factor = ev.deltaY < 0 ? 1.12 : 0.89;
  const newS = Math.max(0.1, Math.min(5, dagTransform.s * factor));
  dagTransform.x = mx + (dagTransform.x - mx) * (newS / dagTransform.s);
  dagTransform.y = my + (dagTransform.y - my) * (newS / dagTransform.s);
  dagTransform.s = newS;
}, { passive: false });

// Touch pinch-to-zoom for DAG on mobile
let dagTouchDist = null;
let dagTouchCenter = null;
cv.addEventListener("touchstart", ev => {
  if (viewMode !== "dag") return;
  if (ev.touches.length === 2) {
    ev.preventDefault();
    const dx = ev.touches[0].clientX - ev.touches[1].clientX;
    const dy = ev.touches[0].clientY - ev.touches[1].clientY;
    dagTouchDist = Math.sqrt(dx * dx + dy * dy);
    const rect = cv.getBoundingClientRect();
    dagTouchCenter = {
      x: (ev.touches[0].clientX + ev.touches[1].clientX) / 2 - rect.left,
      y: (ev.touches[0].clientY + ev.touches[1].clientY) / 2 - rect.top,
    };
  } else if (ev.touches.length === 1) {
    dagDrag = { sx: ev.touches[0].clientX, sy: ev.touches[0].clientY, ox: dagTransform.x, oy: dagTransform.y };
    dagDragMoved = false;
  }
}, { passive: false });
cv.addEventListener("touchmove", ev => {
  if (viewMode !== "dag") return;
  if (ev.touches.length === 2 && dagTouchDist) {
    ev.preventDefault();
    const dx = ev.touches[0].clientX - ev.touches[1].clientX;
    const dy = ev.touches[0].clientY - ev.touches[1].clientY;
    const newDist = Math.sqrt(dx * dx + dy * dy);
    const factor = newDist / dagTouchDist;
    const newS = Math.max(0.1, Math.min(5, dagTransform.s * factor));
    dagTransform.x = dagTouchCenter.x + (dagTransform.x - dagTouchCenter.x) * (newS / dagTransform.s);
    dagTransform.y = dagTouchCenter.y + (dagTransform.y - dagTouchCenter.y) * (newS / dagTransform.s);
    dagTransform.s = newS;
    dagTouchDist = newDist;
  } else if (ev.touches.length === 1 && dagDrag) {
    const dx = ev.touches[0].clientX - dagDrag.sx, dy = ev.touches[0].clientY - dagDrag.sy;
    if (Math.abs(dx) + Math.abs(dy) > 3) dagDragMoved = true;
    dagTransform.x = dagDrag.ox + dx;
    dagTransform.y = dagDrag.oy + dy;
  }
}, { passive: false });
cv.addEventListener("touchend", () => { dagDrag = null; dagTouchDist = null; });

cv.addEventListener("click", ev => {
  if (viewMode === "dag" && dagDragMoved) return;
  const rect = cv.getBoundingClientRect();
  const mx = ev.clientX - rect.left, my = ev.clientY - rect.top;
  if (viewMode === "dag") {
    const h = dagHitTest(mx, my);
    if (h) setFocus(focusId === h ? null : h);
  } else {
    const h = hitTest(mx, my);
    if (h) setFocus(focusId === h ? (byId[h].p || null) : h);
  }
});

document.addEventListener("keydown", ev => {
  if (ev.key !== "Escape") return;
  if (appMode === "runs" && currentRunId) {
    openRun(null);
    return;
  }
  if (focusId) setFocus(viewMode === "dag" ? null : (byId[focusId]?.p || null));
});

document.getElementById("btn-back").addEventListener("click", () => {
  if (appMode === "runs") {
    openRun(null);
  } else {
    setFocus(null);
  }
});

cv.addEventListener("touchstart", ev => {
  ev.preventDefault();
  const t = ev.touches[0], rect = cv.getBoundingClientRect();
  if (viewMode === "dag") {
    const h = dagHitTest(t.clientX - rect.left, t.clientY - rect.top);
    if (h) setFocus(focusId === h ? null : h);
  } else {
    const h = hitTest(t.clientX - rect.left, t.clientY - rect.top);
    if (h) setFocus(focusId === h ? (byId[h].p || null) : h);
  }
}, { passive: false });

// ── Detail Panel Rendering ──────────────────────────────────

function renderDetailPanel(detail, w) {
  const body = document.getElementById("detail-body");
  let html = "";

  // Objective
  if (detail.objective) {
    html += `<div class="detail-section">
      <div class="detail-section-label">Objective</div>
      <div class="detail-content">${renderMarkdown(detail.objective)}</div>
    </div>`;
  }

  // Hydration summary
  if (detail.summary) {
    html += `<div class="detail-section">
      <div class="detail-section-label">Summary</div>
      <div class="detail-content">${renderMarkdown(detail.summary)}</div>
    </div>`;
  }

  // Open questions
  if (detail.openQuestions && detail.openQuestions.length > 0) {
    html += `<div class="detail-section">
      <div class="detail-section-label">Open Questions</div>`;
    for (const q of detail.openQuestions) {
      html += `<div class="detail-list-item"><span class="item-prefix">?</span>${escapeHtml(q)}</div>`;
    }
    html += `</div>`;
  }

  // Next actions
  if (detail.nextActions && detail.nextActions.length > 0) {
    html += `<div class="detail-section">
      <div class="detail-section-label">Next Actions</div>`;
    for (const a of detail.nextActions) {
      html += `<div class="detail-list-item"><span class="item-prefix-green">→</span>${escapeHtml(a)}</div>`;
    }
    html += `</div>`;
  }

  // Notes
  if (detail.notes && detail.notes.length > 0) {
    html += `<div class="detail-section">
      <div class="detail-section-label">Notes</div>`;
    for (const note of detail.notes) {
      const text = note.text || note.content || note.body || "";
      const type = note.note_type || note.type || "";
      html += `<div class="detail-list-item">`;
      if (type) html += `<span style="color:#555;font-family:'SF Mono',monospace;font-size:10px;margin-right:6px">${type}</span>`;
      html += `<div class="detail-content" style="margin-top:4px">${renderMarkdown(text)}</div></div>`;
    }
    html += `</div>`;
  }

  // Updates
  if (detail.updates && detail.updates.length > 0) {
    html += `<div class="detail-section">
      <div class="detail-section-label">Updates</div>`;
    for (const u of detail.updates) {
      const msg = u.message || u.text || "";
      const by = u.created_by || "";
      html += `<div class="detail-list-item">`;
      if (by) html += `<span style="color:#555;font-size:11px">${escapeHtml(by)}</span> `;
      html += `${escapeHtml(msg)}</div>`;
    }
    html += `</div>`;
  }

  // Attestations
  if (detail.attestations && detail.attestations.length > 0) {
    html += `<div class="detail-section">
      <div class="detail-section-label">Attestations</div>`;
    for (const a of detail.attestations) {
      const result = a.result || "?";
      const kind = a.verifier_kind || "";
      const summary = a.summary || "";
      const color = result === "passed" ? "#408868" : result === "failed" ? "#d06060" : "#888";
      html += `<div class="detail-list-item">
        <span style="color:${color};font-weight:500">${escapeHtml(result)}</span>
        <span style="color:#666;margin:0 6px">·</span>
        <span style="color:#888">${escapeHtml(kind)}</span>
        ${summary ? `<div style="color:#777;font-size:12px;margin-top:2px">${escapeHtml(summary)}</div>` : ""}
      </div>`;
    }
    html += `</div>`;
  }

  // Children
  if (detail.children && detail.children.length > 0) {
    html += `<div class="detail-section">
      <div class="detail-section-label">Children</div>`;
    for (const c of detail.children) {
      const title = c.title || c.work_id || "";
      const state = c.execution_state || "";
      html += `<div class="detail-list-item">
        <span class="state state-${state}" style="font-size:10px;padding:1px 6px;margin-right:6px">${state}</span>
        ${escapeHtml(title)}
      </div>`;
    }
    html += `</div>`;
  }

  // Doc content (full documents stored in the work graph)
  if (detail.docs && detail.docs.length > 0) {
    for (const doc of detail.docs) {
      const docTitle = doc.title || doc.path || "Document";
      const docPath = doc.path || "";
      html += `<div class="detail-section">
        <div class="detail-section-label">${escapeHtml(docTitle)}${docPath ? ` <span style="opacity:0.4;font-weight:400">${escapeHtml(docPath)}</span>` : ""}</div>
        <div class="detail-content">${renderMarkdown(doc.body || "")}</div>
      </div>`;
    }
  }

  if (!html) {
    html = '<div style="color:#555;padding:20px 0">No details available yet.</div>';
  }

  body.innerHTML = html;
}

function renderRunDetailPanel(detail) {
  const body = document.getElementById("detail-body");
  if (!detail) {
    body.innerHTML = '<div style="color:#d06060;padding:20px 0">Failed to load run details.</div>';
    return;
  }

  const work = detail.work || {};
  let html = "";

  if (detail.objective || work.objective) {
    html += `<div class="detail-section">
      <div class="detail-section-label">Objective</div>
      <div class="detail-content">${renderMarkdown(detail.objective || work.objective || "")}</div>
    </div>`;
  }

  if (detail.updates && detail.updates.length > 0) {
    html += `<div class="detail-section">
      <div class="detail-section-label">Run Updates</div>`;
    for (const update of detail.updates) {
      const msg = update.message || "";
      const by = update.created_by || "";
      const at = update.created_at ? formatDateTime(update.created_at) : "";
      html += `<div class="detail-list-item">`;
      if (at || by) {
        html += `<div style="color:#555;font-family:'SF Mono',monospace;font-size:10px;margin-bottom:4px">${escapeHtml([at, by].filter(Boolean).join(" · "))}</div>`;
      }
      html += `<div class="detail-content">${renderMarkdown(msg)}</div></div>`;
    }
    html += `</div>`;
  }

  if (detail.notes && detail.notes.length > 0) {
    html += `<div class="detail-section">
      <div class="detail-section-label">Run Notes</div>`;
    for (const note of detail.notes) {
      const text = note.body || note.text || note.content || "";
      const type = note.note_type || note.type || "";
      const by = note.created_by || "";
      const at = note.created_at ? formatDateTime(note.created_at) : "";
      html += `<div class="detail-list-item">`;
      if (type || at || by) {
        html += `<div style="color:#555;font-family:'SF Mono',monospace;font-size:10px;margin-bottom:4px">${escapeHtml([type, at, by].filter(Boolean).join(" · "))}</div>`;
      }
      html += `<div class="detail-content">${renderMarkdown(text)}</div></div>`;
    }
    html += `</div>`;
  }

  if (detail.attestation) {
    const att = detail.attestation;
    const color = att.result === "passed" ? "#408868" : att.result === "failed" ? "#d06060" : "#888";
    html += `<div class="detail-section">
      <div class="detail-section-label">Attestation</div>
      <div class="detail-list-item">
        <span style="color:${color};font-weight:500">${escapeHtml(att.result || "unknown")}</span>
        <span style="color:#666;margin:0 6px">·</span>
        <span style="color:#888">${escapeHtml(att.verifier_kind || "attestation")}</span>
        ${att.summary ? `<div style="color:#777;font-size:12px;margin-top:2px">${escapeHtml(att.summary)}</div>` : ""}
      </div>
    </div>`;
  }

  if (detail.bash_log && detail.bash_log.length > 0) {
    html += `<div class="detail-section">
      <div class="detail-section-label">Bash Command Log</div>
      <div style="font-family:'SF Mono',monospace;font-size:12px;line-height:1.5;">`;
    for (const entry of detail.bash_log) {
      if (entry.comment) {
        html += `<div style="color:#555;padding:4px 0;font-style:italic"># ${escapeHtml(entry.comment)}</div>`;
      } else {
        const mark = entry.exit_code === 0 ? '<span style="color:#50b888">✓</span>' : '<span style="color:#d06060">✗</span>';
        html += `<div style="padding:2px 0">${mark} <span style="color:#c4a060">$</span> <span style="color:#c8c0b8">${escapeHtml(entry.command || "")}</span></div>`;
        if (entry.output_preview) {
          html += `<div style="color:#555;padding:0 0 4px 20px;font-size:11px;white-space:pre-wrap;max-height:100px;overflow:hidden">${escapeHtml(entry.output_preview)}</div>`;
        }
      }
    }
    html += `</div></div>`;
  }

  if (!html) {
    html = '<div style="color:#555;padding:20px 0">No run details available yet.</div>';
  }

  body.innerHTML = html;
}

// Simple markdown → HTML (no dependencies)
function renderMarkdown(text) {
  if (!text) return "";
  let html = escapeHtml(text);

  // Code blocks (``` ... ```)
  html = html.replace(/```(\w*)\n([\s\S]*?)```/g, '<pre><code>$2</code></pre>');
  // Inline code
  html = html.replace(/`([^`]+)`/g, '<code>$1</code>');
  // Headers
  html = html.replace(/^### (.+)$/gm, '<h3>$1</h3>');
  html = html.replace(/^## (.+)$/gm, '<h2>$1</h2>');
  html = html.replace(/^# (.+)$/gm, '<h1>$1</h1>');
  // Bold
  html = html.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');
  // Italic
  html = html.replace(/\*([^*]+)\*/g, '<em>$1</em>');
  // Blockquotes
  html = html.replace(/^&gt; (.+)$/gm, '<blockquote>$1</blockquote>');
  // Unordered lists
  html = html.replace(/^- (.+)$/gm, '<li>$1</li>');
  html = html.replace(/(<li>.*<\/li>\n?)+/g, '<ul>$&</ul>');
  // Ordered lists
  html = html.replace(/^\d+\. (.+)$/gm, '<li>$1</li>');
  // Links
  html = html.replace(/\[([^\]]+)\]\(([^)]+)\)/g, '<a href="$2">$1</a>');
  // Paragraphs (double newlines)
  html = html.replace(/\n\n+/g, '</p><p>');
  // Single newlines within paragraphs
  html = html.replace(/\n/g, '<br>');

  return `<p>${html}</p>`;
}

function escapeHtml(str) {
  return str
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

function formatDuration(ms) {
  if (!Number.isFinite(ms) || ms < 0) return "—";
  const totalSeconds = Math.max(0, Math.floor(ms / 1000));
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const seconds = totalSeconds % 60;
  if (hours > 0) return `${hours}h ${String(minutes).padStart(2, "0")}m`;
  if (minutes > 0) return `${minutes}m ${String(seconds).padStart(2, "0")}s`;
  return `${seconds}s`;
}

function formatDateTime(value) {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return date.toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
  });
}

function statusClass(status) {
  const value = String(status || "").toLowerCase();
  if (value === "passed") return "done";
  if (value === "failed") return "failed";
  if (value === "blocked") return "blocked";
  if (value === "cancelled") return "failed";
  if (value === "completed") return "done";
  if (value === "running" || value === "in_progress" || value === "claimed") return "claimed";
  return value || "ready";
}

function updateRunStats() {
  const counts = { passed: 0, failed: 0, running: 0, other: 0 };
  runItems.forEach(item => {
    const status = String(item.status || item.job_state || "").toLowerCase();
    if (status === "passed") counts.passed++;
    else if (status === "failed" || status === "cancelled") counts.failed++;
    else if (status === "running" || status === "claimed" || status === "in_progress" || status === "queued" || status === "starting") counts.running++;
    else counts.other++;
  });
  const parts = [];
  if (counts.passed) parts.push(`${counts.passed} passed`);
  if (counts.failed) parts.push(`${counts.failed} failed`);
  if (counts.running) parts.push(`${counts.running} active`);
  parts.push(`${runItems.length} runs`);
  document.getElementById("stats").textContent = parts.join(" · ");
  document.getElementById("focus-label").textContent = currentRunId ? `→ ${runById[currentRunId]?.work_title || currentRunId}` : "";
}

// ── Sidebar ─────────────────────────────────────────────────

function renderSidebar() {
  const sidebar = document.getElementById("sidebar");
  if (!sidebar) return;

  if (appMode === "runs") {
    renderRunSidebar();
    return;
  }

  // Group by state for ordering
  const stateOrder = ["claimed", "in_progress", "blocked", "ready", "done", "completed", "failed", "cancelled"];
  const sorted = [...W].sort((a, b) => {
    const sa = stateOrder.indexOf(a.s), sb = stateOrder.indexOf(b.s);
    if (sa !== sb) return sa - sb;
    return a.t.localeCompare(b.t);
  });

  sidebar.innerHTML = sorted.map(w => {
    const col = COL[w.s] || "#666";
    const filtered = !stateMatchesFilter(w.s, activeFilter);
    return `<div class="sidebar-item ${w.id === focusId ? 'active' : ''} ${filtered ? 'filtered-out' : ''}" data-id="${w.id}">
      <span class="item-dot" style="background:${col}"></span>
      <span class="item-title">${escapeHtml(w.t)}</span>
      <span class="item-kind">${w.k.slice(0, 4)}</span>
    </div>`;
  }).join("");

  sidebar.querySelectorAll(".sidebar-item").forEach(el => {
    el.addEventListener("click", () => {
      const id = el.dataset.id;
      setFocus(focusId === id ? null : id);
      renderSidebar(); // update active state
      // Close mobile sidebar
      sidebar.classList.remove("mobile-open");
    });
  });
}

function renderRunSidebar() {
  const sidebar = document.getElementById("sidebar");
  if (!sidebar) return;

  if (!runItems.length) {
    sidebar.innerHTML = `<div style="padding:12px;color:#555;font-size:12px;line-height:1.5;">
      <div style="color:#c8c0b8;margin-bottom:6px">Recent runs</div>
      No runs available yet.
    </div>`;
    return;
  }

  sidebar.innerHTML = runItems.map(item => {
    const active = item.job_id === currentRunId ? "active" : "";
    const status = item.status || item.job_state || "unknown";
    const statClass = statusClass(status);
    const filesChanged = item.files_changed == null ? "—" : item.files_changed;
    const loc = item.lines_added == null && item.lines_removed == null
      ? "—"
      : `+${item.lines_added ?? 0} / -${item.lines_removed ?? 0}`;
    return `<div class="run-item ${active}" data-job-id="${escapeHtml(item.job_id)}">
      <div class="run-title">${escapeHtml(item.work_title || item.job_id)}</div>
      <div class="run-subtitle">${escapeHtml(item.adapter || "unknown")}${item.model ? ` / ${escapeHtml(item.model)}` : ""}</div>
      <div class="run-meta">
        <span>${escapeHtml(formatDuration(item.duration_ms))}</span>
        <span>${escapeHtml(String(filesChanged))} files</span>
        <span>${escapeHtml(loc)}</span>
        <span class="state state-${statClass}">${escapeHtml(status)}</span>
      </div>
    </div>`;
  }).join("");

  sidebar.querySelectorAll(".run-item").forEach(el => {
    el.addEventListener("click", () => {
      const jobId = el.dataset.jobId;
      openRun(currentRunId === jobId ? null : jobId);
      renderRunSidebar();
      sidebar.classList.remove("mobile-open");
    });
  });
}

// Mobile toggle
document.getElementById("sidebar-toggle")?.addEventListener("click", () => {
  document.getElementById("sidebar")?.classList.toggle("mobile-open");
});

// ── Diff Rendering ──────────────────────────────────────────

function parseDiffFiles(raw) {
  const files = [];
  let current = null;
  for (const line of raw.split('\n')) {
    if (line.startsWith('diff --git ')) {
      if (current) files.push(current);
      const m = line.match(/^diff --git a\/.+ b\/(.+)$/);
      current = { filename: m ? m[1] : line, added: 0, removed: 0, lines: [line] };
    } else if (current) {
      current.lines.push(line);
      if (line.startsWith('+') && !line.startsWith('+++')) current.added++;
      else if (line.startsWith('-') && !line.startsWith('---')) current.removed++;
    }
  }
  if (current) files.push(current);
  return files;
}

function renderDiffFileLines(lines) {
  let html = '<pre style="font-size:11px;line-height:1.5;overflow-x:auto;background:rgba(0,0,0,0.3);padding:10px 12px;border-radius:0 0 6px 6px;border:1px solid #1a1a1e;border-top:none;margin:0;">';
  for (const line of lines) {
    if (line.startsWith('+++') || line.startsWith('---')) {
      html += `<span style="color:#888">${escapeHtml(line)}</span>\n`;
    } else if (line.startsWith('+')) {
      html += `<span style="color:#50b888">${escapeHtml(line)}</span>\n`;
    } else if (line.startsWith('-')) {
      html += `<span style="color:#d06060">${escapeHtml(line)}</span>\n`;
    } else if (line.startsWith('@@')) {
      html += `<span style="color:#c4a060">${escapeHtml(line)}</span>\n`;
    } else if (line.startsWith('diff ')) {
      html += `<span style="color:#7aa2f7;font-weight:500">${escapeHtml(line)}</span>\n`;
    } else {
      html += `<span style="color:#555">${escapeHtml(line)}</span>\n`;
    }
  }
  html += '</pre>';
  return html;
}

function appendDiffToPanel(diff) {
  if (!diff.trim()) return;
  const body = document.getElementById("detail-body");
  if (!body) return;

  const files = parseDiffFiles(diff);
  if (files.length === 0) return;

  const section = document.createElement('div');
  section.className = 'detail-section';

  const label = document.createElement('div');
  label.className = 'detail-section-label';
  label.textContent = `LIVE DIFF (uncommitted) — ${files.length} file${files.length !== 1 ? 's' : ''}`;
  section.appendChild(label);

  const ul = document.createElement('ul');
  ul.style.cssText = 'list-style:none;padding:0;margin:0;';

  files.forEach((file) => {
    const li = document.createElement('li');
    li.style.marginBottom = '4px';

    const btn = document.createElement('button');
    btn.style.cssText = 'width:100%;display:flex;align-items:center;gap:8px;background:rgba(255,255,255,0.03);border:1px solid #1a1a1e;border-radius:6px;padding:6px 10px;color:#c8c0b8;font-family:inherit;font-size:12px;cursor:pointer;text-align:left;';

    const chev = document.createElement('span');
    chev.textContent = '▶';
    chev.style.cssText = 'color:#555;font-size:10px;flex-shrink:0;transition:transform 0.15s;';

    const name = document.createElement('span');
    name.textContent = file.filename;
    name.style.cssText = 'flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;';

    const adds = document.createElement('span');
    adds.textContent = `+${file.added}`;
    adds.style.cssText = 'color:#50b888;flex-shrink:0;font-size:11px;';

    const dels = document.createElement('span');
    dels.textContent = `−${file.removed}`;
    dels.style.cssText = 'color:#d06060;flex-shrink:0;font-size:11px;margin-left:6px;';

    btn.append(chev, name, adds, dels);

    let pre = null;
    btn.addEventListener('click', () => {
      if (!pre) {
        pre = document.createElement('pre');
        pre.style.cssText = 'font-size:11px;line-height:1.5;overflow-x:auto;background:rgba(0,0,0,0.3);padding:10px 12px;border-radius:0 0 6px 6px;border:1px solid #1a1a1e;border-top:none;margin:0;';
        btn.style.borderRadius = '6px 6px 0 0';
        for (const line of file.lines) {
          const span = document.createElement('span');
          span.textContent = line + '\n';
          if (line.startsWith('+++') || line.startsWith('---')) span.style.color = '#888';
          else if (line.startsWith('+')) span.style.color = '#50b888';
          else if (line.startsWith('-')) span.style.color = '#d06060';
          else if (line.startsWith('@@')) span.style.color = '#c4a060';
          else if (line.startsWith('diff ')) { span.style.color = '#7aa2f7'; span.style.fontWeight = '500'; }
          else span.style.color = '#555';
          pre.appendChild(span);
        }
        li.appendChild(pre);
        chev.style.transform = 'rotate(90deg)';
      } else if (pre.style.display === 'none') {
        pre.style.display = 'block';
        btn.style.borderRadius = '6px 6px 0 0';
        chev.style.transform = 'rotate(90deg)';
      } else {
        pre.style.display = 'none';
        btn.style.borderRadius = '6px';
        chev.style.transform = '';
      }
    });

    li.appendChild(btn);
    ul.appendChild(li);
  });

  section.appendChild(ul);
  body.appendChild(section);
}

// ── Diff + Supervisor Polling ────────────────────────────────

let supervisorData = null;
let currentDiff = "";

let lastDiffStat = "";

async function pollSupervisor() {
  try {
    const res = await fetch("/api/supervisor/status", { signal: AbortSignal.timeout(3000) });
    if (res.ok) {
      const data = await res.json();
      supervisorData = data.supervisor;
      if (data.diff_stat) {
        const lines = data.diff_stat.trim().split('\n');
        lastDiffStat = lines.length > 0 && lines[lines.length-1].includes('changed')
          ? lines[lines.length-1].trim() : "";
      } else {
        lastDiffStat = "";
      }
    }
  } catch (e) { /* optional */ }
}

pollSupervisor(); // initial fetch; subsequent updates via WebSocket

// ── Diff Button ─────────────────────────────────────────────

document.getElementById("btn-diff")?.addEventListener("click", async () => {
  const panel = document.getElementById("detail-panel");
  const canvasWrap = document.getElementById("canvas-wrap");
  panel.classList.add("open");
  canvasWrap.classList.add("has-detail");
  resize();

  document.getElementById("detail-kind").textContent = "LIVE";
  document.getElementById("detail-title").textContent = "Uncommitted Changes";
  document.getElementById("detail-meta").innerHTML = lastDiffStat ? `<span style="color:#888">${lastDiffStat}</span>` : "";
  document.getElementById("detail-body").innerHTML = '<div style="color:#555;padding:20px 0">loading diff...</div>';

  // Clear focus so diff isn't associated with a work item
  focusId = null;
  focusTarget = [0, 0];
  renderSidebar();

  try {
    const res = await fetch("/api/diff", { signal: AbortSignal.timeout(5000) });
    if (res.ok) {
      const { diff } = await res.json();
      document.getElementById("detail-body").innerHTML = "";
      if (diff) {
        appendDiffToPanel(diff);
      } else {
        document.getElementById("detail-body").innerHTML = '<div style="color:#555;padding:20px 0">No uncommitted changes.</div>';
      }
    }
  } catch (e) {
    document.getElementById("detail-body").innerHTML = '<div style="color:#d06060;padding:20px 0">Failed to load diff.</div>';
  }
});

// ── Bash Log Button ─────────────────────────────────────────

document.getElementById("btn-bash")?.addEventListener("click", async () => {
  const panel = document.getElementById("detail-panel");
  const canvasWrap = document.getElementById("canvas-wrap");
  panel.classList.add("open");
  canvasWrap.classList.add("has-detail");
  resize();

  document.getElementById("detail-kind").textContent = "EXECUTION";
  document.getElementById("detail-title").textContent = "Bash Command Log";
  document.getElementById("detail-meta").innerHTML = "";
  document.getElementById("detail-body").innerHTML = '<div style="color:#555;padding:20px 0">loading...</div>';

  focusId = null;
  focusTarget = [0, 0];
  renderSidebar();

  try {
    const res = await fetch("/api/bash-log?job=latest", { signal: AbortSignal.timeout(5000) });
    if (res.ok) {
      const { commands, job_id } = await res.json();
      document.getElementById("detail-meta").innerHTML = `<span style="color:#555">${job_id || "?"}</span><span style="color:#555;margin-left:8px">${commands.length} entries</span>`;
      let html = '<div style="font-family:\'SF Mono\',monospace;font-size:12px;line-height:1.5;">';
      for (const entry of commands) {
        if (entry.comment) {
          html += `<div style="color:#555;padding:4px 0;font-style:italic"># ${escapeHtml(entry.comment)}</div>`;
        } else {
          const mark = entry.exit_code === 0 ? '<span style="color:#50b888">✓</span>' : '<span style="color:#d06060">✗</span>';
          html += `<div style="padding:2px 0">${mark} <span style="color:#c4a060">$</span> <span style="color:#c8c0b8">${escapeHtml(entry.command)}</span></div>`;
          if (entry.output_preview) {
            html += `<div style="color:#555;padding:0 0 4px 20px;font-size:11px;white-space:pre-wrap;max-height:100px;overflow:hidden">${escapeHtml(entry.output_preview)}</div>`;
          }
        }
      }
      html += '</div>';
      document.getElementById("detail-body").innerHTML = html || '<div style="color:#555">No commands recorded.</div>';
    }
  } catch (e) {
    document.getElementById("detail-body").innerHTML = '<div style="color:#d06060">Failed to load bash log.</div>';
  }
});

// ── Filters ─────────────────────────────────────────────────

document.querySelectorAll(".filter-btn").forEach(btn => {
  btn.addEventListener("click", () => {
    const f = btn.dataset.filter;
    activeFilter = (activeFilter === f && f !== "all") ? "all" : f;
    document.querySelectorAll(".filter-btn").forEach(b =>
      b.classList.toggle("active", b.dataset.filter === activeFilter || (activeFilter === "all" && b.dataset.filter === "all"))
    );
    renderSidebar();
  });
});

// ── WebSocket Live Updates ───────────────────────────────────

let ws = null;
let wsRetryDelay = 1000;
const WS_MAX_RETRY = 30000;

function connectWebSocket() {
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  const url = `${proto}//${location.host}/ws`;
  ws = new WebSocket(url);
  wsRetryDelay = 1000;

  ws.onmessage = (ev) => {
    try {
      const msg = JSON.parse(ev.data);
      if (msg.type) {
        refresh();
        pollSupervisor();
      }
    } catch (e) { /* ignore malformed frames */ }
  };

  ws.onclose = () => {
    ws = null;
    setTimeout(() => {
      wsRetryDelay = Math.min(wsRetryDelay * 1.5, WS_MAX_RETRY);
      connectWebSocket();
    }, wsRetryDelay);
  };

  ws.onerror = () => { ws.close(); };
}

// ── UI + Init ───────────────────────────────────────────────

function updateUI() {
  if (appMode === "runs") {
    updateRunStats();
    const back = document.getElementById("btn-back");
    if (back) back.style.display = currentRunId ? "inline" : "none";
    return;
  }

  const counts = {};
  W.forEach(w => { counts[w.s] = (counts[w.s] || 0) + 1; });
  const parts = [];
  const activeCount = (counts.claimed || 0) + (counts.in_progress || 0);
  if (activeCount) parts.push(`${activeCount} active`);
  if (counts.done || counts.completed) parts.push(`${(counts.done||0) + (counts.completed||0)} done`);
  if (counts.blocked) parts.push(`${counts.blocked} blocked`);
  parts.push(`${W.length} total`);

  // Supervisor info (stable, no blinking)
  if (supervisorData) {
    const inf = supervisorData.in_flight || [];
    if (inf.length > 0) parts.push(`${inf.length} in-flight`);
    if (supervisorData.uptime) parts.push(supervisorData.uptime);
  }
  if (lastDiffStat) parts.push(lastDiffStat);

  document.getElementById("stats").textContent = parts.join(" · ");
  document.getElementById("focus-label").textContent = focusId ? ("→ " + byId[focusId].t) : "";
  document.getElementById("btn-back").style.display = focusId ? "inline" : "none";

  // Update filter counts
  const el = (id) => document.getElementById(id);
  if (el("count-all")) el("count-all").textContent = W.length;
  if (el("count-ready")) el("count-ready").textContent = counts.ready || 0;
  if (el("count-active")) el("count-active").textContent = (counts.claimed || 0) + (counts.in_progress || 0);
  if (el("count-blocked")) el("count-blocked").textContent = counts.blocked || 0;
  if (el("count-done")) el("count-done").textContent = (counts.done || 0) + (counts.completed || 0);
  if (el("count-failed")) el("count-failed").textContent = (counts.failed || 0) + (counts.cancelled || 0);
}

async function refresh() {
  if (appMode === "runs") {
    const runs = await loadRuns();
    if (runs) {
      runItems = runs;
      runById = Object.fromEntries(runItems.map(item => [item.job_id, item]));
      if (currentRunId && !runById[currentRunId]) {
        currentRunId = null;
      }
      renderRunSidebar();
      updateRunStats();
      if (currentRunId) {
        const detail = await loadRunDetail(currentRunId);
        if (detail) renderRunDetailPanel(detail);
      }
    }
    return;
  }

  const [items] = await Promise.all([loadData(), loadEdges()]);
  if (!items) return;
  dagLayout = null; // edges may have changed
  if (W.length === 0) {
    initSimulation(items);
  } else {
    refreshData(items);
  }
  updateUI();
  renderSidebar();
}

async function boot() {
  const [items] = await Promise.all([loadData(), loadEdges()]);
  if (items && items.length > 0) {
    initSimulation(items);
  } else {
    // No data — show empty state
    document.getElementById("loading").textContent = "no work items — create some with: cagent inbox \"your idea\"";
    return;
  }
  document.getElementById("loading").style.display = "none";
  document.getElementById("top").style.display = "flex";
  document.getElementById("main").style.display = "flex";
  document.getElementById("bottom").style.display = "flex";
  window.addEventListener("resize", resize);
  resize();
  updateUI();
  renderSidebar();
  draw();
  connectWebSocket();

  // View mode toggle
  document.getElementById("btn-dag-view")?.addEventListener("click", () => {
    if (appMode === "runs") return;
    if (viewMode === "dag") return;
    viewMode = "dag";
    document.getElementById("btn-dag-view").classList.add("view-btn-active");
    document.getElementById("btn-hyp-view").classList.remove("view-btn-active");
  });
  document.getElementById("btn-hyp-view")?.addEventListener("click", () => {
    if (appMode === "runs") return;
    if (viewMode === "hyperbolic") return;
    viewMode = "hyperbolic";
    document.getElementById("btn-hyp-view").classList.add("view-btn-active");
    document.getElementById("btn-dag-view").classList.remove("view-btn-active");
    if (W.length > 0 && Object.keys(nodes).length === 0) initSimulation(W);
  });
  document.getElementById("btn-runs-view")?.addEventListener("click", async () => {
    if (appMode === "runs") return;
    setAppMode("runs");
    const runs = await loadRuns();
    runItems = runs || [];
    runById = Object.fromEntries(runItems.map(item => [item.job_id, item]));
    renderRunSidebar();
    updateRunStats();
  });

  if (appMode === "runs") {
    setAppMode("runs");
  }
}

boot();
