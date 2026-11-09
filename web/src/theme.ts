import { createTheme, localStorageColorSchemeManager, type MantineColorsTuple } from '@mantine/core'

// Stremio purple (#7b5bf5 at index 5) and a purple-tinted charcoal for the dark scheme.
const stremio: MantineColorsTuple = [
  '#f1edff', '#e0d8ff', '#bfaeff', '#9d82fb', '#8a69f8',
  '#7b5bf5', '#6c4ae6', '#5a3acb', '#4a2fa8', '#3b2585',
]
const dark: MantineColorsTuple = [
  '#e9e6f2', '#c4c0d4', '#8e8aa3', '#696580', '#35324a',
  '#25233a', '#1a182b', '#131222', '#0e0d18', '#09080f',
]

export const theme = createTheme({
  primaryColor: 'stremio',
  primaryShade: { light: 6, dark: 5 },
  autoContrast: true,
  colors: { stremio, dark },
  fontFamily: "'Figtree Variable', 'Segoe UI', sans-serif",
  fontFamilyMonospace: "'JetBrains Mono', ui-monospace, monospace",
  headings: { fontFamily: "'Chakra Petch', 'Segoe UI', sans-serif", fontWeight: '700' },
  defaultRadius: 'md',
  cursorType: 'pointer',
  components: {
    Paper: { defaultProps: { withBorder: true, p: 'lg' } },
    Button: { defaultProps: { fw: 600 } },
  },
})

// Same key the pre-paint script in index.html reads.
export const colorSchemeManager = localStorageColorSchemeManager({ key: 'stremio-lan-theme' })

/** Source kind → Mantine colour for its badge, stripe and status light. */
export const KIND_COLOR = { local: 'lime', dlna: 'cyan', smb: 'green', webdav: 'pink', sftp: 'orange' } as const
