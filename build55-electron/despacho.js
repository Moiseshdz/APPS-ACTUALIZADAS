const { app, BrowserWindow, ipcMain } = require('electron');
app.disableHardwareAcceleration();
const path = require('path');
const fs = require('fs');
const { resolveRemoteServer, openAppWindow, readTextSafe, writeTextSafe, statusAt } = require('./common');

const baseDataRoot = path.join(process.env.LOCALAPPDATA || app.getPath('appData'), 'SistemaNotasDespacho');
app.setPath('userData', path.join(baseDataRoot, 'Electron'));
const configFile = path.join(baseDataRoot, 'servidor.txt');
let mainWindow = null, configWindow = null;

async function validateUrl(raw) {
  let text = String(raw || '').trim();
  if (!/^https?:\/\//i.test(text)) text = 'http://' + text;
  const u = new URL(text); const port = Number(u.port || 8765);
  const s = await statusAt(u.hostname, port, 1500);
  if (!s.ok) throw new Error('No se encontró APP SUPERVISOR en esa dirección.');
  return s.url;
}
function openConfigWindow() {
  configWindow = new BrowserWindow({ width:650, height:440, resizable:false, autoHideMenuBar:true, title:'APP DESPACHO — Conectar con Supervisor', webPreferences:{nodeIntegration:true,contextIsolation:false} });
  configWindow.setMenuBarVisibility(false);
  const saved = readTextSafe(configFile);
  const page = `<!doctype html><html lang="es"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><style>*{box-sizing:border-box}body{margin:0;background:#dedede;font-family:Segoe UI,Arial,sans-serif;color:#173d59;display:grid;place-items:center;min-height:100vh}.card{width:590px;background:#fff;border:2px solid #0d3553;border-radius:18px;padding:28px;box-shadow:0 15px 40px #0002}.head{display:flex;align-items:center;gap:15px}.badge{width:54px;height:54px;border-radius:14px;background:#154e73;color:#ffcc00;display:grid;place-items:center;font-weight:900;font-size:24px}h1{margin:0;font-size:23px}.sub{color:#5d6f7b;line-height:1.5;margin:8px 0 22px}label{font-weight:800;display:block;margin-bottom:7px}input{width:100%;height:48px;border:2px solid #0d3553;border-radius:10px;padding:0 12px;font-size:16px}button{width:100%;height:50px;margin-top:15px;border:2px solid #0d3553;border-radius:10px;background:#154e73;color:#ffcc00;font-size:16px;font-weight:900;cursor:pointer}.error{min-height:22px;color:#a32235;margin-top:12px;font-weight:600}.small{font-size:13px;color:#697b86;margin-top:8px}</style></head><body><div class="card"><div class="head"><div class="badge">911</div><div><h1>Conectar con APP SUPERVISOR</h1><div class="sub">No se encontró automáticamente el servidor. Escribe la IP de la PC de Supervisión.</div></div></div><label>Dirección del Supervisor</label><input id="server" value="${saved.replace(/&/g,'&amp;').replace(/"/g,'&quot;').replace(/</g,'&lt;')}" placeholder="192.168.1.73:8765"><button id="go">CONECTAR</button><div class="error" id="error"></div><div class="small">La dirección quedará guardada para la próxima vez.</div></div><script>const {ipcRenderer}=require('electron');const b=document.getElementById('go'),i=document.getElementById('server'),e=document.getElementById('error');async function go(){e.textContent='Conectando…';b.disabled=true;const r=await ipcRenderer.invoke('connect-server',i.value);if(!r.ok){e.textContent=r.error;b.disabled=false}}b.onclick=go;i.onkeydown=x=>{if(x.key==='Enter')go()}</script></body></html>`;
  configWindow.loadURL('data:text/html;charset=utf-8,' + encodeURIComponent(page));
}
ipcMain.handle('connect-server', async (_e, value) => { try { const url=await validateUrl(value); writeTextSafe(configFile,url); configWindow?.close(); configWindow=null; mainWindow=openAppWindow(`${url}?vista=registro`,'despacho'); return {ok:true}; } catch(err) { return {ok:false,error:err.message||String(err)}; } });
app.whenReady().then(async () => { fs.mkdirSync(baseDataRoot,{recursive:true}); const saved=readTextSafe(configFile); const url=await resolveRemoteServer(saved); if(url){writeTextSafe(configFile,url);mainWindow=openAppWindow(`${url}?vista=registro`,'despacho');} else openConfigWindow(); });
app.on('window-all-closed', () => app.quit());
