import { Suspense, lazy, useCallback, useEffect, useRef, useState } from 'react'
import {
  Check,
  ChevronLeft,
  ChevronRight,
  Download,
  ExternalLink,
  FolderOpen,
  Info,
  Link2,
  MoreHorizontal,
  Music,
  Trash2,
  Upload,
  X,
} from 'lucide-react'
import { copyText } from '../lib/clipboard'
import { size } from '../lib/size'
import { PageHeader, EmptyState } from '@/components/page-shell'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { Checkbox } from '@/components/ui/checkbox'
import { Skeleton } from '@/components/ui/skeleton'
import { ToggleGroup, ToggleGroupItem } from '@/components/ui/toggle-group'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'

const MeshView = lazy(() => import('../components/MeshView'))

/* My files: the images, videos, 3D models and songs this account made with the Hub's
 * media tools (/mcp), plus the files it uploaded for them to work on.
 *
 * Everything here is private to its owner (daemon/services/media_store.py);
 * everything stays until deleted. This page is where people see what they have, download
 * what they want to keep, delete the rest, and upload a local file to get a
 * link the tools accept. /upload lands here too. Only the signed-in account's
 * own files are ever listed, admins included. Each result carries the model
 * that made it, so a picture worth repeating can be traced back to one. */

const FILTERS = [
  { id: 'all', label: 'All', test: () => true },
  { id: 'image', label: 'Images', test: (f) => f.kind === 'image' },
  { id: 'video', label: 'Videos', test: (f) => f.kind === 'video' },
  { id: 'mesh', label: '3D', test: (f) => f.kind === 'mesh' },
  { id: 'audio', label: 'Music', test: (f) => f.kind === 'audio' },
  { id: 'upload', label: 'Uploads', test: (f) => f.upload },
]

const KIND_LABEL = { image: 'Image', video: 'Video', mesh: '3D model', audio: 'Song' }

const headCell =
  'h-8 text-[10px] font-medium uppercase tracking-[0.12em] text-muted-foreground'

export default function Files() {
  const [data, setData] = useState(null)
  const [error, setError] = useState(null)
  const [filter, setFilter] = useState('all')
  const [selected, setSelected] = useState(() => new Set())
  const [sending, setSending] = useState(0)     // uploads in flight
  const [failures, setFailures] = useState([])  // the last batch's refused files
  const [dragging, setDragging] = useState(false)
  const [viewing, setViewing] = useState(null)  // the file name open in the viewer
  const [details, setDetails] = useState(false)  // the viewer's details panel, kept while on the page
  const [deleting, setDeleting] = useState(null)  // the names waiting on the delete dialog
  const picker = useRef(null)

  const load = useCallback(async () => {
    const res = await fetch('/api/media')
    if (!res.ok) {
      setError('Could not load your files.')
      return
    }
    setData(await res.json())
    setError(null)
  }, [])

  useEffect(() => { load() }, [load])

  // A 3D model's card shows a still of the model itself. The daemon cannot
  // draw one, so the first page to list a model without one draws it here,
  // one model at a time, and hands it back; every later visit just shows it.
  const drawing = useRef(new Set())
  useEffect(() => {
    const todo = (data?.files || []).filter((f) => f.kind === 'mesh' && !f.rendered && !drawing.current.has(f.name))
    if (!todo.length) return
    todo.forEach((f) => drawing.current.add(f.name))
    ;(async () => {
      const { snapshot } = await import('../lib/mesh3d')
      for (const f of todo) {
        try {
          const res = await fetch(`/api/media/${f.name}/thumb`, { method: 'PUT', body: await snapshot(f.path) })
          if (!res.ok) continue
          setData((d) => ({ ...d, files: d.files.map((x) => (x.name === f.name ? { ...x, rendered: true } : x)) }))
        } catch {
          // No WebGL, or the model would not load: the card keeps its picture.
        }
      }
    })()
  }, [data])

  // A finished upload shows up as the first card (newest first), where its
  // link is; only a refused file needs a message of its own.
  const upload = useCallback(async (list) => {
    if (!list.length) return
    setFailures([])
    setSending((n) => n + list.length)
    for (const file of list) {
      try {
        const res = await fetch('/api/uploads', { method: 'POST', body: file })
        const body = await res.json().catch(() => ({}))
        if (!res.ok) throw new Error(body.detail || `Upload failed (HTTP ${res.status}).`)
      } catch (e) {
        setFailures((f) => [...f, `${file.name || 'Pasted image'}: ${e.message}`])
      }
      setSending((n) => n - 1)
      load()
    }
  }, [load])

  // Paste anywhere on the page, e.g. a screenshot straight from the clipboard.
  useEffect(() => {
    const onPaste = (e) => {
      const list = [...(e.clipboardData?.files || [])]
      if (list.length) upload(list)
    }
    document.addEventListener('paste', onPaste)
    return () => document.removeEventListener('paste', onPaste)
  }, [upload])

  const remove = async (names) => {
    for (const name of names) {
      await fetch(`/api/media/${name}`, { method: 'DELETE' })
    }
    setSelected(new Set())
    load()
  }

  // Deleting always asks first, in the AlertDialog below.
  const askRemove = (names) => setDeleting(names)
  const confirmRemove = async () => {
    if (!deleting) return
    const names = deleting
    setDeleting(null)
    await remove(names)
  }

  const toggle = (name) => setSelected((s) => {
    const next = new Set(s)
    next.has(name) ? next.delete(name) : next.add(name)
    return next
  })

  if (error) {
    return (
      <div className="page">
        <div className="rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2">
          <p className="m-0 text-sm leading-relaxed text-destructive">{error}</p>
        </div>
      </div>
    )
  }

  if (!data) {
    return (
      <div className="page flex flex-col gap-3">
        <p className="m-0 text-sm text-muted-foreground">Loading files…</p>
        <Card className="gap-0 p-0 shadow-none">
          {[0, 1, 2, 3].map((i) => (
            <div key={i} className="flex items-center gap-3 border-b border-border px-3 py-3 last:border-0">
              <Skeleton className="size-4 rounded-[4px]" />
              <Skeleton className="size-12 shrink-0" />
              <Skeleton className="h-3.5 w-40" />
              <Skeleton className="ml-auto h-5 w-16" />
              <Skeleton className="h-3 w-14" />
            </div>
          ))}
        </Card>
      </div>
    )
  }

  const test = FILTERS.find((f) => f.id === filter).test
  const shown = data.files.filter(test)
  const chosen = data.files.filter((f) => selected.has(f.name))
  const allShown = shown.length > 0 && shown.every((f) => selected.has(f.name))
  const viewable = shown.filter((f) => f.kind !== 'audio')
  const viewIndex = viewable.findIndex((f) => f.name === viewing)
  const deleteWhat = deleting ? (deleting.length === 1 ? 'this file' : `${deleting.length} files`) : ''

  return (
    <div
      className="page flex flex-col gap-6"
      onDragOver={(e) => { e.preventDefault(); setDragging(true) }}
      onDragLeave={(e) => { if (!e.currentTarget.contains(e.relatedTarget)) setDragging(false) }}
      onDrop={(e) => { e.preventDefault(); setDragging(false); upload([...e.dataTransfer.files]) }}
    >
      <PageHeader
        title="My files"
        description="Images, videos, 3D models and songs you made with the Hub, and files you uploaded for it. Only you can open them. Upload a file to get a link you can give the image and video tools. Everything here stays until you delete it."
        actions={
          <>
            <input
              ref={picker}
              type="file"
              multiple
              accept="image/*,video/mp4,video/quicktime,audio/*"
              className="hidden"
              onChange={(e) => { upload([...e.target.files]); e.target.value = '' }}
            />
            <Button onClick={() => picker.current?.click()}>
              <Upload />
              Upload files
            </Button>
          </>
        }
      />

      {failures.map((msg) => (
        <div key={msg} className="rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2">
          <p className="m-0 text-xs leading-relaxed text-destructive">{msg}</p>
        </div>
      ))}

      {/* toolbar: the kind filters and, on the right, the bulk selection toggle */}
      <div className="flex flex-wrap items-center gap-3">
        <ToggleGroup
          type="single"
          value={filter}
          onValueChange={(next) => { if (next) setFilter(next) }}
          variant="outline"
          size="sm"
          className="max-w-full overflow-x-auto"
        >
          {FILTERS.map((f) => (
            <ToggleGroupItem
              key={f.id}
              value={f.id}
              className="shrink-0 whitespace-nowrap data-[state=on]:border-primary/40 data-[state=on]:bg-primary/10 data-[state=on]:text-primary"
            >
              {f.label}
              <span className="ml-1.5 font-mono text-[11px] tabular-nums text-muted-foreground">
                {data.files.filter(f.test).length}
              </span>
            </ToggleGroupItem>
          ))}
        </ToggleGroup>
        {shown.length > 1 && (
          <Button
            variant="ghost"
            size="sm"
            className="ml-auto text-muted-foreground hover:text-foreground"
            onClick={() => setSelected(allShown
              ? new Set([...selected].filter((name) => !shown.some((f) => f.name === name)))
              : new Set([...selected, ...shown.map((f) => f.name)]))}
          >
            {allShown ? 'Deselect all' : `Select all ${shown.length}`}
          </Button>
        )}
      </div>

      {/* Songs cannot be uploaded, so the Music filter gets no upload tile. */}
      {filter !== 'audio' && (
        <UploadTile dragging={dragging} sending={sending} onPick={() => picker.current?.click()} />
      )}

      {shown.length === 0 && filter === 'audio' ? (
        <EmptyState
          icon={<Music className="h-6 w-6" />}
          title="Nothing here yet"
          description="Songs you make with the Hub's music tool show up here."
        />
      ) : shown.length === 0 ? (
        <EmptyState
          icon={<FolderOpen className="h-6 w-6" />}
          description={filter === 'upload'
            ? 'No uploads yet.'
            : 'Nothing here yet. What you make with the Hub’s image, video and music tools shows up here too.'}
        />
      ) : (
        <>
          <Card className="hidden gap-0 overflow-hidden p-0 shadow-none md:block">
            <Table>
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  <TableHead className={`w-9 pl-3 pr-0 ${headCell}`}>
                    <span className="sr-only">Select</span>
                  </TableHead>
                  <TableHead className={`w-14 pl-1 ${headCell}`}>
                    <span className="sr-only">Preview</span>
                  </TableHead>
                  <TableHead className={headCell}>File</TableHead>
                  <TableHead className={`hidden sm:table-cell ${headCell}`}>Kind</TableHead>
                  <TableHead className={`text-right ${headCell}`}>Size</TableHead>
                  <TableHead className={`hidden lg:table-cell ${headCell}`}>Made</TableHead>
                  <TableHead className={`hidden xl:table-cell ${headCell}`}>Details</TableHead>
                  <TableHead className={`pr-3 text-right ${headCell}`}>
                    <span className="sr-only">Actions</span>
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {shown.map((f) => (
                  <FileRow
                    key={f.name}
                    file={f}
                    picked={selected.has(f.name)}
                    selecting={selected.size > 0}
                    onToggle={toggle}
                    onDelete={askRemove}
                    onOpen={setViewing}
                  />
                ))}
              </TableBody>
            </Table>
          </Card>

          <div className="grid gap-3 md:hidden">
            {shown.map((f) => (
              <FileCard
                key={f.name}
                file={f}
                picked={selected.has(f.name)}
                selecting={selected.size > 0}
                onToggle={toggle}
                onDelete={askRemove}
                onOpen={setViewing}
              />
            ))}
          </div>
        </>
      )}

      {chosen.length > 0 && (
        <div className="sticky bottom-4 z-10 mx-auto flex items-center gap-3 rounded-lg border border-border bg-card px-4 py-2 shadow-none">
          <span className="font-mono text-xs tabular-nums text-foreground">
            {chosen.length} selected · {size(chosen.reduce((sum, f) => sum + f.bytes, 0))}
          </span>
          <Button variant="ghost" size="sm" onClick={() => setSelected(new Set())}>Clear</Button>
          <Button
            variant="destructive"
            size="sm"
            onClick={() => askRemove(chosen.map((f) => f.name))}
          >
            Delete selected
          </Button>
        </div>
      )}

      {viewIndex >= 0 && (
        <Viewer
          files={viewable}
          index={viewIndex}
          onIndex={(i) => setViewing(viewable[i].name)}
          onClose={() => setViewing(null)}
          details={details}
          onDetails={setDetails}
        />
      )}

      {deleting && (
        <AlertDialog open onOpenChange={(open) => { if (!open) setDeleting(null) }}>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>{`Delete ${deleteWhat}?`}</AlertDialogTitle>
              <AlertDialogDescription>
                Links to it stop working. This cannot be undone.
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel onClick={() => setDeleting(null)}>Cancel</AlertDialogCancel>
              <AlertDialogAction variant="destructive" onClick={confirmRemove}>Delete</AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      )}
    </div>
  )
}

/* ── one file, as a table row (desktop) ──────────────────────────────────── */

function FileRow({ file, picked, selecting, onToggle, onDelete, onOpen }) {
  const { copied, copy } = useCopyLink(file)
  return (
    <TableRow className={picked ? 'bg-muted/50' : undefined}>
      <TableCell className="pl-3 pr-0">
        <Checkbox
          checked={picked}
          onCheckedChange={() => onToggle(file.name)}
          title={picked ? 'Unselect' : 'Select'}
        />
      </TableCell>
      <TableCell className="pl-1">
        <Preview file={file} selecting={selecting} onToggle={onToggle} onOpen={onOpen} compact />
      </TableCell>
      <TableCell className="whitespace-normal">
        <div className="flex min-w-0 flex-col gap-1">
          <span className="truncate font-mono text-xs text-foreground" title={file.name}>{file.name}</span>
          {file.model && (
            <span className="truncate text-[11px] text-muted-foreground" title={file.model}>{file.model}</span>
          )}
          {file.kind === 'audio' && (
            <audio controls preload="none" src={file.path} className="h-8 w-56 max-w-full" />
          )}
        </div>
      </TableCell>
      <TableCell className="hidden sm:table-cell">
        <div className="flex items-center gap-1.5">
          <Badge variant="outline" className="text-[10px] uppercase tracking-[0.12em]">
            {KIND_LABEL[file.kind]}
          </Badge>
          {file.upload && (
            <Badge variant="secondary" className="text-[10px] uppercase tracking-[0.12em]">upload</Badge>
          )}
        </div>
      </TableCell>
      <TableCell className="text-right font-mono text-xs tabular-nums text-foreground">
        {size(file.bytes)}
      </TableCell>
      <TableCell className="hidden text-xs text-muted-foreground lg:table-cell">
        {when(file.created)}
      </TableCell>
      <TableCell className="hidden text-xs text-muted-foreground xl:table-cell">
        {[shapeOf(file), file.seconds != null ? `Took ${took(file.seconds)}` : null].filter(Boolean).join(' · ')}
      </TableCell>
      <TableCell className="pr-3 text-right">
        <FileActions
          file={file}
          copied={copied}
          onCopy={copy}
          onDelete={() => onDelete([file.name])}
        />
      </TableCell>
    </TableRow>
  )
}

/* ── the same file as a card, for phones ─────────────────────────────────── */

function FileCard({ file, picked, selecting, onToggle, onDelete, onOpen }) {
  const { copied, copy } = useCopyLink(file)
  return (
    <Card className={`gap-0 p-0 shadow-none ${picked ? 'ring-primary/40' : ''}`}>
      <Preview file={file} selecting={selecting} onToggle={onToggle} onOpen={onOpen} />
      <div className="flex items-start gap-2 p-3">
        <Checkbox
          checked={picked}
          onCheckedChange={() => onToggle(file.name)}
          title={picked ? 'Unselect' : 'Select'}
          className="mt-0.5"
        />
        <div className="flex min-w-0 flex-1 flex-col gap-1">
          <span className="truncate font-mono text-xs text-foreground" title={file.name}>{file.name}</span>
          {file.model && (
            <span className="truncate text-[11px] text-muted-foreground" title={file.model}>{file.model}</span>
          )}
          <div className="flex flex-wrap items-center gap-1.5">
            <Badge variant="outline" className="text-[10px] uppercase tracking-[0.12em]">
              {KIND_LABEL[file.kind]}
            </Badge>
            {file.upload && (
              <Badge variant="secondary" className="text-[10px] uppercase tracking-[0.12em]">upload</Badge>
            )}
            <span className="ml-auto font-mono text-[11px] tabular-nums text-muted-foreground">
              {size(file.bytes)}
            </span>
          </div>
          <div className="text-[11px] text-muted-foreground">{when(file.created)}</div>
          {shapeOf(file) && (
            <div className="truncate text-[11px] text-muted-foreground">{shapeOf(file)}</div>
          )}
          {file.seconds != null && (
            <div className="truncate text-[11px] text-muted-foreground">Took {took(file.seconds)}</div>
          )}
        </div>
        <FileActions
          file={file}
          copied={copied}
          onCopy={copy}
          onDelete={() => onDelete([file.name])}
        />
      </div>
    </Card>
  )
}

/* Copy the absolute link, then tick the icon for a moment. Shared by the row
 * and the card so both speak the same "Copy link" / "Link copied". */
function useCopyLink(file) {
  const [copied, setCopied] = useState(false)
  const copy = async () => {
    if (await copyText(window.location.origin + file.path)) {
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    }
  }
  return { copied, copy }
}

/* The quick action is the one people came for (download); everything else
 * lives in the menu beside it. */
function FileActions({ file, copied, onCopy, onDelete }) {
  return (
    <div className="flex shrink-0 items-center justify-end gap-1">
      <Button variant="ghost" size="icon-sm" asChild title="Download">
        <a href={file.path} download={file.name}>
          <Download />
        </a>
      </Button>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="ghost" size="icon-sm" aria-label="Actions">
            <MoreHorizontal />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="min-w-44">
          {file.kind !== 'audio' && (
            <DropdownMenuItem asChild>
              <a href={file.path} target="_blank" rel="noopener noreferrer">
                <ExternalLink /> Open in a new tab
              </a>
            </DropdownMenuItem>
          )}
          <DropdownMenuItem onClick={onCopy}>
            {copied ? <Check /> : <Link2 />} {copied ? 'Link copied' : 'Copy link'}
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem variant="destructive" onClick={onDelete}>
            <Trash2 /> Delete
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  )
}

/* A file can be dropped anywhere on the page or pasted; this panel is where a
 * new upload lands and it lights up while one is dragged over. */
function UploadTile({ dragging, sending, onPick }) {
  return (
    <button
      onClick={onPick}
      className={`flex w-full flex-col items-center justify-center gap-2 rounded-lg border border-dashed px-4 py-5 text-center transition-colors ${
        dragging
          ? 'border-primary/40 bg-primary/5 text-foreground'
          : 'border-border bg-muted/30 text-muted-foreground hover:bg-muted/50 hover:text-foreground'
      }`}
    >
      <span className={`flex size-9 items-center justify-center rounded-md border transition-colors ${
        dragging ? 'border-primary/30 bg-primary/10 text-primary' : 'border-border bg-card text-muted-foreground'
      }`}>
        <Upload className="size-4" />
      </span>
      <span className="text-sm font-medium text-foreground">
        {sending ? (sending > 1 ? `Uploading ${sending} files…` : 'Uploading…') : 'Drop or paste an image or MP4'}
      </span>
      <span className="text-xs">{sending ? 'It shows up here when it is done' : 'or click to choose files'}</span>
    </button>
  )
}

/* The still of a file: a 48px thumb in the table, a wide preview on a card.
 * A plain click opens the viewer; Ctrl/Cmd/middle click still opens a new tab. */
function Preview({ file, selecting, onToggle, onOpen, compact }) {
  const box = compact ? 'size-12' : 'aspect-video w-full'
  if (file.kind === 'audio') {
    if (compact) {
      return (
        <div className={`flex ${box} items-center justify-center rounded-md border border-border bg-muted`}>
          <Music className="size-4 text-muted-foreground" />
        </div>
      )
    }
    return (
      <div className={`flex ${box} flex-col items-center justify-center gap-3 bg-muted px-4`}>
        <Music className="size-5 text-muted-foreground" />
        <audio controls preload="none" src={file.path} className="w-full" />
      </div>
    )
  }
  const pick = (e) => {
    if (e.ctrlKey || e.metaKey || e.shiftKey) return
    e.preventDefault()
    selecting ? onToggle(file.name) : onOpen(file.name)
  }
  return (
    <a href={file.path} target="_blank" rel="noopener noreferrer" onClick={pick}
       className={`relative block ${box} overflow-hidden rounded-md border border-border bg-muted`}>
      {file.kind === 'video' ? (
        <video src={`${file.path}#t=0.5`} preload="metadata" muted className="size-full object-cover" />
      ) : (
        // A 3D model's is a still of the model once one is drawn (above), and
        // a fresh URL then, so the cached picture it was made from goes.
        <img src={`/api/media/${file.name}/thumb${file.rendered ? '?render' : ''}`} alt="" loading="lazy"
             className="size-full object-cover" />
      )}
      {(file.orbit || file.kind === 'mesh') && (
        <span className="absolute bottom-1 left-1 rounded-full bg-black/60 px-1.5 py-0.5 font-mono text-[10px] font-semibold text-white">
          {file.orbit ? '360°' : '3D'}
        </span>
      )}
    </a>
  )
}

/* The picture or clip at its own size, shrunk only to fit the screen, with
 * arrows (and the ← → keys) through everything the filter shows. The (i)
 * button (or the I key) shows and hides its details. Esc, the X or a click
 * beside it closes. */
function Viewer({ files, index, onIndex, onClose, details, onDetails }) {
  const file = files[index]
  const [mesh, setMesh] = useState(null)   // { name, vertices, triangles } once a 3D model is loaded
  const step = useCallback((d) => onIndex((index + d + files.length) % files.length), [index, files.length, onIndex])

  useEffect(() => {
    const onKey = (e) => {
      if (e.key === 'Escape') onClose()
      else if (e.key === 'ArrowLeft') step(-1)
      else if (e.key === 'ArrowRight') step(1)
      else if (e.key === 'i' || e.key === 'I') onDetails(!details)
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [step, onClose, details, onDetails])

  useEffect(() => {
    const overflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    return () => { document.body.style.overflow = overflow }
  }, [])

  const stop = (e) => e.stopPropagation()
  const fit = 'block max-w-[calc(100vw-2rem)] max-h-[calc(100dvh-2rem)]'
  const onMesh = useCallback((counts) => setMesh({ name: file.name, ...counts }), [file.name])
  const known = mesh?.name === file.name ? mesh : null
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/90" onClick={onClose}>
      {file.kind === 'image' ? (
        <img key={file.name} src={file.path} alt="" onClick={stop} className={fit} />
      ) : file.kind === 'mesh' ? (
        <Suspense fallback={<span className="text-sm font-semibold text-white/80">Opening the 3D model…</span>}>
          <MeshView key={file.name} file={file} onLoaded={onMesh} />
        </Suspense>
      ) : file.orbit ? (
        <Orbit key={file.name} file={file} className={fit} />
      ) : (
        <video key={file.name} src={file.path} controls autoPlay playsInline onClick={stop}
               className={fit} />
      )}

      <span className="absolute left-4 top-4 font-mono text-xs tabular-nums text-white/80">
        {index + 1} / {files.length}
      </span>
      <div className="absolute right-3 top-3 flex gap-2" onClick={stop}>
        <Button
          variant="ghost"
          size="icon-lg"
          onClick={() => onDetails(!details)}
          title={details ? 'Hide details' : 'Show details'}
          className={overlayBtn}
        >
          <Info />
        </Button>
        <Button variant="ghost" size="icon-lg" asChild title="Open in a new tab" className={overlayBtn}>
          <a href={file.path} target="_blank" rel="noopener noreferrer">
            <ExternalLink />
          </a>
        </Button>
        <Button variant="ghost" size="icon-lg" onClick={onClose} title="Close" className={overlayBtn}>
          <X />
        </Button>
      </div>
      {files.length > 1 && (
        <>
          <Button
            variant="ghost"
            size="icon-lg"
            onClick={(e) => { stop(e); step(-1) }}
            title="Previous"
            className={`${overlayBtn} absolute left-3 top-1/2 -translate-y-1/2`}
          >
            <ChevronLeft />
          </Button>
          <Button
            variant="ghost"
            size="icon-lg"
            onClick={(e) => { stop(e); step(1) }}
            title="Next"
            className={`${overlayBtn} absolute right-3 top-1/2 -translate-y-1/2`}
          >
            <ChevronRight />
          </Button>
        </>
      )}

      {details && (
        <dl
          onClick={stop}
          className="absolute bottom-4 left-4 m-0 grid max-w-[calc(100vw-2rem)] grid-cols-[auto_1fr] gap-x-4 gap-y-1 rounded-lg border border-white/15 bg-black/75 px-4 py-3 text-sm text-white backdrop-blur-sm"
        >
          <Detail label="Type" value={`${KIND_LABEL[file.kind]}${file.orbit ? ' · 360° orbit' : ''}${file.upload ? ' · upload' : ''}`} />
          {file.model && <Detail label="Model" value={file.model} />}
          <Detail label="Made" value={when(file.created)} />
          {file.seconds != null && <Detail label="Took" value={took(file.seconds)} />}
          {shapeOf(file) && <Detail label={file.duration ? 'Length' : 'Size'} value={shapeOf(file)} />}
          {known?.triangles && (
            <Detail label="Mesh" value={`${fmtCount(known.triangles)} triangles · ${fmtCount(known.vertices)} vertices`} />
          )}
          <Detail label="File" value={size(file.bytes)} />
        </dl>
      )}
    </div>
  )
}

function Detail({ label, value }) {
  return (
    <>
      <dt className="text-white/60">{label}</dt>
      <dd className="m-0 min-w-0 break-words">{value}</dd>
    </>
  )
}

/* A 360° orbit (first frame = last): it turns by itself until it is first
 * grabbed, then stays wherever the hand leaves it. A drag turns it one full
 * turn across its width, the subject following the hand; each angle is a jump
 * to its frame. The Hub stores these clips with every frame a keyframe
 * (video_service.all_keyframes) so each jump is instant. One jump at a time:
 * the next waits for the last to land, then goes straight to where the drag is. */
function Orbit({ file, className }) {
  const video = useRef(null)
  const drag = useRef(null)                       // { x, t } where the drag began
  const seek = useRef({ busy: false, at: 0, want: 0 })
  const [turned, setTurned] = useState(false)

  const seekTo = (t) => {
    const s = seek.current
    s.want = t
    if (s.busy) return
    s.busy = true
    s.at = t
    video.current.currentTime = t
  }
  const onSeeked = () => {
    const s = seek.current
    s.busy = false
    if (s.want !== s.at) seekTo(s.want)
  }

  const down = (e) => {
    e.preventDefault()
    e.currentTarget.setPointerCapture(e.pointerId)
    video.current.pause()
    drag.current = { x: e.clientX, t: video.current.currentTime }
    setTurned(true)
  }
  const move = (e) => {
    const d = video.current.duration
    if (!drag.current || !d) return
    // The camera circles one way, so the subject turns the other: playing
    // backward is what turns it the way the hand moves.
    const t = drag.current.t - ((e.clientX - drag.current.x) / e.currentTarget.clientWidth) * d
    seekTo(((t % d) + d) % d)
  }
  const up = () => { drag.current = null }

  return (
    <div className="relative" onClick={(e) => e.stopPropagation()}>
      <video
        ref={video}
        src={file.path}
        autoPlay
        loop
        muted
        playsInline
        onSeeked={onSeeked}
        onPointerDown={down}
        onPointerMove={move}
        onPointerUp={up}
        onPointerCancel={up}
        className={`${className} cursor-grab touch-none select-none active:cursor-grabbing`}
      />
      {!turned && (
        <span className="pointer-events-none absolute bottom-4 left-1/2 -translate-x-1/2 rounded-full bg-black/60 px-3 py-1.5 text-sm font-semibold text-white">
          Drag to turn it
        </span>
      )}
    </div>
  )
}

/* ── bits ────────────────────────────────────────────────────────────────── */

function fmtCount(n) {
  return n >= 1e6 ? `${(n / 1e6).toFixed(1)}M` : n >= 1e3 ? `${Math.round(n / 1e3)}K` : `${n}`
}

// What the file is, to read the time it took against: "1024 × 1024" for a
// picture, "5.2 s · 1344 × 768" for a clip, "2 min 30 s" for a song.
function shapeOf(file) {
  const dims = file.width ? `${file.width} × ${file.height}` : null
  const length = file.duration ? clipLength(file.duration) : null
  return [length, dims].filter(Boolean).join(' · ') || null
}

function clipLength(seconds) {
  return seconds < 10 ? `${seconds.toFixed(1)} s` : took(seconds)
}

// How long making a file took: "9 s", "1 min 25 s", "1 h 5 min".
function took(seconds) {
  const s = Math.round(seconds)
  if (s < 60) return `${s} s`
  if (s < 3600) return `${Math.floor(s / 60)} min${s % 60 ? ` ${s % 60} s` : ''}`
  return `${Math.floor(s / 3600)} h${Math.round((s % 3600) / 60) ? ` ${Math.round((s % 3600) / 60)} min` : ''}`
}

function when(seconds) {
  return new Date(seconds * 1000).toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' })
}

// The viewer sits on a black scrim, so its controls are the one place the
// neutral palette inverts: same Button, dark glass ground.
const overlayBtn = 'border border-white/15 bg-black/60 text-white backdrop-blur-sm hover:bg-black/80 hover:text-white'
