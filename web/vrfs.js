/* Device-local VRF declarations and shared interface controls. */
window.vrfUI = (() => {
  const meta = (owner) => owner?.meta || owner?.data || owner || {};
  const role = (owner) => {
    const d = meta(owner),
      visual = String(
        d.device_visual || d.device_type_id || d.device_type || "",
      ).toLowerCase();
    return visual.includes("router")
      ? "router"
      : visual.includes("switch")
        ? "switch"
        : "";
  };
  const names = (owner) =>
    Array.isArray(meta(owner).vrfs) ? meta(owner).vrfs : [];
  const parse = (value) =>
    String(value || "")
      .split(/\r?\n/)
      .map((s) => s.trim())
      .filter(Boolean);
  function fields(owner, port = {}) {
    const kind = role(owner);
    const mode =
      port.vrf_mode ||
      (kind === "router" && !Object.hasOwn(port, "vrfs") ? "all" : "selected");
    const ownerKind = owner.kind || meta(owner).kind || "device";
    const passive = [
      "patch_panel",
      "shape",
      "label",
      "manhole",
      "service",
      "outlet",
    ].includes(ownerKind);
    const defaultType = passive
      ? "passive"
      : ["virtual_machine", "virtual_switch"].includes(ownerKind)
        ? "virtual"
        : "physical";
    const fields = [
      {
        key: "interface_usage",
        label: "Interface usage",
        options: [
          { value: "regular", label: "Regular" },
          { value: "uplink", label: "Uplink" },
        ],
        value: port.interface_usage || "regular",
      },
      {
        key: "interface_type",
        label: "Interface type",
        options: (passive
          ? ["passive"]
          : ["physical", "virtual", "logical"]
        ).map((value) => ({
          value,
          label: value[0].toUpperCase() + value.slice(1),
        })),
        value: port.interface_type || defaultType,
      },
    ];
    if (!kind) return fields;
    if (kind === "router")
      fields.push({
        key: "vrf_mode",
        label: "VRF assignment",
        options: [
          { value: "all", label: "All device VRFs (default)" },
          { value: "selected", label: "Select specific VRFs" },
        ],
        value: mode,
      });
    fields.push({
      key: "vrfs",
      label: kind === "router" ? "Interface VRFs" : "Interface VRF",
      options: names(owner).map((name) => ({ value: name, label: name })),
      multiple: kind === "router",
      value: kind === "router" ? port.vrfs || [] : port.vrfs?.[0] || "",
      empty: kind === "router" ? "" : "Default / global routing table",
      full: true,
      help:
        kind === "router"
          ? "All device VRFs includes VRFs added later. Choose specific VRFs to limit this interface. Declare VRFs with right-click → Edit on the device."
          : "Choose one of the VRFs declared with right-click → Edit on the device, or use the default routing table.",
    });
    return fields;
  }
  function parseRate(text) {
    if (String(text || "").length > 64)
      throw Error("Network rate is too long.");
    const m = String(text || "")
      .trim()
      .match(/^([0-9]+(?:\.[0-9]+)?)\s*([kKmMgGtTpP]?)(?:b(?:ps|\/s))?$/);
    if (!m)
      throw Error("Use a positive network rate such as 100Mbps or 1Gbps.");
    const [whole, fraction = ""] = m[1].split("."),
      power = { "": 0, K: 3, M: 6, G: 9, T: 12, P: 15 }[m[2].toUpperCase()];
    const numerator = BigInt(whole + fraction) * 10n ** BigInt(power),
      denominator = 10n ** BigInt(fraction.length);
    if (numerator % denominator)
      throw Error("Rates must resolve to whole bits per second.");
    const value = numerator / denominator;
    if (value <= 0n || value > BigInt(Number.MAX_SAFE_INTEGER))
      throw Error("Rate is outside the supported positive range.");
    return Number(value);
  }
  function formatRate(rate) {
    for (const [unit, scale] of [
      ["Tbps", 1e12],
      ["Gbps", 1e9],
      ["Mbps", 1e6],
      ["Kbps", 1e3],
    ])
      if (rate >= scale && rate % scale === 0) return rate / scale + unit;
    return rate + "bps";
  }
  function bind(editor, owner, port = {}, host = editor.grid) {
    host.capacityEditor?.dispose?.();
    const inputs = editor.inputs,
      mode = inputs.vrf_mode,
      choices = inputs.vrfs,
      speed = inputs.speed;
    const nestedEditor = host.id === "legacyInterfaceVRFs";
    const panel = document.createElement("div");
    panel.className = "field full interfaceBudgetPanel";
    const heading = document.createElement("strong");
    heading.textContent = "VRF bandwidth allocations";
    const rows = document.createElement("div"),
      total = document.createElement("div");
    total.className = "interfaceBudgetTotal";
    total.setAttribute("role", "status");
    panel.append(heading, rows, total);
    host.append(panel);
    const amounts = new Map(
      Object.entries(port.vrf_bandwidth_bps || {}).map(([name, rate]) => [
        name,
        formatRate(rate),
      ]),
    );
    let rendered = "",
      active = [];
    const values = () =>
      Object.fromEntries(
        Object.entries(inputs).map(([key, input]) => [
          key,
          input.multiple
            ? [...input.selectedOptions].map((o) => o.value)
            : input.value,
        ]),
      );
    const selected = () =>
      mode?.value === "all"
        ? names(owner)
        : choices
          ? [...choices.selectedOptions].map((o) => o.value).filter(Boolean)
          : [];
    function update() {
      if (choices) choices.disabled = mode?.value === "all";
      active = selected();
      const uplink =
        role(owner) === "router" && inputs.interface_usage.value === "uplink";
      panel.hidden = !uplink || !active.length;
      const key = JSON.stringify(active);
      if (key !== rendered) {
        rendered = key;
        rows.replaceChildren();
        active.forEach((name, index) => {
          const row = document.createElement("div");
          row.className = "field";
          const label = document.createElement("label"),
            input = document.createElement("input");
          input.id = panel.id + "_allocation_" + index;
          input.dataset.vrf = name;
          input.value = amounts.get(name) || "";
          input.placeholder = "e.g. 250Mbps";
          input.maxLength = 64;
          label.htmlFor = input.id;
          label.textContent = name;
          input.oninput = () => {
            amounts.set(name, input.value);
            update();
          };
          row.append(label, input);
          rows.append(row);
        });
      }
      const capacityRequired = uplink && active.length >= 2;
      if (speed) {
        speed.required =
          !nestedEditor &&
          (inputs.interface_type.value === "physical" || capacityRequired);
        speed.placeholder = "e.g. 1Gbps";
        speed.maxLength = 64;
      }
      for (const input of rows.querySelectorAll("input"))
        input.required = !nestedEditor && capacityRequired;
      let capacity = 0,
        sum = 0,
        error = "",
        missing = 0;
      try {
        if (speed?.value.trim()) capacity = parseRate(speed.value);
        for (const name of active) {
          const text = amounts.get(name)?.trim();
          if (text) {
            const rate = parseRate(text);
            if (sum > Number.MAX_SAFE_INTEGER - rate)
              throw Error("Total bandwidth exceeds the supported range.");
            sum += rate;
          } else missing++;
        }
      } catch (e) {
        error = e.message;
      }
      if (!error && capacity && sum > capacity)
        error = "Allocated bandwidth exceeds uplink capacity.";
      total.classList.toggle("formError", !!error);
      total.textContent =
        error ||
        "Capacity: " +
          (capacity ? formatRate(capacity) : "required") +
          " · Allocated: " +
          formatRate(sum) +
          " · Remaining: " +
          (capacity ? formatRate(capacity - sum) : "unknown") +
          (missing ? " · " + missing + " VRF allocation(s) missing" : "");
    }
    panel.id = (host.id || "interface") + "_bandwidth";
    for (const input of [
      mode,
      choices,
      inputs.interface_usage,
      inputs.interface_type,
    ])
      input?.addEventListener("change", update);
    speed?.addEventListener("input", update);
    editor.readCapacity = () => {
      const v = values();
      const speedRequired =
        v.interface_type === "physical" ||
        (role(owner) === "router" &&
          v.interface_usage === "uplink" &&
          selected().length >= 2);
      if (speedRequired && !speed?.value.trim())
        throw Error("Interface speed is required.");
      if (speed?.value.trim()) parseRate(speed.value);
      const data = {
        interface_usage: v.interface_usage,
        interface_type: v.interface_type,
        ...(role(owner) ? assignment(v) : {}),
        vrf_bandwidth_bps: {},
      };
      if (role(owner) === "router" && v.interface_usage === "uplink") {
        let sum = 0;
        for (const name of selected()) {
          const text = amounts.get(name)?.trim();
          if (!text) {
            if (selected().length >= 2)
              throw Error("Missing bandwidth allocation for VRF " + name);
            continue;
          }
          const rate = parseRate(text);
          if (sum > Number.MAX_SAFE_INTEGER - rate)
            throw Error("Total bandwidth exceeds the supported range.");
          sum += rate;
          data.vrf_bandwidth_bps[name] = rate;
        }
        if (sum && !speed?.value.trim())
          throw Error("Uplink speed is required.");
        if (speed?.value.trim() && sum > parseRate(speed.value))
          throw Error("Allocated bandwidth exceeds uplink capacity.");
      }
      return data;
    };
    editor.dispose = () => speed?.removeEventListener("input", update);
    update();
    return editor;
  }
  function assignment(values) {
    const selected = Array.isArray(values.vrfs)
      ? values.vrfs
      : values.vrfs
        ? [values.vrfs]
        : [];
    return {
      vrf_mode: values.vrf_mode || "selected",
      vrfs: values.vrf_mode === "all" ? [] : selected,
    };
  }
  function summary(port, owner) {
    const all =
      port.vrf_mode === "all" ||
      (!port.vrf_mode &&
        role(owner) === "router" &&
        !Object.hasOwn(port, "vrfs"));
    const selected =
      port.effective_vrfs || (all ? names(owner) : port.vrfs || []);
    return all
      ? "All device VRFs" +
          (selected.length ? " · " + selected.join(", ") : " (none declared)")
      : selected.length
        ? selected.join(", ")
        : "Default / global";
  }
  function mountLegacy(host, owner, port = {}) {
    host.replaceChildren();
    const inputs = {};
    for (const field of fields(owner, port)) {
      const box = document.createElement("div");
      box.className = "field full";
      const label = document.createElement("label"),
        select = document.createElement("select");
      select.id = host.id + "_" + field.key;
      label.htmlFor = select.id;
      label.textContent = field.label;
      select.multiple = !!field.multiple;
      if (field.multiple)
        select.size = Math.min(6, Math.max(3, field.options.length));
      if (!field.multiple && field.empty)
        select.append(new Option(field.empty, ""));
      for (const option of field.options)
        select.append(
          new Option(
            option.label,
            option.value,
            false,
            Array.isArray(field.value)
              ? field.value.includes(option.value)
              : field.value === option.value,
          ),
        );
      box.append(label, select);
      if (field.help) {
        const help = document.createElement("small");
        help.textContent = field.help;
        box.append(help);
      }
      host.append(box);
      inputs[field.key] = select;
    }
    inputs.speed = document.querySelector(
      host.id === "legacyInterfaceVRFs"
        ? "#editInterfaceSpeed"
        : "#interfaceSpeed",
    );
    host.capacityEditor = bind({ inputs }, owner, port, host);
    host.vrfInputs = inputs;
  }
  function readLegacy(host, owner) {
    return host.capacityEditor?.readCapacity() || {};
  }
  function portSummary(port, owner) {
    let result =
      (port.interface_usage === "uplink" ? "Uplink" : "Regular") +
      " · " +
      (port.speed ||
        (port.interface_type === "physical"
          ? "Speed missing"
          : port.interface_type === "passive"
            ? "Passive"
            : "Speed unspecified"));
    if (role(owner)) result += " · " + summary(port, owner);
    if (port.interface_usage === "uplink" && role(owner) === "router")
      result += " · Allocated " + formatRate(port.allocated_bps || 0);
    if (port.validation_issues?.length)
      result += " · Needs attention: " + port.validation_issues.join("; ");
    return result;
  }
  return {
    role,
    names,
    parse,
    fields,
    bind,
    assignment,
    summary,
    mountLegacy,
    readLegacy,
    parseRate,
    formatRate,
    portSummary,
  };
})();
