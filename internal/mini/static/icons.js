// Line icons, drawn once and reused. They replace the emoji the tab bar used
// to show: an emoji is a different typeface on every platform and cannot take
// the accent colour, so the active tab could not actually look active.
import { html } from '/mini/assets/vendor/preact-htm.module.js'

const svg = (size, children, extra = {}) => html`
  <svg width=${size} height=${size} viewBox="0 0 24 24" fill="none"
    stroke="currentColor" stroke-width=${extra.weight || 1.9}
    stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${children}</svg>`

export const IconHome = ({ size = 22 }) =>
  svg(size, html`
    <path d="M4 10.5 12 4l8 6.5V19a1.5 1.5 0 0 1-1.5 1.5h-13A1.5 1.5 0 0 1 4 19z" />
    <path d="M9.5 20.5v-6h5v6" />`)

export const IconCalendar = ({ size = 22 }) =>
  svg(size, html`
    <rect x="3.5" y="5" width="17" height="15.5" rx="3" />
    <path d="M8 3v4M16 3v4M3.5 10h17" />`)

export const IconBook = ({ size = 22 }) =>
  svg(size, html`
    <path d="M4 5.5A1.5 1.5 0 0 1 5.5 4H10a3 3 0 0 1 3 3v13a2.5 2.5 0 0 0-2.5-2.5H4z" />
    <path d="M20 5.5A1.5 1.5 0 0 0 18.5 4H14a3 3 0 0 0-3 3v13a2.5 2.5 0 0 1 2.5-2.5H20z" />`)

export const IconClock = ({ size = 14 }) =>
  svg(size, html`<circle cx="12" cy="12" r="9" /><path d="M12 7v5l3 2" />`, { weight: 2 })

export const IconPin = ({ size = 12 }) =>
  svg(size, html`
    <path d="M12 21s7-6.3 7-11a7 7 0 1 0-14 0c0 4.7 7 11 7 11z" />
    <circle cx="12" cy="10" r="2.5" />`, { weight: 2 })

export const IconInfo = ({ size = 13 }) =>
  svg(size, html`<circle cx="12" cy="12" r="9" /><path d="M12 8v5" /><path d="M12 16.5h.01" />`, { weight: 2 })

export const IconChevron = ({ size = 16 }) =>
  svg(size, html`<path d="M9 5l7 7-7 7" />`, { weight: 2.2 })

export const IconPlus = ({ size = 18 }) =>
  svg(size, html`<path d="M12 5v14M5 12h14" />`, { weight: 2.4 })

export const IconAlert = ({ size = 34 }) =>
  svg(size, html`<circle cx="12" cy="12" r="9" /><path d="M12 7.5v5.5" /><path d="M12 16.5h.01" />`, { weight: 1.6 })
