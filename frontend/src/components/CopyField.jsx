import { useState } from 'react'
import { copyText } from '../lib/clipboard'

/* One label/value row with a copy button.
 *
 * The value is what lands in the clipboard, so a secret renders blurred and is
 * still copied in full. The Hub is usually served over plain HTTP on the LAN,
 * where navigator.clipboard does not exist — lib/clipboard falls back to a
 * temp textarea, which is why this goes through it instead of calling the API. */
export default function CopyField({ label, value, secret = false, hint }) {
  const [copied, setCopied] = useState(false)
  const text = String(value ?? '')
  const copy = async () => {
    if (await copyText(text)) {
      setCopied(true)
      setTimeout(() => setCopied(false), 1200)
    }
  }
  return (
    <div className="flex items-center justify-between gap-3 min-w-0">
      <div className="min-w-0">
        <span className="text-[10px] text-text-dim font-label block">{label}</span>
        <code className={`text-xs text-text-muted font-mono break-all ${secret ? 'blur-[3px] hover:blur-none transition' : ''}`}>
          {text}
        </code>
        {hint && <span className="text-[10px] text-text-dim block mt-0.5">{hint}</span>}
      </div>
      <button
        onClick={copy}
        className="shrink-0 p-1.5 bg-surface border-none rounded-lg cursor-pointer text-text-dim hover:text-primary transition-colors"
        title="Copy"
      >
        {copied ? (
          <svg className="w-3.5 h-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round"><polyline points="20 6 9 17 4 12" /></svg>
        ) : (
          <svg className="w-3.5 h-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" /><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1" /></svg>
        )}
      </button>
    </div>
  )
}
