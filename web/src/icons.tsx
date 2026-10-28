// SVG marks copied from the approved mockup docs/prototypes/tutor-screens.html.
import type { Schemas } from './api/client'

export function Logo() {
  return (
    <svg aria-hidden="true" viewBox="0 0 48 48">
      <rect width="48" height="48" rx="13" fill="#afc6b7" />
      <path d="M11 13c5-1 9 0 13 3 4-3 8-4 13-3v21c-5-1-9 0-13 3-4-3-8-4-13-3V13Z" fill="none" stroke="#1c2922" strokeWidth="2.5" strokeLinejoin="round" />
      <path d="M24 17v19M17 21h2M17 26h2M29 21h2M29 26h2" stroke="#1c2922" strokeWidth="2.5" strokeLinecap="round" />
    </svg>
  )
}

export const directionIcons: Record<Schemas['DirectionId'], React.ReactNode> = {
  python: (
    <svg viewBox="0 0 48 48" aria-hidden="true">
      <path fill="#73a8d3" d="M24 3c-11 0-10 5-10 5v6h11v2H10S3 15 3 25s6 10 6 10h4v-6s0-6 6-6h10s5 0 5-5V9s1-6-10-6Z" />
      <circle cx="19" cy="9" r="2" fill="#17212b" />
      <path fill="#f1d477" d="M24 45c11 0 10-5 10-5v-6H23v-2h15s7 1 7-9-6-10-6-10h-4v6s0 6-6 6H19s-5 0-5 5v9s-1 6 10 6Z" />
      <circle cx="29" cy="39" r="2" fill="#342e1a" />
    </svg>
  ),
  git: (
    <svg viewBox="0 0 48 48" aria-hidden="true">
      <rect x="7" y="7" width="34" height="34" rx="4" transform="rotate(45 24 24)" fill="#e87358" />
      <path d="M15 10l18 18M22 17v20" fill="none" stroke="#241d1c" strokeWidth="3.2" />
      <g fill="#241d1c">
        <circle cx="22" cy="17" r="3.5" />
        <circle cx="33" cy="28" r="3.5" />
        <circle cx="22" cy="36" r="3.5" />
      </g>
    </svg>
  ),
  pm: (
    <svg viewBox="0 0 48 48" fill="none" aria-hidden="true">
      <rect x="5" y="7" width="38" height="34" rx="7" stroke="#b2a4d8" strokeWidth="2" />
      <path d="M5 17h38M17 17v24M30 17v24" stroke="#796c9e" strokeWidth="1.5" />
      <rect x="9" y="22" width="4" height="10" rx="2" fill="#b2a4d8" />
      <rect x="21" y="22" width="5" height="14" rx="2" fill="#b2a4d8" />
      <path d="m34 25 2 2 4-5" stroke="#d1c3f2" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  ),
}
