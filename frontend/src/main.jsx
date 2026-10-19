import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import './index.css'
import App from './App.jsx'
import AuthGate from './components/AuthGate.jsx'
import { installAuthInterceptor } from './lib/api'

// Theme lives on <html>: data-theme drives our palette, `.dark` drives the
// shadcn primitives' dark: variants. Both must agree before the first paint.
function applyThemeClass() {
  const light = document.documentElement.getAttribute('data-theme') === 'light'
  document.documentElement.classList.toggle('dark', !light)
}
applyThemeClass()

// Installed before the first render so no request can slip past it.
installAuthInterceptor()

createRoot(document.getElementById('root')).render(
  <StrictMode>
    <BrowserRouter>
      <AuthGate>
        <App />
      </AuthGate>
    </BrowserRouter>
  </StrictMode>,
)
