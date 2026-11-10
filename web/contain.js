// Square containment physics. Location squares auto-size to their children;
// this module pushes top-level outsiders away when a square overlaps them
// (dragging, growing, expanding) so squares never swallow foreign items.
// Compound nesting itself is enforced by Cytoscape: children stay inside.
const squareContain = (() => {
  let map = null, frame = 0, dragged = null;
  const GAP = 6, PASSES = 4;

  function roots() {
    return map.nodes().filter(n => !n.parent().length);
  }

  function boxes(nodes) {
    const out = new Map();
    nodes.forEach(n => out.set(n.id(), n.boundingBox({ includeLabels: false })));
    return out;
  }

  function excluded(n) {
    if (!dragged) return false;
    // The dragged node and the square it sits in move together already.
    return n.id() === dragged.id() || dragged.ancestors().contains(n);
  }

  // Pushes overlapping top-level nodes out along the smallest-penetration
  // axis. Returns true when anything moved.
  function declutter() {
    if (!map || map.destroyed()) return false;
    let moved = false;
    for (let pass = 0; pass < PASSES; pass++) {
      let clean = true;
      const tops = roots();
      const box = boxes(tops);
      const pushers = tops.filter(n => n.isParent());
      for (const p of pushers) {
        const pb = box.get(p.id());
        for (const t of tops) {
          if (t.id() === p.id() || excluded(t)) continue;
          const tb = box.get(t.id());
          const ox = Math.min(pb.x2, tb.x2) - Math.max(pb.x1, tb.x1);
          const oy = Math.min(pb.y2, tb.y2) - Math.max(pb.y1, tb.y1);
          if (ox <= 0 || oy <= 0) continue;
          clean = false;
          moved = true;
          const pos = { ...t.position() };
          if (ox < oy) {
            const dir = (tb.x1 + tb.x2) / 2 < (pb.x1 + pb.x2) / 2 ? -1 : 1;
            pos.x += dir * (ox + GAP);
          } else {
            const dir = (tb.y1 + tb.y2) / 2 < (pb.y1 + pb.y2) / 2 ? -1 : 1;
            pos.y += dir * (oy + GAP);
          }
          t.position(pos);
          box.set(t.id(), t.boundingBox({ includeLabels: false }));
        }
      }
      if (clean) break;
    }
    return moved;
  }

  function schedule() {
    if (frame) return;
    frame = requestAnimationFrame(() => { frame = 0; declutter(); });
  }

  function bind(nextMap) {
    map = nextMap;
    dragged = null;
    map.on('grab', 'node', e => { dragged = e.target; });
    map.on('drag', schedule);
    map.on('dragfree', 'node', () => {
      dragged = null;
      if (declutter()) saveLayout();
    });
    // Expansion and layout restores can land squares on top of items.
    requestAnimationFrame(() => { if (declutter()) saveLayout(); });
  }

  return { bind, declutter };
})();
