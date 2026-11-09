export type Kind = 'local' | 'dlna' | 'smb' | 'webdav' | 'sftp'

/** A chosen folder of a host: a row in the library. */
export interface FolderView {
  id: string // host ID + "#" + path
  host: string
  kind: Kind
  name: string // label
  addr: string // folder path
  items: number
  error?: string
  stale?: boolean // scan failed; listing the last good scan's files
  scanned: boolean // false until its first scan finishes
  kinds: ContentKind[]
}

export interface Host {
  id: string
  kind: Kind
  addr: string
  name: string
  user?: string
  folders: FolderView[]
}

export interface Discovered {
  id: string
  kind: Kind
  addr: string
  name: string
}

/** What a folder contributes to the library. Torrents are opt-in. */
export type ContentKind = 'video' | 'audio' | 'playlist' | 'torrent'
export const DEFAULT_KINDS: ContentKind[] = ['video', 'audio', 'playlist']

export interface Folder {
  path: string
  label: string
  kinds?: ContentKind[]
}

export interface Listing {
  path: string
  entries: { name: string; path: string }[]
  videos: number
}

/** How to reach a host: a saved one by id, or a new one by kind + addr. */
export interface Conn {
  id?: string
  kind: Kind
  addr: string
  name?: string
  user?: string
  pass?: string
}

export interface Status {
  scanning: boolean
  scanningId: string // source being rescanned alone; "" = full scan
  built?: string
  items: number
  movies: number
  shows: number
  hosts: Host[]
  discovered: Discovered[]
  manifestUrl: string
  installUrl: string
}

export class HttpError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

async function request(path: string, body?: unknown): Promise<Response> {
  const res = await fetch(path, {
    method: body === undefined ? 'GET' : 'POST',
    headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (!res.ok) throw new HttpError(res.status, (await res.text()).trim() || `HTTP ${res.status}`)
  return res
}

export const getAuth = async (): Promise<{ setup: boolean; authed: boolean }> => (await request('/api/auth')).json()
export const setup = (password: string) => request('/api/setup', { password })
export const login = (password: string) => request('/api/login', { password })
export const logout = () => request('/api/logout', {})
export const changePassword = (current: string, next: string) => request('/api/password', { current, new: next })
export const newToken = () => request('/api/token', {})
/** Desktop app window? It loads the panel with ?desktop=1 (kept for the session). */
export const inDesktopApp = (() => {
  try {
    if (new URLSearchParams(location.search).has('desktop')) sessionStorage.setItem('desktop', '1')
    return sessionStorage.getItem('desktop') === '1'
  } catch {
    return new URLSearchParams(location.search).has('desktop')
  }
})()
/** Desktop app: ask it to open a stremio:// link with the OS (its window can't). */
export const openExternal = (url: string) => request('/api/open', { url })
export interface DesktopPrefs {
  quitOnClose: boolean
  startHidden: boolean
  autostart: boolean
}
export const getDesktop = async (): Promise<DesktopPrefs> => {
  const p = await (await request('/api/desktop')).json()
  return { quitOnClose: !!p.quitOnClose, startHidden: !!p.startHidden, autostart: !!p.autostart }
}
export const setDesktop = (p: DesktopPrefs) => request('/api/desktop', p)
export const getStatus = async (): Promise<Status> => (await request('/api/status')).json()
export const rescan = () => request('/rescan', {})
export const browse = async (c: Conn, path: string): Promise<Listing> => (await request('/api/browse', { ...c, path })).json()
export const saveHost = (c: Conn, folders: Folder[]) => request('/api/hosts', { ...c, folders })
export const removeHost = (id: string) => request('/api/hosts/remove', { id })
/** Rescan a host, a folder ("host#path"), or "discover" to look for servers. */
export const rescanSource = (id: string) => request('/api/sources/rescan', { id })
