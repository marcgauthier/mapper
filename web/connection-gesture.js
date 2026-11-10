// The preview stays outside Cytoscape so it never becomes a saved edge or layout item.
const connectionGesture = (() => {
  const canvas = document.querySelector('#cy');
  const preview = document.querySelector('#connectionPreview');
  const line = preview.querySelector('line');
  const origin = preview.querySelector('circle');
  let pending = null;
  let map = null;
  let restoreGrabbing = false;
  let suppressDoubleTapUntil = 0;
  const connectable = node => ['device', 'patch_panel', 'virtual_machine', 'virtual_switch', 'shape', 'label', 'manhole'].includes(node.data('kind'));
  const sourceNode = () => {
    if (!pending || !map) return null;
    const direct = map.getElementById(pending.id);
    return direct.length ? direct : map.nodes().filter(n => n.data('object_id') === pending.id).first();
  };

  function draw() {
    if (!pending || !map) return;
    const node = sourceNode();
    if (node?.length) pending.position = {...node.position()};
    const rect = canvas.getBoundingClientRect(), pan = map.pan(), zoom = map.zoom();
    const x = pending.position.x * zoom + pan.x, y = pending.position.y * zoom + pan.y;
    line.setAttribute('x1', x);
    line.setAttribute('y1', y);
    line.setAttribute('x2', pending.pointer.x - rect.left);
    line.setAttribute('y2', pending.pointer.y - rect.top);
    origin.setAttribute('cx', x);
    origin.setAttribute('cy', y);
  }

  function cancel(announce = true) {
    if (!pending) return;
    pending = null;
    preview.toggleAttribute('hidden', true);
    canvas.classList.remove('connection-picking');
    if (map && !map.destroyed()) {
      map.nodes().removeClass('connection-source connection-target');
      map.autoungrabify(restoreGrabbing);
    }
    if (announce) statusEl.textContent = 'Connection canceled';
  }

  function start(source, event) {
    if (window.userRole === 'viewer') return;
    if (!source || source.type !== 'object' || !map) return;
    cancel(false);
    const node = map.getElementById(source.id).length ? map.getElementById(source.id) : map.nodes().filter(n => n.data('object_id') === source.id).first();
    if (!node?.length || !connectable(node)) {
      statusEl.textContent = 'Select a visible device or map item to connect';
      return;
    }
    const rect = canvas.getBoundingClientRect(), point = node.renderedPosition();
    pending = {
      id: source.id, name: source.label, position: {...node.position()},
      pointer: {x: event?.clientX ?? rect.left + point.x, y: event?.clientY ?? rect.top + point.y}
    };
    window.selectedItem = source;
    map.nodes().unselect();
    node.select().addClass('connection-source');
    restoreGrabbing = map.autoungrabify();
    map.autoungrabify(true);
    canvas.classList.add('connection-picking');
    preview.toggleAttribute('hidden', false);
    draw();
    statusEl.textContent = 'Connecting from ' + source.label + ': click a second item, or press Esc to cancel';
  }

  // Return true when a click belongs to this gesture, so normal inspection and
  // Shift-click connection handling do not also run for the same click.
  function choose(node) {
    if (!pending) return false;
    const target = node.data('object_id') || node.id();
    if (!connectable(node) || target === pending.id) {
      statusEl.textContent = 'Click a different device or map item, or press Esc to cancel';
      return true;
    }
    const source = pending.id;
    suppressDoubleTapUntil = Date.now() + 500;
    cancel(false);
    connectInterfaces(source, target);
    return true;
  }

  function bind(nextMap) {
    map = nextMap;
    if (pending) {
      restoreGrabbing = map.autoungrabify();
      map.autoungrabify(true);
      sourceNode()?.addClass('connection-source');
      draw();
    }
    map.on('pan zoom position', draw);
    map.on('mouseover', 'node', e => {
      if (pending && connectable(e.target) && (e.target.data('object_id') || e.target.id()) !== pending.id) e.target.addClass('connection-target');
    });
    map.on('mouseout', 'node', e => e.target.removeClass('connection-target'));
    map.on('tap', e => {
      if (pending && e.target === map) statusEl.textContent = 'Click a second device or map item, or press Esc to cancel';
    });
  }

  canvas.addEventListener('pointermove', e => {
    if (!pending) return;
    pending.pointer = {x: e.clientX, y: e.clientY};
    preview.toggleAttribute('hidden', false);
    draw();
  });
  canvas.addEventListener('pointerleave', () => { if (pending) preview.toggleAttribute('hidden', true); });
  document.addEventListener('keydown', e => {
    if (e.key === 'Escape' && pending) { e.preventDefault(); cancel(); }
  });
  document.addEventListener('pointerdown', e => {
    if (pending && !e.target.closest('.canvasWrap')) cancel();
  });
  window.addEventListener('resize', draw);
  return {start, cancel, choose, bind, get active() {return !!pending;}, get suppressDoubleTap() {return !!pending || Date.now() < suppressDoubleTapUntil;}};
})();
