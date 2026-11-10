# Mapper

Mapper is a single-server physical and virtual network topology editor written in Go. It uses Cytoscape.js for the shared map and `../libmurmur` for encrypted database storage. All users work on the same topology. Authenticated edits are committed to the database, recorded in the event log, and pushed to other open browsers over WebSockets. Browsers replay event batches after a notification and periodically check the event cursor so a dropped WebSocket notification is recovered.

## Run

Requires Go 1.26.5 or newer and the native build prerequisites documented by `../libmurmur/clients/go`.

```sh
go run ./cmd/mapper
```

Alternatively, `./run.sh` compiles `./cmd/mapper` to `./bin/mapper` and starts it, forwarding any arguments. The browser UI in `web/` is embedded in the binary, so it serves from any working directory.

Settings live in `cmd/mapper/mapper.yaml` next to the server (`MAPPER_CONFIG` selects another path). Every setting can still be overridden with an environment variable: `MAPPER_ADDR` (default `:8080`), `MAPPER_DB` (Murmur store directory, default `./data/mapper`), `MAPPER_TLS_CERT`/`MAPPER_TLS_KEY` (set together to serve HTTPS), `MAPPER_REQUIRE_TLS=1` (refuse startup without HTTPS), `MAPPER_CLIENT_CA` (optional verified client certificates), `MAPPER_LOG_DIR` (action-log directory, default `./data/logs`), and `MAPPER_LOG_RETENTION_DAYS` (default `365`). Unknown keys in `cmd/mapper/mapper.yaml` are rejected so typos fail loudly.

Open `/unlock` on first use, enter a database key, and create the initial administrator. On later starts, unlock with the same key. The database key is SHA-256 derived before passing it to libmurmur. A wrong key does not initialize a replacement store over an existing one.

Sign in at `/login` with a username and password, or with a PKI card: when TLS and a client certificate authority are configured (`MAPPER_TLS_CERT`/`MAPPER_TLS_KEY` plus `MAPPER_CLIENT_CA`), "Login with Certificate" establishes a session from the verified client certificate presented by the browser or its card provider. Certificate identities resolve to usernames through SAN email addresses first, then the subject common name, matched without regard to case. TLS is strongly recommended anywhere traffic leaves the local trusted host.

Unlock is limited to three attempts per peer in five minutes; password and certificate login share a limit of five. Sessions use HttpOnly, SameSite Strict cookies and expire after twelve hours. Administrators can list, create, update, or disable users with `admin`, `editor`, or `viewer` roles. The last administrator cannot be disabled or demoted.

## Encrypted backup and restore

Administrators can choose **Menu → Administration → Download encrypted backup** while signed in. Mapper uses libmurmur's online checkpoint API and downloads a `.murmur` archive containing the encrypted native checkpoint. The archive uses the same database encryption key as the live store.

Restore is available from `/unlock` while Mapper's database is locked. Select a Mapper `.murmur` file and enter its database key. Mapper verifies the archive and key, opens the restored store, applies migrations, and checks that it contains configured users before staging it into `MAPPER_DB`. A bad key, invalid archive, or invalid application database leaves the current store unchanged. Restoring replaces the current store; keep a separate copy of any data you may need.

Schema changes are tracked in the encrypted `schema_migrations` table. Migrations run transactionally at unlock; new schema changes must be added as a new, append-only migration in `internal/app/migrations.go`.

## Action logs

Loggy stores user activity and application diagnostics separately. Every signed-in user can review **Menu → Activity → User logs**. **Menu → Administration → System logs** requires administrator rights. See “User and system logs” below for filters, retention and captured diagnostics.

## API and live updates

- `POST /api/login`, `POST /api/login/certificate`, `POST /api/logout`, `GET /api/me`, `GET /api/server-info`
- `GET|POST|DELETE /api/objects`, `GET|POST|DELETE /api/interfaces`, and `GET|POST|DELETE /api/links`
- `GET /api/search?q=...`; `GET /api/layout?grouping=...` and `POST` layout updates
- `GET|POST|DELETE /api/pairings`; `GET|POST /api/routes`; `GET /api/trace?interface_id=...&link_id=...`
- `GET /api/export` downloads topology CSV; `POST /api/import`; `GET /api/logs?category=user` for all signed-in users; admin-only `GET /api/logs?category=system`, `GET /api/audit`, `GET /api/backup`, `GET|POST|PUT|DELETE /api/users`, `GET|POST|PUT|DELETE /api/device-types`, and `GET|POST|PUT|DELETE /api/connection-types`; locked-page `POST /restore` accepts a `.murmur` archive and database key
- Authenticated `GET /api/events?after=<sequence>` replays durable changes; authenticated `/ws` notifies clients to check for new events.

Trace defaults to the physical cable path; an optional `link_id` selects a particular connection on a shared interface. Virtual, tunnel, and provider traces show that connection directly. Only physical connections accept conduit/manhole cable routes in the API and CSV importer.

Object viewport queries accept `min_x`, `max_x`, `min_y`, `max_y`, `grouping`, `limit`, and `offset`. Interface requests support `device_id` or up to 500 `device_ids`. Object queries also support up to 500 `host_ids` or `container_ids` to retrieve complete group contents outside the viewport; link queries support `device_ids` for complete group connectivity. Route requests support `link_id` or up to 500 `link_ids`.

## Topology features

- An icon palette under the topbar mirrors the items creatable at the current level; drag an icon onto the canvas to open its creation form at the drop position, or click it to add at the default position. Connection actions stay in the Create menu and context menu.
- Right-clicking a connection edge offers Edit connection, opening the same editor as the inspector; edges bundling several connections open the summary list to pick one.
- **Menu → Topology tools → Physics layout** runs a force-directed arrangement with node repulsion over the visible items to spread stacked nodes and reduce line crossings, then saves the positions like a manual move.
- Double-clicking a container opens it; double-clicking a device, panel, power, or outlet item opens its edit form (editors only, viewers keep the read-only inspector).
- A find-node search box floats on the canvas; typing lists matching nodes in the current view and Enter centers, highlights, and inspects the match.
- Right-click the canvas to add device, patch panel, conduit, manhole, and text label objects; edit or delete items through the right-click context menu or by double-clicking (the selection inspector shows read-only details), and connections through the connection inspector or context menu. Locations (sites, buildings, telecom rooms, racks) are managed under Menu → Sites & cable plant; every item references them for location. The header has one sectioned Menu dropdown: Create, Sites & cable plant, Topology tools, Import / Export, Administration (administrators only), and Sign out. New map items are added by right-clicking the canvas. CSV actions are available in the menu. Devices, interfaces, connections, routes, and search results display names instead of internal UUIDs.
- Right-click the canvas and choose **Add shape** for a square, rectangle, circle, or cloud. Edit its dimensions (24–2000 map units) and fill color. Each shape has exactly one automatically created, protected connection point, which multiple items can share. Devices, VMs, patch panels, text labels, manholes, and other shapes can connect to it.
- **Menu → Locations → Sites / Buildings / Telecom rooms / Racks** opens searchable management lists with add, edit, and delete actions. Buildings can select a site; telecom rooms select their building; racks select their telecom room. Add devices directly from a rack inspector or context menu, or choose a rack in the device editor’s Network tab. Rack assignment derives the device’s site, building, and room. Referenced locations are protected from deletion. Viewers can browse these lists. Each site carries a role — Data Centre, Major Users Site, Branch Office, Edge / Local Site, Disaster Recovery Site, or Cloud / Colocation — which selects its map icon; sites created before roles show the default globe until edited.
- Text labels can be moved, edited, and styled with font family, size (8–144), color, bold, and italic. Label changes and positions are shared and persisted like other map items.
- An administrator-editable network item catalog with the original defaults plus 92 additional types using local Cisco icons: Nexus and Layer 3 switches, ASR and carrier routers, ASA/PIX firewalls, VPN and security appliances, wireless controllers/APs, servers, endpoints, storage, voice/video, and optical equipment; plus encryptors (inline/serial with red/black sided interfaces), airgaps (low/high sided interfaces), media converters, containers, pods, container hosts, Kubernetes nodes, cloud providers, and SaaS/PaaS/IaaS/FaaS services. Attach a service to its cloud provider with a provider-service connection. The item editor previews the selected icon. Existing catalog entries are preserved by the migration.
- Device metadata is organized in a Network tab. The Interfaces tab provides a scrollable, searchable list suitable for 48- and 96-port devices, with natural port ordering and add, edit, and remove actions. Editing preserves the interface ID and its connections, pairings, and custom metadata; fields include name, IP address or prefix, connector, speed, and description.
- The Hypervisors catalog group includes ESXi, VMware vSphere, Hyper-V, Proxmox VE, KVM/QEMU, Xen, XCP-ng, Nutanix AHV, oVirt, bhyve, and a generic Hypervisor type. Custom types can select the Hypervisor visual style.
- Hypervisors contain VM and vSwitch devices. Use **Add VM** or **Add vSwitch** in the inspector or context menu. VMs support Linux and Windows; hosted items inherit the hypervisor location and rack. Connecting a VM to a vSwitch defaults to virtual Ethernet and automatically creates another vSwitch port when its existing ports are occupied. Each port retains the one-peer rule. Hypervisors with hosted items are protected from deletion or conversion to ordinary device types.
- Physical, virtual, logical tunnel, and provider service connections between interfaces. Administrators can add custom connection types per category with map edge colors under Menu → Administration → Connection types; custom physical types require cable details like other cabled media. Each normal interface can connect to only one peer item across all connection classes, with at most one physical connection; multiple virtual connections, tunnels, and provider services to that same peer remain possible. Shape points allow multiple peers and physical connections. The picker marks unavailable ports, and the API and CSV importer enforce these rules transactionally. Virtual connections may join distinct interfaces on the same device. New devices start with an interface; sided types start with their role interfaces. Existing stores migrate automatically at unlock while preserving connections and cable routes.
- Drag one device or patch panel onto another, click a device then Shift-click a second device, or right-click a device and choose **Connect to Device…**. This selects the source and shows a line following the pointer; clicking a second item opens the connection confirmation form. Press Escape or right-click to cancel. **Connect to Device (form)** opens the target picker directly. No connection is saved until the confirmation form is submitted. The interface picker preselects a sole interface and creates “Interface 1” when an endpoint has none. Right-click devices to copy them and the canvas to paste duplicates with their interface definitions.
- Patch panel ports and pairings, ordered conduit and manhole route details shown on cable edges, and a trace view across connected segments. Open a conduit to inspect its contained cables and edit cable routes through the connection inspector.
- A conduit spans independently chosen site, building, floor, ordinary-room, telecom-room or manhole endpoints. Building endpoints do not require a room. Cable routes list conduit endpoints by name. Deleting an endpoint or route member is blocked until references are removed.
- The workspace opens focused levels for sites/cloud services, sites, buildings, floors, ordinary rooms, telecom rooms, racks and conduits. Double-click a container to enter it; use breadcrumbs or Back to return. Creation and filtering follow the current level. Connections crossing a scope boundary retain their relevant external endpoint; internal connections are summarized until their container is opened. Wheel zoom changes magnification within the current level. See **Focused topology levels** below for placement and power inventory.
- Pan, zoom, viewport-bounded object and link queries, device-only search results in the header, and a shared saved location layout. The map uses the full main workspace without a left sidebar. A bottom-right navigator shows the whole graph with a draggable viewport square to jump to any portion of the map; it collapses to a single button and hides on narrow screens.
- **Menu → Import / Export** offers **Export CSV** and **Import CSV**. Export produces a consistent topology snapshot in dependency order, including inventory, interfaces, connections, pairings, and cable routes. The `metadata_json` column preserves arbitrary metadata and arrays. Import performs a transactional dry run followed by an all-or-nothing commit. Custom device type IDs must exist in the destination catalog before importing inventory that uses them.
- **Menu → Demo data → Populate demo data…** generates a deterministic demo inventory: sites with roles, buildings, floors, ordinary and telecom rooms, racks, routers, switches, firewalls, servers, hypervisors with VMs and vSwitches, patch panels with port pairings, UPS and generator power, DVO and fiber outlets, building-ring and riser conduits routed through a manhole, VRF interfaces with uplink budgets, copper and fiber cabling, Internet/MPLS/PLS attachments, site-to-HQ tunnels, and cloud hosting relationships. Tune the counts or disable racks, conduits, power, transports, or cloud; re-running replaces previous demo data.
- Administrator user management, audit history, safe undo for the latest uncontested object edit, and live multi-user updates with stale object edit conflicts.

CSV rows use `kind` plus fields for the object type. Inventory rows need `kind,name` and may include `id,x,y` and arbitrary metadata columns such as `device_type_id,device_type,site_id,building_id,telecom_room_id,network,supportgroup`. Virtual machine rows use `kind=virtual_machine,name,device_type_id,host_id,guest_os`; use the `vm` type by default, import the hypervisor first, and set `guest_os` to `Linux` or `Windows`. Virtual switch rows use `kind=virtual_switch,name,device_type_id,host_id` with the `vswitch` catalog type. Building rows need `site_id`; telecom room rows need `building_id`. Rack inventory uses `kind=rack,name` with `telecom_room_id` or `building_id`; device and patch panel rows must name a `building_id`, `telecom_room_id`, or `rack_id`. Import locations before racks, racks before devices, and hypervisors before VMs and vSwitches. Interface rows use `kind=interface,device_id,name,id`; connection rows use `kind=connection,source_interface,target_interface,id` plus connection-specific columns. Physical media are `fiber,copper_twisted_pair,coax,twinax_dac,wireless_radio,backplane,other`; cable fields include `cable_type,connector_a,connector_b,strand_count,length_m,part_number,shielding`. Virtual rows use `connection_class=virtual,virtual_type` with `ethernet,vlan,bridge,veth,port_group,overlay,bond,lag,loopback,other`; optional fields are `vlan_id` (1–4094), `vni` (1–16777215), `network_name`, and `bandwidth`. Logical tunnel rows use `connection_class=logical_tunnel,tunnel_protocol,tunnel_mode,local_endpoint,remote_endpoint`; supported protocols are `ipsec,wireguard,gre,vxlan,l2tp,geneve,ipip,sit,openvpn,ssl_vpn,other`. Provider rows use `connection_class=provider_service,service_type,provider,circuit_id,bandwidth` with types `pls,leased_circuit,dark_fiber,wavelength,carrier_ethernet,mpls,internet,evpn,sd_wan,other`. Each category supports `other` with a required descriptive `custom_type`. All connection categories accept optional `label` and `description`. Shape rows use `kind=shape,name` with optional `shape_type` (`square,rectangle,circle,cloud`), `width,height,fill_color,x,y`. Circle and square heights match their width. Include one interface row to preserve a shape point ID when importing connections; standalone shape imports receive a point automatically. Text label rows use `kind=label,name` with optional `font_family,font_size,font_color,font_weight,font_style,x,y`; font names match the editor choices, colors use `#RRGGBB`, weight is `normal` or `bold`, and style is `normal` or `italic`. Pairing rows use `kind=pairing,panel_id,port_a,port_b,id`; route rows use `kind=route,link_id,infrastructure_id,sequence,id`. Conduit rows use `kind=conduit,name` with optional `endpoint_a_id,endpoint_b_id` naming two distinct sites, buildings, telecom rooms, or manholes. Place referenced objects before interfaces, interfaces before connections and pairings, and connections before route rows.

See [Application feature plan and technical details](#application-feature-plan-and-technical-details) below for the data model, workflows, and acceptance criteria.

## Current limits

This is an early single-process implementation. The browser uses local Cytoscape.js, jQuery, Select2, and topology icon assets copied from OV22, so the diagram does not depend on a public CDN. Node positions and topology are shared; location filters and viewport are local to each browser session. The map requests records intersecting the current viewport in pages; dense views can still require substantial browser memory. Browser rendering and sustained deployment capacity have not been profiled. The app does not yet provide scheduled or off-site backup retention; administrators must download and store backups separately. Review TLS, retention, and access controls before production use.

All item types have local transparent icons in creation controls and editor previews. Non-Cisco items use original Mapper SVGs with consistent 1.6-unit strokes, a 32-unit canvas, and a slate/blue palette; Cisco PNGs retain their artwork and alpha transparency. Sites, buildings, floors, racks and services use compact cards with small side symbols. Rooms and telecom rooms display centered labels without oversized symbols; expanded floor groups use plain, lightly shaded boundaries. SVG sizes are explicit rather than stretched to fill nodes. Power equipment, outlets, panels, VMs, and cloud service categories have distinct symbols. Icon resolution is shared between the focused map, palette and editors. Icon sources and licenses are listed in [VENDORS.md](VENDORS.md).

## Transport and zoom

HTTP entity bodies and WebSocket application messages use Zstandard level 3. HTTP clients must advertise `Accept-Encoding: zstd`; unsupported clients receive HTTP 406. Body-bearing API requests require `Content-Encoding: zstd` (otherwise 415). The browser loads a local WebAssembly codec to compress uploads; native browser decoding handles responses. Use a browser with native Zstd HTTP support, and HTTPS for remote access so the browser advertises that support. Proxies must preserve Zstd negotiation and encoding. HTTP headers, bodyless responses and WebSocket protocol/control frames keep their standard wire format.

WebSockets negotiate `mapper.zstd.v1` and carry Zstd-compressed JSON in binary messages. Older uncompressed WebSocket clients must be updated. The server compresses each broadcast once for all subscribers. Decoded WebSocket messages are limited to 1 MiB; normal decoded HTTP uploads to 128 MiB. Restore retains its 4 GiB limit. Large decoded uploads spool to private temporary files and are removed after the request. Browser uploads use concatenated 1 MiB Zstd frames.

The bottom zoom toolbar provides a logarithmic 1%–800% slider, percentage display, zoom buttons and Fit map. Slider and mouse-wheel zoom stay synchronized.

## User and system logs

Embedded Loggy stores two categories: **User action** and **Application logs**. **Menu → Activity → User logs** is available to every signed-in user, including viewers. **Menu → Administration → System logs** and its API category require administrator rights. Views provide text/actor filters, severity, row limits, expandable structured details and optional refresh. Logs live in `MAPPER_LOG_DIR` with configurable retention (365 days by default).

Committed graph/account changes, login/logout and mutation request outcomes appear in User logs. Rolled-back changes are excluded. System logs capture server startup/shutdown, standard Go application logging, HTTP requests/status/duration/request IDs, handler errors/panic stacks, WebSocket diagnostics and authenticated browser exceptions (at most ten reports per minute per page). Request bodies, cookies and authorization headers are excluded; credentials in structured audit snapshots are redacted. Startup failures that prevent opening Loggy are reported to stderr. Loggy flushes regularly and closes on normal server shutdown. Log files are separate from the encrypted topology backup; manage their storage permissions and retention separately.

## Workspace compatibility

Cytoscape.js provides the diagram workspace, including buildings, telecom rooms,
racks, devices, conduits and cable routes. No geographic basemap is loaded.
Previously saved geographic metadata remains archived in encrypted backups and
CSV exports/imports; it does not control diagram editing. Old `map_style` settings
are accepted and ignored.

## Focused topology levels

The header level selector starts at **Global overview · Sites & Cloud**. Double-click a
site, building, floor, ordinary room, telecom room, rack, cloud service or conduit
to open its focused view. Single-click an item to inspect it. Breadcrumbs and Back
return to earlier views. The wheel and zoom slider change magnification within
the current view.

Child maps (floors, rooms and racks) and equipment belonging to the current view
remain visible even without connections. Parent and peer location maps appear
only when an actual physical cable or conduit connects them to this view.
Power-placement associations and logical-only links do not introduce external
location maps. The current building or room is not duplicated just to anchor its
own power equipment. External map references are paged with their connecting
edges, so loading a node-only page does not show isolated neighboring maps.
Cloud/service views retain their explicit relationship navigation.

Use **Create…** or the canvas right-click menu for actions appropriate to that
level. Buildings receive a Ground floor automatically; select a different floor
when adding rooms. Ordinary rooms and telecom rooms have separate views. Add DVO
or fiber outlets inside rooms and edit their ports through the inspector.

**Generators** belong directly to buildings. Add one from Site view (choose its
building) or Building view. **UPS units** can belong to a building, ordinary room,
or telecom room. Site view shows each building with its generator/building UPS;
room UPS units appear inside their room. Dotted **Supplies power** lines express
these associations separately from network connections.

Cloud/provider/service records include SaaS, PaaS, cloud hosting, Google Cloud,
Azure, AWS, Internet, MPLS and PLS instances. Add explicit access, hosting and dependency
relationships. Hosting within a cloud view is saved with the new resource.
Internet/MPLS/PLS attachments require a gateway device and real interface. Existing
cloud catalog devices can be explicitly converted through their right-click menu after
resolving incompatible connections.

Conduits can connect buildings without named rooms, specific floors, rooms on one
floor, or rooms across floors/buildings. Choose each endpoint independently by
its location path. Open a conduit to inspect/assign cables; edit a cable's ordered
route in its connection inspector. Wider views summarize underlying connections
and retain relevant external endpoints. Load more is explicit in dense views.

Run backend checks and the capacity benchmark with:

```sh
go test ./...
go vet ./...
go test ./internal/app/ -run '^$' -bench BenchmarkFocusedTopology100K -benchtime=5x -benchmem
```

The browser test builds a disposable server and encrypted database, then exercises
the real Cytoscape UI. Install development tooling outside this repository:

```sh
npm install --prefix /tmp/mapper-browser-tools puppeteer-core
NODE_PATH=/tmp/mapper-browser-tools/node_modules node tests/topology-browser.cjs
```

Set `MAPPER_BROWSER` if Chrome is not at `/usr/bin/google-chrome`.
`MAPPER_BROWSER_SCREENSHOT` optionally saves overview and site-view screenshots.


## VRFs on device interfaces

Edit a switch or router and enter its **Device VRFs**, one name per line.
In the interface editor, switches select one VRF (or the default/global table).
Router interfaces default to **All device VRFs**, including any added later;
choose **Select specific VRFs** to assign multiple VRFs or an empty selection.
The inspector lists each interface's assignment. Reassign interfaces before
removing a VRF that they explicitly use. VRF declarations and assignments survive
CSV export/import and encrypted backups; edits update collaborating users through
the existing WebSocket connection.


## Interface speeds and uplink bandwidth

Interfaces are **Regular** by default. Mark any interface **Uplink** when useful;
there is no requirement to have an uplink on every device. Physical interfaces
require a speed when added or edited, for example `1Gbps` or `100Mbps`. Virtual,
logical and passive interfaces are identified separately. Older/default/imported
ports without speed remain visible with an attention message.

Router uplinks carrying multiple VRFs show an allocation row for each VRF and
live capacity, allocated and remaining totals. Allocations cannot exceed uplink
speed: a 1 Gbps uplink accepts 600 + 400 Mbps but rejects 600 + 500 Mbps.
**All device VRFs** remains dynamic; adding a VRF can leave its uplink allocation
incomplete until filled in. CSV imports preserve missing details but reject
invalid rates or over-capacity allocations. Conflicting interface edits keep your
form open and request a refresh instead of overwriting another editor's changes.

## Application feature plan and technical details

### Product goal

Build a Go web application for maintaining and collaboratively viewing one physical and virtual network topology. The map is the main workspace. It must represent where equipment is installed, which physical interfaces connect, and how cables travel through patch panels, conduits, and manholes. Multiple authenticated users edit the same dataset and see updates quickly.

The first deployment target is one Linux server and one encrypted libmurmur database. The application reuses OV22’s Cytoscape page patterns and local topology assets, then removes OV22-specific pages and data/API coupling. Mapper keeps its own global topology model and uses the same compound-node, device-icon, search-highlight, and fit-to-view behaviors.

### Users, authentication, and access

- Provide a database unlock page at `/unlock`. The first unlock creates the initial administrator; later unlocks use the same database key. A wrong key must not initialize a replacement database in place of an existing one.
- Store all application records inside the encrypted libmurmur database. Store passwords as bcrypt hashes.
- Support `admin`, `editor`, and `viewer` roles. Admins manage users; editors create and change topology; viewers can search and inspect it.
- Support password login and optional mTLS login. When mTLS is enabled, a verified client certificate maps to an application username through SAN email addresses first, then the subject common name, matched without regard to case.
- Use secure session cookies, expire sessions after a fixed lifetime, rate limit unlock and login attempts, and restrict WebSocket origins.
- Support HTTPS configuration and an option to refuse startup without TLS.

### Inventory and topology data

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

#### Device interfaces

- Each device and patch panel can have named interfaces or ports.
- Interface records include connector/transceiver, speed, description, and other metadata. Physical equipment interface speeds are required on explicit interface saves; missing legacy/default/imported details remain flagged.
- Enforce unique interface names per device.
- A normal interface connects to one peer item across all connection classes, with one physical cable and optional multiple logical services to that peer. Shapes have one shared connection point that accepts multiple peers and cables.

#### Physical links and cable specifications

A connection joins two distinct interfaces. Physical connections require different devices; virtual connections may stay within one host. Classify each edge as physical, virtual, logical tunnel, or provider service. Provide an Other option with a descriptive custom type in every category.

- Physical media: fiber, copper twisted pair, coax, twinax/DAC, wireless/radio, and backplane. Record applicable cable type, endpoint connectors, strand count, length, part number, and shielding.
- Virtual connections: Ethernet, VLAN, bridge/virtual switch, veth, port group, overlay, bond, link aggregation, loopback, and custom types, with optional VLAN ID, VNI, network name, and bandwidth.
- Logical tunnel protocols: IPsec, WireGuard, GRE, VXLAN, L2TP, Geneve, IPIP, SIT, OpenVPN, SSL VPN, and custom types. Record optional mode and local/remote endpoints.
- Provider services: PLS, leased circuit, dark fiber, wavelength, carrier Ethernet, MPLS, Internet, EVPN, SD-WAN, and custom types. Record optional provider, circuit ID, and bandwidth.

Connection details are persisted with each edge, appear on the diagram and in edge details, and are supported by CSV import.

#### Patch panels and cable route

- A patch panel is an inventory object with interfaces representing ports.
- Record a pairing between two ports on the same panel to represent its internal pass-through.
- A physical link may have an ordered route referencing conduit and manhole records. Conduits are displayed as route details on the cable edge, never as devices or graph nodes; manholes may appear as route markers.
- Trace from a device interface through connected cables and paired panel ports to the far endpoint. Report missing, unpaired, cyclic, or excessively long paths clearly.

### Diagram and navigation

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

### Collaborative editing

- Maintain one shared topology for all authorized users.
- Commit mutations transactionally with their audit record and durable event sequence. Include an object version in map records and reject stale updates so concurrent users cannot silently overwrite the same object.
- Publish change notifications over WebSockets so open browsers refresh promptly.
- Replay persisted event batches by sequence number after connecting or receiving a notification. Periodically check the event cursor so clients recover from dropped WebSocket notifications.
- Refresh affected map state while preserving the user's viewport and active location filters.

### Editing, import, history

- Provide workflows to create, edit, and safely delete inventory objects; add or remove interfaces; connect or disconnect interfaces; pair or unpair panel ports; and set ordered cable routes. Refuse deletion when the object or interface is still referenced by a cable, pairing, or route.
- Let administrators add, rename, reorder, restyle, and delete unused network item types in the encrypted catalog. Preserve a type while inventory objects reference it.
- Allow a cable to be started by dragging one physical device or patch panel onto another, then require the editor to choose the interface at both ends and specify cable properties.
- Support CSV import for inventory, interfaces, cable connections and their specifications, panel pairings, and route segments.
- CSV preview must validate syntax and referenced records without committing changes. Applying the import must be all-or-nothing.
- Record actor, time, action, and relevant before/after data in the audit log for topology and account changes.
- Provide administrators an audit view.
- Provide safe undo for the latest uncontested object edit. Reject undo if another user has changed that object since the edit.

### Database and operational behavior

- Use `../libmurmur/clients/go` as the Go database client. Keep topology, user accounts, audit records, and replay events in the encrypted Murmur store.
- Create schema and indexes through an explicit versioned migration ledger. Apply each migration transactionally during unlock and refuse a store created by a newer Mapper version.
- Allow an administrator to download a native encrypted online backup while the application is unlocked. On the locked unlock page, allow restore from an uploaded backup after validating its archive, encryption key, contents, and application schema; install it through a staged directory swap so failed validation preserves the current store.
- Run as one Go server process, serving API, static web client, and WebSockets.
- Document database location, unlock procedure, TLS/mTLS configuration, CSV formats, and deployment assumptions.

- Focused views use contextual selectors and paginated searchable destination pickers. Child choices are scoped to their parent. Legacy location-management forms retain their local Select2 controls.

### Acceptance criteria

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

### Current implementation and remaining operational work

The Go implementation provides encrypted-store unlock, accounts and roles, mTLS mapping, an editable network item catalog, inventory and interfaces, hypervisor/VM containment, version-checked object edits, safe deletion of referenced topology objects, drag-to-connect cable editing, patch pairing/unpairing, route trace, independent device placement with location labels and filters, viewport paging, search, CSV import validation/apply, WebSocket event replay, audit entries, safe object undo, transactional schema migrations, and encrypted backup/restore. It remains a single-process early release. The browser locally serves the Cytoscape.js library and selected topology icon assets reused from OV22. Sustained deployment capacity profiling remains separate deployment work.

### Location and virtualization hierarchy

- Model Site → Building → Telecom room → Rack → Device, allowing partial placement for existing inventory. Racks derive their parent location and devices can be assigned to racks.
- Support ESXi, VMware vSphere, Hyper-V, Proxmox, KVM, Xen, XCP-ng, Nutanix AHV, oVirt, bhyve, and custom hypervisor types.
- Add VMs and vSwitches inside hypervisors. VM-to-vSwitch connections default to virtual Ethernet, with a fresh port when needed. Each port has one peer.
- Double-click locations and racks to enter their focused view; breadcrumbs and Back return to prior scopes. Inspect hypervisors to manage their hosted VMs/vSwitches. Search reveals the containing scope while preserving relevant external connections.

### Transparent item icons

- Give every catalog visual and every item kind a local transparent icon, including generic devices, hypervisors, VMs, panels, sites, buildings, rooms, racks, conduits, manholes, labels, and shape variants.
- Retain Cisco PNG symbols with alpha transparency. Use the original, consistently drawn Mapper SVG set in `web/img/mapper/` for non-Cisco items. The previously bundled Microsoft Fluent assets, license and source manifest remain in `web/img/fluent/` for provenance. No runtime dependency or external image request is needed.
- Share icon resolution between topology node data, editor previews and inspector headings. Render compact side symbols on location/service cards, label-only rooms, transparent symbols on leaf nodes, and type previews for editable text and shapes. Verify that no catalog entry is iconless and that raster symbols contain transparency.

### Zstandard transport, zoom and operational logging

- Apply Zstandard level 3 to all HTTP entity bodies, requiring explicit client negotiation, and require compressed request bodies. Preserve protocol headers/control frames. Stream responses, validate/decode uploads before mutation, spool large uploads to private temporary files, and enforce decoded limits (128 MiB general, 4 GiB restore, 1 MiB WebSocket).
- Negotiate `mapper.zstd.v1`, encode JSON as binary Zstd messages, and compress each broadcast once for all clients. Serve the pinned browser WebAssembly codec locally and compress uploads in 1 MiB frames.
- Provide a bottom logarithmic zoom slider (1%–800%), synced percentage, buttons and fit action. Bind controls whenever Cytoscape is rebuilt.
- Persist Loggy categories `User action` and `Application logs`. Publish transaction audit entries only after commit; redact credentials. Capture standard Go logs, HTTP diagnostics/errors/panics with request IDs, lifecycle events, WebSocket failures and bounded authenticated browser exception reports.
- All authenticated roles can view/search User logs under Activity. Only administrators can view/search System logs under Administration. Enforce category permissions on the API; provide separate views, severity filters and expandable structured event details.

### Diagram workspace and legacy data compatibility

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

### Focused topology views, cloud services, and power equipment

#### Navigation and visible content

The default workspace is **Global overview · Sites & Cloud**. Double-click a location,
cloud service or conduit to enter a focused view; use breadcrumbs or Back to
return. The level selector opens a searchable destination picker. Mouse-wheel
and slider zoom change magnification within the selected view.

| View | Contents and creation actions |
|---|---|
| Global | Sites, named cloud/service offerings, Google/Azure/AWS or other provider instances, Internet/MPLS/PLS services and actual summarized connections. Add sites/services and access/hosting/dependency relationships. |
| Cloud/service | Directly hosted resources, connected external sites/services and relationships. Add hosted resources/services, relationships and network attachments. |
| Site | Buildings, their generators and building-level UPS units, inter-building conduits/connections and external services. Add buildings, generators, UPS units, conduits, relationships and Internet/MPLS/PLS attachments. |
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

#### Model, power, and migration

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
- Every site carries a `site_role`: `data_center`, `major_site`, `branch_office`,
  `edge_site`, `disaster_recovery`, or `cloud_colo`. The role is required on
  object API writes and selects the site map badge; unknown values are rejected.
  Imports, undo, and pre-role sites stay accepted and render the default globe.
- References protect locations, conduit endpoints, hosted resources, services and
  connected ports from unsafe deletion. An empty automatically created Ground
  may be removed together with its otherwise empty building.

#### Cloud and WAN connectivity

- A `service` object has `service_category`: `cloud_provider`, `saas`, `paas`, `iaas`,
  `faas`, `cloud_hosting`, `cloud_resource`, `internet`, `mpls`, or `pls`. Provider, account,
  region and description distinguish instances; a physical location is not required.
- Offerings/providers appear globally; detailed `cloud_resource` records appear in
  their hosting view. No resources or usage are inferred from provider names.
- Versioned `accesses`, `hosts` and `depends_on` relationships have no physical-port
  requirement. Hosting is service-to-service and rejects cycles. Creating a hosted
  resource in a service view creates its hosting relationship in the same transaction.
- Internet/MPLS/PLS attachments require a real gateway device interface and matching
  provider-service connection. Service-side attachment interfaces are distinct.
- Physical network connections and dashed directed cloud relationships remain
  separate. Service identity is ID-based; equal labels never merge services.
- Existing cloud-type devices remain unchanged until explicitly converted. Conversion
  retains IDs/interfaces and requires incompatible physical connections and hosted
  VMs to be resolved first. WAN conversion additionally validates connection type.

#### Conduits and projection

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

#### APIs, performance, and collaboration

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

#### Validation

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


#### Device and interface VRFs

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


#### Interface usage, speeds and router-uplink bandwidth

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

## Multi-network and VRF analysis

Choose a **VRF name** in the focused workspace and select **Color selected VRF**
or **Filter selected VRF**. Participating devices and location summaries have blue
borders; interface rows in the inspector highlight matching assignments. Filtering
uses actual endpoint interface membership when selecting connections. Suggestions
come from the visible inventory; type any declared name to search other scopes.
The selection is local to the browser and refreshes through the existing durable
WebSocket replay. Membership is derived once per shared topology snapshot, including
dynamic router **All device VRFs** assignments, and propagated to location ancestors.

**Data quality** reviews address reuse, duplicates within a network and named VRF,
undeclared/deleted VRF assignments, and invalid IP text. Prefixes compare as host
addresses: `192.0.2.1/24` matches `192.0.2.1`; equivalent IPv6 and IPv4-mapped IPv6
addresses normalize. Multiple network memberships, including legacy comma-separated
labels, are checked separately. Cross-network/VRF reuse may be intentional. VRFs
remain device-local; matching names alone do not establish a shared routing domain.
These findings review stored inventory and do not monitor devices.

**VRF path** selects source and destination devices/interfaces and a named VRF.
It follows recorded connections, transparent patch-panel pairings, and internal
router/switch hops between interfaces carrying that VRF. The result lists named
interface hops or reports no modeled path. Endpoint assignments must be valid;
interfaces with stale assignments cannot carry that missing VRF. Traversal visits
each interface and expands each device once, avoiding cycles and quadratic port
expansion. This is potential inventory connectivity: matching device-local VRF
names and internal forwarding are assumed. Routing policy, encapsulation and live
operational state are not evaluated.

**Save snapshot** downloads a JSON inventory snapshot for all networks, or the
selected network. Snapshots contain inventory, interfaces, connections, patch
pairings, cable routes, cloud relationships and shared scoped layout coordinates;
they contain no accounts, sessions or database keys. Use encrypted backups for
full database recovery. **Compare** reviews two current networks or two saved
snapshot files. Matching uses stable kind/ID pairs; equal names alone do not match
distinct equipment. Changes are **added**, **removed**, **moved** (location, host,
network or coordinates), or **changed**. Expand a row to review before/after data.
A moved row also retains any simultaneous metadata changes in its details.

Authenticated APIs, available to viewers as read-only operations:

- `GET /api/topology/view?vrf=<name>` filters the existing bounded projection.
  Nodes include aggregated `vrfs`; the existing scene cache is capped at 32 entries.
- `GET /api/topology/quality?offset=0` returns the sequence, total findings and up
  to 100 findings, each with up to 100 interface details and full `port_count`.
  `kind`, `address`, `scope` and `port_offset` page through members of one finding.
- `GET /api/trace?interface_id=<A>&target_interface_id=<B>&vrf=<name>` returns
  `reachable`, named `steps`, the inventory sequence and interpretation. Existing
  cable tracing without `vrf` remains available.
- `GET /api/topology/snapshot?network=<optional network>` downloads the versioned
  `mapper.topology.v1` snapshot. Capture reads one transaction for consistency.
- `GET /api/topology/compare?before_network=<A>&after_network=<B>` compares current
  inventories in one transaction. `POST /api/topology/compare` accepts
  `{"before": <snapshot>, "after": <snapshot>}` for saved-file comparison.
  Responses include counts, up to 100 changes, sequences and `next_cursor`.
  Supply that cursor as `after`; `sequence` guards pagination against changes.
  Quality member pagination accepts the same sequence guard.

Snapshot comparison and data-quality scans run on demand. No production dependencies,
schema changes, cloud calls or monitoring probes were added. Existing transport
compression, upload limits and HTTP diagnostics apply.

Validation includes Go integration checks for VRF isolation, panel traversal,
legacy stale references, multiple network memberships, scoped diagram moves,
identity validation, pagination and viewer permissions. The Chromium test exercises
color/filter controls, quality review, file comparison and the trace form. Local
100,000-record samples on an Intel Core i5-6500 measured about 101 ms for quality
analysis and 264 ms for comparison (47 MB and 109 MB allocated respectively);
these are in-memory algorithm samples, excluding database loading and transport.
The existing 100,000-device/100,000-link projection check measured 2.5 s cold and
0.68 ms cached; with two VRFs and filtering enabled it measured 3.2 s cold and
0.92 ms cached. These synthetic samples are not sustained production capacity results.

```sh
go test ./...
go vet ./...
NODE_PATH=/tmp/mapper-browser-tools/node_modules node tests/topology-browser.cjs
go test ./internal/app -run '^$' -bench 'BenchmarkVRFTopology100K|BenchmarkNetworkQuality100K|BenchmarkNetworkCompare100K' -benchtime=1x -benchmem
```

## Palette drops and connections across maps

The top bar offers all inventory tools plus **Link** for editors. Compatible drop
targets highlight blue; incompatible targets highlight red and make no changes.
Dropping inventory onto empty canvas opens its creation form at that position.
Dropping onto a compatible container opens that map first and preselects its
parent in the form. Saving keeps the destination map open and selects the new
item; canceling creates nothing. Rooms dropped onto buildings use their existing
Ground floor. Unsupported parent/type combinations are rejected rather than
inventing locations.

Drop **Link** onto equipment to start a connection preview. Dropping Link onto a
site, building, floor, room, telecom room or rack instead prepares a new DVO in
that container. The current map stays open. Single-click containers to select;
double-click them to navigate while the preview remains active. Breadcrumbs,
Back, pan/zoom and live refresh also preserve the draft. When its source is
outside the current view, a temporary marker shows the first endpoint's container.
These draft-only markers are an exception to normal external-map visibility;
they are never saved as topology, layout coordinates or inventory counts.

Select the second equipment/outlet endpoint to review interfaces and cable
properties. A sole interface is preselected; multiple interfaces require a
choice. A zero-interface endpoint offers **Interface 1 (created on confirmation)**.
A new DVO offers Data/Voice RJ45 ports, defaulting to Data and a copper cable.
Automatic equipment interfaces retain unknown speed/VRF details until configured,
using existing validation/attention behavior rather than guessed values.

**Confirm connection** saves all generated inventory and the cable in one
transaction. Before confirmation, Escape, right-click or Cancel discards the
preview without creating DVOs or interfaces. Saving disables cancellation and
repeat submission until its outcome is known. Conflicts keep the form and draft
open; use **Refresh interface choices** to review changed endpoints and retry.
Reload/sign-out clears browser drafts. Viewers cannot create drafts or records.

`POST /api/topology/connect` is the authenticated editor API. Its JSON contains
`id`, `source`, `target`, and the existing connection `data` specification. Each
endpoint supplies its owner `version` and either:

- `object_id`, selected `interface_id`, and optional interface `revision`;
- `object_id` and `auto_interface: true` for a zero-interface owner;
- `container_id`, optional stable `provisional_id`, and `interface_name` (Data or
  Voice) to generate a DVO.

The response includes the link ID, committed sequence, and resolved endpoint IDs
with their created object/interface IDs. Stale/missing owners, changed ports or
newly occupied interfaces return 409 with no partial writes. Existing
`/api/links` clients remain compatible. DVOs can belong directly to sites and
buildings, as well as floors, rooms and racks; older outlets keep their placement.
CSV, snapshots and encrypted backups retain these ancestry fields. Generated
records and links share transactional audit/events and existing WebSocket replay.
No schema migration or production dependency was added.

The dedicated browser check uses the same external Puppeteer tooling as the
existing topology check:

```sh
NODE_PATH=/tmp/mapper-browser-tools/node_modules node tests/drag-drop-browser.cjs
```
