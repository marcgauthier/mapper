// Focused scopes change topology detail; the existing zoom slider remains camera magnification.
const topologyWorkspace = (() => {
  const $ = (s) => document.querySelector(s),
    copy = structuredClone;
  const labels = {
    global: "Global overview · Sites & Cloud",
    service: "One Cloud Provider / Service",
    site: "One Site",
    building: "One Building",
    floor: "One Floor",
    room: "One Room",
    telecom_room: "One Telecom Room",
    rack: "One Rack",
    conduit: "One Conduit",
  };
  const createLabels = {
    site: "Add site",
    service: "Add cloud/service",
    building: "Add building",
    generator: "Add generator",
    ups: "Add UPS",
    floor: "Add floor",
    room: "Add room",
    telecom_room: "Add telecom room",
    rack: "Add rack",
    device: "Add device",
    patch_panel: "Add patch panel",
    dvo: "Add DVO",
    fiber: "Add fiber outlet",
    conduit: "Add conduit",
    cable: "Connect cable/interfaces",
    interface: "Add interface",
    relationship: "Add relationship",
    attachment: "Connect Internet/MPLS service",
    assign_cable: "Assign existing cable",
  };
  const categories = {
    cloud_provider: "Cloud provider",
    saas: "SaaS",
    paas: "PaaS",
    iaas: "IaaS",
    faas: "FaaS",
    cloud_hosting: "Cloud hosting",
    cloud_resource: "Hosted resource",
    internet: "Internet",
    mpls: "MPLS",
  };
  let viewport = {},
    dragging = false,
    navigationRevision = 0,
    refreshPending = false,
    refreshKey = "";
  let scope = { kind: "global", id: "" },
    history = [],
    scene = null,
    nodes = new Map(),
    edges = new Map(),
    generation = 0,
    abort = null,
    page = "",
    sequence = null,
    refreshTimer,
    selected = null,
    deferred = false,
    loading = false,
    savedCamera = null;
  try {
    scope = JSON.parse(localStorage.getItem("mapper_scope")) || scope;
    history = JSON.parse(localStorage.getItem("mapper_scope_history")) || [];
  } catch {}
  if (!labels[scope.kind]) scope = { kind: "global", id: "" };
  const toolbar = document.createElement("div");
  toolbar.id = "scopeToolbar";
  toolbar.innerHTML =
    '<select id="scopeLevel" aria-label="Topology level"></select><button id="scopeBack" title="Back to previous view">←</button><button id="scopeOpen">Open…</button><select id="scopeCategory" aria-label="Service category"><option value="">All service categories</option></select><select id="scopeRoomType" aria-label="Room type"><option value="">All room types</option><option value="room">Ordinary rooms</option><option value="telecom_room">Telecom rooms</option></select><select id="scopeNetwork" aria-label="Network"><option value="">All networks</option></select><select id="scopeCreate" aria-label="Create in current view"><option value="">Create…</option></select>';
  $(".toolbar").prepend(toolbar);
  document.body.classList.add("scope-active");
  $("#scopeLevel").append(
    ...Object.entries(labels).map(([v, t]) => new Option(t, v)),
  );
  $("#scopeCategory").append(
    ...Object.entries(categories).map(([v, t]) => new Option(t, v)),
  );
  const breadcrumbs = document.createElement("nav");
  breadcrumbs.id = "scopeBreadcrumbs";
  breadcrumbs.setAttribute("aria-label", "Topology navigation");
  $(".canvasWrap").append(breadcrumbs);
  const notice = document.createElement("div");
  notice.id = "scopeNotice";
  notice.setAttribute("role", "status");
  $(".canvasWrap").append(notice);
  const more = document.createElement("button");
  more.id = "scopeMore";
  more.textContent = "Load more";
  more.hidden = true;
  $(".canvasWrap").append(more);
  const dialog = document.createElement("dialog");
  dialog.id = "scopeDialog";
  document.body.append(dialog);
  const editor = () => window.userRole && window.userRole !== "viewer";
  const error = (e) => {
    if (e.name !== "AbortError") {
      notice.textContent = e.message;
      statusEl.textContent = e.message;
    }
  };
  const query = () =>
    new URLSearchParams({
      scope_kind: scope.kind,
      scope_id: scope.id,
      network: $("#scopeNetwork").value,
      service_category: $("#scopeCategory").value,
      room_type: $("#scopeRoomType").value,
      ...viewport,
    });
  const object = (id) =>
    nodes.get(id) || (scene?.scope?.id === id && scene.scope);
  const cameraKey = () => `mapper_camera:${scope.kind}:${scope.id}`;
  function rememberCamera() {
    if (cy && !cy.destroyed())
      localStorage.setItem(
        cameraKey(),
        JSON.stringify({ zoom: cy.zoom(), pan: cy.pan() }),
      );
  }
  async function request(path, opt = {}) {
    const response = await fetch(path, opt);
    if (response.status === 401) {
      loginBox.hidden = false;
      throw Error("Sign in to continue");
    }
    if (!response.ok) {
      const e = Error(await response.text());
      e.status = response.status;
      throw e;
    }
    return response.status === 204 ? null : response.json();
  }
  function selectOptions(select, options, value = "", empty = "") {
    select.replaceChildren(
      ...(empty ? [new Option(empty, "")] : []),
      ...options.map((o) => new Option(o.label ?? o.name, o.value ?? o.id)),
    );
    select.value = value;
  }
  function form(title, fields, submit) {
    dialog.replaceChildren();
    const f = document.createElement("form"),
      h = document.createElement("h2"),
      grid = document.createElement("div"),
      err = document.createElement("p"),
      actions = document.createElement("div");
    h.textContent = title;
    grid.id = "scopeFields";
    err.id = "scopeError";
    err.setAttribute("role", "alert");
    actions.id = "scopeDialogActions";
    const inputs = {},
      pickers = {};
    for (const field of fields) {
      const wrap = document.createElement("div");
      wrap.className = "field" + (field.full ? " full" : "");
      const label = document.createElement("label");
      label.textContent = field.label;
      const input = document.createElement(
        field.options || field.picker
          ? "select"
          : field.type === "textarea"
            ? "textarea"
            : "input",
      );
      if (!field.options && !field.picker) input.type = field.type || "text";
      input.name = field.key;
      input.id = "scopeField_" + field.key;
      label.htmlFor = input.id;
      input.required = !!field.required;
      input.disabled = !!field.disabled;
      if (field.min !== undefined) input.min = field.min;
      if (field.max !== undefined) input.max = field.max;
      if (field.step) input.step = field.step;
      wrap.append(label);
      if (field.options) {
        selectOptions(
          input,
          field.options,
          field.value || "",
          field.empty ?? "",
        );
        if (field.multiple) {
          input.multiple = true;
          input.size = Math.min(6, Math.max(3, field.options.length));
          input.replaceChildren(
            ...field.options.map(
              (o) =>
                new Option(
                  o.label,
                  o.value,
                  false,
                  (field.value || []).includes(o.value),
                ),
            ),
          );
        }
      } else if (field.picker) {
        const box = document.createElement("div");
        box.className = "scopePicker";
        const search = document.createElement("input");
        search.type = "search";
        search.placeholder = "Search " + field.label.toLowerCase();
        search.setAttribute("aria-label", search.placeholder);
        const next = document.createElement("button");
        next.type = "button";
        next.textContent = "More choices";
        next.hidden = true;
        box.append(search, input, next);
        wrap.append(box);
        let cursor = "",
          batch = 0,
          timer;
        const fetchOptions = async (more = false) => {
          const n = ++batch,
            params = new URLSearchParams({
              kinds: field.picker.kinds || "",
              q: search.value,
              parent_id: field.picker.parent || "",
              service_category: field.picker.category || "",
              after: more ? cursor : "",
            });
          try {
            const data = await request("/api/topology/choices?" + params);
            if (n !== batch) return;
            const prior = input.value || field.value || "";
            if (!more)
              input.replaceChildren(new Option(field.empty || "Choose…", ""));
            for (const o of data.items)
              if (![...input.options].some((v) => v.value === o.id)) {
                const option = new Option(o.path || o.name, o.id);
                option.dataset.record = JSON.stringify(o);
                input.append(option);
              }
            if (prior && ![...input.options].some((o) => o.value === prior)) {
              const saved = await request(
                "/api/objects?ids=" + encodeURIComponent(prior),
              );
              if (saved[0]) input.append(new Option(saved[0].name, prior));
            }
            input.value = prior;
            cursor = data.next_cursor;
            next.hidden = !cursor;
          } catch (e) {
            err.textContent = e.message;
          }
        };
        search.oninput = () => {
          clearTimeout(timer);
          timer = setTimeout(() => fetchOptions(), 180);
        };
        next.onclick = () => fetchOptions(true);
        pickers[field.key] = { refresh: fetchOptions, search };
        fetchOptions();
      } else input.value = field.value ?? "";
      if (!field.picker) wrap.append(input);
      if (field.help) {
        const hint = document.createElement("small");
        hint.textContent = field.help;
        wrap.append(hint);
      }
      grid.append(wrap);
      inputs[field.key] = input;
    }
    const cancel = document.createElement("button");
    cancel.type = "button";
    cancel.textContent = "Cancel";
    cancel.onclick = () => dialog.close();
    const save = document.createElement("button");
    save.type = "submit";
    save.className = "primary";
    save.textContent = "Save";
    actions.append(cancel, save);
    f.append(h, grid, err, actions);
    f.onsubmit = async (e) => {
      e.preventDefault();
      save.disabled = true;
      err.textContent = "";
      try {
        await submit(
          Object.fromEntries(
            Object.entries(inputs).map(([k, v]) => [
              k,
              v.multiple ? [...v.selectedOptions].map((o) => o.value) : v.value,
            ]),
          ),
          inputs,
        );
      } catch (e) {
        err.textContent = e.message;
      } finally {
        save.disabled = false;
      }
    };
    dialog.append(f);
    if (!dialog.open) dialog.showModal();
    return { form: f, inputs, pickers, error: err, actions, grid };
  }
  function scopeParent(kind) {
    if (scene?.scope?.kind === kind) return scene.scope.id;
    return scene?.breadcrumbs?.find((o) => o.kind === kind)?.id || "";
  }
  function ancestry() {
    const d = {};
    for (const o of [
      ...(scene?.breadcrumbs || []),
      ...(scene?.scope ? [scene.scope] : []),
    ]) {
      const key = {
        site: "site_id",
        building: "building_id",
        floor: "floor_id",
        room: "room_id",
        telecom_room: "telecom_room_id",
        rack: "rack_id",
      }[o.kind];
      if (key) d[key] = o.id;
    }
    return d;
  }
  async function navigate(kind, id = "", push = true) {
    if (dragging || dialog.open || $("#objectDialog").open) {
      notice.textContent = "Finish or cancel your open edit before navigating.";
      return;
    }
    navigationRevision++;
    rememberCamera();
    if (push && !(scope.kind === kind && scope.id === id))
      history.push({ ...scope });
    history = history.slice(-50);
    scope = { kind, id };
    localStorage.setItem("mapper_scope", JSON.stringify(scope));
    localStorage.setItem("mapper_scope_history", JSON.stringify(history));
    sequence = null;
    page = "";
    nodes.clear();
    edges.clear();
    selected = null;
    savedCamera = null;
    try {
      savedCamera = JSON.parse(localStorage.getItem(cameraKey()));
    } catch {}
    $("#scopeRoomType").value = "";
    $("#scopeCategory").value = "";
    await refresh(false, true);
  }
  async function refresh(append = false, fit = false, fromViewport = false) {
    if (fit) viewport = {};
    if (fromViewport && cy && scene?.node_count > 500) {
      const b = cy.extent();
      viewport = {
        min_x: b.x1 - 100,
        min_y: b.y1 - 100,
        max_x: b.x2 + 100,
        max_y: b.y2 + 100,
      };
    }
    if (!window.userRole) return;
    if (
      dragging ||
      dialog.open ||
      $("#objectDialog").open ||
      $("#linkDialog").open
    ) {
      deferred = true;
      return;
    }
    if (window.loading) {
      setTimeout(() => refresh(append, fit), 60);
      return;
    }
    const key = scope.kind + ":" + scope.id;
    if (loading && refreshKey === key && !append && !fit && !fromViewport) {
      refreshPending = true;
      return;
    }
    refreshKey = key;
    const n = ++generation;
    abort?.abort();
    abort = new AbortController();
    loading = true;
    const params = query();
    if (append && page) {
      params.set("after", page);
      params.set("sequence", sequence);
    }
    try {
      const reply = await request("/api/topology/view?" + params, {
        signal: abort.signal,
      });
      if (n !== generation) return;
      scene = reply;
      sequence = reply.sequence;
      page = reply.next_cursor;
      if (!append) {
        nodes.clear();
        edges.clear();
      }
      for (const o of reply.nodes) nodes.set(o.id, o);
      for (const e of reply.edges) edges.set(e.id, e);
      render(fit);
      syncControls();
      if (selected) {
        const current = nodes.get(selected.id);
        if (current) inspect(current);
      }
    } catch (e) {
      if (e.name === "AbortError") return;
      if (e.status === 409) {
        page = "";
        setTimeout(() => refresh(false), 0);
        return;
      }
      if (e.status === 404 && scope.kind !== "global") {
        notice.textContent =
          "This location no longer exists. Returning to the global overview.";
        scope = { kind: "global", id: "" };
        setTimeout(() => refresh(false, true), 0);
        return;
      }
      error(e);
    } finally {
      if (n === generation) {
        loading = false;
        if (refreshPending) {
          refreshPending = false;
          setTimeout(() => refresh(), 80);
        }
      }
    }
  }
  function syncControls() {
    updateMenu();
    selectOptions(
      $("#scopeLevel"),
      Object.entries(labels).map(([value, label]) => ({ value, label })),
      scope.kind,
    );
    $("#scopeBack").disabled = !history.length;
    $("#scopeOpen").textContent = {
      global: "Open site/cloud…",
      site: "Open building…",
      building: "Select floor…",
      floor: "Open room…",
      room: "Open rack…",
      telecom_room: "Open rack…",
      rack: "Choose rack…",
      service: "Open cloud/service…",
      conduit: "Choose conduit…",
    }[scope.kind];
    $("#scopeCategory").hidden = !["global", "service"].includes(scope.kind);
    $("#scopeRoomType").hidden = !["building", "floor"].includes(scope.kind);
    const prior = $("#scopeNetwork").value;
    selectOptions(
      $("#scopeNetwork"),
      networkInventory.map((o) => ({ value: o.id, label: o.name })),
      prior,
      "All networks",
    );
    selectOptions(
      $("#scopeCreate"),
      (scene.allowed_creation_kinds || []).map((value) => ({
        value,
        label: createLabels[value],
      })),
      "",
      "Create…",
    );
    $("#scopeCreate").hidden = !editor();
    more.hidden = !page;
    breadcrumbs.replaceChildren();
    const add = (label, action) => {
      const b = document.createElement("button");
      b.textContent = label;
      b.onclick = action;
      breadcrumbs.append(b);
    };
    add("Sites & Cloud", () => navigate("global"));
    for (const o of scene.breadcrumbs || [])
      add(o.name, () => navigate(o.kind, o.id));
    if (
      scene.scope &&
      !(scene.breadcrumbs || []).some((o) => o.id === scene.scope.id)
    )
      add(scene.scope.name, () => inspect(scene.scope));
    if (scope.kind !== "global") {
      const b = document.createElement("button");
      b.textContent = "ⓘ";
      b.setAttribute("aria-label", "Inspect current scope");
      b.onclick = () => inspect(scene.scope);
      breadcrumbs.append(b);
    }
    notice.textContent = `${labels[scope.kind]} · ${scene.node_count} items · ${scene.edge_count} connections${page ? " · More items available" : ""}. Click a container to enter it.`;
  }
  function icon(o) {
    if (o.kind === "service") {
      const cat = o.data.service_category,
        visual = ["saas", "paas", "iaas", "faas"].includes(cat) ? cat : "cloud";
      return "/img/" + (visual === "cloud" ? "fluent/cloud" : visual) + ".svg";
    }
    if (o.kind === "generator" || o.kind === "ups")
      return "/img/fluent/flash.svg";
    if (o.kind === "floor") return "/img/fluent/stack.svg";
    if (o.kind === "room") return "/img/fluent/door.svg";
    if (o.kind === "outlet")
      return (
        "/img/fluent/" +
        (o.data.outlet_type === "fiber" ? "flash" : "plug-connected") +
        ".svg"
      );
    return itemIcons.item(o);
  }
  function render(fit = false) {
    const old =
      cy && !cy.destroyed() ? { zoom: cy.zoom(), pan: cy.pan() } : null;
    const elements = [];
    let i = 0;
    for (const o of nodes.values()) {
      const position =
        scene.positions[o.id] ||
        (o.x || o.y
          ? { x: o.x, y: o.y }
          : { x: 180 + (i % 5) * 240, y: 160 + Math.floor(i / 5) * 180 });
      i++;
      elements.push({
        data: {
          id: o.id,
          object_id: o.id,
          label: o.name,
          map_label:
            o.name +
            (o.boundary
              ? "\n↗ External"
              : o.count
                ? "\n" + o.count + " items"
                : ""),
          kind: o.kind,
          icon: icon(o),
          ...(o.parent && nodes.has(o.parent) ? { parent: o.parent } : {}),
        },
        position,
        classes: [
          o.kind,
          o.boundary ? "boundary" : "",
          o.parent ? "member" : "",
          o.warning ? "incomplete" : "",
        ]
          .filter(Boolean)
          .join(" "),
      });
    }
    for (const e of edges.values())
      if (nodes.has(e.source) && nodes.has(e.target))
        elements.push({
          data: {
            id: e.id,
            source: e.source,
            target: e.target,
            label: e.label + (e.count > 1 ? " × " + e.count : ""),
            category: e.category,
            object_id: e.object_id || "",
          },
          classes:
            e.category === "power"
              ? "power"
              : e.category === "conduit"
                ? "conduit"
                : e.category.startsWith("relationship:")
                  ? "relationship"
                  : "",
        });
    if (cy) cy.destroy();
    cy = cytoscape({
      container: $("#cy"),
      autoungrabify: !editor(),
      elements,
      layout: { name: "preset", fit: false },
      style: [
        {
          selector: "node",
          style: {
            label: "data(map_label)",
            width: 80,
            height: 70,
            "background-color": "#edf4ff",
            "background-image": "data(icon)",
            "background-width": 34,
            "background-height": 34,
            "background-position-y": "12px",
            "background-fit": "contain",
            "border-width": 2,
            "border-color": "#91a5c0",
            shape: "round-rectangle",
            "font-size": 12,
            "text-valign": "bottom",
            "text-margin-y": 7,
            "text-wrap": "wrap",
            "text-max-width": 180,
          },
        },
        {
          selector: "node.site",
          style: {
            width: 135,
            height: 95,
            "background-color": "#fff0cc",
            "border-color": "#d59b32",
          },
        },
        {
          selector: "node.building",
          style: { width: 125, height: 90, "background-color": "#e0efff" },
        },
        {
          selector: "node.service",
          style: {
            shape: "ellipse",
            width: 130,
            height: 90,
            "background-color": "#efe6ff",
            "border-color": "#9872bf",
          },
        },
        { selector: "node.room", style: { "background-color": "#e5f8ef" } },
        {
          selector: "node.telecom_room",
          style: { "background-color": "#dceeff", "border-color": "#3789c5" },
        },
        { selector: "node.rack", style: { "background-color": "#e0e8f2" } },
        {
          selector: "node.floor:parent",
          style: {
            "background-opacity": 0.25,
            "background-position-x": "10px",
            "background-position-y": "8px",
            "background-width": 24,
            "background-height": 24,
            "text-valign": "top",
            "text-margin-y": -8,
            padding: 35,
            "min-width": 220,
            "min-height": 160,
          },
        },
        {
          selector: "node.boundary",
          style: { "border-style": "dashed", "background-opacity": 0.7 },
        },
        { selector: "node.incomplete", style: { "border-color": "#c2410c" } },
        {
          selector: "edge",
          style: {
            label: "data(label)",
            width: 2,
            "line-color": "#7189a6",
            "font-size": 10,
            "text-background-color": "#fff",
            "text-background-opacity": 0.9,
            "text-background-padding": 3,
            "curve-style": "bezier",
            "text-rotation": "autorotate",
          },
        },
        {
          selector: "edge.power",
          style: {
            "line-color": "#b7791f",
            "line-style": "dotted",
            "target-arrow-shape": "triangle",
            "target-arrow-color": "#b7791f",
          },
        },
        {
          selector: "node.generator, node.ups",
          style: { "background-color": "#fff3c4", "border-color": "#b7791f" },
        },
        {
          selector: "edge.conduit",
          style: { width: 7, "line-color": "#93887b", "line-opacity": 0.7 },
        },
        {
          selector: "edge.relationship",
          style: {
            "line-style": "dashed",
            "line-color": "#9872bf",
            "target-arrow-shape": "triangle",
            "target-arrow-color": "#9872bf",
          },
        },
        {
          selector: ":selected",
          style: {
            "overlay-color": "#f39800",
            "overlay-opacity": 0.16,
            "overlay-padding": 7,
          },
        },
      ],
    });
    zoomControls.bind(cy);
    navigatorView.bind(cy);
    const camera = savedCamera || (!fit ? old : null);
    if (camera) {
      cy.viewport(camera);
      savedCamera = null;
    } else if (elements.length) cy.fit(undefined, 90);
    cy.on("zoom pan", () => {
      clearTimeout(refreshTimer);
      refreshTimer = setTimeout(() => {
        rememberCamera();
        if (scene?.node_count > 500) refresh(false, false, true);
      }, 200);
    });
    cy.on("tap", "node", (e) => {
      const o = nodes.get(e.target.id());
      if (!o) return;
      if (
        e.originalEvent?.shiftKey &&
        selected &&
        selected.id !== o.id &&
        editor()
      ) {
        cableForm(selected, o);
        return;
      }
      if (labels[o.kind]) navigate(o.kind, o.id);
      else inspect(o);
    });
    cy.on("tap", "edge", (e) => {
      const edge = edges.get(e.target.id());
      if (edge?.category === "conduit") navigate("conduit", edge.object_id);
      else if (edge?.category === "power") inspect(nodes.get(edge.object_id));
      else if (edge) summary(edge);
    });
    cy.on("grab", "node", () => (dragging = true));
    cy.on("free", "node", () => {
      dragging = false;
    });
    cy.on("dragfree", "node", () => savePositions());
    cy.on("cxttap", (e) => context(e));
  }
  async function savePositions() {
    if (!editor() || !scene) return;
    const positions = cy
      .nodes()
      .filter((n) => !n.isParent())
      .map((n) => ({ id: n.id(), ...n.position() }));
    try {
      await request(
        "/api/layout?grouping=" + encodeURIComponent(scene.layout_group),
        json("POST", { positions, expected_version: scene.layout_version }),
      );
    } catch (e) {
      error(e);
    }
  }
  function detail(label, value) {
    if (value === undefined || value === null || value === "") return;
    let dl = $("#inspectorContent .scopeDetails");
    if (!dl) {
      dl = document.createElement("dl");
      dl.className = "scopeDetails";
      inspectorContent.append(dl);
    }
    const dt = document.createElement("dt"),
      dd = document.createElement("dd");
    dt.textContent = label;
    dd.textContent = Array.isArray(value) ? value.join(", ") : String(value);
    dl.append(dt, dd);
  }
  function action(label, fn) {
    const b = document.createElement("button");
    b.textContent = label;
    b.onclick = () => Promise.resolve(fn()).catch(error);
    inspectorActions.append(b);
    return b;
  }
  async function inspect(o) {
    selected = o;
    window.selectedItem = {
      type: "object",
      id: o.id,
      kind: o.kind,
      label: o.name,
      meta: o.data,
      version: o.version,
    };
    inspectorContent.replaceChildren();
    inspectorActions.replaceChildren();
    const h = document.createElement("strong");
    h.textContent = o.name;
    inspectorContent.append(h);
    detail("Type", o.kind.replaceAll("_", " "));
    for (const [key, label] of [
      ["service_category", "Category"],
      ["provider", "Provider"],
      ["account", "Account/subscription"],
      ["region", "Region"],
      ["description", "Description"],
      ["outlet_type", "Outlet type"],
    ])
      detail(label, o.data[key]);
    if (o.warning) detail("Needs attention", o.warning);
    if (labels[o.kind]) action("Open " + o.name, () => navigate(o.kind, o.id));
    if (editor()) {
      action("Edit item", () => objectForm(o.kind, o));
      action("Delete item", async () => {
        if (!confirm("Delete " + o.name + "?")) return;
        await request("/api/objects?id=" + encodeURIComponent(o.id), {
          method: "DELETE",
        });
        selected = null;
        await refresh();
      });
      if (
        o.kind === "device" &&
        [
          "cloud",
          "cloud-provider",
          "aws",
          "azure",
          "google-cloud",
          "oracle-cloud",
          "saas",
          "paas",
          "iaas",
          "faas",
        ].includes(o.data.device_type_id || o.data.device_visual)
      )
        action("Convert to global cloud/service", () => conversion(o));
    }
    if (
      [
        "device",
        "patch_panel",
        "outlet",
        "service",
        "virtual_machine",
        "virtual_switch",
      ].includes(o.kind)
    ) {
      try {
        const ports = await request(
          "/api/interfaces?device_id=" + encodeURIComponent(o.id),
        );
        if (selected?.id !== o.id) return;
        interfaces = ports;
        const list = document.createElement("div");
        list.className = "scopeList";
        for (const port of ports) {
          const row = document.createElement("div");
          row.className = "scopeRow";
          row.textContent =
            port.name + " · " + vrfUI.portSummary(interfaceMeta(port), o);
          if (editor()) {
            const edit = document.createElement("button");
            edit.textContent = "Edit port";
            edit.onclick = () => portForm(o, port);
            row.append(edit);
          }
          list.append(row);
        }
        inspectorContent.append(list);
        if (editor()) {
          action("Add interface/port", () => portForm(o));
          action("Connect from this item", () => cableForm(o));
        }
        if (o.kind === "patch_panel") {
          if (editor()) action("Pair panel ports", () => panelPair(o, ports));
          action("Port pairings", () => panelPairs(o, ports));
        }
        if (o.kind === "device" && isHypervisor(o)) {
          if (editor()) {
            action("Add VM", () => addVM(o));
            action("Add vSwitch", () => addVSwitch(o));
          }
          action("Hosted VMs and switches", async () => {
            const children = await request(
              "/api/objects?host_ids=" +
                encodeURIComponent(o.id) +
                "&limit=500",
            );
            for (const child of children) {
              const b = document.createElement("button");
              b.textContent = child.name;
              b.onclick = () => inspect(child);
              list.append(b);
            }
          });
        }
      } catch (e) {
        error(e);
      }
    }
    if (o.kind === "conduit") {
      detail("Endpoint A", await endpointLabel(o.data.endpoint_a_id));
      detail("Endpoint B", await endpointLabel(o.data.endpoint_b_id));
      if (!o.data.endpoint_a_id || !o.data.endpoint_b_id)
        detail("Warning", "Endpoints are incomplete");
      if (editor()) {
        action("Assign existing cable", () => assignCable(o));
        action("Create cable through conduit", () =>
          cableForm(null, null, null, o),
        );
      }
    }
    if (editor())
      action("Undo last object edit", async () => {
        await request("/api/undo", json("POST", {}));
        await refresh();
      });
  }
  async function endpointLabel(id) {
    if (!id) return "Not specified";
    const rows = await request("/api/objects?ids=" + encodeURIComponent(id));
    return rows[0]?.name || "Missing endpoint";
  }
  async function summary(edge, after = "") {
    selected = null;
    if (!after) {
      inspectorContent.replaceChildren();
      inspectorActions.replaceChildren();
      const h = document.createElement("strong");
      h.textContent = edge.label;
      inspectorContent.append(h);
      detail("Connections", edge.count);
    }
    const params = query();
    params.set("edge_id", edge.id);
    params.set("after", after);
    params.set("sequence", sequence);
    try {
      const reply = await request("/api/topology/details?" + params);
      const list = document.createElement("div");
      list.className = "scopeList";
      for (const item of reply.items) {
        const row = document.createElement("div");
        row.className = "scopeRow";
        const title = document.createElement("strong");
        title.textContent = item.source?.name + " → " + item.target?.name;
        row.append(title);
        if (item.type === "link") {
          const info = document.createElement("span");
          info.textContent = [
            item.data.connection_class || "physical",
            item.data.medium || item.data.service_type,
            item.data.strand_count ? item.data.strand_count + " strands" : "",
          ]
            .filter(Boolean)
            .join(" · ");
          row.append(info);
          if (editor()) {
            const b = document.createElement("button");
            b.textContent = "Edit connection";
            b.onclick = () => cableForm(item.source, item.target, item);
            row.append(b);
            if ((item.data.connection_class || "physical") === "physical") {
              const route = document.createElement("button");
              route.textContent = "Edit route";
              route.onclick = () => editCableRoute(item);
              row.append(route);
            }
            const del = document.createElement("button");
            del.textContent = "Delete";
            del.onclick = async () => {
              if (confirm("Delete this connection?")) {
                await request("/api/links?id=" + encodeURIComponent(item.id), {
                  method: "DELETE",
                });
                await refresh();
              }
            };
            row.append(del);
          }
          const trace = document.createElement("button");
          trace.textContent = "Trace";
          trace.onclick = async () => {
            try {
              const result = await request(
                "/api/trace?interface_id=" +
                  encodeURIComponent(item.source_interface) +
                  "&link_id=" +
                  encodeURIComponent(item.id),
              );
              detail(
                "Trace",
                "Destination " +
                  result.destination_name +
                  " · " +
                  result.segments.length +
                  " cable segments",
              );
            } catch (e) {
              error(e);
            }
          };
          if ((item.data.connection_class || "physical") === "physical")
            row.append(trace);
          for (const id of item.route || []) {
            const b = document.createElement("button");
            b.textContent = "Open route item";
            b.onclick = async () => {
              const record = (
                await request("/api/objects?ids=" + encodeURIComponent(id))
              )[0];
              if (record?.kind === "conduit") navigate("conduit", id);
              else if (record) inspect(record);
            };
            row.append(b);
          }
        } else if (item.type === "relationship") {
          row.append(
            document.createTextNode(
              item.relationship.relationship_type.replaceAll("_", " "),
            ),
          );
          if (editor()) {
            const b = document.createElement("button");
            b.textContent = "Edit relationship";
            b.onclick = () => relationshipForm(item.relationship);
            row.append(b);
            const del = document.createElement("button");
            del.textContent = "Delete";
            del.onclick = async () => {
              await request(
                "/api/relationships",
                json("DELETE", item.relationship),
              );
              await refresh();
            };
            row.append(del);
          }
        }
        list.append(row);
      }
      inspectorContent.append(list);
      if (reply.next_cursor)
        action("More connections", () => summary(edge, reply.next_cursor));
    } catch (e) {
      error(e);
    }
  }
  function panelPair(owner, ports) {
    form(
      "Pair ports on " + owner.name,
      [
        { key: "a", label: "Port A", options: ports, required: true },
        { key: "b", label: "Port B", options: ports, required: true },
      ],
      async (v) => {
        await request(
          "/api/pairings",
          json("POST", { panel_id: owner.id, port_a: v.a, port_b: v.b }),
        );
        dialog.close();
        await refresh();
        await inspect(owner);
      },
    );
  }
  async function panelPairs(owner, ports) {
    const rows = await request(
      "/api/pairings?panel_id=" + encodeURIComponent(owner.id),
    );
    const list = document.createElement("div");
    list.className = "scopeList";
    if (!rows.length) list.textContent = "No paired ports.";
    for (const pair of rows) {
      const row = document.createElement("div");
      row.className = "scopeRow";
      row.textContent =
        (ports.find((p) => p.id === pair.port_a)?.name || "Missing port") +
        " ↔ " +
        (ports.find((p) => p.id === pair.port_b)?.name || "Missing port");
      if (editor()) {
        const b = document.createElement("button");
        b.textContent = "Unpair";
        b.onclick = async () => {
          try {
            await request("/api/pairings?id=" + encodeURIComponent(pair.id), {
              method: "DELETE",
            });
            await refresh();
            await inspect(owner);
          } catch (e) {
            error(e);
          }
        };
        row.append(b);
      }
      list.append(row);
    }
    inspectorContent.append(list);
  }
  function parentField(kind, o) {
    const a = ancestry(),
      d = o?.data || {};
    if (kind === "building")
      return {
        key: "parent",
        label: "Site",
        required: true,
        picker: { kinds: "site" },
        value: d.site_id || a.site_id || "",
      };
    if (kind === "generator")
      return {
        key: "parent",
        label: "Building",
        required: true,
        picker: { kinds: "building", parent: a.site_id || "" },
        value: d.building_id || a.building_id || "",
      };
    if (kind === "ups")
      return {
        key: "parent",
        label: "Building, room or telecom room",
        required: true,
        picker: {
          kinds: "building,room,telecom_room",
          parent: a.site_id || "",
        },
        value:
          d.room_id ||
          d.telecom_room_id ||
          d.building_id ||
          a.room_id ||
          a.telecom_room_id ||
          a.building_id ||
          "",
      };
    if (kind === "floor")
      return {
        key: "parent",
        label: "Building",
        required: true,
        picker: { kinds: "building", parent: a.site_id || "" },
        value: d.building_id || a.building_id || "",
      };
    if (kind === "room" || kind === "telecom_room")
      return {
        key: "parent",
        label: "Floor",
        required: true,
        picker: {
          kinds: "floor",
          parent: a.building_id || d.building_id || "",
        },
        value:
          d.floor_id ||
          a.floor_id ||
          (a.building_id || d.building_id
            ? "ground:" + (a.building_id || d.building_id)
            : ""),
      };
    if (["rack", "device", "patch_panel", "outlet"].includes(kind)) {
      const kinds =
        kind === "rack"
          ? "floor,room,telecom_room"
          : "floor,room,telecom_room,rack";
      return {
        key: "parent",
        label: "Place in",
        required: true,
        picker: { kinds, parent: a.building_id || "" },
        value:
          d.rack_id ||
          d.telecom_room_id ||
          d.room_id ||
          d.floor_id ||
          a.rack_id ||
          a.telecom_room_id ||
          a.room_id ||
          a.floor_id ||
          "",
      };
    }
    return null;
  }
  async function objectForm(kind, o = null) {
    if (!editor()) return;
    if (kind === "dvo" || kind === "fiber") {
      o = o || { data: { outlet_type: kind === "dvo" ? "dvo" : "fiber" } };
      kind = "outlet";
    }
    const existing = !!o?.id,
      d = o?.data || {},
      id = o?.id || crypto.randomUUID();
    const fields = [
      { key: "name", label: "Name", required: true, value: o?.name || "" },
      {
        key: "description",
        label: "Description",
        type: "textarea",
        value: d.description || "",
        full: true,
      },
    ];
    const parent = parentField(kind, o);
    if (parent) fields.push(parent);
    if (kind === "service")
      fields.push(
        {
          key: "category",
          label: "Service category",
          options: Object.entries(categories).map(([value, label]) => ({
            value,
            label,
          })),
          value:
            d.service_category ||
            (scope.kind === "service" ? "cloud_resource" : "cloud_provider"),
          required: true,
        },
        {
          key: "provider",
          label: "Provider",
          value: d.provider || "",
          help: "For example Google Cloud, Azure, AWS, or your provider name.",
        },
        {
          key: "account",
          label: "Account / subscription",
          value: d.account || "",
        },
        { key: "region", label: "Region", value: d.region || "" },
      );
    if (kind === "floor")
      fields.push({
        key: "order",
        label: "Floor order",
        type: "number",
        value: d.sort_order || 0,
      });
    if (kind === "outlet")
      fields.push({
        key: "outlet_type",
        label: "Outlet type",
        options: [
          { value: "dvo", label: "Data / Voice (copper)" },
          { value: "fiber", label: "Fiber outlet" },
        ],
        value: d.outlet_type || "dvo",
      });
    if (["device", "patch_panel"].includes(kind))
      fields.push({
        key: "device_type",
        label: "Equipment type",
        required: true,
        options: deviceTypes
          .filter((t) => t.object_kind === kind)
          .map((t) => ({ value: t.id, label: t.name })),
        value: d.device_type_id || "",
      });
    if (["device", "virtual_switch"].includes(kind))
      fields.push({
        key: "device_vrfs",
        label: "Device VRFs (one name per line)",
        type: "textarea",
        value: (d.vrfs || []).join("\n"),
        full: true,
        help: "Declare routing domains here, then assign them in each interface editor. Router interfaces default to all device VRFs.",
      });
    if (kind === "conduit")
      fields.push(
        {
          key: "endpoint_a",
          label: "Endpoint A",
          required: !existing,
          picker: { kinds: "site,building,floor,room,telecom_room,manhole" },
          value: d.endpoint_a_id || "",
          full: true,
          help: "Choose a building, optionally a specific floor or room. A room is not required.",
        },
        {
          key: "endpoint_b",
          label: "Endpoint B",
          required: !existing,
          picker: { kinds: "site,building,floor,room,telecom_room,manhole" },
          value: d.endpoint_b_id || "",
          full: true,
        },
      );
    const objectEditor = form(
      (existing ? "Edit " : "Add ") +
        (kind === "service" ? "cloud/service" : kind.replaceAll("_", " ")),
      fields,
      async (v) => {
        const saveNavigation = navigationRevision;
        const payload = {
          ...copy(d),
          id,
          kind,
          name: v.name.trim(),
          description: v.description,
          x: o?.x || 0,
          y: o?.y || 0,
          ...(existing ? { version: o.version } : {}),
        };
        if (v.parent) {
          const parent = (
            await request("/api/objects?ids=" + encodeURIComponent(v.parent))
          )[0];
          if (!parent) throw Error("The selected parent was removed.");
          for (const key of [
            "site_id",
            "building_id",
            "floor_id",
            "room_id",
            "telecom_room_id",
            "rack_id",
          ])
            delete payload[key];
          for (const key of [
            "site_id",
            "building_id",
            "floor_id",
            "room_id",
            "telecom_room_id",
            "rack_id",
          ])
            if (parent.data?.[key]) payload[key] = parent.data[key];
          for (const key of [
            "id",
            "kind",
            "name",
            "description",
            "x",
            "y",
            "version",
            "sort_order",
            "auto_ground",
          ])
            delete payload[key];
          Object.assign(payload, {
            id,
            kind,
            name: v.name.trim(),
            description: v.description,
            x: o?.x || 0,
            y: o?.y || 0,
            ...(existing ? { version: o.version } : {}),
          });
          const key = {
            site: "site_id",
            building: "building_id",
            floor: "floor_id",
            room: "room_id",
            telecom_room: "telecom_room_id",
            rack: "rack_id",
          }[parent.kind];
          payload[key] = parent.id;
        }
        if (kind === "service") {
          payload.service_category = v.category;
          payload.provider = v.provider;
          payload.account = v.account;
          payload.region = v.region;
          if (!existing && scope.kind === "service")
            payload.hosting_source_id = scope.id;
        }
        if (kind === "floor") payload.sort_order = Number(v.order) || 0;
        if (kind === "outlet") payload.outlet_type = v.outlet_type;
        if (["device", "patch_panel"].includes(kind))
          payload.device_type_id = v.device_type;
        if (["device", "virtual_switch"].includes(kind))
          payload.vrfs = vrfUI.parse(v.device_vrfs);
        if (kind === "conduit") {
          payload.endpoint_a_id = v.endpoint_a;
          payload.endpoint_b_id = v.endpoint_b;
          if (!existing) Object.assign(payload, ancestry());
        }
        await request("/api/objects", json("POST", payload));
        dialog.close();
        deferred = false;
        await refresh();
        if (!existing && labels[kind] && saveNavigation === navigationRevision)
          await navigate(kind, id);
      },
    );
    const declarations = objectEditor.inputs.device_vrfs;
    if (declarations) {
      const update = () => {
        const type = deviceTypes.find(
          (t) => t.id === objectEditor.inputs.device_type?.value,
        );
        const role = vrfUI.role({
          device_visual:
            type?.visual || d.device_visual || d.device_type_id || "",
        });
        declarations.closest(".field").hidden = !role;
      };
      objectEditor.inputs.device_type?.addEventListener("change", update);
      update();
    }
  }
  function relationshipForm(o = null) {
    const initial = o || {};
    const relationForm = form(
      o ? "Edit relationship" : "Add cloud relationship",
      [
        {
          key: "type",
          label: "Relationship",
          required: true,
          options: [
            { value: "accesses", label: "Accesses" },
            { value: "hosts", label: "Hosts" },
            { value: "depends_on", label: "Depends on" },
          ],
          value: initial.relationship_type || "accesses",
        },
        { key: "label", label: "Label", value: initial.label || "" },
        {
          key: "source",
          label: "Source",
          required: true,
          picker: {
            kinds:
              "site,building,floor,room,telecom_room,rack,device,service,outlet,virtual_machine,virtual_switch",
          },
          value: initial.source_id || scope.id,
          full: true,
        },
        {
          key: "target",
          label: "Target service",
          required: true,
          picker: { kinds: "service" },
          value: initial.target_id || "",
          full: true,
        },
      ],
      async (v) => {
        await request(
          "/api/relationships",
          json(o ? "PUT" : "POST", {
            id: o?.id || crypto.randomUUID(),
            version: o?.version || 0,
            source_id: v.source,
            target_id: v.target,
            relationship_type: v.type,
            label: v.label,
          }),
        );
        dialog.close();
        await refresh();
      },
    );
    if (o) {
      const undo = document.createElement("button");
      undo.type = "button";
      undo.textContent = "Undo last relationship edit";
      undo.onclick = async () => {
        try {
          await request("/api/relationships/undo", json("POST", {}));
          dialog.close();
          await refresh();
        } catch (e) {
          relationForm.error.textContent = e.message;
        }
      };
      relationForm.actions.prepend(undo);
    }
  }
  function conversion(o) {
    form(
      "Convert " + o.name + " to a global cloud/service",
      [
        {
          key: "category",
          label: "Service category",
          options: Object.entries(categories).map(([value, label]) => ({
            value,
            label,
          })),
          value: "cloud_provider",
          help: "Existing interfaces and IDs are retained. Physical connections must be resolved first.",
        },
      ],
      async (v) => {
        await request(
          "/api/topology/convert-service",
          json("POST", {
            id: o.id,
            version: o.version,
            service_category: v.category,
          }),
        );
        dialog.close();
        await navigate("service", o.id);
      },
    );
  }
  async function portForm(owner, port = null) {
    try {
      owner = (
        await request("/api/objects?ids=" + encodeURIComponent(owner.id))
      )[0];
      if (!owner) throw Error("This device was removed.");
      if (port) {
        const current = await request(
          "/api/interfaces?ids=" + encodeURIComponent(port.id),
        );
        if (!current[0]) throw Error("This interface was removed.");
        port = current[0];
      }
    } catch (e) {
      error(e);
      return;
    }
    let d = {};
    if (port)
      try {
        d =
          typeof port.data === "string"
            ? JSON.parse(port.data)
            : port.data || {};
      } catch {}
    const f = form(
      port ? "Edit port" : "Add interface / port",
      [
        { key: "name", label: "Name", required: true, value: port?.name || "" },
        { key: "connector", label: "Connector", value: d.connector || "" },
        { key: "speed", label: "Speed", value: d.speed || "" },
        { key: "ip", label: "IP address / prefix", value: d.ip_address || "" },
        {
          key: "description",
          label: "Description",
          value: d.description || "",
        },
        ...vrfUI.fields(owner, d),
      ],
      async (v) => {
        await request(
          "/api/interfaces",
          json("POST", {
            ...(port ? { id: port.id, expected_revision: port.revision } : {}),
            device_id: owner.id,
            name: v.name,
            data: {
              ...d,
              ...f.readCapacity(),
              connector: v.connector,
              speed: v.speed,
              ip_address: v.ip,
              description: v.description,
            },
          }),
        );
        dialog.close();
        await refresh();
        await inspect(owner);
      },
    );
    vrfUI.bind(f, owner, d);
    if (port) {
      const del = document.createElement("button");
      del.type = "button";
      del.textContent = "Delete port";
      del.className = "danger";
      del.onclick = async () => {
        try {
          await request("/api/interfaces?id=" + encodeURIComponent(port.id), {
            method: "DELETE",
          });
          dialog.close();
          await refresh();
          await inspect(owner);
        } catch (e) {
          f.error.textContent = e.message;
        }
      };
      f.actions.prepend(del);
    }
  }
  async function populatePorts(select, id, selected = "") {
    if (!id) {
      select.replaceChildren(new Option("Choose an item first", ""));
      return;
    }
    const ports = await request(
      "/api/interfaces?device_id=" + encodeURIComponent(id),
    );
    selectOptions(
      select,
      ports.sort((a, b) =>
        a.name.localeCompare(b.name, undefined, { numeric: true }),
      ),
      selected || (ports.length === 1 ? ports[0].id : ""),
      "Choose interface",
    );
  }
  function cableForm(
    source = null,
    target = null,
    existing = null,
    conduit = null,
  ) {
    if (!editor()) return;
    const d = existing?.data || {},
      id = existing?.id || crypto.randomUUID();
    const fields = [
      {
        key: "source",
        label: "Source item",
        required: true,
        picker: {
          kinds:
            "device,patch_panel,outlet,service,virtual_machine,virtual_switch",
        },
        value: source?.id || "",
        full: true,
      },
      {
        key: "source_port",
        label: "Source interface",
        required: true,
        options: [],
        value: existing?.source_interface || "",
      },
      {
        key: "target",
        label: "Target item",
        required: true,
        picker: {
          kinds:
            "device,patch_panel,outlet,service,virtual_machine,virtual_switch",
        },
        value: target?.id || "",
        full: true,
      },
      {
        key: "target_port",
        label: "Target interface",
        required: true,
        options: [],
        value: existing?.target_interface || "",
      },
      {
        key: "class",
        label: "Connection class",
        options: [
          { value: "physical", label: "Physical cable" },
          { value: "virtual", label: "Virtual" },
          { value: "provider_service", label: "Provider service" },
          { value: "logical_tunnel", label: "Logical tunnel" },
        ],
        value: d.connection_class || "physical",
      },
      {
        key: "medium",
        label: "Physical medium",
        options: [
          { value: "fiber", label: "Fiber" },
          { value: "copper_twisted_pair", label: "Copper twisted pair" },
          { value: "backplane", label: "Backplane" },
          { value: "wireless", label: "Wireless/radio" },
          { value: "coax", label: "Coax" },
        ],
        value: d.physical_type || d.medium || "fiber",
      },
      { key: "label", label: "Connection label", value: d.label || "" },
      { key: "cable_type", label: "Cable type", value: d.cable_type || "OS2" },
      {
        key: "connector_a",
        label: "Connector A",
        value: d.connector_a || "LC",
      },
      {
        key: "connector_b",
        label: "Connector B",
        value: d.connector_b || "LC",
      },
      {
        key: "strands",
        label: "Fiber strand count",
        type: "number",
        min: 1,
        step: 1,
        value: d.strand_count || 12,
      },
      {
        key: "virtual_type",
        label: "Virtual type",
        value: d.virtual_type || "ethernet",
      },
      {
        key: "service_type",
        label: "Provider service",
        options: [
          { value: "internet", label: "Internet" },
          { value: "mpls", label: "MPLS" },
          { value: "dark_fiber", label: "Dark fiber" },
          { value: "leased_circuit", label: "Leased circuit" },
          { value: "carrier_ethernet", label: "Carrier Ethernet" },
        ],
        value: d.service_type || "internet",
      },
      { key: "provider", label: "Provider", value: d.provider || "" },
      { key: "circuit", label: "Circuit ID", value: d.circuit_id || "" },
      {
        key: "tunnel",
        label: "Tunnel protocol",
        value: d.tunnel_protocol || "GRE",
      },
      {
        key: "description",
        label: "Description",
        type: "textarea",
        value: d.description || "",
        full: true,
      },
    ];
    const f = form(
      existing
        ? "Edit connection"
        : conduit
          ? "Create cable through " + conduit.name
          : "Connect interfaces",
      fields,
      async (v) => {
        const spec = {
          ...d,
          connection_class: v.class,
          label: v.label,
          description: v.description,
        };
        if (v.class === "physical") {
          Object.assign(spec, {
            medium: v.medium,
            physical_type: v.medium,
            cable_type: v.cable_type,
            connector_a: v.connector_a,
            connector_b: v.connector_b,
          });
          if (v.medium === "fiber") spec.strand_count = Number(v.strands);
        }
        if (v.class === "virtual") spec.virtual_type = v.virtual_type;
        if (v.class === "provider_service")
          Object.assign(spec, {
            service_type: v.service_type,
            provider: v.provider,
            circuit_id: v.circuit,
          });
        if (v.class === "logical_tunnel") spec.tunnel_protocol = v.tunnel;
        await request(
          "/api/links",
          json(existing ? "PUT" : "POST", {
            id,
            source_interface: v.source_port,
            target_interface: v.target_port,
            data: spec,
            ...(existing ? { expected_data: JSON.stringify(d) } : {}),
            ...(conduit ? { conduit_id: conduit.id } : {}),
          }),
        );
        dialog.close();
        await refresh();
      },
    );
    f.inputs.source.onchange = () =>
      populatePorts(f.inputs.source_port, f.inputs.source.value).catch(
        (e) => (f.error.textContent = e.message),
      );
    f.inputs.target.onchange = () =>
      populatePorts(f.inputs.target_port, f.inputs.target.value).catch(
        (e) => (f.error.textContent = e.message),
      );
    populatePorts(
      f.inputs.source_port,
      source?.id,
      existing?.source_interface,
    ).catch(error);
    populatePorts(
      f.inputs.target_port,
      target?.id,
      existing?.target_interface,
    ).catch(error);
    const visibility = () => {
      const cls = f.inputs.class.value;
      for (const key of [
        "medium",
        "cable_type",
        "connector_a",
        "connector_b",
        "strands",
      ])
        f.inputs[key].closest(".field").hidden = cls !== "physical";
      f.inputs.strands.closest(".field").hidden =
        cls !== "physical" || f.inputs.medium.value !== "fiber";
      f.inputs.virtual_type.closest(".field").hidden = cls !== "virtual";
      for (const key of ["service_type", "provider", "circuit"])
        f.inputs[key].closest(".field").hidden = cls !== "provider_service";
      f.inputs.tunnel.closest(".field").hidden = cls !== "logical_tunnel";
    };
    f.inputs.class.onchange = visibility;
    f.inputs.medium.onchange = visibility;
    visibility();
  }
  function attachment() {
    const f = form(
      "Connect Internet / MPLS service",
      [
        {
          key: "gateway",
          label: "Gateway device",
          required: true,
          picker: { kinds: "device", parent: scopeParent("site") },
          full: true,
        },
        {
          key: "port",
          label: "Gateway interface",
          required: true,
          options: [],
        },
        {
          key: "service",
          label: "Internet / MPLS service",
          required: true,
          picker: { kinds: "service", category: "internet,mpls" },
          value: scope.kind === "service" ? scope.id : "",
          full: true,
        },
        { key: "circuit", label: "Circuit ID" },
        { key: "bandwidth", label: "Bandwidth" },
        { key: "label", label: "Connection label" },
      ],
      async (v) => {
        await request(
          "/api/topology/attachment",
          json("POST", {
            gateway_interface: v.port,
            service_id: v.service,
            circuit_id: v.circuit,
            bandwidth: v.bandwidth,
            label: v.label,
          }),
        );
        dialog.close();
        await refresh();
      },
    );
    f.inputs.gateway.onchange = () =>
      populatePorts(f.inputs.port, f.inputs.gateway.value).catch(
        (e) => (f.error.textContent = e.message),
      );
  }
  function assignCable(conduit = scene.scope) {
    const f = form(
      "Assign cable to " + conduit.name,
      [
        {
          key: "cable",
          label: "Physical cable",
          required: true,
          options: [],
          full: true,
        },
        { key: "search", label: "Search by cable label / endpoints" },
      ],
      async (v) => {
        await request(
          "/api/topology/assign-conduit",
          json("POST", {
            conduit_id: conduit.id,
            link_id: v.cable,
            sequence: sequence,
          }),
        );
        dialog.close();
        await refresh();
      },
    );
    let timer;
    const choices = async () => {
      const data = await request(
        "/api/topology/cables?q=" + encodeURIComponent(f.inputs.search.value),
      );
      selectOptions(
        f.inputs.cable,
        data.items.map((item) => ({ value: item.id, label: item.label })),
        f.inputs.cable.value,
        "Choose physical cable",
      );
    };
    f.inputs.search.oninput = () => {
      clearTimeout(timer);
      timer = setTimeout(() => choices().catch(error), 180);
    };
    choices().catch(error);
  }

  async function editCableRoute(item) {
    const prior = await request(
        "/api/routes?link_id=" + encodeURIComponent(item.id),
      ),
      ids = prior.map((r) => r.infrastructure_id);
    const f = form("Cable route", [], async () => {
      await request(
        "/api/routes",
        json("POST", {
          link_id: item.id,
          infrastructure_ids: ids,
          expected_infrastructure_ids: prior.map((r) => r.infrastructure_id),
        }),
      );
      dialog.close();
      await refresh();
    });
    const list = document.createElement("div");
    list.className = "scopeList";
    list.style.gridColumn = "1/-1";
    f.grid.append(list);
    const render = async () => {
      list.replaceChildren();
      for (const [index, id] of ids.entries()) {
        const row = document.createElement("div");
        row.className = "scopeRow";
        const label = document.createElement("strong");
        label.textContent =
          (await request("/api/objects?ids=" + encodeURIComponent(id)))[0]
            ?.name || "Missing route item";
        row.append(label);
        for (const [text, fn] of [
          [
            "↑",
            () => {
              if (index) {
                [ids[index - 1], ids[index]] = [ids[index], ids[index - 1]];
                render();
              }
            },
          ],
          [
            "↓",
            () => {
              if (index < ids.length - 1) {
                [ids[index + 1], ids[index]] = [ids[index], ids[index + 1]];
                render();
              }
            },
          ],
          [
            "Remove",
            () => {
              ids.splice(index, 1);
              render();
            },
          ],
        ]) {
          const b = document.createElement("button");
          b.type = "button";
          b.textContent = text;
          b.onclick = fn;
          row.append(b);
        }
        list.append(row);
      }
    };
    await render();
    const search = document.createElement("input"),
      picker = document.createElement("select"),
      add = document.createElement("button");
    search.placeholder = "Search conduits and manholes";
    search.setAttribute("aria-label", "Find route infrastructure");
    picker.setAttribute("aria-label", "Route infrastructure");
    add.type = "button";
    add.textContent = "Add route item";
    add.onclick = () => {
      if (picker.value) {
        ids.push(picker.value);
        render();
      }
    };
    const choices = async () => {
      const result = await request(
        "/api/topology/choices?kinds=conduit,manhole&q=" +
          encodeURIComponent(search.value),
      );
      selectOptions(
        picker,
        result.items,
        picker.value,
        "Choose infrastructure",
      );
    };
    let timer;
    search.oninput = () => {
      clearTimeout(timer);
      timer = setTimeout(() => choices().catch(error), 180);
    };
    f.grid.append(search, picker, add);
    await choices();
  }
  function chooseScope(kind = null) {
    const kinds =
      kind ||
      {
        global: "site,service",
        site: "building",
        building: "floor",
        floor: "room,telecom_room",
        room: "rack",
        telecom_room: "rack",
        rack: "rack",
        service: "service",
        conduit: "conduit",
      }[scope.kind];
    form(
      "Open topology view",
      [
        {
          key: "item",
          label: "Location / service",
          picker: {
            kinds,
            parent: kind
              ? ""
              : ["site", "building", "floor", "room", "telecom_room"].includes(
                    scope.kind,
                  )
                ? scope.id
                : "",
          },
          required: true,
          full: true,
        },
      ],
      async (v) => {
        const row = (
          await request("/api/objects?ids=" + encodeURIComponent(v.item))
        )[0];
        dialog.close();
        await navigate(row.kind, row.id);
      },
    );
  }
  function create(kind) {
    if (!editor()) return;
    if (!(scene?.allowed_creation_kinds || []).includes(kind)) {
      notice.textContent = "That action is unavailable at this level.";
      return;
    }
    if (kind === "relationship") return relationshipForm();
    if (kind === "attachment") return attachment();
    if (kind === "cable")
      return cableForm(
        null,
        null,
        null,
        scope.kind === "conduit" ? scene.scope : null,
      );
    if (kind === "assign_cable") return assignCable();
    if (kind === "interface") {
      if (
        selected &&
        [
          "device",
          "patch_panel",
          "outlet",
          "service",
          "virtual_machine",
          "virtual_switch",
        ].includes(selected.kind)
      )
        return portForm(selected);
      notice.textContent = "Select rack equipment, then Add interface.";
      return;
    }
    return objectForm(kind);
  }
  function context(e) {
    const menu = $("#canvasContextMenu");
    menu.replaceChildren();
    const o = e.target === cy ? null : nodes.get(e.target.id());
    const add = (label, fn) => {
      const b = document.createElement("button");
      b.textContent = label;
      b.setAttribute("role", "menuitem");
      b.addEventListener("click", () => {
        menu.hidden = true;
        Promise.resolve(fn()).catch(error);
      });
      menu.append(b);
    };
    if (o) {
      add("Inspect " + o.name, () => inspect(o));
      if (labels[o.kind]) add("Open " + o.name, () => navigate(o.kind, o.id));
      if (editor()) add("Edit " + o.name, () => objectForm(o.kind, o));
    }
    for (const kind of scene.allowed_creation_kinds || [])
      add(createLabels[kind], () => create(kind));
    const original = e.originalEvent,
      rect = $(".canvasWrap").getBoundingClientRect();
    menu.style.left =
      Math.min(
        Math.max(0, (original?.clientX || rect.left) - rect.left),
        rect.width - 220,
      ) + "px";
    menu.style.top =
      Math.min(
        Math.max(0, (original?.clientY || rect.top) - rect.top),
        rect.height - 250,
      ) + "px";
    menu.hidden = false;
  }
  function updateMenu() {
    const section = $("#menuPanel .menuSection .menuItems");
    section.replaceChildren();
    for (const kind of scene?.allowed_creation_kinds || []) {
      const b = document.createElement("button");
      b.type = "button";
      b.setAttribute("role", "menuitem");
      b.textContent = createLabels[kind];
      b.onclick = () => create(kind);
      section.append(b);
    }
    if (!section.children.length) {
      const text = document.createElement("span");
      text.className = "hint";
      text.textContent = "Browse and inspect topology";
      section.append(text);
    }
  }
  $("#scopeLevel").onchange = () => {
    const kind = $("#scopeLevel").value;
    if (kind === "global") navigate("global");
    else chooseScope(kind);
    $("#scopeLevel").value = scope.kind;
  };
  $("#scopeBack").onclick = () => {
    const previous = history.pop();
    if (previous) navigate(previous.kind, previous.id, false);
  };
  $("#scopeOpen").onclick = () => chooseScope();
  for (const key of ["scopeCategory", "scopeRoomType", "scopeNetwork"])
    $("#" + key).onchange = () => refresh(false, true);
  $("#scopeCreate").onchange = () => {
    const kind = $("#scopeCreate").value;
    $("#scopeCreate").value = "";
    create(kind);
  };
  more.onclick = () => refresh(true);
  for (const editDialog of [dialog, $("#objectDialog"), $("#linkDialog")])
    editDialog.addEventListener("close", () => {
      if (deferred) {
        deferred = false;
        refresh().catch(error);
      }
    });
  document.addEventListener("click", (e) => {
    if (!e.target.closest("#canvasContextMenu"))
      $("#canvasContextMenu").hidden = true;
  });
  // Reuse authentication, catalogs and durable WebSocket replay. Replace the
  // original all-inventory diagram loader with the focused server projection.
  const originalLoad = load;
  load = async () => {
    await refresh();
    updateMenu();
  };
  openSearchHit = async (hit) => {
    searchResults.hidden = true;
    if (labels[hit.kind]) return navigate(hit.kind, hit.id);
    const d = hit.data || {},
      parent =
        d.rack_id ||
        d.telecom_room_id ||
        d.room_id ||
        d.floor_id ||
        d.building_id ||
        d.site_id ||
        d.host_id;
    if (parent) {
      const record = (
        await request("/api/objects?ids=" + encodeURIComponent(parent))
      )[0];
      if (record) {
        if (labels[record.kind]) await navigate(record.kind, record.id);
        else if (record.data?.floor_id)
          await navigate("floor", record.data.floor_id);
        const visible = nodes.get(hit.id) || nodes.get(d.host_id);
        if (visible) {
          const n = cy.getElementById(visible.id);
          if (n.length) cy.fit(n, 120);
        }
        return inspect(hit);
      }
    }
    notice.textContent =
      "This item has an unresolved legacy location. Edit its placement to include it in focused views.";
    inspect(hit);
  };
  const originalOpen = openObjectForm;
  openObjectForm = (
    kind = "device",
    node = null,
    parent = null,
    placement = null,
  ) => {
    if (node) {
      const item = {
        id: node.data("object_id") || node.id(),
        kind: node.data("kind"),
        name: node.data("label"),
        data: node.data("meta") || {},
        version: node.data("version"),
        ...node.position(),
      };
      return objectForm(item.kind, item);
    }
    if (["label", "shape", "virtual_machine", "virtual_switch"].includes(kind))
      return originalOpen(kind, node, parent, placement);
    if ((scene?.allowed_creation_kinds || []).includes(kind))
      return objectForm(kind);
    notice.textContent =
      "Open the containing location before creating this item.";
  };
  const originalConnect = connectInterfaces;
  connectInterfaces = (source = "", target = "") =>
    Promise.all([
      source ? request("/api/objects?ids=" + encodeURIComponent(source)) : [],
      target ? request("/api/objects?ids=" + encodeURIComponent(target)) : [],
    ])
      .then(([a, b]) => cableForm(a[0], b[0]))
      .catch(error);

  pairPanel = async () => {
    if (selected?.kind !== "patch_panel") {
      notice.textContent = "Select a patch panel first.";
      return;
    }
    panelPair(
      selected,
      await request(
        "/api/interfaces?device_id=" + encodeURIComponent(selected.id),
      ),
    );
  };
  unpairPanel = async () => {
    if (selected?.kind !== "patch_panel") {
      notice.textContent = "Select a patch panel first.";
      return;
    }
    await panelPairs(
      selected,
      await request(
        "/api/interfaces?device_id=" + encodeURIComponent(selected.id),
      ),
    );
  };
  loadHypervisorHosts = async (selectedID = "") => {
    const reply = await request(
      "/api/topology/choices?kinds=device&hypervisor=1",
    );
    hypervisorHosts = reply.items;
    if (selectedID && !hypervisorHosts.some((o) => o.id === selectedID)) {
      const row = (
        await request("/api/objects?ids=" + encodeURIComponent(selectedID))
      )[0];
      if (row) hypervisorHosts.push(row);
    }
    selectOptions(vmHost, hypervisorHosts, selectedID, "Choose hypervisor");
  };
  setCableRoute = async () => {
    const f = form(
      "Choose cable route",
      [
        { key: "link", label: "Physical cable", options: [], required: true },
        { key: "search", label: "Search label/endpoints" },
      ],
      async (v) => {
        dialog.close();
        await editCableRoute({ id: v.link });
      },
    );
    const fill = async () => {
      const result = await request(
        "/api/topology/cables?q=" + encodeURIComponent(f.inputs.search.value),
      );
      selectOptions(
        f.inputs.link,
        result.items.map((o) => ({ value: o.id, label: o.label })),
        f.inputs.link.value,
        "Choose physical cable",
      );
    };
    f.inputs.search.oninput = () => fill().catch(error);
    await fill();
  };
  const originalLocationFilters = loadLocationFilters;
  loadLocationFilters = async () => {
    const result = await request("/api/networks");
    networkInventory = result || [];
    return result;
  };
  const originalApplyRole = applyRole;
  applyRole = (p) => {
    originalApplyRole(p);
    if (scene) syncControls();
    updateMenu();
  };
  const originalInspect = showObjectInspector;
  showObjectInspector = (o) => inspect(o);
  new ResizeObserver(() => {
    document.documentElement.style.setProperty(
      "--scope-header-height",
      document.querySelector("header").offsetHeight + "px",
    );
    if (cy && !cy.destroyed()) cy.resize();
  }).observe(document.querySelector("header"));
  const wait = setInterval(() => {
    if (window.userRole) {
      clearInterval(wait);
      refresh(false, true).then(updateMenu).catch(error);
    }
  }, 80);
  return {
    navigate,
    refresh,
    create,
    inspect,
    get scope() {
      return scope;
    },
    get scene() {
      return scene;
    },
    get nodes() {
      return nodes;
    },
    get edges() {
      return edges;
    },
  };
})();
