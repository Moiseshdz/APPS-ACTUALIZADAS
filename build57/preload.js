const { contextBridge, ipcRenderer, webUtils } = require('electron');

contextBridge.exposeInMainWorld('sistemaNotasDesktop', {
  preparePhoto: (info) => ipcRenderer.invoke('sn:prepare-photo', info),
  startDrag: (filePath) => ipcRenderer.send('sn:start-drag', filePath),
  getPathForFile: (file) => {
    try { return webUtils.getPathForFile(file); } catch { return ''; }
  }
});
