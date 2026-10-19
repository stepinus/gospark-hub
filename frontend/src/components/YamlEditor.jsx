import { useEffect, useRef } from 'react'
import { cn } from 'cn'
import { EditorState } from '@codemirror/state'
import { EditorView, keymap, lineNumbers, highlightActiveLine, highlightActiveLineGutter, drawSelection } from '@codemirror/view'
import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands'
import { HighlightStyle, syntaxHighlighting, bracketMatching, indentOnInput } from '@codemirror/language'
import { yaml } from '@codemirror/lang-yaml'
import { tags as t } from '@lezer/highlight'

const yamlColors = HighlightStyle.define([
  { tag: t.definition(t.propertyName), color: '#7EE787' },
  { tag: [t.string, t.content], color: '#A5D6FF' },
  { tag: t.special(t.string), color: '#79C0FF' },
  { tag: t.lineComment, color: '#8B949E', fontStyle: 'italic' },
  { tag: [t.labelName, t.typeName, t.meta, t.keyword], color: '#D2A8FF' },
  { tag: [t.separator, t.punctuation, t.squareBracket, t.brace], color: '#6E7681' },
])

// Chrome of the editor follows the theme tokens (surface, hairline, muted
// labels); the syntax palette below is unchanged.
const theme = EditorView.theme({
  '&': { height: '100%', fontSize: '12px', backgroundColor: 'transparent', color: 'var(--foreground)' },
  '&.cm-focused': { outline: 'none' },
  '.cm-scroller': { fontFamily: 'var(--font-mono, ui-monospace, monospace)', lineHeight: '1.5rem' },
  '.cm-content': { padding: '16px 12px', caretColor: 'var(--foreground)' },
  '.cm-gutters': { backgroundColor: 'transparent', border: 'none', color: 'var(--muted-foreground)' },
  '.cm-activeLine, .cm-activeLineGutter': { backgroundColor: 'color-mix(in oklab, var(--foreground) 6%, transparent)' },
  '.cm-selectionBackground, &.cm-focused .cm-selectionBackground, ::selection': { backgroundColor: 'color-mix(in oklab, var(--primary) 30%, transparent) !important' },
  '.cm-cursor': { borderLeftColor: 'var(--foreground)' },
  '.cm-matchingBracket': { backgroundColor: 'color-mix(in oklab, var(--foreground) 12%, transparent)', outline: 'none' },
}, { dark: true })

export default function YamlEditor({ value, onChange, className }) {
  const hostRef = useRef(null)
  const viewRef = useRef(null)
  const onChangeRef = useRef(onChange)
  onChangeRef.current = onChange

  useEffect(() => {
    const view = new EditorView({
      parent: hostRef.current,
      state: EditorState.create({
        doc: value,
        extensions: [
          lineNumbers(),
          highlightActiveLineGutter(),
          highlightActiveLine(),
          drawSelection(),
          history(),
          indentOnInput(),
          bracketMatching(),
          keymap.of([...defaultKeymap, ...historyKeymap, indentWithTab]),
          yaml(),
          syntaxHighlighting(yamlColors),
          theme,
          EditorView.updateListener.of((u) => {
            if (u.docChanged) onChangeRef.current(u.state.doc.toString())
          }),
        ],
      }),
    })
    viewRef.current = view
    return () => view.destroy()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // Outside changes (load, Restore Default) replace the editor's text.
  useEffect(() => {
    const view = viewRef.current
    if (view && value !== view.state.doc.toString()) {
      view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: value } })
    }
  }, [value])

  return (
    <div
      ref={hostRef}
      data-slot="yaml-editor"
      className={cn(
        'h-full w-full overflow-hidden rounded-lg border border-border bg-card text-foreground',
        className,
      )}
    />
  )
}
