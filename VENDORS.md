# VENDORS.md — External dependencies in production

Mapper keeps external dependencies to a minimum. Custom code is preferred;
when custom code is not the best option, libraries backed by large
corporations are preferred over community or single-author libraries.
All frontend third-party code is vendored locally under `web/vendor/` so the
app does not depend on a public CDN at runtime.

## Backend (Go)

Declared in `go.mod` / `go.sum`:

| Dependency | Version | Source | License | Why it is needed |
|---|---|---|---|---|
| `github.com/marcgauthier/libmurmur/clients/go` | local path (`replace` → `../libmurmur/clients/go`) | sibling repo, single author | see libmurmur repo | Encrypted database storage engine; no stdlib equivalent |
| `github.com/marcgauthier/websocket` | v1.0.0 | single author | see module | WebSocket server for live multi-user updates |
| `github.com/marcgauthier/loggy` | v0.0.0-20261022121611-4421778da37f | single author | Apache-2.0 | Embedded compressed user-action and application-log store (VictoriaLogs engine) with 365-day retention; requested explicitly, no stdlib equivalent |
| `github.com/DataDog/zstd` | v1.5.7 | Datadog (corporation) | BSD-3-Clause (Simplified BSD) | zstd HTTP/WebSocket transport compression in `internal/app/transport.go`; no stdlib equivalent |
| `golang.org/x/crypto` | v0.41.0 | Google (golang.org/x) | BSD-3-Clause | Password hashing (bcrypt), key derivation |
| `golang.org/x/net` | v0.43.0 | Google (golang.org/x), indirect | BSD-3-Clause | Pulled in by x/crypto and the WebSocket stack |
| `golang.org/x/sys` | v0.46.0 | Google (golang.org/x), indirect | BSD-3-Clause | Pulled in by loggy and x/crypto |

The `mapper.yaml` settings parser is custom code (`internal/app/config.go`); no YAML
library is used. Everything else is Go standard library. Build requires Go
1.26.5+ and the native prerequisites documented by
`../libmurmur/clients/go`.

## Frontend (browser)

Vendored in `web/vendor/`, served from the Mapper server itself:

| Dependency | Version | Source | License | Why it is needed |
|---|---|---|---|---|
| Cytoscape.js (`cytoscape.min.js`) | 3.33.1 | Cytoscape Consortium (community, widely adopted) | MIT (`cytoscape.LICENSE`) | Graph rendering/interaction for the topology map; custom canvas code is not the best option here |
| jQuery (`jquery.min.js`) | 3.7.1 | OpenJS Foundation | MIT (`jquery.LICENSE`) | Required by Select2 |
| `@bokuweb/zstd-wasm` | 0.0.27 | bokuweb (community wrapper), Meta Zstandard engine | MIT wrapper / BSD-3-Clause engine (see `web/vendor/zstd/NOTICE`, `LICENSE-zstd`, upstream README) | Browser uploads and WebSocket level-3 compression; pinned package integrity and modifications in `SOURCE.json` |
| Select2 (`select2.min.js`, `select2.min.css`) | 4.1.0-rc.0 | community (Kevin Brown et al.) | MIT (`select2.LICENSE`) | Searchable site/building/room filter dropdowns |

Icon and image assets:

| Asset | Location | Source | Notes |
|---|---|---|---|
| Cisco topology icons (100 PNGs) | `web/img/cisco/` | OV22 collection copy, bundled locally | Device map symbols; no runtime download |
| Microsoft Fluent UI System Icons (16 SVGs) | `web/img/fluent/` | Microsoft, commit `9bfae3d782fda4c8764f2128cec4d0dda160b283` | MIT (`web/img/fluent/LICENSE`); original download URLs in `SOURCES.json`; original vendored assets retained for provenance; current non-Cisco map symbols use the original Mapper set below |
| Mapper topology symbols (33 SVGs) | `web/img/mapper/` | original, in-repo | Consistent 32-unit outline symbols for non-Cisco inventory, locations, power and cable plant; no third-party dependency |
| Cloud shape | `web/img/cloud.svg` | custom, in-repo | Shape fill rendering |
| Branch office badge | `web/img/site-branch.svg` | custom, in-repo | Branch-office site role map badge |
| Edge site badge | `web/img/site-edge.svg` | custom, in-repo | Edge/local site role map badge |
| Disaster recovery badge | `web/img/site-dr.svg` | custom, in-repo | Disaster-recovery site role map badge |
| Mapper logo | `web/img/mapper-logo.svg` | custom, in-repo | Login/unlock banner and brand lockup |
| Mapper mark | `web/img/mapper-mark.svg` | custom, in-repo | Header brand, favicon, login banner mark |
| Red/black, low/high split squares | `web/img/red-black.svg`, `web/img/low-high.svg` | custom, in-repo | Encryptor and airgap map symbols |
| Container and pod shapes | `web/img/container.svg`, `web/img/pod.svg` | custom, in-repo | Container map symbols |
| Cloud service badges | `web/img/saas.svg`, `web/img/paas.svg`, `web/img/iaas.svg`, `web/img/faas.svg` | custom, in-repo | SaaS/PaaS/IaaS/FaaS map symbols |

All other frontend code (`web/index.html`, `web/logs.html`, `web/*.js`) is
custom code with zero additional runtime dependencies. There are no basemap,
analytics, font, or telemetry calls to external hosts.

Every device visual has a local icon. Cisco PNGs retain alpha transparency. Original Mapper SVGs replace non-Cisco map and catalog symbols with consistent strokes, restrained colors and transparent canvases. Location/service cards use compact side symbols; rooms have labels without map icons. Cloud service, branch, edge, recovery, container/pod and sided-device SVGs are also original in-repo artwork. The original Fluent SVG files, source manifest and MIT license remain unchanged for provenance. Creation controls and editor previews share icon resolution with the map.

## Focused topology workspace

The level navigation, server projections/caches, relationship model, contextual
forms and generator/UPS display are custom code. They reuse the existing local
Cytoscape library and original Mapper SVG symbols and SaaS/PaaS/cloud badges. No additional
production library, basemap, hosted cloud API, or external asset request is used.
Google Cloud, Azure, AWS and other provider names describe inventory only.

`puppeteer-core` is optional Google-supported development tooling for
`tests/topology-browser.cjs`; install it outside the application tree. It is not
served to browsers or required by the production server.

Device/interface VRF storage, validation, selectors and dynamic router defaults
use custom Go and browser JavaScript with existing JSON storage and WebSocket
transport. No external dependency or hosted service was added.

Interface speed parsing, usage classification, per-VRF capacity accounting and
revision tokens use custom code and the Go standard library (`math/big`,
`crypto/sha256`). The editors use native DOM controls; no production dependency
was added for bandwidth allocation or validation.

Palette drop targeting, cross-map connection drafts, provisional DVO markers and
atomic interface/DVO/link provisioning are custom DOM/Cytoscape and Go code.
They add no production dependency; the Link tool reuses a local Mapper SVG.
