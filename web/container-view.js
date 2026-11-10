// Location groups start collapsed to keep large inventories usable. Explicit
// choices, including hypervisor expansion, survive map refreshes and reloads.
const containerView = (() => {
  let choices = {}, legacy = [];
  try {choices = JSON.parse(localStorage.getItem('mapper_container_expansion') || '{}');} catch {}
  try {legacy = JSON.parse(localStorage.getItem('mapper_collapsed_hypervisors') || '[]');} catch {}
  if (!choices || typeof choices !== 'object' || Array.isArray(choices)) choices = {};
  for (const id of Array.isArray(legacy) ? legacy : []) if (!(id in choices)) choices[id] = false;
  const locationKind = kind => ['site','building','telecom_room','rack'].includes(kind);
  function save() {try {localStorage.setItem('mapper_container_expansion',JSON.stringify(choices));} catch {}}
  function isCollapsed(id, kind = 'device') {return id in choices ? !choices[id] : locationKind(kind);}
  function expand(id) {if (!id) return; choices[id] = true;save();}
  function toggle(id,name,kind='device') {choices[id]=isCollapsed(id,kind);save();statusEl.textContent=(choices[id]?'Expanded ':'Collapsed ')+name;window.pendingTopologyFocus=id;load();}
  function expandAncestors(item, includeItem=false) {
    const records=[...locationInventory.site,...locationInventory.building,...locationInventory.room,...locationInventory.rack],seen=new Set();
    function walk(record,include){if(!record||seen.has(record.id))return;seen.add(record.id);for(const [key,kind]of [['site_id','site'],['building_id','building'],['telecom_room_id','telecom_room'],['rack_id','rack']]){const ref=record.data?.[key],parent=records.find(o=>o.kind===kind&&(o.id===ref||o.name===ref));if(parent)walk(parent,true);}if(include)expand(record.id);}
    walk(item,includeItem);
  }
  return {isCollapsed,expand,toggle,expandAncestors,isContainer: o=>locationKind(o.kind)||(o.kind==='device'&&isHypervisor(o)),isLocationKind:locationKind};
})();
