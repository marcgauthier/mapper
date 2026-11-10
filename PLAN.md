# Mapper Webapp Feature Plan

## Product goal

Build a Go web application for maintaining and collaboratively viewing one physical and virtual network topology. The map is the main workspace. It must represent where equipment is installed, which physical interfaces connect, and how cables travel through patch panels, conduits, and manholes. Multiple authenticated users edit the same dataset and see updates quickly.

The first deployment target is one Linux server and one encrypted libmurmur database. The application reuses OV22’s Cytoscape page patterns and local topology assets, then removes OV22-specific pages and data/API coupling. Mapper keeps its own global topology model and uses the same compound-node, device-icon, search-highlight, and fit-to-view behaviors.

## Users, authentication, and access

- Provide a database unlock page at `/unlock`. The first unlock creates the initial administrator; later unlocks use the same database key. A wrong key must not initialize a replacement database in place of an existing one.
- Store all application records inside the encrypted libmurmur database. Store passwords as bcrypt hashes.
- Support `admin`, `editor`, and `viewer` roles. Admins manage users; editors create and change topology; viewers can search and inspect it.
- Support password login and optional mTLS login. When mTLS is enabled, a verified client certificate common name maps to an application username.
- Use secure session cookies, expire sessions after a fixed lifetime, rate limit unlock and login attempts, and restrict WebSocket origins.
- Support HTTPS configuration and an option to refuse startup without TLS.

## Inventory and topology data

Every inventory record has a stable ID, kind, name, and metadata. Device records have map positions. The inventory kinds are:

- Site
- Building
- Telecom room
- Device
- Virtual machine hosted by an hypervisor/VMware device, with Linux or Windows guest OS
- Patch panel
- Conduit route record (assigned to a cable; never a graph device or node)
- Manhole route record

Device, VM, and patch-panel choices come from an administrator-editable catalog in the encrypted database. Seed it with router, switch, firewall, wireless access point, hypervisor host, server, storage, UPS, load balancer, modem, gateway, VM, generic patch panel, copper patch panel, and fiber patch panel types; allow custom entries and map styles.
- Keep site, building, telecom room, network, and support group metadata together in a device Network tab.
- Store an optional IP address or prefix on each interface.

Metadata must retain values such as device type, site ID, building ID, telecom room ID, network, support group, description, and other organization-specific columns. Site/building/room records may be referenced by IDs; labels should use the referenced inventory name when available.

### Device interfaces

- Each device and patch panel can have named interfaces or ports.
- Interface records include connector/transceiver, speed, description, and other metadata. Physical equipment interface speeds are required on explicit interface saves; missing legacy/default/imported details remain flagged.
- Enforce unique interface names per device.
- A normal interface connects to one peer item across all connection classes, with one physical cable and optional multiple logical services to that peer. Shapes have one shared connection point that accepts multiple peers and cables.

### Physical links and cable specifications

A connection joins two distinct interfaces. Physical connections require different devices; virtual connections may stay within one host. Classify each edge as physical, virtual, logical tunnel, or provider service. Provide an Other option with a descriptive custom type in every category.

- Physical media: fiber, copper twisted pair, coax, twinax/DAC, wireless/radio, and backplane. Record applicable cable type, endpoint connectors, strand count, length, part number, and shielding.
- Virtual connections: Ethernet, VLAN, bridge/virtual switch, veth, port group, overlay, bond, link aggregation, loopback, and custom types, with optional VLAN ID, VNI, network name, and bandwidth.
- Logical tunnel protocols: IPsec, WireGuard, GRE, VXLAN, L2TP, Geneve, IPIP, SIT, OpenVPN, SSL VPN, and custom types. Record optional mode and local/remote endpoints.
- Provider services: PLS, leased circuit, dark fiber, wavelength, carrier Ethernet, MPLS, Internet, EVPN, SD-WAN, and custom types. Record optional provider, circuit ID, and bandwidth.

Connection details are persisted with each edge, appear on the diagram and in edge details, and are supported by CSV import.

### Patch panels and cable route

- A patch panel is an inventory object with interfaces representing ports.
- Record a pairing between two ports on the same panel to represent its internal pass-through.
- A physical link may have an ordered route referencing conduit and manhole records. Conduits are displayed as route details on the cable edge, never as devices or graph nodes; manholes may appear as route markers.
- Trace from a device interface through connected cables and paired panel ports to the far endpoint. Report missing, unpaired, cyclic, or excessively long paths clearly.

## Diagram and navigation

- Use Cytoscape.js as the graph canvas, with physical devices, patch panels, manhole route markers, and physical links represented in the topology. Conduit membership and ordered route details are shown on the cable edge and its inspector, not as graph nodes.
- Render an hypervisor/VMware device as a containing box with its virtual machines nested inside it; let editors create Linux and Windows VMs from the host inspector.
- Provide pan, zoom, and scroll navigation for large maps. Query objects and links around the current viewport in bounded pages rather than requiring the entire database in the browser.
- Position devices, patch panels, infrastructure, and physical connections directly on the canvas with nested site/building/room/rack containers. Show each item's site, building, and telecom room in its map label and inspector.
- Provide focused scope navigation and context-sensitive site/building/floor/room/rack selectors. Keep viewport and filter state local to each browser; store shared node positions separately for each scope.
- Search by location or device name. Show matching results under the header search box and navigate to the selected map item. Omit the left sidebar to give the map more space.
- Right-click the canvas to add an item, copy/paste a device, edit or delete the selection, or connect from a selected device to a second device. **Connect to Device…** starts a visible pointer-following line and opens confirmation on the second item click; **Connect to Device (form)** opens the target picker directly. Escape or right-click cancels the gesture, and the preview is never a saved map edge. Dragging one device onto another or clicking a device then Shift-clicking another also starts a connection. Create a default interface when an endpoint has none and preselect an endpoint’s only interface.
- Add movable text annotations with editable text, font family, font size, color, bold, and italic, persisted with shared layout and audit history.
- Use names for devices, interfaces, and edges throughout the UI; keep UUIDs internal.
- Use the canvas context menu to add items and geometric shapes (square, rectangle, circle, cloud) with editable size and fill color. Shapes have exactly one protected, shared connection point.
- Expand the device catalog with 92 additional Cisco icon types and expose all visual styles in the administrator catalog editor.
- Combine account and administrative actions in one sectioned Menu dropdown (Create, Sites & cable plant, Topology tools, Import / Export, Administration), including Locations management and Import / Export CSV. Map items are added by right-clicking the canvas. Locations lists support search and CRUD with site/building parent selection. Export consistent topology snapshots with lossless JSON metadata and dependency ordering.
- The item editor has a searchable, scrollable interface list with natural ordering, suitable for 48 or 96 ports, and supports adding, editing, and removing interfaces; prevent removing connected interfaces and keep at least one interface on each device.
- Selecting a link shows medium, cable type, connectors, optional cable details, route segments, and trace results.

## Collaborative editing

- Maintain one shared topology for all authorized users.
- Commit mutations transactionally with their audit record and durable event sequence. Include an object version in map records and reject stale updates so concurrent users cannot silently overwrite the same object.
- Publish change notifications over WebSockets so open browsers refresh promptly.
- Replay persisted event batches by sequence number after connecting or receiving a notification. Periodically check the event cursor so clients recover from dropped WebSocket notifications.
- Refresh affected map state while preserving the user's viewport and active location filters.

## Editing, import, history

- Provide workflows to create, edit, and safely delete inventory objects; add or remove interfaces; connect or disconnect interfaces; pair or unpair panel ports; and set ordered cable routes. Refuse deletion when the object or interface is still referenced by a cable, pairing, or route.
- Let administrators add, rename, reorder, restyle, and delete unused network item types in the encrypted catalog. Preserve a type while inventory objects reference it.
- Allow a cable to be started by dragging one physical device or patch panel onto another, then require the editor to choose the interface at both ends and specify cable properties.
- Support CSV import for inventory, interfaces, cable connections and their specifications, panel pairings, and route segments.
- CSV preview must validate syntax and referenced records without committing changes. Applying the import must be all-or-nothing.
- Record actor, time, action, and relevant before/after data in the audit log for topology and account changes.
- Provide administrators an audit view.
- Provide safe undo for the latest uncontested object edit. Reject undo if another user has changed that object since the edit.

## Database and operational behavior

- Use `../libmurmur/clients/go` as the Go database client. Keep topology, user accounts, audit records, and replay events in the encrypted Murmur store.
- Create schema and indexes through an explicit versioned migration ledger. Apply each migration transactionally during unlock and refuse a store created by a newer Mapper version.
- Allow an administrator to download a native encrypted online backup while the application is unlocked. On the locked unlock page, allow restore from an uploaded backup after validating its archive, encryption key, contents, and application schema; install it through a staged directory swap so failed validation preserves the current store.
- Run as one Go server process, serving API, static web client, and WebSockets.
- Document database location, unlock procedure, TLS/mTLS configuration, CSV formats, and deployment assumptions.

- Focused views use contextual selectors and paginated searchable destination pickers. Child choices are scoped to their parent. Legacy location-management forms retain their local Select2 controls.

## Acceptance criteria

1. A first-time user can initialize the encrypted database and create an administrator; subsequent starts require unlocking the same store.
2. Administrators can create and disable users with roles, and viewers cannot mutate topology.
3. An editor can create a site/building/room/device hierarchy, add hypervisor hosts with nested Linux/Windows VMs, add device and patch panel interfaces, and create a fiber or copper link with cable type and endpoint connectors by dragging between devices and choosing each endpoint interface. The editor can safely remove links, unpair ports, and delete unreferenced inventory objects.
4. The global view summarizes sites and cloud services. Focused location views reveal their appropriate floors, rooms, racks, equipment and conduits; contextual selectors, pan/zoom, external boundary connections and search navigation work at each level.
5. Patch-panel pairing, conduit/manhole route assignment, and cable trace work across connected interfaces.
6. A CSV preview validates cross references without writing data; apply commits a valid batch atomically and rolls back an invalid one.
7. Two concurrent users see committed changes promptly, and a client can recover changes after missing a WebSocket notification.
8. Audit history identifies who changed topology or accounts; safe undo refuses to overwrite a later edit.
9. Viewport queries for objects, interfaces, layouts, and cable routes are paginated and indexed. Go integration tests exercise concurrent editor writes, viewport-crossing links and routes, and WebSocket fan-out. Functional browser workflows were checked against the running Go server. Deployment capacity profiling is deferred and is not a feature-completion gate.
10. Schema migrations are recorded and idempotent; each migration's schema changes and ledger entry commit together. A database backup downloads only for an administrator, can be opened with the same database key, and can be restored from the locked page without replacing the active store on a bad key or malformed archive.
11. Administrators can edit the encrypted network item type catalog; selected type controls item category and map style. hypervisor hosts contain Linux/Windows VM nodes, and drag-to-connect opens the endpoint interface and cable specification form.

## Current implementation and remaining operational work

The Go implementation provides encrypted-store unlock, accounts and roles, mTLS mapping, an editable network item catalog, inventory and interfaces, hypervisor/VM containment, version-checked object edits, safe deletion of referenced topology objects, drag-to-connect cable editing, patch pairing/unpairing, route trace, independent device placement with location labels and filters, viewport paging, search, CSV import validation/apply, WebSocket event replay, audit entries, safe object undo, transactional schema migrations, and encrypted backup/restore. It remains a single-process early release. The browser locally serves the Cytoscape.js library and selected topology icon assets reused from OV22. Sustained deployment capacity profiling remains separate deployment work.

## Location and virtualization hierarchy

- Model Site → Building → Telecom room → Rack → Device, allowing partial placement for existing inventory. Racks derive their parent location and devices can be assigned to racks.
- Support ESXi, VMware vSphere, Hyper-V, Proxmox, KVM, Xen, XCP-ng, Nutanix AHV, oVirt, bhyve, and custom hypervisor types.
- Add VMs and vSwitches inside hypervisors. VM-to-vSwitch connections default to virtual Ethernet, with a fresh port when needed. Each port has one peer.
- Single-click locations and racks to enter their focused view; breadcrumbs and Back return to prior scopes. Inspect hypervisors to manage their hosted VMs/vSwitches. Search reveals the containing scope while preserving relevant external connections.

## Transparent item icons

- Give every catalog visual and every item kind a local transparent icon, including generic devices, hypervisors, VMs, panels, sites, buildings, rooms, racks, conduits, manholes, labels, and shape variants.
- Retain the Cisco PNG symbols with alpha transparency and download missing SVG symbols from Microsoft Fluent UI System Icons. Pin the upstream commit and retain its MIT license and source manifest in `web/img/fluent/`. Add no runtime dependency or external image request.
- Share icon resolution between topology node data, editor previews and inspector headings. Render compact badges on containers, transparent symbols on leaf nodes, and type previews for editable text and shapes. Verify that no catalog entry is iconless and that raster symbols contain transparency.

## Zstandard transport, zoom and operational logging

- Apply Zstandard level 3 to all HTTP entity bodies, requiring explicit client negotiation, and require compressed request bodies. Preserve protocol headers/control frames. Stream responses, validate/decode uploads before mutation, spool large uploads to private temporary files, and enforce decoded limits (128 MiB general, 4 GiB restore, 1 MiB WebSocket).
- Negotiate `mapper.zstd.v1`, encode JSON as binary Zstd messages, and compress each broadcast once for all clients. Serve the pinned browser WebAssembly codec locally and compress uploads in 1 MiB frames.
- Provide a bottom logarithmic zoom slider (1%–800%), synced percentage, buttons and fit action. Bind controls whenever Cytoscape is rebuilt.
- Persist Loggy categories `User action` and `Application logs`. Publish transaction audit entries only after commit; redact credentials. Capture standard Go logs, HTTP diagnostics/errors/panics with request IDs, lifecycle events, WebSocket failures and bounded authenticated browser exception reports.
- All authenticated roles can view/search User logs under Activity. Only administrators can view/search System logs under Administration. Enforce category permissions on the API; provide separate views, severity filters and expandable structured event details.

## Diagram workspace and legacy data compatibility

- Cytoscape.js is the sole map workspace. Keep site/building/telecom-room/rack
  containers, device placement, conduits, cable editing and WebSocket updates.
- The geographic renderer, basemap service, geographic editing API and related
  browser assets have been removed. The browser makes no basemap requests.
- Retain migration 13 unchanged so existing encrypted stores still unlock and
  restore. Previously saved geographic metadata remains archived in the database
  and round-trips through CSV exports/imports without altering diagram topology.
- Archived geographic records do not restrict diagram placement, cable edits,
  deletion or route assignment. Inventory IDs and diagram layouts remain intact.
- Old `map_style` configuration entries are accepted and ignored for compatibility.

## Focused topology views, cloud services, and power equipment

### Navigation and visible content

The default workspace is **Global overview · Sites & Cloud**. Click a location,
cloud service or conduit to enter a focused view; use breadcrumbs or Back to
return. The level selector opens a searchable destination picker. Mouse-wheel
and slider zoom change magnification within the selected view.

| View | Contents and creation actions |
|---|---|
| Global | Sites, named cloud/service offerings, Google/Azure/AWS or other provider instances, Internet/MPLS services and actual summarized connections. Add sites/services and access/hosting/dependency relationships. |
| Cloud/service | Directly hosted resources, connected external sites/services and relationships. Add hosted resources/services, relationships and network attachments. |
| Site | Buildings, their generators and building-level UPS units, inter-building conduits/connections and external services. Add buildings, generators, UPS units, conduits, relationships and Internet/MPLS attachments. |
| Building | Floor groups, ordinary/telecom-room summaries, contained-equipment counts, generators/building UPS units and conduits. Add floors, rooms, telecom rooms, generators, UPS units or conduits. |
| Floor | Rooms, directly placed racks/equipment/outlets and relevant conduits/connections. Add rooms, racks, devices, panels, outlets, cables and conduits. |
| Ordinary room | Racks, equipment, DVO/fiber outlets, room UPS units, cables and conduits. |
| Telecom room | Racks, equipment/panels/outlets, telecom-room UPS units, cables and conduits. |
| Rack | Equipment, patch panels, interfaces and internal/external connections. |
| Conduit | Endpoint location paths, contained cables, media/strand counts and route details. Assign an existing physical cable or create one through the conduit. |

Toolbar, application creation menu and right-click actions use the current scope.
Ordinary rooms and telecom rooms are distinct types/views. Forms inherit context;
creating a room at Building level requires selecting a floor. Each browser keeps
its camera, scope and history locally. Scoped layouts are shared independently
using `scope:<kind>:<id>`; old diagram coordinates remain initial seeds.

### Model, power, and migration

- Migration 15 indexes audit action/object lookups for scoped layout revisions.
- Migration 14 adds live floors, ordinary rooms, outlets, services and relationships,
  plus indexes for floor/room ancestry, service categories, conduit endpoints and
  route membership. Applied migrations, including archived geography, are retained.
- Every existing building receives Ground; resolvable indoor equipment/rooms receive
  its `floor_id`. New buildings create Ground transactionally. Unresolved legacy
  free-text locations remain unresolved rather than being assigned invented sites.
- `floor_id` and ordinary `room_id` supplement existing `telecom_room_id`. Ordinary
  and telecom room memberships are exclusive. Parent edits propagate inherited
  ancestry transactionally and advance affected descendants' versions.
- DVO outlets have editable Data/Voice RJ45 ports. Fiber outlets start with two LC
  ports. Normal interface-peer validation, cable trace and patch pairing remain.
- Generators belong directly to a building. UPS units belong to a building, ordinary
  room or telecom room. Site views place building-level power assets beside their
  building; room-level UPS units stay inside room views. Dotted **Supplies power**
  lines derive from placement and are display associations, not network cables.
- References protect locations, conduit endpoints, hosted resources, services and
  connected ports from unsafe deletion. An empty automatically created Ground
  may be removed together with its otherwise empty building.

### Cloud and WAN connectivity

- A `service` object has `service_category`: `cloud_provider`, `saas`, `paas`, `iaas`,
  `faas`, `cloud_hosting`, `cloud_resource`, `internet`, or `mpls`. Provider, account,
  region and description distinguish instances; a physical location is not required.
- Offerings/providers appear globally; detailed `cloud_resource` records appear in
  their hosting view. No resources or usage are inferred from provider names.
- Versioned `accesses`, `hosts` and `depends_on` relationships have no physical-port
  requirement. Hosting is service-to-service and rejects cycles. Creating a hosted
  resource in a service view creates its hosting relationship in the same transaction.
- Internet/MPLS attachments require a real gateway device interface and matching
  provider-service connection. Service-side attachment interfaces are distinct.
- Physical network connections and dashed directed cloud relationships remain
  separate. Service identity is ID-based; equal labels never merge services.
- Existing cloud-type devices remain unchanged until explicitly converted. Conversion
  retains IDs/interfaces and requires incompatible physical connections and hosted
  VMs to be resolved first. WAN conversion additionally validates connection type.

### Conduits and projection

- Conduits retain independent endpoint IDs. Supported endpoints are sites, buildings,
  floors, ordinary rooms, telecom rooms and legacy manholes. Endpoint pickers show
  full location paths. Buildings do not require room details.
- Support building↔building, building↔room, floor↔floor, same-floor room↔room,
  cross-floor room↔room and cross-building room↔room connections.
- Every view projects endpoints to the nearest visible ancestor and labels remote
  destination markers. Internal hidden connections collapse into container counts;
  entering the container reveals them. A building-only endpoint is not replicated
  into arbitrary floors/rooms. Incomplete legacy endpoints are flagged.
- One conduit retains its ID and cable memberships through all projections. Cable
  creation can atomically assign a conduit. Existing cables can be appended to a
  conduit; the route editor reorders/removes conduit/manhole membership with stale
  route protection. Connectivity/tracing and fiber counts stay cable-level.
- Summary connections count real underlying records and expose paginated details.
  Display summaries, boundary markers and power associations never create cables.

### APIs, performance, and collaboration

- `GET /api/topology/view`: scope kind/ID, network/service-category/room-type filters,
  optional finite `min_x/min_y/max_x/max_y`, numeric cursor, sequence and limit.
  Returns bounded nodes/edges, endpoint dependencies, counts, breadcrumbs, allowed
  creation kinds, positions, scoped layout group and layout revision. Limit is 500
  entries per page, plus required endpoint/compound-parent nodes.
- `GET /api/topology/details`: paginated records behind a summary edge.
- `GET /api/topology/choices`: searchable, keyset-paginated typed object choices,
  with optional ancestor, service-category and hypervisor filters; 100 per page.
- `GET /api/topology/cables`: up to 100 physical-cable choices, searchable by label
  and endpoint names. Narrow the search when additional matches exist.
- `POST/PUT/DELETE /api/relationships`: versioned relationship CRUD;
  `POST /api/relationships/undo`: latest uncontested relationship edit.
- `POST /api/topology/attachment`: atomic WAN interface/connection creation.
- `POST /api/topology/assign-conduit`: append membership with event-sequence guard.
- `POST /api/topology/convert-service`: explicit version-checked cloud conversion.
- Existing object/interface/link/route APIs handle editing. Cable updates can supply
  `expected_data`, route replacements `expected_infrastructure_ids`, and scoped
  layout writes `expected_version`. Object undo checks dependent versions before
  restoring ancestry; it cannot overwrite subsequent child edits.
- Server-side immutable inventory snapshots and at most 32 derived scope scenes
  are shared across browsers and invalidated by durable topology events. Layout-only
  events reuse inventory snapshots. Browser responses and on-demand choices stay
  bounded; dense views support viewport queries and explicit Load more.
- Snapshot rebuilds read the server inventory and connections; capacity is sensitive
  to rebuild frequency under sustained concurrent edits. This is a single-process
  implementation, not a claim of production load certification.
- Reuse transactional audit, Zstandard WebSockets and durable replay. Open edits
  defer diagram refresh; stale edits report conflicts without discarding drafts.
  Scope/camera remain local. Viewer roles cannot mutate inventory or layouts.
- CSV preview/apply/export includes new kinds and relationships in dependency order;
  backups automatically include all encrypted tables. No production dependency,
  external cloud API or basemap service was added.

### Validation

Go tests cover every scope, conduit projections, Ground migration, generators,
building/room/telecom UPS visibility, DVO ports, cloud hosting cycles, WAN validation,
conversion, version conflicts, descendant-aware undo, viewport endpoints, summary
paging, CSV round trips and permissions. A real Chromium/Cytoscape test drives the
creation forms and focused navigation without a map test double.

Local capacity checks on an Intel Xeon Platinum 8362 measured approximately
**0.38 ms** per cached global response and **1.4 s** for the first projection with
100,000 devices and 100,000 connections. A separate 1,000-user WebSocket test measured
approximately **16 ms p95** from mutation request to notification read. These are
synthetic projection/fan-out checks, not sustained full-browser performance results.


### Device and interface VRFs

- VRFs are named routing domains local to each router or switch, independent of
  the managed network classification catalog. Declare `vrfs` as an array of names
  in device metadata; editors accept one name per line. Custom catalog types use
  their routing/switching visual family. Names are case-sensitive, unique after
  trimming, 1–128 characters, without control characters; maximum 256 per device.
- Each switch interface selects one declared VRF or the default/global routing
  table. Router interfaces select any subset, including an explicitly empty set,
  or use **All device VRFs**. New and existing unassigned router interfaces default
  to all; this resolves dynamically when VRFs are added or removed on the device.
- Interface metadata stores `vrf_mode` (`all` or `selected`) and `vrfs`; router
  `all` stores an empty selection rather than copying current device definitions.
  The paginated interface GET resolves `effective_vrfs` inside its metadata JSON.
  This derived field is stripped on writes. Stable interface IDs, cables, pairings,
  and other metadata are preserved by VRF edits.
- API writes, CSV validation/import, device edits, safe undo and catalog visual
  changes validate assignments. Remove/reassign explicit interface references
  before removing a VRF or changing the device to an incompatible type. All-mode
  interfaces follow changes automatically. Existing encrypted JSON storage and
  CSV metadata preserve declarations and modes without a schema migration.
- Changes use existing transactional audit/event writes and WebSocket refreshes.
  Validation reads only the edited device's indexed interfaces; interface reads
  resolve against the owning device without loading the whole device inventory.
  The focused inspector, scoped port forms and hosted-equipment interface editor
  show VRF assignments. No new runtime dependencies.


### Interface usage, speeds and router-uplink bandwidth

- Interfaces have `interface_usage` (`regular` default, or optional `uplink`). Any
  interface may be marked uplink; devices need not contain an uplink. This is
  independent of existing `role` values such as red/black or low/high.
- `interface_type` defaults to physical on equipment, virtual on VMs/vSwitches,
  and passive on panels, outlets, services and shared connection points. Equipment
  may explicitly identify logical or virtual interfaces; passive points remain
  passive. Physical interfaces require a positive speed on add/edit.
- `speed` retains human-readable network rates (e.g. `1G`, `1Gbps`, `1000Mbps`).
  Exact decimal parsing derives `speed_bps`; 1 Gbps = 1,000 Mbps. Byte/binary labels
  such as GiB, nonpositive rates and fractional bits are rejected. Rates are
  bounded by JavaScript's exact integer range; checked sums avoid overflow.
- Router uplinks with at least two effective VRFs require a positive allocation
  for every carried VRF, stored in `vrf_bandwidth_bps` by case-sensitive VRF name,
  and a known speed even on a logical uplink. One allocation applies in both
  directions. Total allocations must be <= speed; exact capacity is valid.
  Zero/one-VRF uplinks need no allocations, but supplied allocations must fit.
  Regular interfaces and switch uplinks need no VRF bandwidth allocations.
- Adding a device VRF preserves dynamic all-mode membership and may leave uplinks
  incomplete; no allocation is guessed. Removing VRFs prunes obsolete all-mode
  allocation entries transactionally; explicit interface references still block
  removal. Interface selection edits prune allocations for VRFs no longer carried.
  Reconciliation snapshots are audited and guarded undo restores them atomically.
- Paginated interface reads include a `revision` token; updates accept optional
  `expected_revision`. All interface edit UIs send it; stale interface or owner
  metadata returns 409 and leaves the form draft intact. The token covers name,
  metadata and owner metadata without a schema migration.
- GET metadata includes computed `validation_issues`, `allocated_bps`,
  `remaining_bps` and effective VRFs. Computed status fields are stripped on writes.
  Inspectors and all port editors show usage, speed, VRFs and attention messages;
  router uplinks have per-VRF rate rows and live capacity/allocated/remaining totals.
- Legacy and automatically created interfaces keep unknown speeds and appear
  incomplete until edited. CSV validation/import preserves missing details, while
  rejecting invalid supplied rates and budgets exceeding capacity. Exports and
  encrypted backups preserve all stored fields. Unknown legacy speed text is
  shown for correction rather than rewritten during reads or unrelated edits.
- Per-device warning counts are accumulated in one interface scan per immutable
  topology snapshot and reused across focused scenes. Individual writes query
  only the affected device's indexed interfaces. Existing transactions, audit,
  event replay and WebSocket updates distribute committed changes. No new dependency.

- Verification for the interface-capacity change includes exact boundary/overflow
  rate tests, optional usage and sided-role preservation, missing-detail imports,
  allocation reconciliation/undo, revision conflicts, and two isolated browser
  sessions receiving committed allocations through WebSocket refresh. A local
  100,000-device/100,000-link projection sample measured about 2.0 s cold and
  0.55 ms cached with interface-warning computation enabled (single synthetic
  sample; not sustained deployment capacity).
