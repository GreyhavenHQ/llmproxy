import ReactMarkdown, { type Components } from 'react-markdown'
import remarkBreaks from 'remark-breaks'
import remarkGfm from 'remark-gfm'
import { cn } from '@/lib/utils'

// Markdown for model replies in the playground. react-markdown renders to
// React elements and drops raw HTML, so untrusted model output cannot inject
// markup. remark-gfm adds tables, strikethrough and task lists; remark-breaks
// keeps single newlines visible, which plain-text answers rely on.
// Typography lives in the .chat-markdown block in index.css.

const components: Components = {
  // Links go to third-party pages, so open them detached from this tab.
  a: ({ children, ...props }) => (
    <a {...props} target="_blank" rel="noopener noreferrer">
      {children}
    </a>
  ),
  // Never fetch a remote URL because a model asked us to; show the alt text.
  img: ({ alt }) => <span className="text-muted-foreground">{alt || '(image)'}</span>,
}

export function Markdown({ text, className }: { text: string; className?: string }) {
  return (
    <div className={cn('chat-markdown', className)}>
      <ReactMarkdown remarkPlugins={[remarkGfm, remarkBreaks]} components={components}>
        {text}
      </ReactMarkdown>
    </div>
  )
}
