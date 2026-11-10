// Build real compound nodes for placement, then alias hidden endpoints to their
// visible ancestor. Collapsing a group changes only the rendered graph.
function topologyNodes(records, positions, bounds, labelFor, iconFor, neighborIDs) {
  const inventory=[...locationInventory.site,...locationInventory.building,...locationInventory.room,...locationInventory.rack];
  const all=new Map([...inventory,...records].map(o=>[o.id,o]));
  const byKindName=new Map();for(const o of all.values())byKindName.set(o.kind+'|'+o.name,o.id);
  const resolve=(kind,ref)=>all.get(ref)?.kind===kind?ref:byKindName.get(kind+'|'+ref);
  const parentFor=o=>{
    const d=o.data||{};
    if(hostedKind(o.kind))return all.has(d.host_id)?d.host_id:null;
    const keys=o.kind==='site'?[]:o.kind==='building'?[['site','site_id']]:o.kind==='telecom_room'?[['building','building_id'],['site','site_id']]:o.kind==='rack'?[['telecom_room','telecom_room_id'],['building','building_id'],['site','site_id']]:['device','patch_panel'].includes(o.kind)?[['rack','rack_id'],['telecom_room','telecom_room_id'],['building','building_id'],['site','site_id']]:[];
    for(const [kind,key]of keys){const parent=resolve(kind,d[key]);if(parent&&parent!==o.id)return parent;}return null;
  };
  const parents=new Map(),children=new Map();
  const spanned=o=>o.kind==='conduit'&&o.data?.endpoint_a_id&&o.data?.endpoint_b_id;
  for(const o of all.values()){if(spanned(o))continue;const parent=parentFor(o);parents.set(o.id,parent);if(parent){if(!children.has(parent))children.set(parent,[]);children.get(parent).push(o);}}
  const neighborChains=new Set();for(const id of neighborIDs||[]){let cursor=id;const seen=new Set();while(cursor&&!seen.has(cursor)){neighborChains.add(cursor);seen.add(cursor);cursor=parents.get(cursor);}}
  for(const list of children.values())list.sort((a,b)=>a.kind===b.kind?a.name.localeCompare(b.name,undefined,{numeric:true}):a.kind==='virtual_switch'?-1:b.kind==='virtual_switch'?1:a.kind.localeCompare(b.kind));
  const elements=[],parentByDevice=new Map(),emitted=new Set();
  const selected=roomFilter.value||buildingFilter.value||siteFilter.value,selectionPath=new Set();let cursor=selected;while(cursor&&!selectionPath.has(cursor)){selectionPath.add(cursor);cursor=parents.get(cursor);}function matchesSelection(o){if(!selected||neighborChains.has(o.id)||selectionPath.has(o.id)||!containerView.isLocationKind(o.kind))return true;let id=o.id,seen=new Set();while(id&&!seen.has(id)){if(id===selected)return true;seen.add(id);id=parents.get(id);}return false;}
  const forcedRoots=new Set();
  if(window.pendingTopologyFocus){let id=window.pendingTopologyFocus,seen=new Set();while(id&&!seen.has(id)){seen.add(id);forcedRoots.add(id);id=parents.get(id);}}
  function data(o){const d=o.data||{};return{id:o.id,object_id:o.id,label:o.name,map_label:labelFor(o),kind:o.kind,meta:d,version:o.version,item_icon:itemIcons.item(o),font_family:d.font_family||'sans-serif',font_size:d.font_size||24,font_weight:d.font_weight||'normal',font_style:d.font_style||'normal',font_color:d.font_color||'#172331',shape_geometry:d.shape_type==='circle'?'ellipse':d.shape_type==='cloud'?'polygon':'rectangle',shape_polygon:cloudShape.points,shape_width:d.width||160,shape_height:d.height||100,shape_color:d.fill_color||'#dbeafe'};}
  function alias(o,rep,seen=new Set()){if(seen.has(o.id))return;seen.add(o.id);parentByDevice.set(o.id,[rep]);for(const child of children.get(o.id)||[])alias(child,rep,seen);}
  function visit(o,parent,position,index=0){
    if(emitted.has(o.id))return;emitted.add(o.id);
    const group=containerView.isContainer(o),collapsed=group&&containerView.isCollapsed(o.id,o.kind),d=data(o),list=(children.get(o.id)||[]).filter(matchesSelection);
    if(parent)d.parent=parent;
    if(group)d.map_label=o.name+'\n'+(collapsed?'▸ ':'▾ ')+list.length+' item'+(list.length===1?'':'s');
    let classes='member';
    if(containerView.isLocationKind(o.kind))classes+=' location-container '+o.kind;
    if(o.kind==='device')classes+=isHypervisor(o)?' hypervisor-host':' deviceicon-'+iconFor(o.data?.device_type_id||o.data?.device_visual||o.data?.device_type||o.data?.type||'');
    if(collapsed)classes+=' collapsed';
    if(o.kind==='virtual_machine')classes+=' virtual-machine '+(o.data?.guest_os==='Windows'?'windows':'linux');
    if(o.kind==='virtual_switch')classes+=' virtual-switch';
    if(o.kind==='patch_panel')classes+=' patch-panel-'+(o.data?.medium||'copper');
    if(o.kind==='shape'&&o.data?.shape_type==='cloud')classes+=' cloud-shape';
    elements.push({data:d,position,classes});const rep={id:o.id,chain:[]};parentByDevice.set(o.id,[rep]);
    if(collapsed){for(const child of list)alias(child,rep);return;}
    for(const [i,child]of list.entries()){
      let p=positions.get(child.id);
      if(!p){if(!hostedKind(child.kind)&&(child.x||child.y))p={x:child.x,y:child.y};else p={x:position.x+(i%3-1)*(containerView.isContainer(child)?230:115),y:position.y+110+Math.floor(i/3)*(containerView.isContainer(child)?160:85)};}
      visit(child,o.id,{x:p.x,y:p.y},i);
    }
  }
  const roots=[...all.values()].filter(o=>!spanned(o)&&!parents.get(o.id)).sort((a,b)=>a.name.localeCompare(b.name,undefined,{numeric:true}));let locationIndex=0;
  for(const o of roots){if(!matchesSelection(o))continue;let p=positions.get(o.id)||{x:o.x||0,y:o.y||0};const location=containerView.isLocationKind(o.kind);if(location&&!positions.has(o.id)&&!o.x&&!o.y){const n=locationIndex++;p={x:280+(n%5)*520,y:160+Math.floor(n/5)*380};}else if(location)locationIndex++;
    const collapsed=containerView.isContainer(o)&&containerView.isCollapsed(o.id,o.kind);
    if(location&&collapsed&&!forcedRoots.has(o.id)&&bounds&&(p.x<bounds.x1-240||p.x>bounds.x2+240||p.y<bounds.y1-180||p.y>bounds.y2+180))continue;
    visit(o,null,{x:p.x,y:p.y});
  }
  return{elements,parentByDevice};
}
