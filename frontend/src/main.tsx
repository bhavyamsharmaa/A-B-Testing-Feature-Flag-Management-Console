import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import { ConfigError } from './components/ConfigError'
import { missingEnv } from './config'

const container = document.getElementById('root')
if (!container) {
  throw new Error('root element not found')
}
const root = createRoot(container)

if (missingEnv.length > 0) {
  // Don't load App (and the Supabase client, which throws without a URL).
  root.render(<ConfigError missing={missingEnv} />)
} else {
  void import('./App').then(({ default: App }) => {
    root.render(
      <StrictMode>
        <App />
      </StrictMode>,
    )
  })
}
