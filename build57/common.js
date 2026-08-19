const { BrowserWindow, dialog, ipcMain, net, app } = require('electron');
const os = require('os');
const http = require('http');
const dgram = require('dgram');
const fs = require('fs');
const path = require('path');
const crypto = require('crypto');

const FIRST_PORT = 8765;
const LAST_PORT = 8795;
const DISCOVERY_PORT = 18767;
const TOKEN = 'SISTEMA_NOTAS_SERVIDOR_57';
const sleep = ms => new Promise(r => setTimeout(r, ms));
let nativeDragInstalled = false;

function localIPv4() {
  const out = [];
  for (const list of Object.values(os.networkInterfaces())) {
    for (const x of list || []) {
      if (x.family === 'IPv4' && !x.internal && x.address) out.push(x.address);
    }
  }
  return [...new Set(out)];
}
function firstLan() { return localIPv4()[0] || '127.0.0.1'; }

function statusAt(host, port, timeout = 700) {
  return new Promise(resolve => {
    const req = http.get({ host, port, path: '/api/status', timeout }, res => {
      let b = '';
      res.setEncoding('utf8');
      res.on('data', c => b += c);
      res.on('end', () => {
        try {
          const d = JSON.parse(b);
          resolve(res.statusCode === 200 && d && d.ok && d.family === 'sistema-notas-local'
            ? { ok: true, url: `http://${host}:${port}/`, port, data: d }
            : { ok: false });
        } catch { resolve({ ok: false }); }
      });
    });
    req.on('timeout', () => req.destroy());
    req.on('error', () => resolve({ ok: false }));
  });
}

async function findLocalServer(limit = 15000) {
  const end = Date.now() + limit;
  while (Date.now() < end) {
    for (let p = FIRST_PORT; p <= LAST_PORT; p++) {
      const s = await statusAt('127.0.0.1', p, 250);
      if (s.ok) return s;
    }
    await sleep(200);
  }
  return null;
}

function startResponder(getPort) {
  const s = dgram.createSocket({ type: 'udp4', reuseAddr: true });
  s.on('error', () => {});
  s.on('message', (m, r) => {
    if (m.toString('utf8').trim() !== TOKEN) return;
    const p = Number(getPort());
    if (!p) return;
    const payload = Buffer.from(JSON.stringify({ family: 'sistema-notas-local', url: `http://${firstLan()}:${p}/` }));
    s.send(payload, r.port, r.address, () => {});
  });
  try { s.bind(DISCOVERY_PORT, '0.0.0.0'); } catch {}
  return s;
}

function discoverUdp(timeout = 2000) {
  return new Promise(resolve => {
    const s = dgram.createSocket('udp4');
    let done = false;
    const finish = v => { if (done) return; done = true; try { s.close(); } catch {} resolve(v); };
    s.on('error', () => finish(null));
    s.on('message', async m => {
      try {
        const d = JSON.parse(m.toString('utf8'));
        if (d.family !== 'sistema-notas-local' || !d.url) return;
        const u = new URL(d.url);
        const chk = await statusAt(u.hostname, Number(u.port || FIRST_PORT), 800);
        if (chk.ok) finish(chk.url);
      } catch {}
    });
    s.bind(0, () => {
      try {
        s.setBroadcast(true);
        const b = Buffer.from(TOKEN);
        s.send(b, DISCOVERY_PORT, '255.255.255.255', () => {});
        for (const ip of localIPv4()) {
          const a = ip.split('.');
          if (a.length === 4) { a[3] = '255'; s.send(b, DISCOVERY_PORT, a.join('.'), () => {}); }
        }
      } catch {}
    });
    setTimeout(() => finish(null), timeout);
  });
}

async function scanSubnet() {
  for (const prefix of [...new Set(localIPv4().map(ip => ip.split('.').slice(0, 3).join('.')))]) {
    for (let port = FIRST_PORT; port <= LAST_PORT; port++) {
      let idx = 1, found = null;
      const workers = Array.from({ length: 80 }, async () => {
        while (!found && idx <= 254) {
          const host = `${prefix}.${idx++}`;
          const s = await statusAt(host, port, 180);
          if (s.ok) { found = s.url; break; }
        }
      });
      await Promise.all(workers);
      if (found) return found;
    }
  }
  return null;
}

async function resolveServer() {
  const udp = await discoverUdp();
  if (udp) return udp;
  return await scanSubnet();
}

function sanitizeFileName(name) {
  let value = String(name || 'foto.jpg').replace(/[<>:"/\\|?*\x00-\x1F]/g, '_').trim();
  if (!value) value = 'foto.jpg';
  if (!/\.[a-z0-9]{2,5}$/i.test(value)) value += '.jpg';
  return value.slice(0, 180);
}

function installNativeDragHandlers() {
  if (nativeDragInstalled) return;
  nativeDragInstalled = true;
  const root = path.join(app.getPath('temp'), 'SistemaNotasDrag');
  fs.mkdirSync(root, { recursive: true });

  ipcMain.handle('sn:prepare-photo', async (_event, info) => {
    try {
      const url = String(info?.url || '');
      const u = new URL(url);
      if (!['http:', 'https:'].includes(u.protocol)) return '';
      const name = sanitizeFileName(info?.name);
      const hash = crypto.createHash('sha1').update(url).digest('hex').slice(0, 16);
      const filePath = path.join(root, `${hash}_${name}`);
      try {
        const st = fs.statSync(filePath);
        if (st.size > 0) return filePath;
      } catch {}
      const response = await net.fetch(url, { cache: 'no-store' });
      if (!response.ok) return '';
      const bytes = Buffer.from(await response.arrayBuffer());
      if (!bytes.length) return '';
      const tmp = filePath + '.tmp';
      fs.writeFileSync(tmp, bytes);
      fs.renameSync(tmp, filePath);
      return filePath;
    } catch { return ''; }
  });

  ipcMain.on('sn:start-drag', (event, filePath) => {
    try {
      const resolved = path.resolve(String(filePath || ''));
      const allowedRoot = path.resolve(root) + path.sep;
      if (!resolved.startsWith(allowedRoot) || !fs.existsSync(resolved)) return;
      event.sender.startDrag({ file: resolved, icon: resolved });
    } catch {}
  });
}

function openNotes(url, role) {
  installNativeDragHandlers();
  const win = new BrowserWindow({
    width: 1500,
    height: 930,
    minWidth: 1050,
    minHeight: 720,
    show: false,
    autoHideMenuBar: true,
    backgroundColor: '#dedede',
    title: role === 'supervisor' ? 'APP SUPERVISOR — Sistema de Notas' : 'APP DESPACHO — Sistema de Notas',
    webPreferences: {
      nodeIntegration: false,
      contextIsolation: true,
      sandbox: false,
      preload: path.join(__dirname, 'preload.js'),
      spellcheck: true,
      backgroundThrottling: false
    }
  });
  win.setMenuBarVisibility(false);
  win.webContents.setWindowOpenHandler(() => ({ action: 'deny' }));
  win.once('ready-to-show', () => { win.maximize(); win.show(); });
  win.loadURL(url).catch(async e => {
    await dialog.showMessageBox(win, { type: 'error', title: 'Sistema de Notas', message: 'No se pudo cargar la interfaz.', detail: String(e?.message || e) });
  });
  return win;
}

async function waitForServer() {
  while (true) {
    const url = await resolveServer();
    if (url) return url;
    const r = await dialog.showMessageBox({
      type: 'warning',
      title: 'Sistema de Notas',
      message: 'No se encontró APP SERVIDOR.',
      detail: 'Abre APP SERVIDOR en la PC servidor y verifica que las PCs estén conectadas a la misma red.',
      buttons: ['Reintentar', 'Salir'], defaultId: 0, cancelId: 1
    });
    if (r.response === 1) return null;
  }
}

module.exports = { findLocalServer, startResponder, openNotes, waitForServer };
