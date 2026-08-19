const { app } = require('electron');
app.disableHardwareAcceleration();
const path = require('path');
const { waitForServer, openNotes } = require('./common');
const dataRoot = path.join(process.env.LOCALAPPDATA || app.getPath('appData'), 'SistemaNotasDespacho');
app.setPath('userData', path.join(dataRoot, 'Electron'));
app.whenReady().then(async () => {
  const url = await waitForServer();
  if (!url) { app.quit(); return; }
  openNotes(url + '?vista=registro', 'despacho');
});
app.on('window-all-closed', () => app.quit());
