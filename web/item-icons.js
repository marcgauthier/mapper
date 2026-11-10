// Every item has a local transparent icon. Diagram annotations retain their
// editable appearance; their type icons appear in the editor and inspector.
const itemIcons = (() => {
  const root = '/img/fluent/';
  const kinds = {
    device: root+'desktop-tower.svg', virtual_machine: root+'desktop.svg',
    virtual_switch: root+'virtual-network.svg', patch_panel: root+'plug-connected.svg',
    site: root+'globe.svg', building: root+'building.svg', telecom_room: root+'door.svg',
    rack: root+'server.svg', conduit: root+'connector.svg', manhole: root+'circle.svg',
    label: root+'text-t.svg', shape: root+'rectangle-landscape.svg'
  };
  const shapes = {square:root+'square.svg',rectangle:root+'rectangle-landscape.svg',circle:root+'circle.svg',cloud:root+'cloud.svg'};
  function kind(kind,shape) {return (kind==='shape'&&shapes[shape])||kinds[kind]||kinds.device;}
  function deviceType(type) {
    if(!type)return kind(objectKind.value,shapeType.value);
    if(type.visual==='generic'&&type.object_kind!=='device')return kind(type.object_kind);
    if(type.visual==='generic'&&type.can_host_vms)return root+'stack.svg';
    return deviceVisuals.find(v=>v.id===type.visual)?.icon||kind(type.object_kind);
  }
  function item(o) {
    const meta=o.data||{},type=deviceTypes.find(t=>t.id===meta.device_type_id||t.id===meta.device_type||t.name===meta.device_type);
    if(type&&['device','virtual_machine','virtual_switch','patch_panel'].includes(o.kind))return deviceType(type);
    if(o.kind==='device'&&isHypervisor(o))return root+'stack.svg';
    const visual=deviceVisuals.find(v=>v.id===meta.device_visual);
    return visual?.icon||kind(o.kind,meta.shape_type);
  }
  return {kind,deviceType,item};
})();
