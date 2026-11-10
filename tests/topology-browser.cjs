const assert = require("node:assert/strict"),
  fs = require("node:fs"),
  os = require("node:os"),
  path = require("node:path"),
  net = require("node:net"),
  { spawn, execFileSync } = require("node:child_process"),
  puppeteer = require("puppeteer-core");
const repo = path.resolve(__dirname, ".."),
  wait = (ms) => new Promise((r) => setTimeout(r, ms));
(async () => {
  const temp = fs.mkdtempSync(
    path.join(os.tmpdir(), "mapper-topology-browser-"),
  );
  let server, browser;
  try {
    const binary = path.join(temp, "mapper");
    execFileSync("go", ["build", "-buildvcs=false", "-o", binary, "."], {
      cwd: repo,
    });
    const port = await new Promise((resolve) => {
      const s = net.createServer();
      s.listen(0, "127.0.0.1", () => {
        const p = s.address().port;
        s.close(() => resolve(p));
      });
    });
    server = spawn(binary, [], {
      cwd: repo,
      env: {
        ...process.env,
        MAPPER_ADDR: "127.0.0.1:" + port,
        MAPPER_DB: path.join(temp, "store"),
        MAPPER_LOG_DIR: path.join(temp, "logs"),
      },
    });
    server.stdout.on("data", () => {});
    server.stderr.on("data", () => {});
    await new Promise((resolve, reject) => {
      let n = 0;
      const probe = () => {
        if (++n > 100) return reject(Error("server startup failed"));
        const s = net.connect(port, "127.0.0.1");
        s.on("connect", () => {
          s.destroy();
          resolve();
        });
        s.on("error", () => setTimeout(probe, 50));
      };
      probe();
    });
    browser = await puppeteer.launch({
      executablePath: process.env.MAPPER_BROWSER || "/usr/bin/google-chrome",
      args: ["--no-sandbox", "--disable-dev-shm-usage"],
    });
    const page = await browser.newPage();
    await page.setViewport({ width: 1600, height: 1000 });
    const errors = [];
    page.on("pageerror", (e) => {
      errors.push(e.message);
      console.log("PAGE ERROR", e.message);
    });
    const requests = [];
    page.on("request", (r) => requests.push(r.url()));
    const base = "http://127.0.0.1:" + port;
    await page.goto(base + "/unlock", { waitUntil: "networkidle0" });
    await page.evaluate(async () => {
      const r = await fetch("/unlock", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          key: "topology-test-key",
          username: "admin",
          password: "topology-test-password",
        }),
      });
      if (!r.ok) throw Error(await r.text());
    });
    await page.goto(base + "/");
    await page.evaluate(async () => {
      await fetch("/api/login", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          username: "admin",
          password: "topology-test-password",
        }),
      });
    });
    await page.reload({ waitUntil: "networkidle0" });
    await page.waitForFunction(
      () =>
        typeof topologyWorkspace !== "undefined" &&
        topologyWorkspace.scene?.scope_kind === "global",
    );
    const api = async (path, method = "GET", body) =>
      page.evaluate(
        async (path, method, body) => {
          const r = await fetch(path, {
            method,
            headers: body ? { "Content-Type": "application/json" } : undefined,
            body: body ? JSON.stringify(body) : undefined,
          });
          const text = await r.text();
          if (!r.ok) throw Error(r.status + ": " + text);
          return text ? JSON.parse(text) : null;
        },
        path,
        method,
        body,
      );
    const create = async (kind, name, values = {}) => {
      await page.waitForFunction(
        (kind) =>
          [...document.querySelector("#scopeCreate").options].some(
            (o) => o.value === kind,
          ),
        {},
        kind,
      );
      await page.select("#scopeCreate", kind);
      await page.waitForSelector("#scopeDialog[open]");
      await page.type("#scopeField_name", name);
      for (const [key, value] of Object.entries(values)) {
        const selector = "#scopeField_" + key;
        if (await page.$eval(selector, (e) => e.tagName !== "SELECT")) {
          await page.type(selector, value);
          continue;
        }
        await page.waitForFunction(
          (selector, value) => {
            const e = document.querySelector(selector);
            return e && [...(e.options || [])].some((o) => o.value === value);
          },
          {},
          selector,
          value,
        );
        await page.select(selector, value);
      }
      await page.click("#scopeDialog button.primary");
      await page
        .waitForFunction(() => !document.querySelector("#scopeDialog").open, {
          timeout: 10000,
        })
        .catch(async (e) => {
          console.log(
            "FORM ERROR",
            kind,
            await page.evaluate(() => ({
              error: document.querySelector("#scopeError").textContent,
              invalid: [
                ...document.querySelectorAll("#scopeDialog :invalid"),
              ].map((e) => ({ name: e.name, value: e.value })),
            })),
          );
          throw e;
        });
    };
    await create("site", "Campus");
    await page.waitForFunction(() => topologyWorkspace.scope.kind === "site");
    let site = (await api("/api/objects?kind=site"))[0];
    await create("building", "Building A");
    await page.waitForFunction(
      () => topologyWorkspace.scope.kind === "building",
    );
    let building = (await api("/api/objects?kind=building"))[0];
    assert(
      (await api("/api/objects?kind=floor")).some((f) => f.name === "Ground"),
    );
    await page.evaluate(
      (site) => topologyWorkspace.navigate("site", site),
      site.id,
    );
    await create("generator", "Generator A", { parent: building.id });
    await page.evaluate(() => topologyWorkspace.refresh());
    await wait(400);
    assert(
      await page.evaluate(() =>
        [...topologyWorkspace.nodes.values()].some(
          (o) => o.kind === "generator",
        ),
      ),
    );
    console.log("site generator visible");
    await create("ups", "Building UPS", { parent: building.id });
    await page.evaluate(() => topologyWorkspace.refresh());
    await wait(400);
    assert(
      await page.evaluate(() =>
        [...topologyWorkspace.nodes.values()].some((o) => o.kind === "ups"),
      ),
    );
    console.log("building UPS visible at site");
    await page.evaluate(
      (id) => topologyWorkspace.navigate("building", id),
      building.id,
    );
    await create("floor", "Upper");
    await page.waitForFunction(() => topologyWorkspace.scope.kind === "floor");
    const upper = (await api("/api/objects?kind=floor")).find(
      (o) => o.name === "Upper",
    );
    await create("room", "Office");
    await page.waitForFunction(() => topologyWorkspace.scope.kind === "room");
    const room = (await api("/api/objects?kind=room"))[0];
    await create("ups", "Room UPS");
    await page.evaluate(() => topologyWorkspace.refresh());
    await wait(350);
    assert(
      await page.evaluate(() =>
        [...topologyWorkspace.nodes.values()].some(
          (o) => o.name === "Room UPS",
        ),
      ),
    );
    await create("dvo", "Desk outlet");
    await page.evaluate(() => topologyWorkspace.refresh());
    await wait(350);
    assert(
      await page.evaluate(() =>
        [...topologyWorkspace.nodes.values()].some((o) => o.kind === "outlet"),
      ),
    );
    console.log("floor, room, room UPS and DVO");
    await page.evaluate(
      (id) => topologyWorkspace.navigate("site", id),
      site.id,
    );
    assert.equal(
      await page.evaluate(() =>
        [...topologyWorkspace.nodes.values()].some(
          (o) => o.name === "Room UPS",
        ),
      ),
      false,
    );
    await page.evaluate(
      (id) => topologyWorkspace.navigate("building", id),
      building.id,
    );
    await create("telecom_room", "MDF");
    await page.waitForFunction(
      () => topologyWorkspace.scope.kind === "telecom_room",
    );
    const telecom = (await api("/api/objects?kind=telecom_room"))[0];
    await create("ups", "MDF UPS");
    await create("device", "Gateway", { device_type: "switch" });
    await page.evaluate(() => topologyWorkspace.refresh());
    await wait(350);
    const gateway = (await api("/api/objects?kind=device")).find(
      (o) => o.name === "Gateway",
    );
    assert(gateway);
    const clickInspector = async (text) => {
      await page.waitForFunction(
        (text) =>
          [...document.querySelectorAll("#inspector button")].some(
            (b) => b.textContent === text,
          ),
        {},
        text,
      );
      await page.evaluate(
        (text) =>
          [...document.querySelectorAll("#inspector button")]
            .find((b) => b.textContent === text)
            .click(),
        text,
      );
    };
    const saveScope = async () => {
      const speed = await page.$("#scopeField_speed");
      if (speed && (await speed.evaluate((e) => e.required && !e.value)))
        await speed.type("1Gbps");
      await page.click("#scopeDialog button.primary");
      await page.waitForFunction(
        () => !document.querySelector("#scopeDialog").open,
        { timeout: 10000 },
      );
    };
    const inspectDevice = async (id) => {
      const device = (await api("/api/objects?ids=" + id))[0];
      await page.evaluate((o) => topologyWorkspace.inspect(o), device);
    };
    await inspectDevice(gateway.id);
    await clickInspector("Edit item");
    await page.waitForSelector("#scopeField_device_vrfs");
    await page.type("#scopeField_device_vrfs", "Management\nProduction\nGuest");
    await saveScope();
    await inspectDevice(gateway.id);
    await clickInspector("Edit port");
    await page.waitForSelector("#scopeField_vrfs");
    assert.equal(
      await page.$eval("#scopeField_vrfs", (e) => e.multiple),
      false,
    );
    assert.equal(
      await page.$eval("#scopeField_speed", (e) => e.required),
      true,
    );
    await page.select("#scopeField_vrfs", "Management");
    await saveScope();
    let switchPorts = await api("/api/interfaces?device_id=" + gateway.id);
    assert.deepEqual(JSON.parse(switchPorts[0].data).vrfs, ["Management"]);
    await create("device", "Core router", {
      device_type: "router",
      device_vrfs: "Management\nProduction\nGuest",
    });
    const router = (await api("/api/objects?kind=device")).find(
      (o) => o.name === "Core router",
    );
    let routerPorts = await api("/api/interfaces?device_id=" + router.id);
    assert.equal(JSON.parse(routerPorts[0].data).vrf_mode, "all");
    await inspectDevice(router.id);
    await clickInspector("Edit port");
    await page.waitForSelector("#scopeField_vrf_mode");
    assert.equal(
      await page.$eval("#scopeField_vrf_mode", (e) => e.value),
      "all",
    );
    assert.equal(await page.$eval("#scopeField_vrfs", (e) => e.disabled), true);
    await page.select("#scopeField_vrf_mode", "selected");
    await page.select("#scopeField_vrfs", "Management", "Guest");
    await saveScope();
    await inspectDevice(router.id);
    await clickInspector("Add interface/port");
    await page.waitForSelector("#scopeField_vrf_mode");
    await page.type("#scopeField_name", "All uplink");
    assert.equal(
      await page.$eval("#scopeField_vrf_mode", (e) => e.value),
      "all",
    );
    await saveScope();
    await inspectDevice(router.id);
    await clickInspector("Edit item");
    await page.waitForSelector("#scopeField_device_vrfs");
    await page.$eval("#scopeField_device_vrfs", (e) => {
      e.value += "\nBackup";
    });
    await saveScope();
    routerPorts = await api("/api/interfaces?device_id=" + router.id);
    assert.deepEqual(
      JSON.parse(routerPorts.find((p) => p.name === "All uplink").data)
        .effective_vrfs,
      ["Management", "Production", "Guest", "Backup"],
    );
    assert.deepEqual(
      JSON.parse(routerPorts.find((p) => p.name !== "All uplink").data)
        .effective_vrfs,
      ["Management", "Guest"],
    );
    await page.reload({ waitUntil: "networkidle0" });
    await page.waitForFunction(
      () => typeof topologyWorkspace !== "undefined" && topologyWorkspace.scene,
    );
    await inspectDevice(router.id);
    assert(
      (await page.$eval("#inspector", (e) => e.textContent)).includes(
        "All device VRFs",
      ),
    );
    await page.evaluate((id) => addInterface(id), gateway.id);
    await page.waitForSelector("#interfaceDialog[open]");
    assert.equal(
      await page.$eval("#standaloneInterfaceVRFs_vrfs", (e) => e.multiple),
      false,
    );
    await page.$eval(
      "#interfaceDevice",
      (e, id) => {
        e.value = id;
      },
      router.id,
    );
    await page.type("#interfaceName", "Standalone port");
    await page.type("#interfaceSpeed", "1Gbps");
    await page.click("#interfaceDialog button.primary");
    await page.waitForFunction(
      () => !document.querySelector("#interfaceDialog").open,
    );
    routerPorts = await api("/api/interfaces?device_id=" + router.id);
    assert.equal(
      JSON.parse(routerPorts.find((p) => p.name === "Standalone port").data)
        .vrf_mode,
      "all",
    );
    console.log(
      "switch interface VRF, router subsets, dynamic all-device default and reload",
    );
    const openAllUplink = async () => {
      await inspectDevice(router.id);
      await page.evaluate(() =>
        [...document.querySelectorAll("#inspector .scopeRow")]
          .find((e) => e.textContent.startsWith("All uplink"))
          .querySelector("button")
          .click(),
      );
      await page.waitForSelector("#scopeField_interface_usage");
    };
    const allocate = async (name, rate) => {
      await page.evaluate(
        (name, rate) => {
          const input = [
            ...document.querySelectorAll("#scopeDialog input[data-vrf]"),
          ].find((e) => e.dataset.vrf === name);
          if (!input) throw Error("Missing allocation row " + name);
          input.value = rate;
          input.dispatchEvent(new Event("input", { bubbles: true }));
        },
        name,
        rate,
      );
    };
    await openAllUplink();
    await page.select("#scopeField_interface_usage", "uplink");
    for (const [name, rate] of Object.entries({
      Management: "400Mbps",
      Production: "300Mbps",
      Guest: "200Mbps",
      Backup: "100Mbps",
    }))
      await allocate(name, rate);
    assert(
      (
        await page.$eval(
          "#scopeDialog .interfaceBudgetTotal",
          (e) => e.textContent,
        )
      ).includes("Remaining: 0bps"),
    );
    await allocate("Backup", "101Mbps");
    await page.click("#scopeDialog button.primary");
    await page.waitForFunction(() =>
      document.querySelector("#scopeError").textContent.includes("exceeds"),
    );
    assert.equal(await page.$eval("#scopeDialog", (e) => e.open), true);
    await allocate("Backup", "100Mbps");
    await saveScope();
    routerPorts = await api("/api/interfaces?device_id=" + router.id);
    let uplink = routerPorts.find((p) => p.name === "All uplink");
    assert.equal(JSON.parse(uplink.data).allocated_bps, 1000000000);
    await inspectDevice(router.id);
    await clickInspector("Edit item");
    await page.waitForSelector("#scopeField_device_vrfs");
    await page.$eval("#scopeField_device_vrfs", (e) => {
      e.value += "\nNew VRF";
    });
    await saveScope();
    routerPorts = await api("/api/interfaces?device_id=" + router.id);
    uplink = routerPorts.find((p) => p.name === "All uplink");
    assert(
      JSON.parse(uplink.data).validation_issues.some((s) =>
        s.includes("New VRF"),
      ),
    );
    await openAllUplink();
    await allocate("Backup", "50Mbps");
    await allocate("New VRF", "50Mbps");
    await saveScope();
    await page.evaluate((id) => addInterface(id), router.id);
    await page.waitForSelector("#interfaceDialog[open]");
    await page.type("#interfaceName", "Standalone uplink");
    await page.type("#interfaceSpeed", "1Gbps");
    await page.select("#standaloneInterfaceVRFs_interface_usage", "uplink");
    await page.$$eval("#standaloneInterfaceVRFs input[data-vrf]", (inputs) => {
      for (const input of inputs) {
        input.value = "200Mbps";
        input.dispatchEvent(new Event("input", { bubbles: true }));
      }
    });
    assert(
      (
        await page.$eval(
          "#standaloneInterfaceVRFs .interfaceBudgetTotal",
          (e) => e.textContent,
        )
      ).includes("Remaining: 0bps"),
    );
    await page.click("#interfaceDialog button.primary");
    await page.waitForFunction(
      () => !document.querySelector("#interfaceDialog").open,
    );
    routerPorts = await api("/api/interfaces?device_id=" + router.id);
    assert.equal(
      JSON.parse(routerPorts.find((p) => p.name === "Standalone uplink").data)
        .allocated_bps,
      1000000000,
    );
    const observerContext = await browser.createBrowserContext();
    const observer = await observerContext.newPage();
    await observer.setCookie(...(await page.cookies()));
    await observer.goto(base + "/", { waitUntil: "networkidle0" });
    await observer.waitForFunction(
      () => typeof topologyWorkspace !== "undefined" && topologyWorkspace.scene,
    );
    await observer.evaluate(
      async ({ id, room }) => {
        await topologyWorkspace.navigate("telecom_room", room);
        const rows = await (await fetch("/api/objects?ids=" + id)).json();
        await topologyWorkspace.inspect(rows[0]);
      },
      { id: router.id, room: telecom.id },
    );
    // A second editor changes the port while the first editor's form remains open.
    await openAllUplink();
    routerPorts = await api("/api/interfaces?device_id=" + router.id);
    uplink = routerPorts.find((p) => p.name === "All uplink");
    await api("/api/interfaces", "POST", {
      id: uplink.id,
      device_id: router.id,
      name: uplink.name,
      expected_revision: uplink.revision,
      data: {
        ...JSON.parse(uplink.data),
        description: "Other editor's change",
        vrf_bandwidth_bps: {
          ...JSON.parse(uplink.data).vrf_bandwidth_bps,
          "New VRF": 40000000,
        },
      },
    });
    await observer.waitForFunction(() =>
      document
        .querySelector("#inspector")
        .textContent.includes("Allocated 990Mbps"),
    );
    await observerContext.close();
    await page.click("#scopeDialog button.primary");
    await page.waitForFunction(() =>
      document.querySelector("#scopeError").textContent.includes("changed"),
    );
    assert.equal(
      await page.$eval("#scopeField_speed", (e) => e.value),
      "1Gbps",
    );
    await page.evaluate(() => document.querySelector("#scopeDialog").close());
    console.log(
      "optional uplinks, required speeds, both bandwidth editors, exact capacity, incomplete additions, WebSocket refresh and stale-edit protection",
    );

    await page.evaluate(
      (id) => topologyWorkspace.navigate("site", id),
      site.id,
    );
    await create("building", "Building B");
    const b2 = (await api("/api/objects?kind=building")).find(
      (o) => o.name === "Building B",
    );
    await page.evaluate(
      (id) => topologyWorkspace.navigate("site", id),
      site.id,
    );
    await create("conduit", "Campus duct", {
      endpoint_a: building.id,
      endpoint_b: b2.id,
    });
    await page.waitForFunction(
      () => topologyWorkspace.scope.kind === "conduit",
    );
    const duct = (await api("/api/objects?kind=conduit"))[0];
    assert(duct);
    await page.evaluate(
      (id) => topologyWorkspace.navigate("building", id),
      building.id,
    );
    await create("conduit", "Riser", {
      endpoint_a: telecom.id,
      endpoint_b: room.id,
    });
    console.log("building and cross-floor room conduit creation");
    await page.evaluate(() => topologyWorkspace.navigate("global"));
    await create("service", "AWS account", { category: "cloud_provider" });
    await page.waitForFunction(
      () => topologyWorkspace.scope.kind === "service",
    );
    const aws = (await api("/api/objects?kind=service")).find(
      (o) => o.name === "AWS account",
    );
    await create("service", "Hosted app", { category: "paas" });
    const hosted = (await api("/api/objects?kind=service")).find(
      (o) => o.name === "Hosted app",
    );
    assert(hosted);
    const relations = (await api("/api/relationships")).items;
    assert(
      relations.some(
        (r) =>
          r.source_id === aws.id &&
          r.target_id === hosted.id &&
          r.relationship_type === "hosts",
      ),
    );
    await page.evaluate(
      (id) => topologyWorkspace.navigate("service", id),
      aws.id,
    );
    assert(
      await page.evaluate(() =>
        [...topologyWorkspace.nodes.values()].some(
          (o) => o.name === "Hosted app",
        ),
      ),
    );
    console.log("cloud provider and transactional hosting relation");
    await page.evaluate(() => topologyWorkspace.navigate("global"));
    await create("service", "Internet", { category: "internet" });
    const internet = (await api("/api/objects?kind=service")).find(
      (o) => o.name === "Internet",
    );
    await page.evaluate(
      (id) => topologyWorkspace.navigate("site", id),
      site.id,
    );
    await page.select("#scopeCreate", "attachment");
    await page.waitForSelector("#scopeDialog[open]");
    await page.waitForFunction(
      (id) =>
        [...document.querySelector("#scopeField_gateway").options].some(
          (o) => o.value === id,
        ),
      {},
      gateway.id,
    );
    await page.select("#scopeField_gateway", gateway.id);
    await page.waitForFunction(
      () => document.querySelector("#scopeField_port").value !== "",
    );
    await page.waitForFunction(
      (id) =>
        [...document.querySelector("#scopeField_service").options].some(
          (o) => o.value === id,
        ),
      {},
      internet.id,
    );
    await page.select("#scopeField_service", internet.id);
    await page.click("#scopeDialog button.primary");
    await page.waitForFunction(
      () => !document.querySelector("#scopeDialog").open,
    );
    await wait(400);
    assert(
      await page.evaluate(() =>
        [...topologyWorkspace.nodes.values()].some(
          (o) => o.name === "Internet",
        ),
      ),
    );
    console.log("Internet gateway attachment and site boundary");
    await page.select("#scopeCreate", "relationship");
    await page.waitForSelector("#scopeDialog[open]");
    await page.waitForFunction(
      (id) =>
        [...document.querySelector("#scopeField_target").options].some(
          (o) => o.value === id,
        ),
      {},
      hosted.id,
    );
    await page.select("#scopeField_target", hosted.id);
    await page.click("#scopeDialog button.primary");
    await page.waitForFunction(
      () => !document.querySelector("#scopeDialog").open,
    );
    await page.evaluate(() => topologyWorkspace.navigate("global"));
    await wait(500);
    assert.equal(await page.evaluate(() => typeof maplibregl), "undefined");
    assert(!requests.some((u) => /openfreemap|maplibre/.test(u)));
    assert.deepEqual(errors, []);
    if (process.env.MAPPER_BROWSER_SCREENSHOT) {
      await page.screenshot({ path: process.env.MAPPER_BROWSER_SCREENSHOT });
      await page.evaluate(
        (id) => topologyWorkspace.navigate("site", id),
        site.id,
      );
      await wait(500);
      await page.screenshot({
        path: process.env.MAPPER_BROWSER_SCREENSHOT + ".site.png",
      });
    }
    console.log(
      "PASS: focused navigation, power inventory, outlets, conduits, cloud hosting, WAN, interface VRFs, uplink capacity and concurrent editors",
    );
  } finally {
    await browser?.close();
    if (server) {
      server.kill("SIGTERM");
      await new Promise((resolve) => {
        server.once("exit", resolve);
        setTimeout(() => {
          server.kill("SIGKILL");
          resolve();
        }, 3000).unref();
      });
    }
    fs.rmSync(temp, { recursive: true, force: true });
  }
})().catch((e) => {
  console.error(e);
  process.exit(1);
});
