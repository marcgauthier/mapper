package app

import (
	"database/sql"
	"fmt"
	"time"
)

// schemaMigrations is ordered and append-only. Never edit an applied migration;
// add a new version for each future schema change.
var schemaMigrations = []struct {
	version    int
	statements []string
}{
	{version: 1, statements: []string{
		`CREATE TABLE IF NOT EXISTS users(id TEXT PRIMARY KEY, username TEXT UNIQUE NOT NULL, password TEXT NOT NULL, role TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS objects(id TEXT PRIMARY KEY, kind TEXT NOT NULL, name TEXT NOT NULL, data TEXT NOT NULL, x REAL NOT NULL DEFAULT 0, y REAL NOT NULL DEFAULT 0, version INTEGER NOT NULL DEFAULT 1)`,
		`CREATE TABLE IF NOT EXISTS links(id TEXT PRIMARY KEY, source_interface TEXT NOT NULL UNIQUE, target_interface TEXT NOT NULL UNIQUE, data TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS interfaces(id TEXT PRIMARY KEY, device_id TEXT NOT NULL, name TEXT NOT NULL, data TEXT NOT NULL, UNIQUE(device_id,name))`,
		`CREATE TABLE IF NOT EXISTS patch_pairings(id TEXT PRIMARY KEY,panel_id TEXT NOT NULL,port_a TEXT NOT NULL,port_b TEXT NOT NULL,UNIQUE(panel_id,port_a),UNIQUE(panel_id,port_b))`,
		`CREATE TABLE IF NOT EXISTS cable_routes(id TEXT PRIMARY KEY,link_id TEXT NOT NULL,sequence INTEGER NOT NULL,infrastructure_id TEXT NOT NULL,UNIQUE(link_id,sequence))`,
		`CREATE TABLE IF NOT EXISTS layouts(grouping TEXT NOT NULL,object_id TEXT NOT NULL,x REAL NOT NULL,y REAL NOT NULL,PRIMARY KEY(grouping,object_id))`,
		`CREATE TABLE IF NOT EXISTS audit(id INTEGER PRIMARY KEY, actor TEXT, action TEXT, object_id TEXT, data TEXT, created INTEGER)`,
		`CREATE TABLE IF NOT EXISTS events(seq INTEGER PRIMARY KEY AUTOINCREMENT,payload TEXT NOT NULL,created INTEGER NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS objects_bounds_idx ON objects(x,y)`,
		`CREATE INDEX IF NOT EXISTS objects_name_idx ON objects(name)`,
		`CREATE INDEX IF NOT EXISTS objects_site_idx ON objects(json_extract(data,'$.site_id'))`,
		`CREATE INDEX IF NOT EXISTS objects_building_idx ON objects(json_extract(data,'$.building_id'))`,
		`CREATE INDEX IF NOT EXISTS objects_room_idx ON objects(json_extract(data,'$.telecom_room_id'))`,
		`CREATE INDEX IF NOT EXISTS layouts_bounds_idx ON layouts(grouping,x,y)`,
		`CREATE INDEX IF NOT EXISTS interfaces_device_idx ON interfaces(device_id)`,
		`CREATE INDEX IF NOT EXISTS cable_routes_link_idx ON cable_routes(link_id,sequence)`,
	}},
	{version: 2, statements: []string{
		`CREATE INDEX IF NOT EXISTS objects_host_idx ON objects(json_extract(data,'$.host_id'))`,
	}},
	{version: 3, statements: []string{
		`CREATE TABLE IF NOT EXISTS device_types(id TEXT PRIMARY KEY,name TEXT UNIQUE NOT NULL,object_kind TEXT NOT NULL CHECK(object_kind IN ('device','virtual_machine','patch_panel')),visual TEXT NOT NULL DEFAULT 'generic',medium TEXT NOT NULL DEFAULT '',sort_order INTEGER NOT NULL DEFAULT 100,enabled INTEGER NOT NULL DEFAULT 1)`,
		`INSERT OR IGNORE INTO device_types(id,name,object_kind,visual,medium,sort_order) VALUES('router','Router','device','router','',10),('switch','Switch','device','switch','',20),('firewall','Firewall','device','firewall','',30),('access-point','Wireless access point','device','accesspoint','',40),('esxi','ESXi host','device','esxi','',50),('server','Server','device','server','',60),('storage','Storage system','device','storage','',70),('ups','UPS','device','ups','',80),('load-balancer','Load balancer','device','generic','',90),('modem','Modem','device','generic','',100),('gateway','Gateway','device','router','',110),('vm','Virtual machine','virtual_machine','vm','',120),('patch-panel-copper','Patch panel copper','patch_panel','patch_panel_copper','copper',130),('patch-panel-fiber','Patch panel fiber','patch_panel','patch_panel_fiber','fiber',140)`,
	}},
	{version: 4, statements: []string{
		`INSERT OR IGNORE INTO device_types(id,name,object_kind,visual,medium,sort_order) VALUES('cloud','Cloud','device','cloud','',115)`,
	}},
	{version: 5, statements: []string{
		`INSERT OR IGNORE INTO device_types(id,name,object_kind,visual,medium,sort_order) VALUES('patch-panel','Patch panel','patch_panel','generic','',125)`,
	}},
	{version: 6, statements: []string{
		`CREATE TABLE links_v6(id TEXT PRIMARY KEY, source_interface TEXT NOT NULL, target_interface TEXT NOT NULL, data TEXT NOT NULL)`,
		`INSERT INTO links_v6 SELECT id,source_interface,target_interface,data FROM links`,
		`DROP TABLE links`,
		`ALTER TABLE links_v6 RENAME TO links`,
		`CREATE INDEX links_source_idx ON links(source_interface)`,
		`CREATE INDEX links_target_idx ON links(target_interface)`,
	}},
	{version: 7, statements: []string{
		`INSERT OR IGNORE INTO device_types(id,name,object_kind,visual,medium,sort_order) VALUES('layer3-switch','Layer 3 switch','device','layer3-switch','',200),('switch-stack','Switch stack','device','switch-stack','',210),('nexus-1000','Cisco Nexus 1000','device','nexus-1000','',220),('nexus-2000','Cisco Nexus 2000','device','nexus-2000','',230),('nexus-5000','Cisco Nexus 5000','device','nexus-5000','',240),('nexus-7000','Cisco Nexus 7000','device','nexus-7000','',250),('atm-switch','ATM switch','device','atm-switch','',260),('multiservice-switch','Multiservice switch','device','multiservice-switch','',270),('virtual-switch','Virtual layer switch','device','virtual-switch','',280),('remote-switch','Remote access switch','device','remote-switch','',290),('programmable-switch','Programmable switch','device','programmable-switch','',300),('asr-1000','Cisco ASR 1000','device','asr-1000','',310),('carrier-router','Carrier routing system','device','carrier-router','',320),('service-router','Service router','device','service-router','',330),('mobile-router','Mobile access router','device','mobile-router','',340),('broadband-router','Broadband router','device','broadband-router','',350),('wireless-router','Wireless router','device','wireless-router','',360),('optical-router','Optical services router','device','optical-router','',370),('voice-router','Voice router','device','voice-router','',380),('storage-router','Storage router','device','storage-router','',390),('netflow-router','NetFlow router','device','netflow-router','',400),('vpn-gateway','VPN gateway','device','vpn-gateway','',410),('vpn-concentrator','VPN concentrator','device','vpn-concentrator','',420),('asa-5500','Cisco ASA 5500','device','asa-5500','',430),('pix-firewall','PIX firewall','device','pix-firewall','',440),('router-firewall','Router with firewall','device','router-firewall','',450),('ios-firewall','IOS firewall','device','ios-firewall','',460),('firewall-module','Firewall service module','device','firewall-module','',470),('nac-appliance','NAC appliance','device','nac-appliance','',480),('ids-sensor','Intrusion detection sensor','device','ids-sensor','',490),('security-manager','Security event manager','device','security-manager','',500),('security-guard','Security guard appliance','device','security-guard','',510),('acs-server','Access control server','device','acs-server','',520),('ssl-terminator','SSL terminator','device','ssl-terminator','',530),('application-control','Application control engine','device','application-control','',540),('content-switch','Content switch','device','content-switch','',550),('local-director','LocalDirector load balancer','device','local-director','',560),('wireless-controller','Wireless LAN controller','device','wireless-controller','',570),('lightweight-ap','Lightweight access point','device','lightweight-ap','',580),('mesh-ap','Mesh access point','device','mesh-ap','',590),('dual-mode-ap','Dual mode access point','device','dual-mode-ap','',600),('wireless-bridge','Wireless bridge','device','wireless-bridge','',610),('wireless-location','Wireless location appliance','device','wireless-location','',620),('antenna','Antenna','device','antenna','',630),('radio-tower','Radio tower','device','radio-tower','',640),('satellite-dish','Satellite dish','device','satellite-dish','',650),('cable-modem','Cable modem','device','cable-modem','',660),('rf-modem','RF modem','device','rf-modem','',670),('dsl-access','DSL access multiplexer','device','dsl-access','',680),('csu-dsu','CSU / DSU','device','csu-dsu','',690),('hub','Hub','device','hub','',700),('repeater','Repeater','device','repeater','',710),('bridge','Network bridge','device','bridge','',720),('protocol-translator','Protocol translator','device','protocol-translator','',730),('file-server','File server','device','file-server','',740),('web-server','Web server','device','web-server','',750),('database-server','Database server','device','database-server','',760),('directory-server','Directory server','device','directory-server','',770),('communications-server','Communications server','device','communications-server','',780),('management-server','Network management server','device','management-server','',790),('content-engine','Content cache engine','device','content-engine','',800),('workstation','Workstation','device','workstation','',810),('laptop','Laptop','device','laptop','',820),('pc','Desktop PC','device','pc','',830),('mainframe','Mainframe','device','mainframe','',840),('supercomputer','Supercomputer','device','supercomputer','',850),('printer','Printer','device','printer','',860),('scanner','Scanner','device','scanner','',870),('terminal','Terminal','device','terminal','',880),('san-switch','Fibre Channel switch','device','san-switch','',890),('san-director','Fibre Channel director','device','san-director','',900),('disk-array','Disk array','device','disk-array','',910),('tape-array','Tape array','device','tape-array','',920),('jbod','JBOD storage','device','jbod','',930),('iscsi-router','iSCSI router','device','iscsi-router','',940),('ip-phone','IP phone','device','ip-phone','',950),('phone','Telephone','device','phone','',960),('pbx','PBX','device','pbx','',970),('call-manager','Cisco CallManager','device','call-manager','',980),('voice-gateway','Universal voice gateway','device','voice-gateway','',990),('voice-switch','Voice switch','device','voice-switch','',1000),('sip-proxy','SIP proxy server','device','sip-proxy','',1010),('unity-server','Unity messaging server','device','unity-server','',1020),('video-camera','Video camera','device','video-camera','',1030),('video-conference','Video conference MCU','device','video-conference','',1040),('iptv-server','IPTV server','device','iptv-server','',1050),('television','Television','device','television','',1060),('optical-transport','Optical transport','device','optical-transport','',1070),('optical-amplifier','Optical amplifier','device','optical-amplifier','',1080),('wavelength-router','Wavelength router','device','wavelength-router','',1090),('multiplexer','Multiplexer','device','multiplexer','',1100),('metro-optical','Metro optical system','device','metro-optical','',1110)`,
	}},
	{version: 8, statements: []string{
		`CREATE TABLE device_types_v8(id TEXT PRIMARY KEY,name TEXT UNIQUE NOT NULL,object_kind TEXT NOT NULL CHECK(object_kind IN ('device','virtual_machine','virtual_switch','patch_panel')),visual TEXT NOT NULL DEFAULT 'generic',medium TEXT NOT NULL DEFAULT '',sort_order INTEGER NOT NULL DEFAULT 100,enabled INTEGER NOT NULL DEFAULT 1)`,
		`INSERT INTO device_types_v8 SELECT id,name,object_kind,visual,medium,sort_order,enabled FROM device_types`,
		`DROP TABLE device_types`,
		`ALTER TABLE device_types_v8 RENAME TO device_types`,
		`INSERT OR IGNORE INTO device_types(id,name,object_kind,visual,sort_order) VALUES('hypervisor','Hypervisor','device','hypervisor',51),('vmware','VMware vSphere','device','hypervisor',52),('hyper-v','Microsoft Hyper-V','device','hypervisor',53),('proxmox','Proxmox VE','device','hypervisor',54),('kvm','KVM / QEMU','device','hypervisor',55),('xen','Xen','device','hypervisor',56),('xcp-ng','XCP-ng','device','hypervisor',57),('nutanix-ahv','Nutanix AHV','device','hypervisor',58),('ovirt','oVirt','device','hypervisor',59),('bhyve','bhyve','device','hypervisor',60),('vswitch','vSwitch','virtual_switch','vswitch',121)`,
	}},
	{version: 9, statements: []string{
		`CREATE INDEX IF NOT EXISTS objects_rack_idx ON objects(json_extract(data,'$.rack_id'))`,
	}},
	{version: 10, statements: []string{
		`CREATE TABLE IF NOT EXISTS connection_types(id TEXT PRIMARY KEY,name TEXT UNIQUE NOT NULL,connection_class TEXT NOT NULL CHECK(connection_class IN ('physical','virtual','logical_tunnel','provider_service')),color TEXT NOT NULL DEFAULT '',sort_order INTEGER NOT NULL DEFAULT 100,enabled INTEGER NOT NULL DEFAULT 1)`,
		`CREATE INDEX IF NOT EXISTS connection_types_class_idx ON connection_types(connection_class,sort_order)`,
	}},
	{version: 11, statements: []string{
		`INSERT OR IGNORE INTO device_types(id,name,object_kind,visual,medium,sort_order) VALUES('inline-encryptor','Inline encryptor','device','red-black','',1120),('serial-encryptor','Serial encryptor','device','red-black','',1130),('airgap','Airgap','device','low-high','',1140),('media-converter','Media converter','device','bridge','',1150),('container','Container','device','container','',1160),('pod','Pod','device','pod','',1170),('docker-host','Docker host','device','server','',1180),('podman-host','Podman host','device','server','',1190),('kubernetes-node','Kubernetes node','device','server','',1200),('cloud-provider','Cloud provider','device','cloud','',1210),('azure','Microsoft Azure','device','cloud','',1220),('aws','Amazon Web Services','device','cloud','',1230),('google-cloud','Google Cloud','device','cloud','',1240),('oracle-cloud','Oracle Cloud','device','cloud','',1250),('saas','SaaS','device','saas','',1260),('paas','PaaS','device','paas','',1270),('iaas','IaaS','device','iaas','',1280),('faas','FaaS','device','faas','',1290)`,
	}},
	{version: 12, statements: []string{
		`CREATE TABLE IF NOT EXISTS networks(id TEXT PRIMARY KEY,name TEXT UNIQUE NOT NULL,description TEXT NOT NULL DEFAULT '',sort_order INTEGER NOT NULL DEFAULT 100,enabled INTEGER NOT NULL DEFAULT 1)`,
		`CREATE INDEX IF NOT EXISTS networks_sort_idx ON networks(sort_order)`,
		`INSERT OR IGNORE INTO networks(id,name,sort_order,enabled) SELECT DISTINCT replace(lower(trim(value)),' ','-'),trim(value),100,1 FROM (SELECT je.value AS value FROM objects,json_each(objects.data,'$.network') AS je WHERE json_valid(objects.data) AND json_type(objects.data,'$.network')='array' UNION SELECT json_extract(objects.data,'$.network') FROM objects WHERE json_valid(objects.data) AND json_type(objects.data,'$.network')='text') WHERE trim(value)<>''`,
	}},
	{version: 13, statements: []string{
		`CREATE TABLE geography(owner_type TEXT NOT NULL CHECK(owner_type IN ('object','link')),owner_id TEXT NOT NULL,data TEXT NOT NULL,version INTEGER NOT NULL,building_id TEXT NOT NULL DEFAULT '',floor_id TEXT NOT NULL DEFAULT '',parent_id TEXT NOT NULL DEFAULT '',min_x REAL NOT NULL,min_y REAL NOT NULL,max_x REAL NOT NULL,max_y REAL NOT NULL,PRIMARY KEY(owner_type,owner_id))`,
		`CREATE INDEX geography_bounds_idx ON geography(building_id,min_x,max_x,min_y,max_y)`,
		`CREATE INDEX geography_floor_idx ON geography(building_id,floor_id,owner_id)`,
		`CREATE INDEX geography_parent_idx ON geography(parent_id)`,
	}},
	{version: 14, statements: []string{
		`CREATE TABLE relationships(id TEXT PRIMARY KEY,source_id TEXT NOT NULL,target_id TEXT NOT NULL,relationship_type TEXT NOT NULL,version INTEGER NOT NULL DEFAULT 1,label TEXT NOT NULL DEFAULT '')`,
		`CREATE INDEX relationships_source_idx ON relationships(source_id,relationship_type,target_id)`,
		`CREATE INDEX relationships_target_idx ON relationships(target_id,relationship_type,source_id)`,
		`CREATE INDEX objects_floor_idx ON objects(json_extract(data,'$.floor_id'))`,
		`CREATE INDEX objects_ordinary_room_idx ON objects(json_extract(data,'$.room_id'))`,
		`CREATE INDEX objects_service_category_idx ON objects(kind,json_extract(data,'$.service_category'))`,
		`CREATE INDEX objects_conduit_a_idx ON objects(json_extract(data,'$.endpoint_a_id'))`,
		`CREATE INDEX objects_conduit_b_idx ON objects(json_extract(data,'$.endpoint_b_id'))`,
		`CREATE INDEX cable_routes_infrastructure_idx ON cable_routes(infrastructure_id,link_id)`,
		`INSERT OR IGNORE INTO objects(id,kind,name,data,x,y) SELECT 'ground:'||id,'floor','Ground',json_object('id','ground:'||id,'kind','floor','name','Ground','building_id',id,'site_id',json_extract(data,'$.site_id'),'sort_order',0,'auto_ground',json('true')),0,0 FROM objects WHERE kind='building'`,
		`UPDATE objects SET data=json_set(data,'$.floor_id',(SELECT 'ground:'||b.id FROM objects b WHERE b.kind='building' AND (b.id=json_extract(objects.data,'$.building_id') OR b.name=json_extract(objects.data,'$.building_id')) ORDER BY CASE WHEN b.id=json_extract(objects.data,'$.building_id') THEN 0 ELSE 1 END LIMIT 1)) WHERE kind IN ('telecom_room','rack','device','patch_panel') AND json_extract(data,'$.floor_id') IS NULL AND EXISTS(SELECT 1 FROM objects b WHERE b.kind='building' AND (b.id=json_extract(objects.data,'$.building_id') OR b.name=json_extract(objects.data,'$.building_id')))`,
	}},
	{version: 15, statements: []string{`CREATE INDEX audit_action_object_idx ON audit(action,object_id,id)`}},
}

func migrateSchema(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations(version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("create schema migration ledger: %w", err)
	}
	var current int
	if err := db.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("read current schema version: %w", err)
	}
	if len(schemaMigrations) > 0 && current > schemaMigrations[len(schemaMigrations)-1].version {
		return fmt.Errorf("database schema version %d is newer than this Mapper supports", current)
	}
	for _, migration := range schemaMigrations {
		var applied int
		err := db.QueryRow(`SELECT version FROM schema_migrations WHERE version=?`, migration.version).Scan(&applied)
		if err == nil {
			continue
		}
		if err != sql.ErrNoRows {
			return fmt.Errorf("read schema migration %d: %w", migration.version, err)
		}
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin schema migration %d: %w", migration.version, err)
		}
		for _, statement := range migration.statements {
			if _, err := tx.Exec(statement); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("schema migration %d: %w", migration.version, err)
			}
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations(version,applied_at) VALUES(?,?)`, migration.version, time.Now().Unix()); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record schema migration %d: %w", migration.version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit schema migration %d: %w", migration.version, err)
		}
	}
	return nil
}
