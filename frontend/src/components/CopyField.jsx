import { useState } from 'react'
import { Check, Copy } from 'lucide-react'
import { copyText } from '../lib/clipboard'
import { Button } from '@/components/ui/button'

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
    <div className="flex min-w-0 items-center justify-between gap-3 py-1.5">
      <div className="min-w-0">
        <span className="block text-[10px] uppercase tracking-[0.12em] text-muted-foreground">{label}</span>
        <code
          className={`break-all font-mono text-xs text-foreground ${secret ? 'blur-[3px] transition hover:blur-none' : ''}`}
        >
          {text}
        </code>
        {hint && <span className="mt-0.5 block text-[10px] text-muted-foreground">{hint}</span>}
      </div>
      <Button
        type="button"
        variant="outline"
        size="icon-sm"
        onClick={copy}
        title="Copy"
        aria-label={`Copy ${label}`}
        className="shrink-0 text-muted-foreground hover:text-primary"
      >
        {copied ? <Check className="text-primary" /> : <Copy />}
      </Button>
    </div>
  )
}
