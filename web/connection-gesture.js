// Draft-only overlay: nothing here is saved as topology or layout inventory.
const connectionGesture = (() => {
  const canvas = document.querySelector("#cy");
  const preview = document.querySelector("#connectionPreview");
  const line = preview.querySelector("line"), origin = preview.querySelector("circle");
  const endpointKinds = new Set(["device", "patch_panel", "virtual_machine", "virtual_switch", "outlet", "service", "shape", "label", "manhole"]);
  let pending = null, map = null, config = {}, scope = null, restoreGrabbing = false;
  let suppressUntil = 0, revision = 0;
  const isContainer = node => !!node.data("draft_container") || config.isContainer?.(node.data("kind"));
  const sourceID = () => pending?.source.endpoint.provisional_id || pending?.source.record.id;
  function anchor() {
    if (!pending || !map || map.destroyed()) return null;
    for (const id of [sourceID(), pending.source.container?.id, pending.marker]) {
      if (id && map.getElementById(id).length) return map.getElementById(id);
    }
    return null;
  }
  function draw() {
    if (!pending || !map || map.destroyed()) return;
    const node = anchor(); if (!node?.length) return;
    const point = node.renderedPosition(), rect = canvas.getBoundingClientRect();
    line.setAttribute("x1", point.x); line.setAttribute("y1", point.y);
    line.setAttribute("x2", pending.pointer.x - rect.left); line.setAttribute("y2", pending.pointer.y - rect.top);
    origin.setAttribute("cx", point.x); origin.setAttribute("cy", point.y);
  }
  function cancel(announce = true) {
    revision++;
    if (!pending || pending.phase === "saving") return;
    pending = null;
    preview.setAttribute("hidden", "");
    canvas.classList.remove("connection-picking");
    if (map && !map.destroyed()) {
      map.nodes("[draft_transient]").remove();
      map.nodes().removeClass("connection-source connection-target");
      map.autoungrabify(restoreGrabbing);
    }
    if (announce) statusEl.textContent = "Connection canceled";
  }
  async function start(source, event) {
    if (window.userRole === "viewer" || !source || !map || pending?.phase === "saving") return;
    cancel(false); suppressUntil=0; const ticket = revision;
    try {
      const prepared = await config.prepare(source);
      if (ticket !== revision) return;
      const rect = canvas.getBoundingClientRect();
      pending = {source: prepared, phase: "drawing", pointer: {x: event?.clientX ?? rect.left + rect.width / 2, y: event?.clientY ?? rect.top + rect.height / 2}};
      bind(map, scope);
      statusEl.textContent = "Connecting from " + prepared.record.name + ": select an endpoint; double-click maps to enter; Esc cancels";
    } catch (error) { statusEl.textContent = error.message; }
  }
  function choose(node) {
    if (!pending) return false;
    if (pending.phase !== "drawing") return true;
    if (isContainer(node)) { statusEl.textContent = "Double-click this map to choose an endpoint inside"; return true; }
    const id = node.data("object_id") || node.id();
    if (node.data("draft_transient") || id === sourceID() || !endpointKinds.has(node.data("kind"))) {
      statusEl.textContent = "Choose a different equipment or outlet endpoint"; return true;
    }
    pending.phase = "confirming"; suppressUntil = Date.now() + 500;
    const draft = pending;
    config.complete(draft.source, id).catch(error => {
      if (pending === draft) pending.phase = "drawing";
      statusEl.textContent = error.message;
    });
    return true;
  }
  function bind(nextMap, nextScope = scope) {
    const changed = nextMap !== map;
    map = nextMap; scope = nextScope;
    if (changed) restoreGrabbing = map.autoungrabify();
    if (pending) {
      map.nodes("[draft_transient]").remove();
      const source = pending.source;
      if (!map.getElementById(sourceID()).length && !map.getElementById(source.container?.id || source.record.id).length) {
        const inside = source.endpoint.container_id && scope?.id === source.endpoint.container_id;
        const context = inside ? {id:sourceID(),kind:"outlet",name:"New DVO (pending)"} : source.container || source.record;
        const id = inside ? sourceID() : "draft-source:" + context.id;
        const extent = map.extent();
        map.add({data:{id,object_id:context.id,kind:context.kind,label:context.name,map_label:context.name + (inside ? "\nPending DVO" : "\n↗ Connection source"),icon:config.icon(context),draft_transient:true,draft_container:inside ? "" : (config.isContainer(context.kind) ? context.id : "")},position:{x:extent.x1+110/map.zoom(),y:extent.y1+110/map.zoom()},classes:"boundary mapper-symbol " + (config.isContainer(context.kind) ? "map-container " : "") + context.kind});
        pending.marker = id;
      }
      map.autoungrabify(true);
      anchor()?.addClass("connection-source");
      canvas.classList.add("connection-picking");
      preview.removeAttribute("hidden"); draw();
    }
    // Each map instance is bound once; start() may reuse that instance.
    if (map.scratch("_mapperGestureBound")) return;
    map.scratch("_mapperGestureBound", true);
    map.on("pan zoom position", draw);
    map.on("mouseover", "node", e => {
      if (pending && (isContainer(e.target) || endpointKinds.has(e.target.data("kind")))) e.target.addClass("connection-target");
    });
    map.on("mouseout", "node", e => e.target.removeClass("connection-target"));
  }
  canvas.addEventListener("pointermove", e => {if (pending?.phase === "drawing") {pending.pointer={x:e.clientX,y:e.clientY};preview.removeAttribute("hidden");draw();}});
  canvas.addEventListener("pointerleave", () => {if (pending?.phase === "drawing") preview.setAttribute("hidden", "");});
  document.addEventListener("keydown", e => {
    if (e.key === "Escape" && pending) {
      if (pending.phase === "saving") {e.preventDefault();statusEl.textContent="Saving connection…";}
      else cancel();
    }
  });
  window.addEventListener("resize", draw);
  return {start,cancel,choose,bind,setSaving(value){if(pending)pending.phase=value?"saving":"confirming";},get saving(){return pending?.phase==="saving";},configure(options){config=options;},get active(){return !!pending;},get source(){return pending?.source;},get confirming(){return pending?.phase === "confirming";},get suppressDoubleTap(){return pending?.phase === "confirming" || Date.now()<suppressUntil;}};
})();
