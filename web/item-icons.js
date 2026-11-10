// Mapper's original SVG symbols share one stroke weight and slate/blue palette.
// Cisco device artwork stays in its existing catalog. Rooms use labels on the
// map; their symbols remain available in creation controls and editor previews.
const itemIcons = (() => {
  const root = "/img/mapper/";
  const kinds = {
    device: "desktop-tower", virtual_machine: "desktop",
    virtual_switch: "virtual-network", patch_panel: "patch-panel",
    site: "globe", building: "building", floor: "floor", room: "room",
    telecom_room: "telecom-room", rack: "rack", conduit: "connector",
    manhole: "manhole", generator: "generator", ups: "ups",
    outlet: "rj45", service: "cloud", label: "text-t", shape: "rectangle-landscape",
  };
  const shapes = {
    square: "square", rectangle: "rectangle-landscape", circle: "circle", cloud: "cloud",
  };
  const siteRoles = {
    data_center: root + "server.svg", major_site: root + "campus.svg",
    branch_office: "/img/site-branch.svg", edge_site: "/img/site-edge.svg",
    disaster_recovery: "/img/site-dr.svg", cloud_colo: root + "cloud.svg",
  };
  function kind(kind, shape) {
    const name = (kind === "shape" && shapes[shape]) || kinds[kind] || kinds.device;
    return root + name + ".svg";
  }
  function deviceType(type) {
    if (!type) return kind(objectKind.value, shapeType.value);
    if (type.visual === "generic" && type.object_kind !== "device")
      return kind(type.object_kind);
    if (type.visual === "generic" && type.can_host_vms) return root + "stack.svg";
    return deviceVisuals.find(v => v.id === type.visual)?.icon || kind(type.object_kind);
  }
  function item(o) {
    const meta = o.data || {};
    const type = deviceTypes.find(t =>
      t.id === meta.device_type_id || t.id === meta.device_type || t.name === meta.device_type);
    if (type && ["device", "virtual_machine", "virtual_switch", "patch_panel"].includes(o.kind))
      return deviceType(type);
    if (o.kind === "service")
      return ["saas", "paas", "iaas", "faas"].includes(meta.service_category)
        ? "/img/" + meta.service_category + ".svg" : root + "cloud.svg";
    if (o.kind === "outlet") return root + (meta.outlet_type === "fiber" ? "fiber" : "rj45") + ".svg";
    if (o.kind === "device" && isHypervisor(o)) return root + "stack.svg";
    if (o.kind === "site" && siteRoles[meta.site_role]) return siteRoles[meta.site_role];
    const visual = deviceVisuals.find(v => v.id === meta.device_visual);
    return visual?.icon || kind(o.kind, meta.shape_type);
  }
  return { kind, deviceType, item };
})();
