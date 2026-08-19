const { BrowserWindow, dialog } = require('electron');
const fs = require('fs');
const path = require('path');
const os = require('os');
const http = require('http');
const dgram = require('dgram');

const FIRST_PORT = 8765;
const LAST_PORT = 8795;
const DISCOVERY_PORT = 18765;
const DISCOVERY_TOKEN = 'SISTEMA_NOTAS_DISCOVER_55';

function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

function statusAt(host, port, timeout = 800) {
  return new Promise(resolve => {
    const req = http.get({ host, port, path: '/api/status', timeout }, res => {
      let body = '';
      res.setEncoding('utf8');
      res.on('data', c => body += c);
      res.on('end', () => {
        try {
          const data = JSON.parse(body);
          if (res.statusCode === 200 && data && data.ok && data.family === 'sistema-notas-local') {
            resolve({ ok: true, data, url: `http://${host}:${port}/`, port });
          } else resolve({ ok: false });
        } catch { resolve({ ok: false }); }
      });
    });
    req.on('timeout', () => req.destroy());
    req.on('error', () => resolve({ ok: false }));
  });
}

async function findLocalServer(timeoutMs = 18000) {
  const started = Date.now();
  while (Date.now() - started < timeoutMs) {
    for (let port = FIRST_PORT; port <= LAST_PORT; port++) {
      const s = await statusAt('127.0.0.1', port, 350);
      if (s.ok) return s;
    }
    await sleep(250);
  }
  return null;
}

function localIPv4() {
  const result = [];
  for (const entries of Object.values(os.networkInterfaces())) {
    for (const item of entries || []) {
      if (item.family === 'IPv4' && !item.internal && item.address) result.push(item.address);
    }
  }
  return [...new Set(result)];
}

function firstLanIPv4() { return localIPv4()[0] || '127.0.0.1'; }

function startDiscoveryResponder(getPort) {
  const socket = dgram.createSocket({ type: 'udp4', reuseAddr: true });
  socket.on('error', () => {});
  socket.on('message', (msg, rinfo) => {
    if (msg.toString('utf8').trim() !== DISCOVERY_TOKEN) return;
    const port = Number(getPort());
    if (!port) return;
    const payload = Buffer.from(JSON.stringify({
      app: 'Sistema de Notas', family: 'sistema-notas-local', version: '5.5',
      url: `http://${firstLanIPv4()}:${port}/`
    }), 'utf8');
    socket.send(payload, rinfo.port, rinfo.address, () => {});
  });
  try { socket.bind(DISCOVERY_PORT, '0.0.0.0'); } catch {}
  return socket;
}

function discoverByUdp(timeoutMs = 1800) {
  return new Promise(resolve => {
    const socket = dgram.createSocket('udp4');
    let done = false;
    const finish = value => {
      if (done) return; done = true;
      try { socket.close(); } catch {}
      resolve(value);
    };
    socket.on('error', () => finish(null));
    socket.on('message', async msg => {
      try {
        const data = JSON.parse(msg.toString('utf8'));
        if (data.family !== 'sistema-notas-local' || !data.url) return;
        const u = new URL(data.url);
        const s = await statusAt(u.hostname, Number(u.port || FIRST_PORT), 800);
        if (s.ok) finish(s.url);
      } catch {}
    });
    socket.bind(0, () => {
      try {
        socket.setBroadcast(true);
        const data = Buffer.from(DISCOVERY_TOKEN, 'utf8');
        socket.send(data, DISCOVERY_PORT, '255.255.255.255', () => {});
        for (const ip of localIPv4()) {
          const parts = ip.split('.');
          if (parts.length === 4) { parts[3] = '255'; socket.send(data, DISCOVERY_PORT, parts.join('.'), () => {}); }
        }
      } catch {}
    });
    setTimeout(() => finish(null), timeoutMs);
  });
}

async function scanSubnet() {
  const prefixes = [...new Set(localIPv4().map(ip => ip.split('.').slice(0, 3).join('.')).filter(Boolean))];
  for (const prefix of prefixes) {
    const targets = [];
    for (let i = 1; i <= 254; i++) for (let p = FIRST_PORT; p <= LAST_PORT; p++) targets.push([`${prefix}.${i}`, p]);
    let found = null;
    let index = 0;
    const workers = Array.from({ length: 64 }, async () => {
      while (!found && index < targets.length) {
        const [host, port] = targets[index++];
        const s = await statusAt(host, port, 200);
        if (s.ok) { found = s.url; break; }
      }
    });
    await Promise.all(workers);
    if (found) return found;
  }
  return null;
}

async function resolveRemoteServer(savedUrl) {
  if (savedUrl) {
    try {
      const u = new URL(savedUrl);
      const s = await statusAt(u.hostname, Number(u.port || FIRST_PORT), 1000);
      if (s.ok) return s.url;
    } catch {}
  }
  const udp = await discoverByUdp();
  if (udp) return udp;
  return await scanSubnet();
}

function openAppWindow(url, role) {
  const win = new BrowserWindow({
    width: 1500, height: 930, minWidth: 1050, minHeight: 720,
    show: false, backgroundColor: '#dedede', autoHideMenuBar: true,
    title: role === 'supervisor' ? 'APP SUPERVISOR — Sistema de Notas' : 'APP DESPACHO — Sistema de Notas',
    webPreferences: {
      nodeIntegration: false, contextIsolation: true, sandbox: true,
      spellcheck: true, backgroundThrottling: false
    }
  });
  win.setMenuBarVisibility(false);
  win.webContents.setWindowOpenHandler(({ url: target }) => {
    try { if (new URL(target).origin === new URL(url).origin) return { action: 'allow' }; } catch {}
    return { action: 'deny' };
  });
  win.webContents.on('will-navigate', (event, target) => {
    try { if (new URL(target).origin !== new URL(url).origin) event.preventDefault(); } catch { event.preventDefault(); }
  });
  win.once('ready-to-show', () => { win.maximize(); win.show(); });
  win.loadURL(url).catch(async err => {
    await dialog.showMessageBox(win, { type: 'error', title: 'Sistema de Notas', message: 'No se pudo cargar la interfaz original.', detail: String(err?.message || err) });
  });
  return win;
}

function readTextSafe(file) { try { return fs.readFileSync(file, 'utf8').trim(); } catch { return ''; } }
function writeTextSafe(file, value) { try { fs.mkdirSync(path.dirname(file), { recursive: true }); fs.writeFileSync(file, value, 'utf8'); } catch {} }

module.exports = { FIRST_PORT, LAST_PORT, sleep, statusAt, findLocalServer, localIPv4, startDiscoveryResponder, resolveRemoteServer, openAppWindow, readTextSafe, writeTextSafe };
