import { html, render, useState, useEffect, useCallback } from '/mini/assets/vendor/preact-htm.module.js'

// The whole Telegram bridge lives in this file: read initData once, send it as
// a header, apply theme and viewport. Everything else is a plain list.

const tg = window.Telegram && window.Telegram.WebApp

// initData is empty in a normal browser. That is the development case: the
// server accepts unsigned requests only when MINI_DEV_USER is set and no
// webhook is configured, so an empty header here is never an authenticated
// user in production.
const initData = (tg && tg.initData) || ''

function boot() {
  if (!tg) return
  tg.ready()
  tg.expand()
  // Without this the client swallows the vertical swipe to dismiss the app and
  // scrolling the list closes it instead of scrolling.
  if (tg.disableVerticalSwipes) tg.disableVerticalSwipes()
}

async function fetchAppointments() {
  const headers = initData ? { Authorization: `tma ${initData}` } : {}
  const res = await fetch('/mini/api/appointments', { headers })
  let body = null
  try {
    body = await res.json()
  } catch (_) {
    // fall through to the status-based message
  }
  if (!res.ok) {
    const err = (body && body.error) || {}
    throw { code: err.code || 'internal', message: err.message || `HTTP ${res.status}` }
  }
  return body
}

function Status({ value }) {
  if (value === 'planned') return null
  const label = { done: 'було', cancelled: 'скасовано' }[value] || value
  return html`<span class="pill pill-${value}">${label}</span>`
}

function Item({ item }) {
  return html`
    <li class="item">
      <div class="time">
        <span class="start">${item.time}</span>
        ${item.endTime && html`<span class="end">${item.endTime}</span>`}
      </div>
      <div class="body">
        <div class="line">
          <span class="title">${item.title}</span>
          <${Status} value=${item.status} />
        </div>
        ${item.person && html`<div class="person">${item.person}</div>`}
        ${item.location && html`<div class="location">${item.location}</div>`}
        ${item.note && html`<div class="note">${item.note}</div>`}
      </div>
    </li>`
}

function Day({ day }) {
  return html`
    <section class="day">
      <h2 class="day-label">${day.label}</h2>
      <ul class="items">
        ${day.items.map((item) => html`<${Item} key=${item.id} item=${item} />`)}
      </ul>
    </section>`
}

function App() {
  const [state, setState] = useState({ phase: 'loading' })

  const load = useCallback(async () => {
    try {
      const data = await fetchAppointments()
      setState({ phase: 'ready', days: data.days || [] })
    } catch (err) {
      setState({ phase: 'error', code: err.code, message: err.message })
    }
  }, [])

  useEffect(() => {
    boot()
    load()
    // The list goes stale when somebody edits a visit through the bot. Read
    // only, so refetching when the app comes back into view is enough.
    const onVisible = () => {
      if (document.visibilityState === 'visible') load()
    }
    document.addEventListener('visibilitychange', onVisible)
    return () => document.removeEventListener('visibilitychange', onVisible)
  }, [load])

  if (state.phase === 'loading') {
    return html`<div class="center muted">Завантаження…</div>`
  }
  if (state.phase === 'error') {
    // 403 is terminal — nothing the user can do. Anything else is worth a retry.
    const terminal = state.code === 'forbidden'
    return html`
      <div class="center">
        <p class="error">${state.message}</p>
        ${!terminal && html`<button class="retry" onClick=${load}>Спробувати ще</button>`}
      </div>`
  }
  if (state.days.length === 0) {
    return html`<div class="center muted">Попереду візитів немає</div>`
  }
  return html`
    <main>
      ${state.days.map((day) => html`<${Day} key=${day.date} day=${day} />`)}
    </main>`
}

render(html`<${App} />`, document.getElementById('root'))
