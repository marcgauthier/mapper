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
    execFileSync("go", ["build", "-buildvcs=false", "-o", binary, "./cmd/mapper"], {
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
    await page.goto(base + "/login", { waitUntil: "networkidle0" });
    await page.waitForSelector("#passwordForm:not([hidden])");
    const serverInfo = await page.evaluate(
      async () => (await fetch("/api/server-info")).json(),
    );
    assert.equal(serverInfo.mtls_enabled, false);
    assert(await page.$eval(".login-banner", (e) => e.getAttribute("src")));
    await page.click("#certButton");
    await page.waitForSelector("#certView:not([hidden]) #certBack:not([hidden])");
    console.log("login page without mTLS");
    await page.evaluate(async () => {
      const r = await fetch("/api/login", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          username: "admin",
          password: "topology-test-password",
        }),
      });
      if (!r.ok) throw Error(await r.text());
    });
    await page.goto(base + "/", { waitUntil: "networkidle0" });
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
    const openMenu = async () => {
      await page.evaluate(() => {
        if (document.querySelector("#menuPanel").hidden)
          document.querySelector("#menuButton").click();
      });
      await page.waitForSelector("#menuPanel:not([hidden])");
    };
    const create = async (kind, name, values = {}) => {
      await openMenu();
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
    // Palette drag-and-drop onto the canvas.
    await page.waitForSelector(
      "#paletteBar:not([hidden]) .paletteBtn[data-kind='site']",
    );
    assert(await page.$("#paletteBar .paletteBtn[data-kind='service']"));
    const dropTarget = await page.evaluate(() => {
      const rect = cy.container().getBoundingClientRect(),
        zoom = cy.zoom(),
        pan = cy.pan();
      const cx = rect.left + 400,
        cyy = rect.top + 300;
      return {
        cx,
        cy: cyy,
        x: (cx - rect.left - pan.x) / zoom,
        y: (cyy - rect.top - pan.y) / zoom,
      };
    });
    await page.evaluate(
      ({ cx, cy: cyy }) => {
        const dt = new DataTransfer();
        dt.setData("application/x-mapper-kind", "site");
        document
          .querySelector("#cy")
          .dispatchEvent(
            new DragEvent("drop", {
              dataTransfer: dt,
              clientX: cx,
              clientY: cyy,
              bubbles: true,
              cancelable: true,
            }),
          );
      },
      { cx: dropTarget.cx, cy: dropTarget.cy },
    );
    await page.waitForSelector("#scopeDialog[open]");
    assert(
      (await page.$eval("#scopeDialog", (e) => e.textContent)).includes(
        "Add site",
      ),
    );
    await page.type("#scopeField_name", "Palette Site");
    await page.select("#scopeField_role", "branch_office");
    await page.click("#scopeDialog button.primary");
    await page.waitForFunction(
      () => !document.querySelector("#scopeDialog").open,
    );
    await page.waitForFunction(() => topologyWorkspace.scope.kind === "global");
    const paletteSite = (await api("/api/objects?kind=site")).find(
      (o) => o.name === "Palette Site",
    );
    assert(paletteSite);
    assert(
      Math.abs(paletteSite.x - dropTarget.x) < 1 &&
        Math.abs(paletteSite.y - dropTarget.y) < 1,
    );
    await api("/api/objects?id=" + paletteSite.id, "DELETE");
    await page.evaluate(() => topologyWorkspace.navigate("global"));
    console.log("palette drag-and-drop");
    await create("site", "Campus", { role: "major_site" });
    await page.waitForFunction(() => topologyWorkspace.scope.kind === "site");
    let site = (await api("/api/objects?kind=site"))[0];
    assert.equal(site.data.site_role, "major_site");
    assert.deepEqual(
      await page.evaluate(() =>
        [
          "data_center",
          "major_site",
          "branch_office",
          "edge_site",
          "disaster_recovery",
          "cloud_colo",
        ].map((role) =>
          itemIcons.item({ kind: "site", data: { site_role: role } }),
        ),
      ),
      [
        "/img/mapper/server.svg",
        "/img/mapper/campus.svg",
        "/img/site-branch.svg",
        "/img/site-edge.svg",
        "/img/site-dr.svg",
        "/img/mapper/cloud.svg",
      ],
    );
    assert.equal(
      await page.evaluate(() => itemIcons.item({ kind: "site", data: {} })),
      "/img/mapper/globe.svg",
    );
    await page.evaluate(
      (o) => topologyWorkspace.inspect(o),
      (await api("/api/objects?ids=" + site.id))[0],
    );
    assert(
      (await page.$eval("#inspector", (e) => e.textContent)).includes(
        "Major Users Site",
      ),
    );
    console.log("site role form, icons and inspector label");
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
    // Right-click an edge to edit it.
    const edgeA = crypto.randomUUID(), edgeB = crypto.randomUUID();
    for (const [id, name, x] of [
      [edgeA, "Edge A", 0],
      [edgeB, "Edge B", 400],
    ]) {
      await api("/api/objects", "POST", {
        id,
        kind: "device",
        name,
        x,
        y: 0,
        telecom_room_id: telecom.id,
        device_type_id: "switch",
      });
    }
    const portA = crypto.randomUUID(), portB = crypto.randomUUID();
    await api("/api/interfaces", "POST", {
      id: portA,
      device_id: edgeA,
      name: "eth0",
      data: { speed: "1G" },
    });
    await api("/api/interfaces", "POST", {
      id: portB,
      device_id: edgeB,
      name: "eth0",
      data: { speed: "1G" },
    });
    await api("/api/links", "POST", {
      source_interface: portA,
      target_interface: portB,
      data: {
        connection_class: "physical",
        physical_type: "fiber",
        cable_type: "OM4",
        connector_a: "LC",
        connector_b: "LC",
      },
    });
    const edgeLink = (await api("/api/links")).find(
      (l) => l.source_interface === portA,
    );
    assert(edgeLink);
    await page.evaluate(() => topologyWorkspace.refresh());
    await page.waitForFunction(
      ([a, b]) =>
        cy.edges().some((e) => {
          const ends = [
            e.source().id(),
            e.source().data("object_id"),
            e.target().id(),
            e.target().data("object_id"),
          ];
          return ends.includes(a) && ends.includes(b);
        }),
      {},
      [edgeA, edgeB],
    );
    const mid = await page.evaluate(([a, b]) => {
      const node = (id) =>
        cy
          .nodes()
          .filter((n) => n.id() === id || n.data("object_id") === id)[0];
      const pa = node(a).position(),
        pb = node(b).position();
      const rect = cy.container().getBoundingClientRect(),
        zoom = cy.zoom(),
        pan = cy.pan();
      return {
        x: rect.left + ((pa.x + pb.x) / 2) * zoom + pan.x,
        y: rect.top + ((pa.y + pb.y) / 2) * zoom + pan.y,
      };
    }, [edgeA, edgeB]);
    await page.mouse.click(mid.x, mid.y, { button: "right" });
    await page.waitForSelector("#canvasContextMenu:not([hidden])");
    assert(
      (
        await page.$$eval("#canvasContextMenu button", (bs) =>
          bs.map((b) => b.textContent),
        )
      ).includes("Edit connection"),
    );
    await page.evaluate(
      () =>
        [...document.querySelectorAll("#canvasContextMenu button")]
          .find((b) => b.textContent === "Edit connection")
          .click(),
    );
    await page.waitForSelector("#scopeDialog[open]");
    assert(
      (await page.$eval("#scopeDialog h2", (e) => e.textContent)).includes(
        "Edit connection",
      ),
    );
    await page.waitForFunction(
      () =>
        document.querySelector("#scopeField_source_port").options.length > 1 &&
        document.querySelector("#scopeField_target_port").options.length > 1,
    );
    await page.$eval("#scopeField_label", (e) => {
      e.value = "Edited via edge";
    });
    await page.click("#scopeDialog button.primary");
    await page.waitForFunction(
      () => !document.querySelector("#scopeDialog").open,
    );
    const relabeled = (await api("/api/links")).find(
      (l) => l.id === edgeLink.id,
    );
    assert.equal(JSON.parse(relabeled.data).label, "Edited via edge");
    await api("/api/links?id=" + edgeLink.id, "DELETE");
    await api("/api/objects?id=" + edgeA, "DELETE");
    await api("/api/objects?id=" + edgeB, "DELETE");
    console.log("edge right-click edit");
    // Physics layout spreads stacked items.
    const physIds = [crypto.randomUUID(), crypto.randomUUID(), crypto.randomUUID()];
    for (const [i, id] of physIds.entries()) {
      await api("/api/objects", "POST", {
        id,
        kind: "device",
        name: "Phys " + i,
        x: i * 5,
        y: 0,
        telecom_room_id: telecom.id,
        device_type_id: "switch",
      });
    }
    const physPorts = [];
    for (const [device, name] of [
      [physIds[0], "eth0"],
      [physIds[1], "eth0"],
      [physIds[1], "eth1"],
      [physIds[2], "eth0"],
    ]) {
      const port = crypto.randomUUID();
      physPorts.push(port);
      await api("/api/interfaces", "POST", {
        id: port,
        device_id: device,
        name,
        data: { speed: "1G" },
      });
    }
    for (const [a, b] of [
      [0, 1],
      [2, 3],
    ]) {
      await api("/api/links", "POST", {
        source_interface: physPorts[a],
        target_interface: physPorts[b],
        data: {
          connection_class: "physical",
          physical_type: "fiber",
          cable_type: "OM4",
          connector_a: "LC",
          connector_b: "LC",
        },
      });
    }
    const physLinks = [];
    for (const port of physPorts) {
      const link = (await api("/api/links")).find(
        (l) => l.source_interface === port,
      );
      if (link) physLinks.push(link.id);
    }
    await page.evaluate(() => topologyWorkspace.refresh());
    await page.waitForFunction(
      (ids) =>
        ids.every((id) =>
          cy.nodes().some((n) => n.id() === id || n.data("object_id") === id),
        ),
      {},
      physIds,
    );
    const physX = (ids) =>
      page.evaluate(
        (nodes) =>
          nodes.map(
            (id) =>
              cy
                .nodes()
                .filter((n) => n.id() === id || n.data("object_id") === id)[0]
                .position("x"),
          ),
        ids,
      );
    const beforePhys = await physX(physIds);
    await openMenu();
    await page.click("#physicsLayout");
    await page.waitForFunction(
      () =>
        document.querySelector("#scopeNotice").textContent ===
        "Physics layout applied.",
      { timeout: 60000 },
    );
    const afterPhys = await physX(physIds);
    assert(
      afterPhys.some((x, i) => Math.abs(x - beforePhys[i]) > 5),
      "physics layout did not move nodes",
    );
    for (const id of physLinks) await api("/api/links?id=" + id, "DELETE");
    for (const id of physIds) await api("/api/objects?id=" + id, "DELETE");
    console.log("physics layout spreads items");
    // Double-click opens containers and plain items.
    const dblclickNode = async (id) => {
      const pos = await page.evaluate((nodeId) => {
        const n = cy
          .nodes()
          .filter((x) => x.id() === nodeId || x.data("object_id") === nodeId)[0];
        cy.center(n);
        const p = n.renderedPosition(),
          r = cy.container().getBoundingClientRect();
        return { x: r.left + p.x, y: r.top + p.y };
      }, id);
      await page.mouse.click(pos.x, pos.y);
      await wait(120);
      await page.mouse.click(pos.x, pos.y);
    };
    // Navigation re-renders the canvas a few times; a double-click racing a
    // rebuild loses tap pairing, so retry with fresh positions like a user.
    const dblclickUntil = async (id, check) => {
      for (let i = 0; i < 4; i++) {
        if (await check()) return;
        await dblclickNode(id);
        await wait(900);
      }
      assert(await check(), "double-click had no effect");
    };
    await page.evaluate(() => topologyWorkspace.navigate("global"));
    await page.waitForFunction(
      (id) =>
        topologyWorkspace.scope.kind === "global" &&
        [...topologyWorkspace.nodes.keys()].includes(id),
      {},
      site.id,
    );
    const siteScope = (id) =>
      page.evaluate(
        (want) =>
          topologyWorkspace.scope.kind === "site" &&
          topologyWorkspace.scope.id === want,
        id,
      );
    await dblclickUntil(site.id, () => siteScope(site.id));
    const dblId = crypto.randomUUID();
    await api("/api/objects", "POST", {
      id: dblId,
      kind: "device",
      name: "DblClick",
      x: 600,
      y: 0,
      telecom_room_id: telecom.id,
      device_type_id: "switch",
    });
    await page.evaluate(
      (id) => topologyWorkspace.navigate("telecom_room", id),
      telecom.id,
    );
    await page.waitForFunction(
      () => topologyWorkspace.scope.kind === "telecom_room",
    );
    await page.waitForFunction(
      (id) =>
        cy
          .nodes()
          .some((n) => n.id() === id || n.data("object_id") === id),
      {},
      dblId,
    );
    const dialogOpen = () =>
      page.evaluate(() => !!document.querySelector("#scopeDialog")?.open);
    await dblclickUntil(dblId, dialogOpen);
    assert(
      (await page.$eval("#scopeDialog h2", (e) => e.textContent)).includes(
        "Edit device",
      ),
    );
    await page.evaluate(
      () =>
        [...document.querySelectorAll("#scopeDialog button")]
          .find((b) => b.textContent === "Cancel")
          .click(),
    );
    await page.waitForFunction(
      () => !document.querySelector("#scopeDialog").open,
    );
    await api("/api/objects?id=" + dblId, "DELETE");
    console.log("double-click opens items");
    // Find-node search overlay.
    await page.waitForSelector("#nodeSearchInput");
    await page.click("#nodeSearchInput");
    await page.type("#nodeSearchInput", "Gate");
    await page.waitForSelector("#nodeSearchResults:not([hidden]) button");
    assert(
      (
        await page.$$eval("#nodeSearchResults button", (bs) =>
          bs.map((b) => b.textContent),
        )
      ).some((t) => t.includes("Gateway")),
    );
    await page.keyboard.press("Enter");
    await page.waitForFunction(
      () => cy.nodes(".highlighted").length === 1,
    );
    const hl = await page.evaluate(() => {
      const el = cy.nodes(".highlighted")[0];
      const o = topologyWorkspace.nodes.get(el.id());
      const bb = el.renderedBoundingBox(),
        r = cy.container().getBoundingClientRect();
      return {
        name: o?.name,
        dx: Math.abs((bb.x1 + bb.x2) / 2 - r.width / 2),
        dy: Math.abs((bb.y1 + bb.y2) / 2 - r.height / 2),
      };
    });
    assert.equal(hl.name, "Gateway");
    assert(hl.dx < 2 && hl.dy < 2);
    assert(
      (await page.$eval("#inspectorContent", (e) => e.textContent)).includes(
        "Gateway",
      ),
    );
    await page.keyboard.press("Escape");
    assert.equal(await page.$eval("#nodeSearchInput", (e) => e.value), "");
    assert.equal(
      await page.evaluate(() => cy.nodes(".highlighted").length),
      0,
    );
    console.log("find-node search overlay");
    const clickInspector = async (text) => {
      // The inspector can refresh after a committed event; locate and click
      // within one browser task so replacement cannot race the test.
      await page.waitForFunction((text) => {
        const button = [...document.querySelectorAll("#inspector button")]
          .find((b) => b.textContent === text);
        if (!button) return false;
        button.click();
        return true;
      }, {}, text);
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
    const nodePoint = async (id) =>
      page.evaluate((nodeId) => {
        const n = cy
          .nodes()
          .filter((x) => x.id() === nodeId || x.data("object_id") === nodeId)[0];
        cy.center(n);
        const p = n.renderedPosition(),
          r = cy.container().getBoundingClientRect();
        return { x: r.left + p.x, y: r.top + p.y };
      }, id);
    const contextLabels = () =>
      page.$$eval("#canvasContextMenu button", (bs) =>
        bs.map((b) => b.textContent),
      );
    // Canvas rebuilds can swallow one gesture like a double-click, so retry
    // with a fresh position until the expected menu entry shows up.
    const openContextMenu = async (id, expectLabel = null) => {
      for (let i = 0; i < 4; i++) {
        try {
          const pt = await nodePoint(id);
          await page.mouse.click(pt.x, pt.y, { button: "right" });
          await page.waitForSelector("#canvasContextMenu:not([hidden])", {
            timeout: 2000,
          });
          if (!expectLabel || (await contextLabels()).includes(expectLabel))
            return;
        } catch {
          /* retry with a fresh position */
        }
        await page.evaluate(
          () => (document.querySelector("#canvasContextMenu").hidden = true),
        );
      }
      assert(false, "right-click never opened the context menu");
    };
    const clickContext = async (text) => {
      await page.evaluate((label) => {
        [...document.querySelectorAll("#canvasContextMenu button")]
          .find((b) => b.textContent === label)
          .click();
      }, text);
    };
    const editDeviceViaContext = async (id) => {
      const target = (await api("/api/objects?ids=" + id))[0];
      await page.evaluate(
        (room) => topologyWorkspace.navigate("telecom_room", room),
        telecom.id,
      );
      await page.waitForFunction(
        (nodeId) =>
          cy
            .nodes()
            .some((n) => n.id() === nodeId || n.data("object_id") === nodeId),
        {},
        id,
      );
      await openContextMenu(id, "Edit " + target.name);
      await clickContext("Edit " + target.name);
    };
    // The selection inspector shows details only; Open/Edit/Delete/Undo moved
    // to the right-click menu.
    await page.evaluate(
      (room) => topologyWorkspace.navigate("telecom_room", room),
      telecom.id,
    );
    await page.waitForFunction(
      (nodeId) =>
        cy
          .nodes()
          .some((n) => n.id() === nodeId || n.data("object_id") === nodeId),
      {},
      gateway.id,
    );
    await inspectDevice(gateway.id);
    const inspectorLabels = await page.$$eval("#inspector button", (bs) =>
      bs.map((b) => b.textContent),
    );
    assert(
      !inspectorLabels.some(
        (t) =>
          t === "Edit item" ||
          t === "Delete item" ||
          t === "Undo last object edit" ||
          t.startsWith("Open "),
      ),
      "inspector still shows item actions: " + inspectorLabels.join(", "),
    );
    assert(
      (await page.$eval("#inspectorContent", (e) => e.textContent)).includes(
        "Gateway",
      ),
    );
    await openContextMenu(gateway.id, "Edit Gateway");
    const movedLabels = await contextLabels();
    for (const label of [
      "Edit Gateway",
      "Delete Gateway",
      "Undo last object edit",
    ])
      assert(movedLabels.includes(label), movedLabels.join(", "));
    await clickContext("Edit Gateway");
    await page.waitForSelector("#scopeDialog[open]");
    assert(
      (await page.$eval("#scopeDialog h2", (e) => e.textContent)).includes(
        "Edit device",
      ),
    );
    await page.evaluate(
      () =>
        [...document.querySelectorAll("#scopeDialog button")]
          .find((b) => b.textContent === "Cancel")
          .click(),
    );
    await page.waitForFunction(
      () => !document.querySelector("#scopeDialog").open,
    );
    // Edit, undo, then delete a scratch device entirely from the menu.
    const ctxId = crypto.randomUUID();
    await api("/api/objects", "POST", {
      id: ctxId,
      kind: "device",
      name: "CtxScratch",
      x: 900,
      y: 0,
      telecom_room_id: telecom.id,
      device_type_id: "switch",
    });
    await page.evaluate(() => topologyWorkspace.refresh());
    await page.waitForFunction(
      (nodeId) =>
        cy
          .nodes()
          .some((n) => n.id() === nodeId || n.data("object_id") === nodeId),
      {},
      ctxId,
    );
    await openContextMenu(ctxId, "Edit CtxScratch");
    await clickContext("Edit CtxScratch");
    await page.waitForSelector("#scopeField_name");
    await page.$eval("#scopeField_name", (e) => {
      e.value = "CtxScratchRenamed";
    });
    await saveScope();
    assert.equal(
      (await api("/api/objects?ids=" + ctxId))[0].name,
      "CtxScratchRenamed",
    );
    await openContextMenu(ctxId, "Undo last object edit");
    await clickContext("Undo last object edit");
    await page.waitForFunction(
      async (nodeId) => {
        const rows = await (await fetch("/api/objects?ids=" + nodeId)).json();
        return rows[0]?.name === "CtxScratch";
      },
      {},
      ctxId,
    );
    await page.waitForFunction(
      (nodeId) => topologyWorkspace.nodes.get(nodeId)?.name === "CtxScratch",
      {},
      ctxId,
    );
    await openContextMenu(ctxId, "Delete CtxScratch");
    page.once("dialog", (dialog) => dialog.accept());
    await clickContext("Delete CtxScratch");
    await page.waitForFunction(
      async (nodeId) => {
        const rows = await (await fetch("/api/objects?ids=" + nodeId)).json();
        return rows.length === 0;
      },
      {},
      ctxId,
    );
    console.log("read-only inspector with context-menu edit, undo and delete");
    await inspectDevice(gateway.id);
    await editDeviceViaContext(gateway.id);
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
    await editDeviceViaContext(router.id);
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
    await editDeviceViaContext(router.id);
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
    await page.evaluate(() => topologyWorkspace.navigate("global"));
    await create("service", "PLS WAN", { category: "pls" });
    const pls = (await api("/api/objects?kind=service")).find(
      (o) => o.name === "PLS WAN",
    );
    assert(pls);
    await page.evaluate(
      (id) => topologyWorkspace.navigate("site", id),
      site.id,
    );
    await page.evaluate((id) => addInterface(id), gateway.id);
    await page.waitForSelector("#interfaceDialog[open]");
    await page.type("#interfaceName", "PLS uplink");
    await page.type("#interfaceSpeed", "1Gbps");
    if (await page.$("#standaloneInterfaceVRFs_vrfs"))
      await page.select("#standaloneInterfaceVRFs_vrfs", "Management");
    await page.click("#interfaceDialog button.primary");
    await page.waitForFunction(
      () => !document.querySelector("#interfaceDialog").open,
    );
    const plsPort = (
      await api("/api/interfaces?device_id=" + gateway.id)
    ).find((p) => p.name === "PLS uplink");
    assert(plsPort);
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
      (id) =>
        [...document.querySelector("#scopeField_port").options].some(
          (o) => o.value === id,
        ),
      {},
      plsPort.id,
    );
    await page.select("#scopeField_port", plsPort.id);
    await page.waitForFunction(
      (id) =>
        [...document.querySelector("#scopeField_service").options].some(
          (o) => o.value === id,
        ),
      {},
      pls.id,
    );
    await page.select("#scopeField_service", pls.id);
    await page.click("#scopeDialog button.primary");
    await page.waitForFunction(
      () => !document.querySelector("#scopeDialog").open,
    );
    await wait(400);
    assert(
      await page.evaluate(() =>
        [...topologyWorkspace.nodes.values()].some(
          (o) => o.name === "PLS WAN",
        ),
      ),
    );
    console.log("PLS gateway attachment and site boundary");
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
    // Multi-network tools use the real diagram and authenticated transport.
    await openMenu();
    await page.type("#scopeVRF", "Management");
    await page.select("#scopeVRFMode", "color");
    await page.waitForFunction(() => cy.nodes().some(n=>n.style("border-width")==="5px"));
    assert((await page.$eval("#vrfLegend",e=>e.textContent)).includes("Management"));
    await page.select("#scopeVRFMode", "filter");
    await page.waitForFunction(() => topologyWorkspace.scene?.nodes?.length && topologyWorkspace.scene.nodes.every(n=>n.vrfs?.includes("Management")));
    // Another authenticated browser commits a VRF; the filtered view refreshes
    // through WebSocket notification without a manual map reload.
    await page.$eval("#scopeVRF",e=>{e.value="Overlay live";e.dispatchEvent(new Event("change"))});
    await page.waitForFunction(()=>topologyWorkspace.scene?.nodes?.length===0);
    const overlayContext=await browser.createBrowserContext();
    const overlayEditor=await overlayContext.newPage();
    await overlayEditor.setCookie(...(await page.cookies()));
    await overlayEditor.goto(base+"/",{waitUntil:"networkidle0"});
    await overlayEditor.evaluate(async id=>{
      const o=(await (await fetch("/api/objects?ids="+id)).json())[0];
      const r=await fetch("/api/objects",{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({...o.data,id:o.id,kind:o.kind,name:o.name,x:o.x,y:o.y,version:o.version,vrfs:[...o.data.vrfs,"Overlay live"]})});
      if(!r.ok)throw Error(await r.text());
    },router.id);
    await page.waitForFunction(()=>topologyWorkspace.scene?.nodes?.some(n=>n.vrfs?.includes("Overlay live")));
    await overlayContext.close();
    await page.$eval("#scopeVRF",e=>{e.value="Management";e.dispatchEvent(new Event("change"))});
    await page.select("#scopeVRFMode", "");
    for (const owner of [router.id,gateway.id]) {
      await api("/api/interfaces","POST",{id:crypto.randomUUID(),device_id:owner,name:"Quality review",data:{speed:"1G",vrfs:["Management"],ip_address:"192.0.2.240"}});
    }
    await openMenu();
    await page.click("#networkQuality");
    await page.waitForSelector("#scopeDialog[open]");
    assert((await page.$eval("#scopeDialog",e=>e.textContent)).includes("Network and VRF data quality"));
    assert((await page.$eval("#scopeDialog",e=>e.textContent)).includes("192.0.2.240"));
    await page.evaluate(()=>[...document.querySelectorAll("#scopeDialog button")].find(b=>b.textContent==="Close").click());
    for (const [network,name,owner] of [["review-red","Review red",router.id],["review-blue","Review blue",gateway.id]]) {
      await api("/api/networks","POST",{id:network,name});
      const o=(await api("/api/objects?ids="+owner))[0];
      await api("/api/objects","POST",{...o.data,id:o.id,kind:o.kind,name:o.name,x:o.x,y:o.y,version:o.version,network:[network]});
    }
    await page.waitForFunction(()=>[...document.querySelector("#scopeNetwork").options].some(o=>o.value==="review-blue"));
    await openMenu();
    await page.click("#compareNetworks");
    await page.select("#scopeField_before_network","review-red");
    await page.select("#scopeField_after_network","review-blue");
    await page.click("#scopeDialog button.primary");
    await page.waitForFunction(()=>document.querySelector("#scopeDialog").textContent.includes("added:") && document.querySelector("#scopeDialog").textContent.includes("removed:"));
    await page.evaluate(()=>[...document.querySelectorAll("#scopeDialog button")].find(b=>b.textContent==="Close").click());
    const snapshotBefore=await api("/api/topology/snapshot");
    const snapshotAfter=structuredClone(snapshotBefore);
    snapshotAfter.entities.find(e=>e.id===router.id).x+=123;
    const beforePath=path.join(temp,"before.json"),afterPath=path.join(temp,"after.json");
    fs.writeFileSync(beforePath,JSON.stringify(snapshotBefore));fs.writeFileSync(afterPath,JSON.stringify(snapshotAfter));
    await openMenu();
    await page.click("#compareNetworks");
    await page.select("#scopeField_mode","snapshots");
    await (await page.$("#scopeField_before_file")).uploadFile(beforePath);
    await (await page.$("#scopeField_after_file")).uploadFile(afterPath);
    await page.click("#scopeDialog button.primary");
    await page.waitForFunction(()=>document.querySelector("#scopeDialog").textContent.includes("moved: 1"));
    await page.evaluate(()=>[...document.querySelectorAll("#scopeDialog button")].find(b=>b.textContent==="Close").click());
    await openMenu();
    await page.click("#traceVRF");
    await page.waitForFunction(id=>[...document.querySelector("#scopeField_source").options].some(o=>o.value===id),{},router.id);
    await page.select("#scopeField_source",router.id);
    await page.select("#scopeField_target",router.id);
    await page.waitForFunction(()=>document.querySelector("#scopeField_source_port").options.length>1 && document.querySelector("#scopeField_target_port").options.length>1);
    routerPorts=await api("/api/interfaces?device_id="+router.id);
    const tracePort=routerPorts.find(p=>JSON.parse(p.data).effective_vrfs.includes("Management"));
    assert(tracePort);
    await page.select("#scopeField_source_port",tracePort.id);
    await page.select("#scopeField_target_port",tracePort.id);
    await page.click("#scopeDialog button.primary");
    await page.waitForFunction(()=>document.querySelector("#scopeDialog").textContent.includes("VRF path found"));
    await page.evaluate(()=>[...document.querySelectorAll("#scopeDialog button")].find(b=>b.textContent==="Close").click());
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
      "PASS: focused navigation, site roles, power inventory, outlets, conduits, cloud hosting, WAN/PLS, interface VRFs, uplink capacity, concurrent editors, VRF overlays, data quality, snapshot comparison, VRF tracing and context-menu item edit/undo/delete",
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
