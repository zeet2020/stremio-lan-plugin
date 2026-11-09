import { useState, type FormEvent, type ReactNode } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  ActionIcon,
  Alert,
  Anchor,
  Badge,
  Box,
  Breadcrumbs,
  Button,
  Center,
  Checkbox,
  Chip,
  CloseButton,
  Container,
  CopyButton,
  Drawer,
  Group,
  Loader,
  Paper,
  PasswordInput,
  SegmentedControl,
  SimpleGrid,
  Stack,
  Switch,
  Text,
  TextInput,
  Title,
  Tooltip,
  UnstyledButton,
  useMantineColorScheme,
  type MantineColorScheme,
  Tabs,
} from '@mantine/core'
import { useDisclosure, useMediaQuery } from '@mantine/hooks'
import {
  browse,
  changePassword,
  getAuth,
  getStatus,
  login,
  logout,
  newToken,
  getDesktop,
  getLibrary,
  clearThumbs,
  setLibrary,
  type LibraryPrefs,
  setDesktop,
  type DesktopPrefs,
  inDesktopApp,
  openExternal,
  removeHost,
  rescan,
  rescanSource,
  saveHost,
  setup,
  type Conn,
  type ContentKind,
  DEFAULT_KINDS,
  type Discovered,
  type Folder,
  type Host,
  type Kind,
  type Status,
} from './api'
import { KIND_COLOR } from './theme'

const KINDS: { kind: Kind; label: string; hint: string }[] = [
  { kind: 'local', label: 'Folder', hint: 'A folder on the machine running Stremio LAN Library: a USB disk, a NAS share the OS has mounted, ~/Videos.' },
  { kind: 'smb', label: 'SMB', hint: 'Windows shares, Samba, most NAS boxes. Found by themselves; add one here if it needs a login.' },
  { kind: 'sftp', label: 'SFTP', hint: 'Any machine with SSH. Leave the password blank to use this device’s SSH key. The host key is trusted on add.' },
  { kind: 'webdav', label: 'WebDAV', hint: 'Nextcloud, ownCloud, many NAS apps. Playback goes through here, so the login never reaches Stremio.' },
  { kind: 'dlna', label: 'DLNA', hint: 'MiniDLNA, Jellyfin, Plex, Serviio. Found by themselves; add the description URL if discovery is blocked.' },
]
const KIND_LABEL = Object.fromEntries(KINDS.map((k) => [k.kind, k.label])) as Record<Kind, string>
const KIND_ORDER = Object.fromEntries(KINDS.map((k, i) => [k.kind, i])) as Record<Kind, number>
const PLACEHOLDER: Record<Kind, string> = {
  local: '/media/usb/Movies',
  dlna: 'http://192.168.0.50:8200/rootDesc.xml',
  smb: '192.168.0.60',
  webdav: 'https://nas.local/remote.php/dav/files/me/Videos',
  sftp: '192.168.0.70  or  192.168.0.70:2222',
}
const mono = { input: { fontFamily: 'var(--mantine-font-family-monospace)' } }

// --- icons (inline, stroke-based) ---

const svg = (d: ReactNode, size = 16) => (
  <svg viewBox="0 0 24 24" width={size} height={size} fill="none" stroke="currentColor" strokeWidth={2} strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    {d}
  </svg>
)
const Icon = {
  scan: svg(<path d="M20 12a8 8 0 1 1-2.34-5.66M20 4v5h-5" />),
  copy: svg(<><rect x="8" y="8" width="12" height="12" rx="2" /><path d="M16 8V6a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v8a2 2 0 0 0 2 2h2" /></>),
  check: svg(<path d="M5 12l5 5L20 7" />),
  play: svg(<path d="M8 5v14l11-7z" fill="currentColor" stroke="none" />),
  key: svg(<><circle cx="8" cy="15" r="4" /><path d="M11 12l9-9M17 6l3 3M15 8l2 2" /></>),
  power: svg(<path d="M12 3v9M6.3 6.3a8 8 0 1 0 11.4 0" />),
  gear: svg(<><circle cx="12" cy="12" r="3" /><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z" /></>, 18),
  plus: svg(<path d="M12 5v14M5 12h14" />),
  close: svg(<path d="M6 6l12 12M18 6L6 18" />, 14),
  chevron: svg(<path d="M9 6l6 6-6 6" />, 14),
  up: svg(<path d="M12 19V5M5 12l7-7 7 7" />),
  folder: svg(<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" />, 15),
  alert: svg(<><circle cx="12" cy="12" r="9" /><path d="M12 8v5M12 16h.01" /></>),
}

// --- receiver bits Mantine doesn't have ---

/** Vacuum-fluorescent style digits: unlit "8"s glow faintly behind the value. */
function Vfd({ value, width = 4 }: { value: string | number; width?: number }) {
  const v = String(value).padStart(width, ' ')
  return (
    <span className="vfd" aria-label={String(value).trim()}>
      <span className="vfd-ghost" aria-hidden="true">{v.replace(/[^\s:]/g, '8').replace(/ /g, '8')}</span>
      <span className="vfd-lit" aria-hidden="true">{v}</span>
    </span>
  )
}

function Led({ color, state }: { color: string; state: 'live' | 'off' | 'fault' | 'busy' }) {
  return <span className={`led ${state}`} style={{ ['--led' as string]: `var(--mantine-color-${color}-filled)` }} />
}

function Brand() {
  return (
    <Group gap="sm" wrap="nowrap">
      <span className="power-led" />
      <Text ff="heading" fw={700} fz={22} lts="0.16em" lh={1}>
        <Text span inherit fw={500} c="dimmed">
          STREMIO{' '}
        </Text>
        LAN{' '}
        <Text span inherit fw={500} c="dimmed">
          LIBRARY
        </Text>
      </Text>
    </Group>
  )
}

function PanelTitle({ tag, children }: { tag: string; children: ReactNode }) {
  return (
    <Group gap="xs" mb={4}>
      <Badge variant="outline" radius="sm" size="sm" ff="monospace">
        {tag}
      </Badge>
      <Title order={2} fz="md" lts="0.16em" tt="uppercase">
        {children}
      </Title>
    </Group>
  )
}

function ErrorAlert({ children }: { children: ReactNode }) {
  return (
    <Alert color="red" variant="light" icon={Icon.alert} py="xs">
      <Text size="sm" style={{ overflowWrap: 'anywhere' }}>{children}</Text>
    </Alert>
  )
}

function Eyebrow({ children }: { children: ReactNode }) {
  return (
    <Text ff="monospace" fz={11} lts="0.2em" tt="uppercase" c="dimmed">
      {children}
    </Text>
  )
}

// --- app ---

export default function App() {
  const auth = useQuery({ queryKey: ['auth'], queryFn: getAuth })
  if (auth.isError)
    return (
      <Center mih="100vh" p="md">
        <ErrorAlert>Can’t reach the add-on: {auth.error.message}</ErrorAlert>
      </Center>
    )
  if (!auth.data) return null
  if (!auth.data.authed) return <PasswordGate setupMode={auth.data.setup} />
  return <ControlPanel />
}

function PasswordGate({ setupMode }: { setupMode: boolean }) {
  const qc = useQueryClient()
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const go = useMutation({
    mutationFn: () => (setupMode ? setup(password) : login(password)),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['auth'] }),
  })
  const mismatch = setupMode && confirm !== '' && confirm !== password
  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (!mismatch) go.mutate()
  }
  return (
    <Center mih="100vh" p="md" pos="relative">
      <Box pos="absolute" top={16} right={16}>
        <ThemeSwitch size="xs" />
      </Box>
      <Paper component="form" onSubmit={submit} w="100%" maw={420} shadow="xl" className="rise">
        <Stack gap="md">
          <Center>
            <Brand />
          </Center>
          <div className="glass gate-display">
            <span className={`display-status big ${go.isPending ? 'blink' : ''}`}>
              {go.isPending ? 'CHECKING' : setupMode ? 'FIRST RUN' : 'LOCKED'}
            </span>
            <span className="display-caption">
              {setupMode ? 'Choose the admin password for this panel. 8+ characters.' : 'Enter the admin password.'}
            </span>
          </div>
          <PasswordInput
            label="Password"
            required
            minLength={setupMode ? 8 : undefined}
            autoFocus
            autoComplete={setupMode ? 'new-password' : 'current-password'}
            value={password}
            onChange={(e) => setPassword(e.currentTarget.value)}
          />
          {setupMode && (
            <PasswordInput
              label="Repeat password"
              required
              autoComplete="new-password"
              value={confirm}
              onChange={(e) => setConfirm(e.currentTarget.value)}
              error={mismatch ? 'Passwords don’t match' : undefined}
            />
          )}
          {go.isError && <ErrorAlert>{go.error.message}</ErrorAlert>}
          <Button type="submit" size="md" leftSection={Icon.power} loading={go.isPending} disabled={mismatch}>
            {setupMode ? 'Set password' : 'Unlock'}
          </Button>
        </Stack>
      </Paper>
    </Center>
  )
}

function ControlPanel() {
  const qc = useQueryClient()
  const status = useQuery({
    queryKey: ['status'],
    queryFn: getStatus,
    refetchInterval: (q) => (q.state.data?.scanning ? 2000 : 15000),
  })
  const doLogout = useMutation({ mutationFn: logout, onSuccess: () => qc.invalidateQueries({ queryKey: ['auth'] }) })
  // Changes queue a rescan server-side; poll soon so it shows up.
  const refresh = () => setTimeout(() => qc.invalidateQueries({ queryKey: ['status'] }), 300)
  const doRescan = useMutation({ mutationFn: rescan, onSuccess: refresh })
  const [settingsOpen, settings] = useDisclosure(false)
  const [explorer, setExplorerState] = useState<ExplorerTarget | null>(null)
  const [explorerSeq, setExplorerSeq] = useState(0)
  const setExplorer = (t: ExplorerTarget | null) => {
    if (t) setExplorerSeq((n) => n + 1)
    setExplorerState(t)
  }
  const s = status.data
  const scanning = !!s?.scanning || doRescan.isPending

  return (
    <Container size="xl" py="lg">
      <Stack gap="lg">
        <Paper component="header" py="md" className="rise">
          <Group justify="space-between" gap="md">
            <Group gap="lg">
              <Brand />
              <Text ff="monospace" fz={11} lts="0.18em" c="dimmed" visibleFrom="md">
                DLNA · SMB · WEBDAV · SFTP{' '}
                <Text span inherit className="accent-text">
                  →
                </Text>{' '}
                STREMIO
              </Text>
            </Group>
            <Group gap="xs">
              <Button variant="default" leftSection={Icon.scan} loading={scanning} loaderProps={{ type: 'dots' }} onClick={() => doRescan.mutate()}>
                Rescan
              </Button>
              <Button variant="subtle" color="gray" leftSection={Icon.power} onClick={() => doLogout.mutate()}>
                Log out
              </Button>
              <Tooltip label="Settings">
                <ActionIcon variant="default" size="lg" onClick={settings.open} aria-label="Settings">
                  {Icon.gear}
                </ActionIcon>
              </Tooltip>
            </Group>
          </Group>
        </Paper>

        {status.isError && <ErrorAlert>Can’t reach the add-on: {status.error.message}</ErrorAlert>}
        {s && <Readout s={s} scanning={scanning} />}

        <div className="bays">
          <div>{s && <Library s={s} scanning={scanning} onChange={refresh} onOpen={setExplorer} />}</div>
          <Stack gap="lg">
            {s && <Install s={s} />}
            {s && <FoundOnNetwork found={s.discovered} scanning={scanning} scanningId={s.scanningId} onOpen={setExplorer} onChange={refresh} />}
          </Stack>
        </div>
        {explorer && (
          <Explorer key={explorerSeq} target={explorer} onClose={() => setExplorer(null)} onSaved={refresh} />
        )}
      </Stack>
      <SettingsDrawer opened={settingsOpen} onClose={settings.close} />
    </Container>
  )
}

function Readout({ s, scanning }: { s: Status; scanning: boolean }) {
  const folders = s.hosts.flatMap((h) => h.folders)
  const on = folders.filter((f) => !f.error || f.stale).length
  const time = s.built ? new Date(s.built).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hour12: false }) : '--:--'
  const cells: [string, string | number, number][] = [
    ['Movies', s.movies, 4],
    ['Shows', s.shows, 4],
    ['Files', s.items, 5],
    ['Folders live', `${on}/${folders.length}`, 5],
  ]
  return (
    <section className={`glass readout rise ${scanning ? 'scanning' : ''}`} aria-live="polite">
      {cells.map(([label, value, width]) => (
        <div className="cell" key={label}>
          <span className="cell-label">{label}</span>
          <Vfd value={value} width={width} />
        </div>
      ))}
      <div className="cell cell-status">
        <span className="cell-label">Last scan</span>
        <Vfd value={time} width={5} />
        <span className={`display-status ${scanning ? 'blink' : ''}`}>{scanning ? '◉ SCANNING' : '● READY'}</span>
      </div>
      <span className="sweep" aria-hidden="true" />
    </section>
  )
}

// A row is busy while its host or folder is being scanned (or a full scan runs).
const scanningRow = (id: string, scanning: boolean, scanningId: string) =>
  scanning && (scanningId === '' || scanningId === id || id.startsWith(scanningId + '#'))

const folderList = (h: Host): Folder[] => h.folders.map((f) => ({ path: f.addr, label: f.name, kinds: f.kinds }))

const CONTENT_KINDS: { kind: ContentKind; label: string; tip: string }[] = [
  { kind: 'video', label: 'Video', tip: 'Movies and episodes' },
  { kind: 'audio', label: 'Audio', tip: 'Music, as albums by folder (LAN Audio)' },
  { kind: 'playlist', label: 'Playlists', tip: 'm3u / m3u8 / pls files (LAN Playlists)' },
  { kind: 'torrent', label: 'Torrents', tip: '.torrent files: play from the internet swarm through Stremio, not from your LAN' },
]

/** Toggle chips for what a folder indexes; at least one stays on. */
function KindChips({ value, onChange, disabled }: { value: ContentKind[]; onChange: (k: ContentKind[]) => void; disabled?: boolean }) {
  return (
    <Chip.Group multiple value={value} onChange={(v) => v.length > 0 && onChange(v as ContentKind[])}>
      <Group gap={4} wrap="wrap">
        {CONTENT_KINDS.map((c) => (
          <Tooltip key={c.kind} label={c.tip} openDelay={400}>
            <Chip value={c.kind} size="xs" radius="sm" variant="light" disabled={disabled} color={c.kind === 'torrent' ? 'orange' : undefined}>
              {c.label}
            </Chip>
          </Tooltip>
        ))}
      </Group>
    </Chip.Group>
  )
}

function Library({ s, scanning, onChange, onOpen }: { s: Status; scanning: boolean; onChange: () => void; onOpen: (t: ExplorerTarget) => void }) {
  const hosts = [...s.hosts].sort((a, b) => KIND_ORDER[a.kind] - KIND_ORDER[b.kind] || a.name.localeCompare(b.name))
  return (
    <Paper className="rise">
      <Group justify="space-between" mb={4}>
        <PanelTitle tag="IN">Library</PanelTitle>
        <Button size="xs" leftSection={Icon.plus} onClick={() => onOpen({ conn: { kind: 'smb', addr: '' }, folders: [], step: 'connect' })}>
          Add source
        </Button>
      </Group>
      <Text size="sm" c="dimmed" mb="md">
        Only the folders you choose are scanned and shown in Stremio, each with its subfolders.
      </Text>
      {hosts.length === 0 ? (
        <Stack align="center" gap={6} py="xl" className="no-signal">
          <span className="display-status blink">NO SIGNAL</span>
          <Text size="sm" c="dimmed" ta="center">
            Nothing chosen yet. Add a source, or open a server found on your network, and pick the folders to scan.
          </Text>
        </Stack>
      ) : (
        <Stack gap="sm">
          {hosts.map((h, i) => (
            <HostCard key={h.id} host={h} i={i} scanning={scanning} scanningId={s.scanningId} onChange={onChange} onOpen={onOpen} />
          ))}
        </Stack>
      )}
    </Paper>
  )
}

function HostCard({ host, i, scanning, scanningId, onChange, onOpen }: {
  host: Host
  i: number
  scanning: boolean
  scanningId: string
  onChange: () => void
  onOpen: (t: ExplorerTarget) => void
}) {
  const color = KIND_COLOR[host.kind]
  const rescanOne = useMutation({ mutationFn: rescanSource, onSuccess: onChange })
  const save = useMutation({ mutationFn: (folders: Folder[]) => saveHost({ id: host.id, kind: host.kind, addr: host.addr }, folders), onSuccess: onChange })
  const remove = useMutation({ mutationFn: () => removeHost(host.id), onSuccess: onChange })
  const hostBusy = scanningRow(host.id, scanning, scanningId)
  const err = rescanOne.error ?? save.error ?? remove.error
  const browseIt = () => onOpen({ conn: { id: host.id, kind: host.kind, addr: host.addr, name: host.name }, folders: folderList(host), step: 'browse' })
  return (
    <Paper p="sm" className="unit live" style={{ ['--i' as string]: i, ['--kind' as string]: `var(--mantine-color-${color}-filled)` }}>
      <Group wrap="nowrap" gap="sm" align="center">
        <Led color={color} state={hostBusy ? 'busy' : host.folders.some((f) => f.error && !f.stale) ? 'fault' : 'live'} />
        <Badge variant="light" color={color} radius="sm" w={72} ff="monospace" visibleFrom="xs">
          {KIND_LABEL[host.kind]}
        </Badge>
        <Box style={{ flex: 1, minWidth: 0 }}>
          <Text fw={650} style={{ overflowWrap: 'anywhere' }}>
            {host.name}
          </Text>
          <Text ff="monospace" fz="xs" c="dimmed" style={{ overflowWrap: 'anywhere' }}>
            {host.addr || 'folders on the machine running Stremio LAN Library'}
            {host.user && ` · ${host.user}`}
          </Text>
        </Box>
        <Button size="compact-sm" variant="default" leftSection={Icon.folder} onClick={browseIt}>
          Folders
        </Button>
        <Tooltip label="Rescan this host">
          <ActionIcon variant="subtle" color="gray" loading={hostBusy} disabled={host.folders.length === 0} onClick={() => rescanOne.mutate(host.id)} aria-label={`Rescan ${host.name}`}>
            {Icon.scan}
          </ActionIcon>
        </Tooltip>
        <Tooltip label="Remove this host">
          <ActionIcon
            variant="subtle"
            color="red"
            loading={remove.isPending}
            onClick={() => confirm(`Remove ${host.name}? Its folders leave Stremio.`) && remove.mutate()}
            aria-label={`Remove ${host.name}`}
          >
            {Icon.close}
          </ActionIcon>
        </Tooltip>
      </Group>
      {err && <ErrorAlert>{err.message}</ErrorAlert>}
      <Stack gap={4} mt="xs" component="ul" className="rack folders">
        {host.folders.length === 0 && (
          <Text component="li" size="sm" c="dimmed">
            No folders chosen: nothing is scanned.{' '}
            <Anchor component="button" type="button" size="sm" onClick={browseIt}>
              Choose folders
            </Anchor>
          </Text>
        )}
        {host.folders.map((f) => {
          const busy = scanningRow(f.id, scanning, scanningId) || !f.scanned
          return (
            <Group component="li" key={f.id} wrap="nowrap" gap="sm" className="folder-row">
              <Text c="dimmed" lh={1}>{Icon.folder}</Text>
              <Box style={{ flex: 1, minWidth: 0 }}>
                <Text size="sm" fw={600} style={{ overflowWrap: 'anywhere' }}>
                  {f.name}
                </Text>
                {f.name !== f.addr && host.kind !== 'dlna' && (
                  <Text ff="monospace" fz={11} c="dimmed" style={{ overflowWrap: 'anywhere' }}>
                    {f.addr}
                  </Text>
                )}
                {f.error && (
                  <Text fz="xs" c="red" style={{ overflowWrap: 'anywhere' }}>
                    {f.error}
                    {f.stale && <Text span inherit c="dimmed"> · Showing files from the last successful scan.</Text>}
                  </Text>
                )}
                <Box mt={4}>
                  <KindChips
                    value={f.kinds}
                    disabled={save.isPending}
                    onChange={(kinds) => save.mutate(folderList(host).map((x) => (x.path === f.addr ? { ...x, kinds } : x)))}
                  />
                </Box>
              </Box>
              <Box ta="right" miw={64}>
                {busy ? (
                  <Group gap={6} justify="flex-end" wrap="nowrap">
                    <Loader size={12} />
                    <Text ff="monospace" fz={10} lts="0.14em" className="accent-text" tt="uppercase">
                      scanning
                    </Text>
                  </Group>
                ) : (
                  <>
                    <Text ff="monospace" fw={700} className="accent-text" lh={1.1}>
                      {f.items}
                    </Text>
                    <Text ff="monospace" fz={10} lts="0.14em" c="dimmed" tt="uppercase">
                      files
                    </Text>
                  </>
                )}
              </Box>
              <Tooltip label="Rescan this folder">
                <ActionIcon variant="subtle" color="gray" size="sm" disabled={busy} onClick={() => rescanOne.mutate(f.id)} aria-label={`Rescan ${f.name}`}>
                  {Icon.scan}
                </ActionIcon>
              </Tooltip>
              <Tooltip label="Stop scanning this folder">
                <ActionIcon
                  variant="subtle"
                  color="gray"
                  size="sm"
                  loading={save.isPending && save.variables?.every((x) => x.path !== f.addr)}
                  onClick={() => save.mutate(folderList(host).filter((x) => x.path !== f.addr))}
                  aria-label={`Stop scanning ${f.name}`}
                >
                  {Icon.close}
                </ActionIcon>
              </Tooltip>
            </Group>
          )
        })}
      </Stack>
    </Paper>
  )
}

function FoundOnNetwork({ found, scanning, scanningId, onOpen, onChange }: {
  found: Discovered[]
  scanning: boolean
  scanningId: string
  onOpen: (t: ExplorerTarget) => void
  onChange: () => void
}) {
  const discover = useMutation({ mutationFn: () => rescanSource('discover'), onSuccess: onChange })
  const looking = discover.isPending || (scanning && (scanningId === 'discover' || scanningId === ''))
  return (
    <Paper className="rise">
      <Group justify="space-between" mb={4}>
        <PanelTitle tag="NET">Found on network</PanelTitle>
        <Button size="xs" variant="default" leftSection={Icon.scan} loading={looking} loaderProps={{ type: 'dots' }} onClick={() => discover.mutate()}>
          Look again
        </Button>
      </Group>
      <Text size="sm" c="dimmed" mb="sm">
        Listed only, never scanned by themselves. Open one to choose folders.
      </Text>
      {found.length === 0 ? (
        <Text size="sm" c="dimmed">{looking ? 'Looking…' : 'Nothing new found. DLNA and SMB servers appear here.'}</Text>
      ) : (
        <Stack gap={6}>
          {found.map((d) => (
            <Group key={d.id} wrap="nowrap" gap="sm">
              <Badge variant="light" color={KIND_COLOR[d.kind]} radius="sm" w={64} ff="monospace">
                {KIND_LABEL[d.kind]}
              </Badge>
              <Box style={{ flex: 1, minWidth: 0 }}>
                <Text size="sm" fw={600} style={{ overflowWrap: 'anywhere' }}>
                  {d.name}
                </Text>
                {d.name !== d.addr && (
                  <Text ff="monospace" fz={11} c="dimmed" style={{ overflowWrap: 'anywhere' }}>
                    {d.addr}
                  </Text>
                )}
              </Box>
              <Button
                size="compact-sm"
                variant="default"
                onClick={() =>
                  onOpen({
                    conn: { kind: d.kind, addr: d.addr, name: d.name },
                    folders: [],
                    step: d.kind === 'smb' ? 'connect' : 'browse', // SMB may need a login first
                  })
                }
              >
                Open
              </Button>
            </Group>
          ))}
        </Stack>
      )}
    </Paper>
  )
}

// --- explorer wizard: connect → choose folders → save ---

export type ExplorerTarget = { conn: Conn; folders: Folder[]; step: 'connect' | 'browse' }

// Whether folder p sits inside chosen folder a (then it's already included).
// DLNA container IDs carry no hierarchy, so they never nest.
const isInside = (kind: Kind, a: string, p: string) =>
  kind !== 'dlna' && a !== p && (a === '' || p.startsWith(a.endsWith('/') ? a : a + '/'))

// pickName is a chosen folder's last part ("Movies" for /mnt/nas/Movies, or
// "Shows" for "Videos / Shows"): long paths read better as name + path below.
const pickName = (f: Folder) => (f.label || f.path || '/').split(/[/\\]| \/ /).filter(Boolean).pop() ?? '/'

// Mounted fresh for each target (keyed by the caller), so state starts from it.
// A wide side panel (not a modal): browse on the left, the folders picked so
// far on the right with Save always in reach; one column on a phone.
function Explorer({ target, onClose, onSaved }: { target: ExplorerTarget; onClose: () => void; onSaved: () => void }) {
  const [conn, setConn] = useState<Conn>(target.conn)
  const [step, setStep] = useState<'connect' | 'browse'>(target.step)
  const [stack, setStackState] = useState([{ name: target.conn.name || target.conn.addr || 'This machine', path: '' }])
  const [filter, setFilter] = useState('')
  const [sel, setSel] = useState<Folder[]>(target.folders)
  const setStack = (st: typeof stack) => {
    setFilter('') // a new folder: start unfiltered
    setStackState(st)
  }

  const cur = stack[stack.length - 1] ?? { name: '', path: '' }
  const hostKey = conn.id ?? conn.kind + conn.addr
  const list = useQuery({
    queryKey: ['browse', hostKey, conn.user, conn.pass, cur.path],
    queryFn: () => browse(conn, cur.path),
    enabled: step === 'browse',
    retry: false,
    staleTime: 30_000,
  })
  const save = useMutation({
    mutationFn: () => saveHost(conn, sel),
    onSuccess: () => {
      onSaved()
      onClose()
    },
  })

  const here = list.data?.path ?? cur.path // e.g. SFTP home, DLNA root "0"
  const atTop = stack.length === 1
  const hereSelectable = !(atTop && (conn.kind === 'smb' || conn.kind === 'local' || conn.kind === 'sftp')) // shares / starting points
  // Local and SFTP folders are labelled by their path; elsewhere by the breadcrumb names.
  const byPath = conn.kind === 'local' || conn.kind === 'sftp'
  const labelFor = (name: string, path: string) => (byPath ? path : [...stack.slice(1).map((s) => s.name), name].join(' / ') || name)
  const chosen = (p: string) => sel.some((f) => f.path === p)
  const included = (p: string) => sel.some((f) => isInside(conn.kind, f.path, p))
  const toggle = (path: string, label: string) =>
    setSel((s) =>
      s.some((f) => f.path === path)
        ? s.filter((f) => f.path !== path)
        : [...s.filter((f) => !isInside(conn.kind, path, f.path)), { path, label, kinds: DEFAULT_KINDS }],
    )
  const isNew = !conn.id
  const needsLogin = conn.kind === 'smb' || conn.kind === 'webdav' || conn.kind === 'sftp'
  const entries = (list.data?.entries ?? []).filter((e) => e.name.toLowerCase().includes(filter.trim().toLowerCase()))

  return (
    <Drawer
      opened
      onClose={onClose}
      position="right"
      size={step === 'connect' ? 'min(560px, 100%)' : 'min(1040px, 100%)'} // wide once browsing
      title={<PanelTitle tag={step === 'connect' ? '1/2' : '2/2'}>{step === 'connect' ? 'Connect' : `Choose folders · ${stack[0]?.name ?? ''}`}</PanelTitle>}
      overlayProps={{ backgroundOpacity: 0.45, blur: 2 }}
      styles={{ content: { display: 'flex', flexDirection: 'column' }, body: { flex: 1, minHeight: 0, display: 'flex', flexDirection: 'column' } }}
    >
      {step === 'connect' ? (
        <form
          onSubmit={(e) => {
            e.preventDefault()
            setStack([{ name: conn.name || conn.addr || 'This machine', path: '' }])
            setStep('browse')
          }}
        >
          <Stack gap="sm">
            {isNew && (
              <SegmentedControl
                fullWidth
                value={conn.kind}
                onChange={(v) => setConn({ kind: v as Kind, addr: '' })}
                aria-label="Server type"
                data={KINDS.map((k) => ({ value: k.kind, label: <Text span ff="monospace" fz="xs" fw={700}>{k.label}</Text> }))}
              />
            )}
            <Text size="sm" c="dimmed">
              {KINDS.find((k) => k.kind === conn.kind)!.hint}
            </Text>
            {conn.kind !== 'local' && (
              <TextInput
                label={conn.kind === 'smb' || conn.kind === 'sftp' ? 'Host' : 'URL'}
                required
                disabled={!isNew}
                value={conn.addr}
                onChange={(e) => setConn({ ...conn, addr: e.currentTarget.value })}
                placeholder={PLACEHOLDER[conn.kind]}
                styles={mono}
                data-autofocus
              />
            )}
            {needsLogin && (
              <SimpleGrid cols={{ base: 1, xs: 2 }} spacing="sm">
                <TextInput
                  label="User"
                  value={conn.user ?? ''}
                  onChange={(e) => setConn({ ...conn, user: e.currentTarget.value })}
                  autoComplete="username"
                  required={conn.kind === 'sftp'}
                  placeholder={conn.kind === 'sftp' ? 'pi' : 'guest'}
                />
                <PasswordInput
                  label="Password"
                  value={conn.pass ?? ''}
                  onChange={(e) => setConn({ ...conn, pass: e.currentTarget.value })}
                  autoComplete="current-password"
                  placeholder={conn.kind === 'sftp' ? 'blank = SSH key' : 'optional'}
                />
              </SimpleGrid>
            )}
            <Button type="submit" rightSection={Icon.chevron}>
              Connect
            </Button>
          </Stack>
        </form>
      ) : (
        <div className="explorer">
          {/* left: where you are, its subfolders */}
          <div>
            <Group gap="xs" wrap="nowrap">
              <Tooltip label="Up one folder">
                <ActionIcon variant="default" disabled={atTop} onClick={() => setStack(stack.slice(0, -1))} aria-label="Up one folder">
                  {Icon.up}
                </ActionIcon>
              </Tooltip>
              <Breadcrumbs separator="›" style={{ flexWrap: 'wrap', minWidth: 0 }}>
                {stack.map((s, i) => (
                  <Anchor key={i} component="button" type="button" size="sm" onClick={() => setStack(stack.slice(0, i + 1))} fw={i === stack.length - 1 ? 700 : 400}>
                    {s.name}
                  </Anchor>
                ))}
              </Breadcrumbs>
            </Group>
            {list.isError ? (
              <>
                <ErrorAlert>{list.error.message}</ErrorAlert>
                {isNew && needsLogin && (
                  <Button variant="default" onClick={() => setStep('connect')}>
                    Back: check the address and login
                  </Button>
                )}
              </>
            ) : (
              <>
                {hereSelectable && (
                  <Paper withBorder p="xs" radius="sm">
                    <Checkbox
                      label={
                        <Text size="sm" fw={600}>
                          Scan this folder{' '}
                          <Text span c="dimmed" size="sm" fw={400}>
                            ({list.data ? `${list.data.videos} media files here, plus subfolders` : '…'})
                          </Text>
                        </Text>
                      }
                      checked={chosen(here) || included(here)}
                      disabled={included(here) || !list.data}
                      onChange={() =>
                        toggle(here, atTop ? (conn.kind === 'dlna' ? 'Everything' : here || '/') : byPath ? here : stack.slice(1).map((s) => s.name).join(' / '))
                      }
                    />
                  </Paper>
                )}
                {(list.data?.entries.length ?? 0) > 8 && (
                  <TextInput
                    size="xs"
                    placeholder={`Filter ${list.data!.entries.length} folders…`}
                    value={filter}
                    onChange={(e) => setFilter(e.currentTarget.value)}
                    rightSection={filter && <CloseButton size="xs" onClick={() => setFilter('')} aria-label="Clear filter" />}
                    aria-label="Filter folders"
                  />
                )}
                <div className="explorer-scroll">
                  {list.isPending ? (
                    <Center py="lg">
                      <Loader size="sm" />
                    </Center>
                  ) : entries.length === 0 ? (
                    <Text size="sm" c="dimmed" py="sm">
                      {filter ? 'No folders match.' : 'No subfolders.'}
                    </Text>
                  ) : (
                    <Stack gap={2}>
                      {entries.map((e) => (
                        <Group key={e.path} wrap="nowrap" gap="xs" className="explorer-row">
                          <Checkbox
                            aria-label={`Scan ${e.name}`}
                            checked={chosen(e.path) || included(e.path)}
                            disabled={included(e.path)}
                            onChange={() => toggle(e.path, labelFor(e.name, e.path))}
                          />
                          <UnstyledButton style={{ flex: 1, minWidth: 0 }} onClick={() => setStack([...stack, { name: e.name, path: e.path }])} aria-label={`Open ${e.name}`}>
                            <Group gap={6} wrap="nowrap">
                              <Text c="dimmed" lh={1}>{Icon.folder}</Text>
                              <Text size="sm" style={{ overflowWrap: 'anywhere' }}>{e.name}</Text>
                              <Text c="dimmed" lh={1} ml="auto">{Icon.chevron}</Text>
                            </Group>
                          </UnstyledButton>
                        </Group>
                      ))}
                    </Stack>
                  )}
                </div>
              </>
            )}
          </div>

          {/* right: picked so far, Save always visible */}
          <div className="explorer-picks">
            <Eyebrow>Folders to scan ({sel.length})</Eyebrow>
            <div className="explorer-scroll">
              {sel.length === 0 ? (
                <Text size="sm" c="dimmed">Tick folders on the left. Nothing is scanned until you do.</Text>
              ) : (
                <Stack gap={6}>
                  {sel.map((f) => (
                    <Group key={f.path} wrap="nowrap" gap="xs" className="explorer-row" align="flex-start">
                      <Text c="dimmed" lh={1} mt={3}>{Icon.folder}</Text>
                      <Box style={{ flex: 1, minWidth: 0 }}>
                        <Text size="sm" fw={600} style={{ overflowWrap: 'anywhere' }}>
                          {pickName(f)}
                        </Text>
                        {pickName(f) !== (f.label || f.path || '/') && (
                          <Text size="xs" c="dimmed" ff="monospace" style={{ overflowWrap: 'anywhere' }} title={f.label || f.path}>
                            {f.label || f.path}
                          </Text>
                        )}
                        <Box mt={4}>
                          <KindChips
                            value={f.kinds ?? DEFAULT_KINDS}
                            onChange={(kinds) => setSel(sel.map((x) => (x.path === f.path ? { ...x, kinds } : x)))}
                          />
                        </Box>
                      </Box>
                      <CloseButton size="sm" onClick={() => setSel(sel.filter((x) => x.path !== f.path))} aria-label={`Remove ${f.label}`} />
                    </Group>
                  ))}
                </Stack>
              )}
            </div>
            {save.isError && <ErrorAlert>{save.error.message}</ErrorAlert>}
            <Group justify="space-between">
              {isNew && needsLogin ? (
                <Button variant="subtle" color="gray" onClick={() => setStep('connect')}>
                  Back
                </Button>
              ) : (
                <span />
              )}
              <Button onClick={() => save.mutate()} loading={save.isPending} disabled={list.isError && isNew}>
                {isNew ? 'Save and scan' : 'Save'}
              </Button>
            </Group>
          </div>
        </div>
      )}
    </Drawer>
  )
}

function Install({ s }: { s: Status }) {
  const qc = useQueryClient()
  const rotate = useMutation({ mutationFn: newToken, onSuccess: () => qc.invalidateQueries({ queryKey: ['status'] }) })
  const open = useMutation({ mutationFn: openExternal })
  const confirmRotate = () => {
    if (confirm('Make a new link? Stremio installs using the current link stop working until you reinstall.')) rotate.mutate()
  }
  return (
    <Paper className="rise">
      <PanelTitle tag="OUT">Install in Stremio</PanelTitle>
      <div className="glass tape" title={s.manifestUrl}>
        {s.manifestUrl}
      </div>
      <Group gap="xs" mb="md">
        <Button
          component="a"
          href={s.installUrl}
          leftSection={Icon.play}
          loading={open.isPending}
          onClick={(e: React.MouseEvent) => {
            if (!inDesktopApp) return // a browser hands stremio:// to the OS itself
            e.preventDefault() // the desktop window would navigate away and go blank
            open.mutate(s.installUrl)
          }}
        >
          Open in Stremio
        </Button>
        {window.isSecureContext && (
          <CopyButton value={s.manifestUrl} timeout={1500}>
            {({ copied, copy }) => (
              <Button variant="default" leftSection={copied ? Icon.check : Icon.copy} onClick={copy}>
                {copied ? 'Copied' : 'Copy'}
              </Button>
            )}
          </CopyButton>
        )}
      </Group>
      <Alert variant="light" color="stremio" icon={Icon.key} py="xs">
        <Text size="sm">
          The link holds a secret key. Anyone with it can browse and play your library.{' '}
          <Anchor component="button" type="button" size="sm" onClick={confirmRotate} disabled={rotate.isPending}>
            Make a new link
          </Anchor>
        </Text>
      </Alert>
      {rotate.isError && <ErrorAlert>{rotate.error.message}</ErrorAlert>}
      {open.isError && <ErrorAlert>{open.error.message}. Is Stremio installed? You can also copy the link into Stremio → Add-ons.</ErrorAlert>}
    </Paper>
  )
}

function ThemeSwitch({ size = 'sm', fullWidth }: { size?: 'xs' | 'sm'; fullWidth?: boolean }) {
  const { colorScheme, setColorScheme } = useMantineColorScheme()
  return (
    <SegmentedControl
      size={size}
      fullWidth={fullWidth}
      value={colorScheme}
      onChange={(v) => setColorScheme(v as MantineColorScheme)}
      aria-label="Theme"
      data={[
        { value: 'auto', label: 'Auto' },
        { value: 'light', label: 'Light' },
        { value: 'dark', label: 'Dark' },
      ]}
    />
  )
}

// Settings: vertical tabs down the left, one group at a time; tabs across
// the top on a phone.
function SettingsDrawer({ opened, onClose }: { opened: boolean; onClose: () => void }) {
  const phone = useMediaQuery('(max-width: 48em)')
  const tabs: { value: string; label: string; hint: string; body: ReactNode }[] = [
    { value: 'library', label: 'Library', hint: 'Pictures for your videos in Stremio.', body: <LibrarySettings /> },
    {
      value: 'appearance',
      label: 'Appearance',
      hint: 'Saved in this browser.',
      body: (
        <>
          <Text fw={650}>Theme</Text>
          <ThemeSwitch fullWidth />
        </>
      ),
    },
    ...(inDesktopApp ? [{ value: 'desktop', label: 'Desktop app', hint: 'The window and the tray icon.', body: <DesktopSettings /> }] : []),
    { value: 'password', label: 'Admin password', hint: 'Changing it logs out every other browser.', body: <AdminPassword /> },
  ]
  return (
    <Drawer
      opened={opened}
      onClose={onClose}
      position="right"
      size="min(860px, 100%)"
      title={<PanelTitle tag="SYS">Settings</PanelTitle>}
      overlayProps={{ backgroundOpacity: 0.45, blur: 2 }}
    >
      <Tabs defaultValue="library" orientation={phone ? 'horizontal' : 'vertical'} variant="pills" keepMounted={false}>
        <Tabs.List className="settings-tabs">
          {tabs.map((t) => (
            <Tabs.Tab key={t.value} value={t.value}>
              {t.label}
            </Tabs.Tab>
          ))}
        </Tabs.List>
        {tabs.map((t) => (
          <Tabs.Panel key={t.value} value={t.value} className="settings-panel">
            <Stack gap="sm">
              <div>
                <Text fw={700} size="lg">{t.label}</Text>
                <Text size="sm" c="dimmed">{t.hint}</Text>
              </div>
              {t.body}
            </Stack>
          </Tabs.Panel>
        ))}
      </Tabs>
    </Drawer>
  )
}

// Thumbnails: a frame of each video, made with ffmpegthumbnailer or ffmpeg after scans.
function LibrarySettings() {
  const qc = useQueryClient()
  const prefs = useQuery({ queryKey: ['library'], queryFn: getLibrary })
  const save = useMutation({
    mutationFn: setLibrary,
    onMutate: (patch) => qc.setQueryData(['library'], (p?: LibraryPrefs) => p && { ...p, ...patch }),
    onSettled: () => qc.invalidateQueries({ queryKey: ['library'] }),
  })
  const clear = useMutation({
    mutationFn: clearThumbs,
    onSettled: () => qc.invalidateQueries({ queryKey: ['library'] }),
  })
  const p = prefs.data
  return (
    <Stack gap="xs">
      {prefs.isError && <ErrorAlert>{prefs.error.message}</ErrorAlert>}
      {p && (
        <Group justify="space-between" wrap="nowrap" gap="md" align="flex-start">
          <div>
            <Text fw={650}>Generate thumbnails</Text>
            <Text size="sm" c="dimmed">
              {p.tool
                ? `After each scan, grab a frame of every video without a poster, so you can tell them apart in Stremio. The first run takes a while. Made with ${p.tool}.`
                : 'Needs ffmpegthumbnailer or ffmpeg on the machine running this server (e.g. sudo apt install ffmpegthumbnailer).'}
            </Text>
          </div>
          <Switch
            checked={p.thumbnails}
            disabled={save.isPending || (!p.tool && !p.thumbnails)}
            onChange={() => save.mutate({ thumbnails: !p.thumbnails })}
            aria-label="Generate thumbnails"
          />
        </Group>
      )}
      {p && <ThumbFolder key={p.thumbDir} prefs={p} />}
      {p && p.thumbCount > 0 && (
        <Group justify="space-between" wrap="nowrap" gap="md">
          <Text size="sm" c="dimmed">
            {p.thumbCount} thumbnails saved.{p.thumbnails ? ' Clearing makes them again.' : ''}
          </Text>
          <Button variant="default" size="xs" loading={clear.isPending} onClick={() => clear.mutate()}>
            Clear thumbnails
          </Button>
        </Group>
      )}
      {save.isError && <ErrorAlert>{save.error.message}</ErrorAlert>}
      {clear.isError && <ErrorAlert>{clear.error.message}</ErrorAlert>}
    </Stack>
  )
}

// Where thumbnails are saved. Changing it starts fresh in the new folder; the
// old one's thumbnails stay where they are.
function ThumbFolder({ prefs }: { prefs: LibraryPrefs }) {
  const qc = useQueryClient()
  const [dir, setDir] = useState(prefs.thumbDir)
  const save = useMutation({
    mutationFn: (thumbDir: string) => setLibrary({ thumbDir }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['library'] }),
  })
  const isDefault = prefs.thumbDir === prefs.thumbDirDefault
  const submit = (e: FormEvent) => {
    e.preventDefault()
    save.mutate(dir.trim() === prefs.thumbDirDefault ? '' : dir)
  }
  return (
    <form onSubmit={submit}>
      <Stack gap="xs">
        <TextInput
          label="Thumbnail folder"
          description="A full path on the machine running this server. Files already in it are left alone."
          value={dir}
          onChange={(e) => setDir(e.currentTarget.value)}
          placeholder={prefs.thumbDirDefault}
          spellCheck={false}
        />
        <Group gap="xs" justify="flex-end">
          {!isDefault && (
            <Button variant="subtle" size="xs" disabled={save.isPending} onClick={() => save.mutate('')}>
              Use default
            </Button>
          )}
          <Button type="submit" variant="default" size="xs" loading={save.isPending} disabled={dir.trim() === prefs.thumbDir}>
            Save folder
          </Button>
        </Group>
        {save.isError && <ErrorAlert>{save.error.message}</ErrorAlert>}
      </Stack>
    </form>
  )
}

// Desktop app only: window and tray behaviour, start at login.
function DesktopSettings() {
  const qc = useQueryClient()
  const prefs = useQuery({ queryKey: ['desktop'], queryFn: getDesktop })
  const save = useMutation({
    mutationFn: setDesktop,
    onMutate: (p) => qc.setQueryData(['desktop'], p), // switches move at once
    onSettled: () => qc.invalidateQueries({ queryKey: ['desktop'] }),
  })
  const p = prefs.data
  const set = (patch: Partial<DesktopPrefs>) => p && save.mutate({ ...p, ...patch })
  const rows: { key: keyof DesktopPrefs; label: string; hint: string; on: boolean }[] = p
    ? [
        { key: 'quitOnClose', label: 'Keep running in the tray', hint: 'Closing the window hides it; Stremio keeps working. Quit from the tray icon.', on: !p.quitOnClose },
        { key: 'startHidden', label: 'Start hidden in the tray', hint: 'No window at launch; open it from the tray icon.', on: p.startHidden },
        { key: 'autostart', label: 'Start when I log in', hint: 'Starts hidden in the tray, so your library is always available to Stremio.', on: p.autostart },
      ]
    : []
  return (
    <Stack gap="xs">
      {prefs.isError && <ErrorAlert>{prefs.error.message}</ErrorAlert>}
      {rows.map((r) => (
        <Group key={r.key} justify="space-between" wrap="nowrap" gap="md" align="flex-start">
          <div>
            <Text fw={650}>{r.label}</Text>
            <Text size="sm" c="dimmed">{r.hint}</Text>
          </div>
          <Switch
            checked={r.on}
            disabled={save.isPending}
            onChange={() => set({ [r.key]: r.key === 'quitOnClose' ? r.on : !r.on })}
            aria-label={r.label}
          />
        </Group>
      ))}
      {save.isError && <ErrorAlert>{save.error.message}</ErrorAlert>}
    </Stack>
  )
}

function AdminPassword() {
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const change = useMutation({
    mutationFn: () => changePassword(current, next),
    onSuccess: () => {
      setCurrent('')
      setNext('')
    },
  })
  const submit = (e: FormEvent) => {
    e.preventDefault()
    change.mutate()
  }
  return (
    <form onSubmit={submit}>
      <Stack gap="sm">
        <PasswordInput label="Current" required autoComplete="current-password" value={current} onChange={(e) => setCurrent(e.currentTarget.value)} />
        <PasswordInput label="New" required minLength={8} autoComplete="new-password" value={next} onChange={(e) => setNext(e.currentTarget.value)} />
        {change.isError && <ErrorAlert>{change.error.message}</ErrorAlert>}
        {change.isSuccess && (
          <Alert variant="light" color="green" py="xs">
            <Text size="sm">Password changed. Other browsers are logged out.</Text>
          </Alert>
        )}
        <Button type="submit" variant="default" loading={change.isPending}>
          Change password
        </Button>
      </Stack>
    </form>
  )
}
