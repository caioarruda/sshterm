import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebLinksAddon } from '@xterm/addon-web-links'
import '@xterm/xterm/css/xterm.css'
import {
  Connect,
  Disconnect,
  IsConnected,
  SendInput,
  Resize,
  UploadFile,
  UploadPaths,
  OpenFileDialog,
  OpenFolderDialog,
  GetHosts,
  SaveHost,
  DeleteHost,
} from '../wailsjs/go/main/App'
import { EventsOn } from '../wailsjs/runtime/runtime'

// Terminal setup
const term = new Terminal({
  theme: {
    background: '#0d1117',
    foreground: '#e6edf3',
    cursor: '#e6edf3',
    cursorAccent: '#0d1117',
    black: '#484f58',
    red: '#ff7b72',
    green: '#3fb950',
    yellow: '#d29922',
    blue: '#58a6ff',
    magenta: '#bc8cff',
    cyan: '#39c5cf',
    white: '#b1bac4',
    brightBlack: '#6e7681',
    brightRed: '#ffa198',
    brightGreen: '#56d364',
    brightYellow: '#e3b341',
    brightBlue: '#79c0ff',
    brightMagenta: '#d2a8ff',
    brightCyan: '#56d4dd',
    brightWhite: '#f0f6fc',
  },
  fontFamily: "'Cascadia Code', 'Fira Code', 'Consolas', monospace",
  fontSize: 13,
  lineHeight: 1.2,
  cursorBlink: true,
  cursorStyle: 'block',
  scrollback: 5000,
  allowTransparency: false,
})

const fitAddon = new FitAddon()
term.loadAddon(fitAddon)
term.loadAddon(new WebLinksAddon())

term.open(document.getElementById('terminal'))
fitAddon.fit()

// Resize observer
const ro = new ResizeObserver(() => {
  fitAddon.fit()
  const dims = fitAddon.proposeDimensions()
  if (dims) Resize(dims.cols, dims.rows)
})
ro.observe(document.getElementById('terminal'))

// Keyboard input → SSH
term.onData(data => SendInput(data))

// SSH output → terminal
EventsOn('terminal:data', data => term.write(data))

EventsOn('terminal:closed', () => {
  setStatus('Sessão encerrada', false)
  document.getElementById('connectBtn').textContent = 'Conectar'
  document.getElementById('connectBtn').classList.remove('danger')
})

EventsOn('terminal:pwd', pwd => {
  document.getElementById('pwdDisplay').textContent = pwd
})

EventsOn('upload:progress', msg => setStatus(msg, true))
EventsOn('upload:done', msg => {
  setStatus(msg, true)
  showToast(msg, 'success')
})
EventsOn('upload:error', msg => {
  setStatus(msg, true)
  showToast(msg, 'error')
})
EventsOn('upload:complete', () => setStatus('Upload concluído', true))

// Connect/Disconnect
window.handleConnect = async function () {
  const btn = document.getElementById('connectBtn')
  const connected = await IsConnected()

  if (connected) {
    await Disconnect()
    btn.textContent = 'Conectar'
    btn.classList.remove('danger')
    setStatus('Desconectado', false)
    return
  }

  const host = document.getElementById('host').value.trim()
  const port = document.getElementById('port').value.trim() || '22'
  const user = document.getElementById('username').value.trim()
  const pass = document.getElementById('password').value
  const key = document.getElementById('keypath').value.trim()

  if (!host || !user) {
    showToast('Host e usuário são obrigatórios', 'error')
    return
  }

  btn.textContent = 'Conectando...'
  btn.disabled = true
  setStatus('Conectando...', false)

  try {
    await Connect(host, port, user, pass, key)
    btn.textContent = 'Desconectar'
    btn.classList.add('danger')
    btn.disabled = false
    setStatus(`Conectado — ${user}@${host}`, true)
    term.focus()
    fitAddon.fit()
    const dims = fitAddon.proposeDimensions()
    if (dims) Resize(dims.cols, dims.rows)
    // Auto-collapse sidebar after connect
    if (document.getElementById('sidebar').classList.contains('open')) {
      toggleSidebar()
    }
  } catch (err) {
    btn.textContent = 'Conectar'
    btn.classList.remove('danger')
    btn.disabled = false
    setStatus(`Erro: ${err}`, false)
    showToast(`Erro ao conectar: ${err}`, 'error')
  }
}

// Upload files
window.handleUploadFiles = async function () {
  const paths = await OpenFileDialog()
  if (paths && paths.length > 0) {
    UploadPaths(paths)
  }
}

window.handleUploadFolder = async function () {
  const path = await OpenFolderDialog()
  if (path) {
    UploadPaths([path])
  }
}

// Sidebar toggle
window.toggleSidebar = function () {
  const sidebar = document.getElementById('sidebar')
  const btn = document.getElementById('toggleBtn')
  const isOpen = sidebar.classList.contains('open')
  if (isOpen) {
    sidebar.classList.remove('open')
    sidebar.classList.add('closed')
    btn.textContent = '›'
  } else {
    sidebar.classList.add('open')
    sidebar.classList.remove('closed')
    btn.textContent = '‹'
  }
  setTimeout(() => { fitAddon.fit() }, 220)
}

// Drag and drop — WebView2 exposes file.path for files dragged from Explorer
const dropHint = document.querySelector('.drop-hint')
const termContainer = document.getElementById('termContainer')

termContainer.addEventListener('dragenter', e => {
  e.preventDefault()
  dropHint.classList.add('dragover')
})
termContainer.addEventListener('dragover', e => {
  e.preventDefault()
  e.dataTransfer.dropEffect = 'copy'
})
termContainer.addEventListener('dragleave', e => {
  if (!termContainer.contains(e.relatedTarget)) {
    dropHint.classList.remove('dragover')
  }
})
termContainer.addEventListener('drop', e => {
  e.preventDefault()
  dropHint.classList.remove('dragover')
  const files = Array.from(e.dataTransfer.files)
  if (files.length === 0) return
  // WebView2 (Edge-based) exposes file.path for local files
  const paths = files.map(f => f.path || f.name).filter(p => p && p.includes('\\') || p.includes('/'))
  if (paths.length > 0) {
    UploadPaths(paths)
  } else {
    showToast('Não foi possível obter o caminho dos arquivos', 'error')
  }
})

// ---- Host management ----
let activeHostId = null

async function loadHosts() {
  const hosts = await GetHosts()
  const list = document.getElementById('hostList')
  list.innerHTML = ''
  if (!hosts || hosts.length === 0) {
    list.innerHTML = '<div style="font-size:11px;color:var(--text-muted);padding:4px 8px;">Nenhum host salvo</div>'
    return
  }
  hosts.forEach(h => {
    const item = document.createElement('button')
    item.className = 'host-item' + (h.id === activeHostId ? ' active' : '')
    item.innerHTML = `
      <span class="host-item-name">${h.name || h.host}</span>
      <span class="host-item-addr">${h.user}@${h.host}:${h.port}</span>
      <button class="host-delete" data-id="${h.id}" title="Remover">✕</button>
    `
    item.addEventListener('click', e => {
      if (e.target.classList.contains('host-delete')) return
      fillForm(h)
      activeHostId = h.id
      loadHosts()
    })
    item.querySelector('.host-delete').addEventListener('click', async e => {
      e.stopPropagation()
      await DeleteHost(h.id)
      if (activeHostId === h.id) activeHostId = null
      loadHosts()
    })
    list.appendChild(item)
  })
}

function fillForm(h) {
  document.getElementById('hostName').value = h.name || ''
  document.getElementById('host').value = h.host || ''
  document.getElementById('port').value = h.port || '22'
  document.getElementById('username').value = h.user || ''
  document.getElementById('password').value = ''
  document.getElementById('keypath').value = h.keyPath || ''
}

function getFormHost() {
  return {
    id: activeHostId || '',
    name: document.getElementById('hostName').value.trim(),
    host: document.getElementById('host').value.trim(),
    port: document.getElementById('port').value.trim() || '22',
    user: document.getElementById('username').value.trim(),
    keyPath: document.getElementById('keypath').value.trim(),
  }
}

window.handleSaveHost = async function () {
  const h = getFormHost()
  if (!h.host || !h.user) {
    showToast('Preencha host e usuário antes de salvar', 'error')
    return
  }
  if (!h.name) h.name = `${h.user}@${h.host}`
  await SaveHost(h)
  if (!activeHostId) {
    // get the newly saved host id
    const hosts = await GetHosts()
    const saved = hosts.find(x => x.host === h.host && x.user === h.user)
    if (saved) activeHostId = saved.id
  }
  loadHosts()
  showToast('Host salvo', 'success')
}

loadHosts()

// ---- Status bar ----
function setStatus(msg, connected) {
  const bar = document.getElementById('statusBar')
  const dot = document.createElement('span')
  dot.className = 'status-dot' + (connected ? ' connected' : '')
  bar.innerHTML = ''
  bar.appendChild(dot)
  bar.appendChild(document.createTextNode(' ' + msg))
}

// Toast notifications
let toastTimer = null
function showToast(msg, type = '') {
  const existing = document.querySelector('.toast')
  if (existing) existing.remove()
  if (toastTimer) clearTimeout(toastTimer)

  const toast = document.createElement('div')
  toast.className = `toast ${type}`
  toast.textContent = msg
  document.body.appendChild(toast)
  toastTimer = setTimeout(() => toast.remove(), 4000)
}

// Enter key on inputs connects
document.querySelectorAll('#host, #port, #username, #password, #keypath')
  .forEach(el => el.addEventListener('keydown', e => {
    if (e.key === 'Enter') window.handleConnect()
  }))

setStatus('Desconectado', false)