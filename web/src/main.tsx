import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { MantineProvider } from '@mantine/core'
import { MutationCache, QueryCache, QueryClient, QueryClientProvider } from '@tanstack/react-query'
import '@fontsource/chakra-petch/latin-500.css'
import '@fontsource/chakra-petch/latin-700.css'
import '@fontsource/jetbrains-mono/latin-400.css'
import '@fontsource/jetbrains-mono/latin-700.css'
import '@fontsource-variable/figtree/wght.css'
import '@mantine/core/styles.css'
import './app.css'
import { HttpError } from './api'
import { colorSchemeManager, theme } from './theme'
import App from './App.tsx'

// Any 401 means the session ended: re-check auth, which shows the login screen.
const onError = (err: Error) => {
  if (err instanceof HttpError && err.status === 401) queryClient.invalidateQueries({ queryKey: ['auth'] })
}
const queryClient: QueryClient = new QueryClient({
  queryCache: new QueryCache({ onError }),
  mutationCache: new MutationCache({ onError }),
})

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <MantineProvider theme={theme} colorSchemeManager={colorSchemeManager} defaultColorScheme="auto">
      <QueryClientProvider client={queryClient}>
        <App />
      </QueryClientProvider>
    </MantineProvider>
  </StrictMode>,
)
