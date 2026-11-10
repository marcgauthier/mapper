# Mapper

Mapper is a single-server physical and virtual network topology editor written in Go. It uses Cytoscape.js for the shared map and `../libmurmur` for encrypted database storage. All users work on the same topology. Authenticated edits are committed to the database, recorded in the event log, and pushed to other open browsers over WebSockets. Browsers replay event batches after a notification and periodically check the event cursor so a dropped WebSocket notification is recovered.

## Run

Requires Go 1.26.5 or newer and the native build prerequisites documented by `../libmurmur/clients/go`.

```sh
go run .
```

Settings live in `mapper.yaml` next to the server (`MAPPER_CONFIG` selects another path). Every setting can still be overridden with an environment variable: `MAPPER_ADDR` (default `:8080`), `MAPPER_DB` (Murmur store directory, default `./data/mapper`), `MAPPER_TLS_CERT`/`MAPPER_TLS_KEY` (set together to serve HTTPS), `MAPPER_REQUIRE_TLS=1` (refuse startup without HTTPS), `MAPPER_CLIENT_CA` (optional verified client certificates), `MAPPER_LOG_DIR` (action-log directory, default `./data/logs`), and `MAPPER_LOG_RETENTION_DAYS` (default `365`). Unknown keys in `mapper.yaml` are rejected so typos fail loudly.

Open `/unlock` on first use, enter a database key, and create the initial administrator. On later starts, unlock with the same key. The database key is SHA-256 derived before passing it to libmurmur. A wrong key does not initialize a replacement store over an existing one.

A verified client certificate's common name maps to a username. Password login remains available with TLS. TLS is strongly recommended anywhere traffic leaves the local trusted host.

Unlock is limited to three attempts per peer in five minutes; password login is limited to five. Sessions use HttpOnly, SameSite Strict cookies and expire after twelve hours. Administrators can list, create, update, or disable users with `admin`, `editor`, or `viewer` roles. The last administrator cannot be disabled or demoted.

## Encrypted backup and restore

Administrators can choose **Menu → Administration → Download encrypted backup** while signed in. Mapper uses libmurmur's online checkpoint API and downloads a `.murmur` archive containing the encrypted native checkpoint. The archive uses the same database encryption key as the live store.

Restore is available from `/unlock` while Mapper's database is locked. Select a Mapper `.murmur` file and enter its database key. Mapper verifies the archive and key, opens the restored store, applies migrations, and checks that it contains configured users before staging it into `MAPPER_DB`. A bad key, invalid archive, or invalid application database leaves the current store unchanged. Restoring replaces the current store; keep a separate copy of any data you may need.

Schema changes are tracked in the encrypted `schema_migrations` table. Migrations run transactionally at unlock; new schema changes must be added as a new, append-only migration in `migrations.go`.

## Action logs

Loggy stores user activity and application diagnostics separately. Every signed-in user can review **Menu → Activity → User logs**. **Menu → Administration → System logs** requires administrator rights. See “User and system logs” below for filters, retention and captured diagnostics.

## API and live updates

- `POST /api/login`, `POST /api/logout`, `GET /api/me`
- `GET|POST|DELETE /api/objects`, `GET|POST|DELETE /api/interfaces`, and `GET|POST|DELETE /api/links`
- `GET /api/search?q=...`; `GET /api/layout?grouping=...` and `POST` layout updates
- `GET|POST|DELETE /api/pairings`; `GET|POST /api/routes`; `GET /api/trace?interface_id=...&link_id=...`
- `GET /api/export` downloads topology CSV; `POST /api/import`; `GET /api/logs?category=user` for all signed-in users; admin-only `GET /api/logs?category=system`, `GET /api/audit`, `GET /api/backup`, `GET|POST|PUT|DELETE /api/users`, `GET|POST|PUT|DELETE /api/device-types`, and `GET|POST|PUT|DELETE /api/connection-types`; locked-page `POST /restore` accepts a `.murmur` archive and database key
- Authenticated `GET /api/events?after=<sequence>` replays durable changes; authenticated `/ws` notifies clients to check for new events.

Trace defaults to the physical cable path; an optional `link_id` selects a particular connection on a shared interface. Virtual, tunnel, and provider traces show that connection directly. Only physical connections accept conduit/manhole cable routes in the API and CSV importer.

Object viewport queries accept `min_x`, `max_x`, `min_y`, `max_y`, `grouping`, `limit`, and `offset`. Interface requests support `device_id` or up to 500 `device_ids`. Object queries also support up to 500 `host_ids` or `container_ids` to retrieve complete group contents outside the viewport; link queries support `device_ids` for complete group connectivity. Route requests support `link_id` or up to 500 `link_ids`.

## Topology features

- Right-click the canvas to add device, patch panel, conduit, manhole, and text label objects; edit or delete items and connections through the inspector or context menu. Locations (sites, buildings, telecom rooms, racks) are managed under Menu → Sites & cable plant; every item references them for location. The header has one sectioned Menu dropdown: Create, Sites & cable plant, Topology tools, Import / Export, Administration (administrators only), and Sign out. New map items are added by right-clicking the canvas. CSV actions are available in the menu. Devices, interfaces, connections, routes, and search results display names instead of internal UUIDs.
- Right-click the canvas and choose **Add shape** for a square, rectangle, circle, or cloud. Edit its dimensions (24–2000 map units) and fill color. Each shape has exactly one automatically created, protected connection point, which multiple items can share. Devices, VMs, patch panels, text labels, manholes, and other shapes can connect to it.
- **Menu → Locations → Sites / Buildings / Telecom rooms / Racks** opens searchable management lists with add, edit, and delete actions. Buildings can select a site; telecom rooms select their building; racks select their telecom room. Add devices directly from a rack inspector or context menu, or choose a rack in the device editor’s Network tab. Rack assignment derives the device’s site, building, and room. Referenced locations are protected from deletion. Viewers can browse these lists.
- Text labels can be moved, edited, and styled with font family, size (8–144), color, bold, and italic. Label changes and positions are shared and persisted like other map items.
- An administrator-editable network item catalog with the original defaults plus 92 additional types using local Cisco icons: Nexus and Layer 3 switches, ASR and carrier routers, ASA/PIX firewalls, VPN and security appliances, wireless controllers/APs, servers, endpoints, storage, voice/video, and optical equipment; plus encryptors (inline/serial with red/black sided interfaces), airgaps (low/high sided interfaces), media converters, containers, pods, container hosts, Kubernetes nodes, cloud providers, and SaaS/PaaS/IaaS/FaaS services. Attach a service to its cloud provider with a provider-service connection. The item editor previews the selected icon. Existing catalog entries are preserved by the migration.
- Device metadata is organized in a Network tab. The Interfaces tab provides a scrollable, searchable list suitable for 48- and 96-port devices, with natural port ordering and add, edit, and remove actions. Editing preserves the interface ID and its connections, pairings, and custom metadata; fields include name, IP address or prefix, connector, speed, and description.
- The Hypervisors catalog group includes ESXi, VMware vSphere, Hyper-V, Proxmox VE, KVM/QEMU, Xen, XCP-ng, Nutanix AHV, oVirt, bhyve, and a generic Hypervisor type. Custom types can select the Hypervisor visual style.
- Hypervisors contain VM and vSwitch devices. Use **Add VM** or **Add vSwitch** in the inspector or context menu. VMs support Linux and Windows; hosted items inherit the hypervisor location and rack. Connecting a VM to a vSwitch defaults to virtual Ethernet and automatically creates another vSwitch port when its existing ports are occupied. Each port retains the one-peer rule. Hypervisors with hosted items are protected from deletion or conversion to ordinary device types.
- Physical, virtual, logical tunnel, and provider service connections between interfaces. Administrators can add custom connection types per category with map edge colors under Menu → Administration → Connection types; custom physical types require cable details like other cabled media. Each normal interface can connect to only one peer item across all connection classes, with at most one physical connection; multiple virtual connections, tunnels, and provider services to that same peer remain possible. Shape points allow multiple peers and physical connections. The picker marks unavailable ports, and the API and CSV importer enforce these rules transactionally. Virtual connections may join distinct interfaces on the same device. New devices start with an interface; sided types start with their role interfaces. Existing stores migrate automatically at unlock while preserving connections and cable routes.
- Drag one device or patch panel onto another, click a device then Shift-click a second device, or right-click a device and choose **Connect to Device…**. This selects the source and shows a line following the pointer; clicking a second item opens the connection confirmation form. Press Escape or right-click to cancel. **Connect to Device (form)** opens the target picker directly. No connection is saved until the confirmation form is submitted. The interface picker preselects a sole interface and creates “Interface 1” when an endpoint has none. Right-click devices to copy them and the canvas to paste duplicates with their interface definitions.
- Patch panel ports and pairings, ordered conduit and manhole route details shown on cable edges, and a trace view across connected segments. Open a conduit to inspect its contained cables and edit cable routes through the connection inspector.
- A conduit spans independently chosen site, building, floor, ordinary-room, telecom-room or manhole endpoints. Building endpoints do not require a room. Cable routes list conduit endpoints by name. Deleting an endpoint or route member is blocked until references are removed.
- The workspace opens focused levels for sites/cloud services, sites, buildings, floors, ordinary rooms, telecom rooms, racks and conduits. Click a container to enter it; use breadcrumbs or Back to return. Creation and filtering follow the current level. Connections crossing a scope boundary retain their relevant external endpoint; internal connections are summarized until their container is opened. Wheel zoom changes magnification within the current level. See **Focused topology levels** below for placement and power inventory.
- Pan, zoom, viewport-bounded object and link queries, device-only search results in the header, and a shared saved location layout. The map uses the full main workspace without a left sidebar. A bottom-right navigator shows the whole graph with a draggable viewport square to jump to any portion of the map; it collapses to a single button and hides on narrow screens.
- **Menu → Import / Export** offers **Export CSV** and **Import CSV**. Export produces a consistent topology snapshot in dependency order, including inventory, interfaces, connections, pairings, and cable routes. The `metadata_json` column preserves arbitrary metadata and arrays. Import performs a transactional dry run followed by an all-or-nothing commit. Custom device type IDs must exist in the destination catalog before importing inventory that uses them.
- Administrator user management, audit history, safe undo for the latest uncontested object edit, and live multi-user updates with stale object edit conflicts.

CSV rows use `kind` plus fields for the object type. Inventory rows need `kind,name` and may include `id,x,y` and arbitrary metadata columns such as `device_type_id,device_type,site_id,building_id,telecom_room_id,network,supportgroup`. Virtual machine rows use `kind=virtual_machine,name,device_type_id,host_id,guest_os`; use the `vm` type by default, import the hypervisor first, and set `guest_os` to `Linux` or `Windows`. Virtual switch rows use `kind=virtual_switch,name,device_type_id,host_id` with the `vswitch` catalog type. Building rows need `site_id`; telecom room rows need `building_id`. Rack inventory uses `kind=rack,name` with `telecom_room_id` or `building_id`; device and patch panel rows must name a `building_id`, `telecom_room_id`, or `rack_id`. Import locations before racks, racks before devices, and hypervisors before VMs and vSwitches. Interface rows use `kind=interface,device_id,name,id`; connection rows use `kind=connection,source_interface,target_interface,id` plus connection-specific columns. Physical media are `fiber,copper_twisted_pair,coax,twinax_dac,wireless_radio,backplane,other`; cable fields include `cable_type,connector_a,connector_b,strand_count,length_m,part_number,shielding`. Virtual rows use `connection_class=virtual,virtual_type` with `ethernet,vlan,bridge,veth,port_group,overlay,bond,lag,loopback,other`; optional fields are `vlan_id` (1–4094), `vni` (1–16777215), `network_name`, and `bandwidth`. Logical tunnel rows use `connection_class=logical_tunnel,tunnel_protocol,tunnel_mode,local_endpoint,remote_endpoint`; supported protocols are `ipsec,wireguard,gre,vxlan,l2tp,geneve,ipip,sit,openvpn,ssl_vpn,other`. Provider rows use `connection_class=provider_service,service_type,provider,circuit_id,bandwidth` with types `pls,leased_circuit,dark_fiber,wavelength,carrier_ethernet,mpls,internet,evpn,sd_wan,other`. Each category supports `other` with a required descriptive `custom_type`. All connection categories accept optional `label` and `description`. Shape rows use `kind=shape,name` with optional `shape_type` (`square,rectangle,circle,cloud`), `width,height,fill_color,x,y`. Circle and square heights match their width. Include one interface row to preserve a shape point ID when importing connections; standalone shape imports receive a point automatically. Text label rows use `kind=label,name` with optional `font_family,font_size,font_color,font_weight,font_style,x,y`; font names match the editor choices, colors use `#RRGGBB`, weight is `normal` or `bold`, and style is `normal` or `italic`. Pairing rows use `kind=pairing,panel_id,port_a,port_b,id`; route rows use `kind=route,link_id,infrastructure_id,sequence,id`. Conduit rows use `kind=conduit,name` with optional `endpoint_a_id,endpoint_b_id` naming two distinct sites, buildings, telecom rooms, or manholes. Place referenced objects before interfaces, interfaces before connections and pairings, and connections before route rows.

See [PLAN.md](PLAN.md) for the application feature plan, data model, workflows, and acceptance criteria.

## Current limits

This is an early single-process implementation. The browser uses local Cytoscape.js, jQuery, Select2, and topology icon assets copied from OV22, so the diagram does not depend on a public CDN. Node positions and topology are shared; location filters and viewport are local to each browser session. The map requests records intersecting the current viewport in pages; dense views can still require substantial browser memory. Browser rendering and sustained deployment capacity have not been profiled. The app does not yet provide scheduled or off-site backup retention; administrators must download and store backups separately. Review TLS, retention, and access controls before production use.

All item types have local transparent icons in the editor and inspector. Missing symbols use bundled Microsoft Fluent SVGs; Cisco symbols retain their PNG alpha transparency. Sites, buildings, rooms, racks, and hypervisors display icon badges, while VMs, vSwitches, patch panels, and manholes display map symbols. Icon sources and licenses are listed in [VENDORS.md](VENDORS.md).

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

The header level selector starts at **Global overview · Sites & Cloud**. Click a
site, building, floor, ordinary room, telecom room, rack, cloud service or conduit
to open its focused view. Breadcrumbs and Back return to earlier views. The wheel
and zoom slider change magnification within the current view.

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
Azure, AWS, Internet and MPLS instances. Add explicit access, hosting and dependency
relationships. Hosting within a cloud view is saved with the new resource.
Internet/MPLS attachments require a gateway device and real interface. Existing
cloud catalog devices can be explicitly converted through their inspector after
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
go test -run '^$' -bench BenchmarkFocusedTopology100K -benchtime=5x -benchmem
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
