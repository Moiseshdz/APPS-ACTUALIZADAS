const { app, dialog } = require('electron');
app.disableHardwareAcceleration();
const path = require('path');
const fs = require('fs');
const { spawn } = require('child_process');
const http = require('http');
const { findLocalServer, startDiscoveryResponder, openAppWindow } = require('./common');

const portableDir = process.env.PORTABLE_EXECUTABLE_DIR || path.dirname(process.execPath);
const baseDataRoot = path.join(process.env.LOCALAPPDATA || app.getPath('appData'), 'SistemaNotasSupervisor');
app.setPath('userData', path.join(baseDataRoot, 'Electron'));
let backend = null, responder = null, currentServer = null, mainWindow = null, quitting = false;

function copyDirIfNeeded(source, target) {
  try { if (!fs.existsSync(source) || fs.existsSync(target)) return; fs.mkdirSync(path.dirname(target), { recursive: true }); fs.cpSync(source, target, { recursive: true }); } catch {}
}
function migrateLegacyData() {
  const targetDatos = path.join(baseDataRoot, 'datos');
  const candidates = [path.join(portableDir, 'datos'), path.join(path.dirname(portableDir), 'datos')];
  if (!fs.existsSync(targetDatos)) for (const source of candidates) if (fs.existsSync(path.join(source, 'sistema_notas.db.json'))) { copyDirIfNeeded(source, targetDatos); break; }
  const targetResp = path.join(baseDataRoot, 'respaldos');
  for (const source of [path.join(portableDir, 'respaldos'), path.join(path.dirname(portableDir), 'respaldos')]) if (fs.existsSync(source)) { copyDirIfNeeded(source, targetResp); break; }
}
function backendPath() { return app.isPackaged ? path.join(process.resourcesPath, 'backend.exe') : path.join(__dirname, '..', 'backend.exe'); }
async function stopBackend() {
  if (currentServer) try { await new Promise(resolve => { const u = new URL(currentServer.url); const req = http.request({ hostname:u.hostname, port:u.port, path:'/api/shutdown', method:'POST', timeout:700 }, res=>{res.resume();resolve();}); req.on('error',resolve); req.on('timeout',()=>{req.destroy();resolve();}); req.end('{}'); }); } catch {}
  if (backend && !backend.killed) try { backend.kill(); } catch {}
}

app.whenReady().then(async () => {
  migrateLegacyData(); fs.mkdirSync(baseDataRoot, { recursive: true });
  const exe = backendPath();
  if (!fs.existsSync(exe)) { await dialog.showMessageBox({type:'error',title:'APP SUPERVISOR',message:'No se encontró el servidor interno.'}); app.quit(); return; }
  backend = spawn(exe, ['--servidor'], { windowsHide:true, cwd:baseDataRoot, env:{...process.env,SISTEMA_NOTAS_ROOT:baseDataRoot,SISTEMA_NOTAS_NO_BROWSER:'1'}, stdio:'ignore' });
  currentServer = await findLocalServer(18000);
  if (!currentServer) { await dialog.showMessageBox({type:'error',title:'APP SUPERVISOR',message:'No se pudo iniciar el servidor interno.',detail:'Cierra copias anteriores de Sistema de Notas y vuelve a abrir APP SUPERVISOR.'}); app.quit(); return; }
  responder = startDiscoveryResponder(() => currentServer.port);
  mainWindow = openAppWindow(`${currentServer.url}?vista=dashboard`, 'supervisor');
});
app.on('before-quit', async event => { if (quitting) return; quitting=true; event.preventDefault(); try { responder?.close(); } catch {} await stopBackend(); app.exit(0); });
app.on('window-all-closed', () => app.quit());
