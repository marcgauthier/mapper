// Bottom-right overview map. Renders the whole graph on a small canvas with a
// draggable viewport square that pans the main map. Rebound on every load
// because the main Cytoscape instance is recreated.
const navigatorView = (() => {
  const panel = document.querySelector('#navigator');
  const canvas = document.querySelector('#navigatorCanvas');
  const toggle = document.querySelector('#navigatorToggle');
  const ctx = canvas.getContext('2d');
  const W = 220, H = 150, PAD = 60;
  let map = null, frame = 0, dragging = false, collapsed = false;
  let fit = null;

  function setupCanvas() {
    const ratio = Math.min(window.devicePixelRatio || 1, 2);
    canvas.width = W * ratio;
    canvas.height = H * ratio;
    canvas.style.width = W + 'px';
    canvas.style.height = H + 'px';
    ctx.setTransform(ratio, 0, 0, ratio, 0, 0);
  }

  function computeFit() {
    const nodes = map.nodes();
    if (!nodes.length) return null;
    const bb = map.elements().boundingBox();
    const w = Math.max(bb.w, 1), h = Math.max(bb.h, 1);
    const s = Math.min(W / (w + PAD * 2), H / (h + PAD * 2));
    return {x1: bb.x1 - PAD, y1: bb.y1 - PAD, s, ox: (W - w * s) / 2, oy: (H - h * s) / 2};
  }

  const toMini = (x, y) => ({x: fit.ox + (x - fit.x1) * fit.s, y: fit.oy + (y - fit.y1) * fit.s});
  const toModel = (x, y) => ({x: fit.x1 + (x - fit.ox) / fit.s, y: fit.y1 + (y - fit.oy) / fit.s});

  function draw() {
    if (!map || map.destroyed() || collapsed) return;
    fit = computeFit();
    ctx.clearRect(0, 0, W, H);
    if (!fit) {
      ctx.fillStyle = '#8b99ab';
      ctx.font = '12px system-ui,sans-serif';
      ctx.textAlign = 'center';
      ctx.fillText('No items on the map', W / 2, H / 2);
      return;
    }
    const edges = map.edges();
    if (edges.length < 8000) {
      ctx.strokeStyle = '#b3c2d4';
      ctx.lineWidth = 1;
      ctx.beginPath();
      edges.forEach(e => {
        const s = e.source().position(), t = e.target().position();
        const a = toMini(s.x, s.y), b = toMini(t.x, t.y);
        ctx.moveTo(a.x, a.y);
        ctx.lineTo(b.x, b.y);
      });
      ctx.stroke();
    }
    const selected = new Set(map.nodes(':selected').map(n => n.id()));
    map.nodes().forEach(n => {
      const p = toMini(n.position('x'), n.position('y'));
      ctx.fillStyle = selected.has(n.id()) ? '#f39800' : '#5c6b7e';
      ctx.fillRect(p.x - 1.5, p.y - 1.5, 3, 3);
    });
    const ext = map.extent();
    const a = toMini(ext.x1, ext.y1), b = toMini(ext.x2, ext.y2);
    const rx = Math.min(a.x, b.x), ry = Math.min(a.y, b.y);
    const rw = Math.max(Math.abs(b.x - a.x), 8), rh = Math.max(Math.abs(b.y - a.y), 8);
    ctx.fillStyle = 'rgba(243,152,0,.14)';
    ctx.fillRect(rx, ry, rw, rh);
    ctx.strokeStyle = '#d98700';
    ctx.lineWidth = 1.5;
    ctx.strokeRect(rx, ry, rw, rh);
  }

  function schedule() {
    if (frame || collapsed) return;
    frame = requestAnimationFrame(() => { frame = 0; draw(); });
  }

  function panTo(clientX, clientY) {
    if (!map || map.destroyed() || !fit) return;
    const rect = canvas.getBoundingClientRect();
    const pt = toModel(clientX - rect.left, clientY - rect.top);
    const zoom = map.zoom();
    map.pan({x: map.width() / 2 - pt.x * zoom, y: map.height() / 2 - pt.y * zoom});
  }

  canvas.addEventListener('pointerdown', e => {
    if (collapsed) return;
    dragging = true;
    canvas.setPointerCapture(e.pointerId);
    panTo(e.clientX, e.clientY);
  });
  canvas.addEventListener('pointermove', e => {
    if (dragging) panTo(e.clientX, e.clientY);
  });
  for (const type of ['pointerup', 'pointercancel']) {
    canvas.addEventListener(type, () => { dragging = false; });
  }

  toggle.addEventListener('click', () => {
    collapsed = !collapsed;
    panel.classList.toggle('collapsed', collapsed);
    toggle.textContent = collapsed ? '+' : '–';
    toggle.setAttribute('aria-expanded', String(!collapsed));
    if (!collapsed) draw();
  });

  function bind(nextMap) {
    map = nextMap;
    fit = null;
    dragging = false;
    draw();
    map.on('render add remove', schedule);
  }

  setupCanvas();
  window.addEventListener('resize', () => { setupCanvas(); draw(); });
  return {bind};
})();
