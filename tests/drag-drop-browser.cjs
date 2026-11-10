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
    path.join(os.tmpdir(), "mapper-drag-drop-"),
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
    page.on("dialog",async dialog=>{errors.push("Unexpected dialog: "+dialog.message());await dialog.dismiss()});
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
    const post = object => api("/api/objects","POST",object);
    for(const object of [
      {id:"source-site",kind:"site",name:"Source campus",site_role:"major_site"},
      {id:"target-site",kind:"site",name:"Target campus",site_role:"branch_office"},
      {id:"source-building",kind:"building",name:"Source building",site_id:"source-site"},
      {id:"target-building",kind:"building",name:"Target building",site_id:"target-site"},
      {id:"source-room",kind:"room",name:"Source room",floor_id:"ground:source-building"},
      {id:"target-room",kind:"room",name:"Target room",floor_id:"ground:target-building"},
      {id:"source-rack",kind:"rack",name:"Source rack",room_id:"source-room"},
      {id:"source-device",kind:"device",name:"Source switch",rack_id:"source-rack",device_type_id:"switch"},
      {id:"target-device",kind:"device",name:"Target switch",room_id:"target-room",device_type_id:"switch"},
      {id:"competitor",kind:"device",name:"Other switch",room_id:"target-room",device_type_id:"switch"},
      {id:"bare-endpoint",kind:"manhole",name:"Bare endpoint",room_id:"target-room"},
    ]) await post(object);
    const navigate=async(kind,id="")=>{
      await page.evaluate(([kind,id])=>topologyWorkspace.navigate(kind,id),[kind,id]);
      await page.waitForFunction(([kind,id])=>topologyWorkspace.scene?.scope_kind===kind && (!id||topologyWorkspace.scene.scope?.id===id),{},[kind,id]);
      await wait(100);
    };
    const point=async id=>{
      await page.waitForFunction(id=>cy.getElementById(id).length>0,{},id);
      return page.evaluate(id=>{
        const node=cy.getElementById(id),r=cy.container().getBoundingClientRect();
        let p=node.renderedPosition();
        if(p.x<15||p.y<15||p.x>r.width-15||p.y>r.height-15) {cy.fit(undefined,70);p=node.renderedPosition()}
        return {x:r.left+p.x,y:r.top+p.y};
      },id);
    };
    const drop=async(kind,id=null)=>{
      const p=id?await point(id):await page.evaluate(()=>{const r=cy.container().getBoundingClientRect();return {x:r.left+r.width-120,y:r.top+r.height-130}});
      await page.evaluate(([kind,p])=>{
        const transfer=new DataTransfer();transfer.setData("application/x-mapper-kind",kind);
        document.querySelector(`.paletteBtn[data-kind="${kind}"]`).dispatchEvent(new DragEvent("dragstart",{bubbles:true,dataTransfer:transfer}));
        document.querySelector("#cy").dispatchEvent(new DragEvent("dragover",{bubbles:true,cancelable:true,dataTransfer:transfer,clientX:p.x,clientY:p.y}));
        document.querySelector("#cy").dispatchEvent(new DragEvent("drop",{bubbles:true,cancelable:true,dataTransfer:transfer,clientX:p.x,clientY:p.y}));
      },[kind,p]);
      return p;
    };
    const click=async id=>{const p=await point(id);await page.mouse.click(p.x,p.y)};
    const enter=async(kind,id)=>{
      const p=await point(id);await page.mouse.click(p.x,p.y);await wait(70);await page.mouse.click(p.x,p.y);
      await page.waitForFunction(([kind,id])=>topologyWorkspace.scene?.scope_kind===kind && topologyWorkspace.scene.scope?.id===id,{},[kind,id]);
      assert.equal(await page.evaluate(()=>document.querySelector("#scopeDialog").open),false);
      await wait(350);
    };
    const cancelForm=async()=>{
      await page.evaluate(()=>{const buttons=[...document.querySelectorAll("#scopeDialog button")];buttons.find(b=>b.textContent==="Cancel").click()});
      await page.waitForFunction(()=>!document.querySelector("#scopeDialog").open);
    };
    const save=async()=>{
      await page.click("#scopeDialog button.primary");
      await page.waitForFunction(()=>!document.querySelector("#scopeDialog").open,{timeout:8000}).catch(async e=>{console.log("FORM",await page.$eval("#scopeDialog",e=>e.textContent));throw e});
    };
    await navigate("global");
    assert(await page.$(".paletteBtn[data-kind=device]"));
    assert(await page.$(".paletteBtn[data-kind=cable]"));
    await drop("room","source-site");
    assert.equal(await page.$eval("#scopeDialog",e=>e.open),false);
    const p=await drop("site");
    await page.waitForSelector("#scopeDialog[open]");
    await page.type("#scopeField_name","Canvas site");await page.select("#scopeField_role","edge_site");await save();
    assert.equal(await page.evaluate(()=>topologyWorkspace.scope.kind),"global");
    const canvasSite=(await api("/api/objects?kind=site")).find(o=>o.name==="Canvas site");
    assert(canvasSite);
    console.log("canvas drops and incompatible containers");

    await navigate("site","source-site");
    await drop("room","source-building");
    await page.waitForSelector("#scopeDialog[open]");
    assert.equal(await page.evaluate(()=>topologyWorkspace.scope.id),"source-building");
    await page.waitForFunction(()=>document.querySelector("#scopeField_parent").value==="ground:source-building");
    await page.type("#scopeField_name","Dropped room");await save();
    assert.equal(await page.evaluate(()=>topologyWorkspace.scope.id),"source-building");
    await navigate("room","source-room");
    await drop("device","source-rack");
    await page.waitForSelector("#scopeDialog[open]");
    assert.equal(await page.evaluate(()=>topologyWorkspace.scope.id),"source-rack");
    await page.waitForFunction(()=>document.querySelector("#scopeField_parent").value==="source-rack");
    await page.type("#scopeField_name","Dropped switch");await page.select("#scopeField_device_type","switch");await save();
    assert.equal(await page.evaluate(()=>topologyWorkspace.scope.id),"source-rack");
    const dropped=(await api("/api/objects?kind=device")).find(o=>o.name==="Dropped switch");assert.equal(dropped.data.rack_id,"source-rack");
    console.log("nested drops open their destination and keep the parent selected");

    const startContainer=async()=>{
      await navigate("global");await drop("cable","source-site");
      await page.waitForFunction(()=>connectionGesture.active);
      assert.equal(await page.evaluate(()=>topologyWorkspace.scope.kind),"global");
      await enter("site","target-site");
      assert(await page.evaluate(()=>connectionGesture.active && cy.nodes("[draft_transient]").some(n=>n.data("draft_container")==="source-site")));
      await enter("building","target-building");await enter("room","target-room");
      assert(await page.evaluate(()=>connectionGesture.active && cy.nodes("[draft_transient]").some(n=>n.data("draft_container")==="source-site")));
      await page.mouse.move(650,450);
      assert.equal(await page.$eval("#connectionPreview",e=>e.hasAttribute("hidden")),false);
    };
    await startContainer();
    assert.equal((await api("/api/objects?kind=outlet")).length,0);
    await click("target-device");await page.waitForSelector("#scopeDialog[open]");
    await page.waitForFunction(()=>document.querySelector("#scopeField_source_port").value==="Data" && !!document.querySelector("#scopeField_target_port").value);
    assert.equal((await api("/api/links")).length,0);
    await cancelForm();
    assert.equal(await page.evaluate(()=>connectionGesture.active),false);
    assert.equal((await api("/api/objects?kind=outlet")).length,0);
    assert.equal((await api("/api/links")).length,0);
    console.log("container DVO and preview survive cross-map navigation; cancellation leaves no inventory");

    await startContainer();
    await click("bare-endpoint");await page.waitForSelector("#scopeDialog[open]");
    await page.waitForFunction(()=>document.querySelector("#scopeField_target_port").value==="__auto__");
    assert.equal((await api("/api/interfaces?device_id=bare-endpoint")).length,0);
    await save();
    assert.equal(await page.evaluate(()=>connectionGesture.active),false);
    const dvos=await api("/api/objects?kind=outlet");assert.equal(dvos.length,1);assert.equal(dvos[0].data.site_id,"source-site");assert.equal(dvos[0].data.building_id,undefined);
    assert.equal((await api("/api/interfaces?device_id="+dvos[0].id)).length,2);
    assert.equal((await api("/api/interfaces?device_id=bare-endpoint"))[0].name,"Interface 1");
    assert.equal((await api("/api/links")).length,1);
    assert.equal(await page.evaluate(()=>cy.nodes("[draft_transient]").length),0);
    console.log("confirmation atomically saves DVO, interfaces, and cable");

    await navigate("rack","source-rack");await drop("cable","source-device");
    await page.waitForFunction(()=>connectionGesture.active);
    // Header navigation must preserve the draft just like double-clicking maps.
    await page.evaluate(()=>document.querySelector("#scopeBack").click());
    await page.waitForFunction(()=>connectionGesture.active && topologyWorkspace.scope.id!=="source-rack");
    await navigate("room","target-room");
    assert(await page.evaluate(()=>cy.nodes("[draft_transient]").some(n=>n.data("draft_container")==="source-rack")));
    await page.evaluate(()=>topologyWorkspace.physicsLayout());
    const layout=await api("/api/layout?grouping=scope:room:target-room");
    assert(layout.length>0);assert(!layout.some(p=>p.id?.startsWith("draft-")||p.object_id?.startsWith("draft-")));
    assert(await page.evaluate(()=>connectionGesture.active));
    await click("target-device");await page.waitForSelector("#scopeDialog[open]");
    await page.waitForFunction(()=>!!document.querySelector("#scopeField_source_port").value && !!document.querySelector("#scopeField_target_port").value);
    const context=await browser.createBrowserContext(),other=await context.newPage();
    await other.setCookie(...await page.cookies());await other.goto(base+"/",{waitUntil:"networkidle0"});
    const competing=await other.evaluate(async()=>{
      const ports=async id=>(await (await fetch("/api/interfaces?device_id="+id)).json())[0].id;
      const id=crypto.randomUUID();
      const r=await fetch("/api/links",{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({id,source_interface:await ports("competitor"),target_interface:await ports("target-device"),data:{connection_class:"physical",physical_type:"copper_twisted_pair",cable_type:"Cat6",connector_a:"RJ45",connector_b:"RJ45"}})});
      if(!r.ok)throw Error(await r.text());return id;
    });
    await page.click("#scopeDialog button.primary");
    await page.waitForFunction(()=>document.querySelector("#scopeError").textContent.includes("peer") || document.querySelector("#scopeError").textContent.includes("connect"));
    assert.equal(await page.$eval("#scopeDialog",e=>e.open),true);
    assert.equal(await page.evaluate(()=>connectionGesture.active),true);
    await api("/api/links?id="+competing,"DELETE");
    await page.evaluate(()=>[...document.querySelectorAll("#scopeDialog button")].find(b=>b.textContent==="Refresh interface choices").click());
    await page.waitForFunction(()=>document.querySelector("#scopeError").textContent.includes("Choices refreshed"));
    await save();await context.close();
    assert.equal((await api("/api/links")).length,2);
    console.log("existing interfaces and competing editor conflicts preserve the draft");

    await navigate("global");await drop("cable","target-site");await page.waitForFunction(()=>connectionGesture.active);
    await page.keyboard.press("Escape");assert.equal(await page.evaluate(()=>connectionGesture.active),false);
    assert.equal((await api("/api/objects?kind=outlet")).length,1);
    await drop("cable","target-site");await page.waitForFunction(()=>connectionGesture.active);
    const q=await point("target-site");await page.mouse.click(q.x,q.y,{button:"right"});
    assert.equal(await page.evaluate(()=>connectionGesture.active),false);
    assert.equal((await api("/api/objects?kind=outlet")).length,1);
    const exported=await page.evaluate(async()=>await(await fetch("/api/export")).text());assert(!exported.includes("draft-source:"));
    await api("/api/users","POST",{username:"reader",password:"reader-test-password",role:"viewer"});
    const beforeViewer=(await api("/api/objects")).length;
    const readerContext=await browser.createBrowserContext(),reader=await readerContext.newPage();
    await reader.goto(base+"/login",{waitUntil:"networkidle0"});
    await reader.evaluate(async()=>{const r=await fetch("/api/login",{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({username:"reader",password:"reader-test-password"})});if(!r.ok)throw Error(await r.text())});
    await reader.goto(base+"/",{waitUntil:"networkidle0"});
    await reader.waitForFunction(()=>window.userRole==="viewer" && topologyWorkspace.scene);
    assert(await reader.$eval("#paletteBar",e=>e.hidden));
    const denied=await reader.evaluate(async()=>{
      const dt=new DataTransfer();dt.setData("application/x-mapper-kind","site");
      document.querySelector("#cy").dispatchEvent(new DragEvent("drop",{bubbles:true,cancelable:true,dataTransfer:dt,clientX:500,clientY:400}));
      const response=await fetch("/api/topology/connect",{method:"POST",headers:{"Content-Type":"application/json"},body:"{}"});
      return {status:response.status,form:document.querySelector("#scopeDialog").open,active:connectionGesture.active};
    });
    assert.deepEqual(denied,{status:403,form:false,active:false});
    assert.equal((await api("/api/objects")).length,beforeViewer);
    await readerContext.close();
    assert.deepEqual(errors,[]);
    console.log("PASS: canvas/nested drops, cross-map link preview, provisional DVOs, automatic interfaces, cancellation, and concurrent editors");
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
