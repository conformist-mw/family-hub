// The Telegram bridge and the one way this app talks to the server.

export const tg = window.Telegram && window.Telegram.WebApp

// initData is empty in a normal browser. That is the development case: the
// server accepts unsigned requests only when MINI_DEV_USER is set and no
// webhook is configured, so an empty header is never an authenticated user in
// production.
const initData = (tg && tg.initData) || ''

export function boot() {
  if (!tg) return
  tg.ready()
  tg.expand()
  // Without this the client swallows the vertical swipe to dismiss the app and
  // scrolling a list closes it instead of scrolling.
  if (tg.disableVerticalSwipes) tg.disableVerticalSwipes()
}

export async function api(path, { method = 'GET', body } = {}) {
  const headers = {}
  if (initData) headers.Authorization = `tma ${initData}`
  if (body !== undefined) headers['Content-Type'] = 'application/json'

  const res = await fetch(`/mini/api${path}`, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  })

  let payload = null
  try {
    payload = await res.json()
  } catch (_) {
    // fall through to the status-based message
  }
  if (!res.ok) {
    const err = (payload && payload.error) || {}
    throw { code: err.code || 'internal', message: err.message || `HTTP ${res.status}`, field: err.field }
  }
  return payload
}

export function haptic(kind = 'success') {
  if (tg && tg.HapticFeedback) tg.HapticFeedback.notificationOccurred(kind)
}

// Closing confirmation is only worth it while a form holds unsaved input.
export function guardUnsaved(on) {
  if (!tg) return
  if (on && tg.enableClosingConfirmation) tg.enableClosingConfirmation()
  if (!on && tg.disableClosingConfirmation) tg.disableClosingConfirmation()
}

export function todayISO() {
  const d = new Date()
  const pad = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}
