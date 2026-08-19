const { app, BrowserWindow, dialog } = require('electron');
app.disableHardwareAcceleration();
const path = require('path');
const fs = require('fs');
const http = require('http');
const { spawn } = require('child_process');
const { findLocalServer, startResponder } = require('./common');

const dataRoot = path.join(process.env.LOCALAPPDATA || app.getPath('appData'), 'SistemaNotasServidor');
app.setPath('userData', path.join(dataRoot, 'Electron'));
let backend = null, responder = null, current = null, win = null, quitting = false;

function backendPath() {
  return app.isPackaged ? path.join(process.resourcesPath, 'backend.exe') : path.join(__dirname, '..', 'backend.exe');
}

async function stopBackend() {
  if (current) {
    try {
      await new Promise(resolve => {
        const u = new URL(current.url);
        const req = http.request({ hostname: u.hostname, port: u.port, path: '/api/shutdown', method: 'POST', timeout: 600 }, res => { res.resume(); resolve(); });
        req.on('error', resolve);
        req.on('timeout', () => { req.destroy(); resolve(); });
        req.end('{}');
      });
    } catch {}
  }
  if (backend && !backend.killed) try { backend.kill(); } catch {}
}

function serverWindow() {
  win = new BrowserWindow({ width: 620, height: 420, resizable: false, autoHideMenuBar: true, title: 'APP SERVIDOR — Sistema de Notas', backgroundColor: '#e6edf2', webPreferences: { nodeIntegration: false, contextIsolation: true } });
  win.setMenuBarVisibility(false);
  const html = `<!doctype html><html><head><meta charset='utf-8'><style>body{margin:0;font-family:Segoe UI,Arial;background:#e6edf2;color:#173d59;display:grid;place-items:center;height:100vh}.card{width:500px;background:white;border:2px solid #173d59;border-radius:20px;padding:30px;text-align:center;box-shadow:0 16px 40px #0002}.dot{width:18px;height:18px;border-radius:50%;background:#20a464;display:inline-block;box-shadow:0 0 0 7px #20a46422}h1{font-size:28px;margin:18px 0 8px}.ok{font-weight:800;font-size:19px}.muted{color:#687985;line-height:1.5;margin-top:15px}.badge{display:inline-block;margin-top:18px;padding:9px 14px;border-radius:999px;background:#edf6f1;color:#16623f;font-weight:800}</style></head><body><div class='card'><span class='dot'></span><h1>APP SERVIDOR</h1><div class='ok'>SERVIDOR ACTIVO</div><div class='muted'>Mantén esta ventana abierta.<br>APP SUPERVISOR y APP DESPACHO se conectan automáticamente.</div><div class='badge'>Sistema de Notas 5.7</div></div></body></html>`;
  win.loadURL('data:text/html;charset=utf-8,' + encodeURIComponent(html));
}

app.whenReady().then(async () => {
  fs.mkdirSync(dataRoot, { recursive: true });
  const exe = backendPath();
  if (!fs.existsSync(exe)) {
    await dialog.showMessageBox({ type: 'error', title: 'APP SERVIDOR', message: 'No se encontró el servidor interno.' });
    app.quit(); return;
  }
  backend = spawn(exe, ['--servidor'], { windowsHide: true, cwd: dataRoot, env: { ...process.env, SISTEMA_NOTAS_ROOT: dataRoot, SISTEMA_NOTAS_NO_BROWSER: '1' }, stdio: 'ignore' });
  current = await findLocalServer(16000);
  if (!current) {
    await dialog.showMessageBox({ type: 'error', title: 'APP SERVIDOR', message: 'No se pudo iniciar el servidor.', detail: 'Cierra cualquier copia anterior de Sistema de Notas e inténtalo de nuevo.' });
    app.quit(); return;
  }
  responder = startResponder(() => current.port);
  serverWindow();
});

app.on('before-quit', async e => {
  if (quitting) return;
  quitting = true;
  e.preventDefault();
  try { responder?.close(); } catch {}
  await stopBackend();
  app.exit(0);
});
app.on('window-all-closed', () => app.quit());
