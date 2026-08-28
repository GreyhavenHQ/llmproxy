import * as React from 'react'
import { Slot } from '@radix-ui/react-slot'
import { cva, type VariantProps } from 'class-variance-authority'

import { cn } from '@/lib/utils'

const badgeVariants = cva(
  'inline-flex items-center justify-center rounded-md border font-medium w-fit max-w-full whitespace-nowrap shrink-0 [&>svg]:size-3 gap-1 [&>svg]:pointer-events-none focus-visible:border-ring focus-visible:ring-ring/50 focus-visible:ring-[3px] aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40 aria-invalid:border-destructive transition-[color,box-shadow] overflow-hidden',
  {
    variants: {
      variant: {
        default:
          'border-transparent bg-primary text-primary-foreground [a&]:hover:bg-primary/90',
        secondary:
          'border-transparent bg-secondary text-secondary-foreground [a&]:hover:bg-secondary/90',
        muted:
          'border-transparent bg-muted text-muted-foreground [a&]:hover:bg-muted/80',
        destructive:
          'border-transparent bg-destructive text-white [a&]:hover:bg-destructive/90 focus-visible:ring-destructive/20 dark:focus-visible:ring-destructive/40 dark:bg-destructive/60',
        outline:
          'text-foreground [a&]:hover:bg-accent/10 [a&]:hover:text-accent-foreground',
        // Semantic variants
        success:
          'border-transparent bg-[#1a7f37] text-white [a&]:hover:bg-[#1a7f37]/90',
        warning:
          'border-transparent bg-[#9a6700] text-white [a&]:hover:bg-[#9a6700]/90',
        info:
          'border-transparent bg-[#0969da] text-white [a&]:hover:bg-[#0969da]/90',
        // Tag/Value variants
        tag:
          'border-border bg-card text-foreground [a&]:hover:bg-muted',
        value:
          'border-transparent bg-muted text-foreground font-mono [a&]:hover:bg-muted/80',
        // Channel pills (using Tailwind color equivalents)
        whatsapp:
          'border-transparent bg-[#22c55e] text-white [a&]:hover:bg-[#22c55e]/90',
        email:
          'border-transparent bg-[#4b5563] text-white [a&]:hover:bg-[#4b5563]/90',
        telegram:
          'border-transparent bg-[#3b82f6] text-white [a&]:hover:bg-[#3b82f6]/90',
        zulip:
          'border-transparent bg-[#a855f7] text-white [a&]:hover:bg-[#a855f7]/90',
        platform:
          'border-transparent bg-[#f97316] text-white [a&]:hover:bg-[#f97316]/90',
      },
      size: {
        sm: 'text-xs px-1.5 py-0',
        default: 'text-xs px-2 py-0.5',
        lg: 'text-sm px-3 py-1 [&>svg]:size-3.5',
      },
    },
    defaultVariants: {
      variant: 'default',
      size: 'default',
    },
  },
)

function Badge({
  className,
  variant,
  size,
  asChild = false,
  ...props
}: React.ComponentProps<'span'> &
  VariantProps<typeof badgeVariants> & { asChild?: boolean }) {
  const Comp = asChild ? Slot : 'span'

  return (
    <Comp
      data-slot="badge"
      className={cn(badgeVariants({ variant, size }), className)}
      {...props}
    />
  )
}

export { Badge, badgeVariants }
