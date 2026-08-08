import { html, render, useState, useEffect, useCallback, useRef } from '/mini/assets/vendor/preact-htm.module.js'

// The whole Telegram bridge lives at the top of this file: read initData once,
// send it as a header, drive the native back button. Everything below is a
// plain list and a plain form.

const tg = window.Telegram && window.Telegram.WebApp

// initData is empty in a normal browser. That is the development case: the
// server accepts unsigned requests only when MINI_DEV_USER is set and no
// webhook is configured, so an empty header is never an authenticated user in
// production.
const initData = (tg && tg.initData) || ''

function boot() {
  if (!tg) return
  tg.ready()
  tg.expand()
  // Without this the client swallows the vertical swipe to dismiss the app and
  // scrolling the list closes it instead of scrolling.
  if (tg.disableVerticalSwipes) tg.disableVerticalSwipes()
}

async function api(path, { method = 'GET', body } = {}) {
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

const STATUSES = [
  { value: 'planned', label: 'заплановано' },
  { value: 'done', label: 'було' },
  { value: 'cancelled', label: 'скасовано' },
]

function statusLabel(value) {
  const s = STATUSES.find((s) => s.value === value)
  return s ? s.label : value
}

function todayISO() {
  const d = new Date()
  const pad = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}

// --- list -------------------------------------------------------------------

function Item({ item, date, onOpen }) {
  return html`
    <li class="item" onClick=${() => onOpen({ ...item, date })}>
      <div class="time">
        <span class="start">${item.time}</span>
        ${item.endTime && html`<span class="end">${item.endTime}</span>`}
      </div>
      <div class="body">
        <div class="line">
          <span class="title">${item.title}</span>
          ${item.status !== 'planned' &&
          html`<span class="pill pill-${item.status}">${statusLabel(item.status)}</span>`}
        </div>
        ${item.person && html`<div class="person">${item.person}</div>`}
        ${item.location && html`<div class="location">${item.location}</div>`}
        ${item.note && html`<div class="note">${item.note}</div>`}
      </div>
      <div class="chev">›</div>
    </li>`
}

function List({ days, onOpen, onAdd }) {
  return html`
    <main>
      ${days.length === 0 && html`<div class="center muted">Попереду візитів немає</div>`}
      ${days.map(
        (day) => html`
          <section class="day" key=${day.date}>
            <h2 class="day-label">${day.label}</h2>
            <ul class="items">
              ${day.items.map(
                (item) => html`<${Item} key=${item.id} item=${item} date=${day.date} onOpen=${onOpen} />`,
              )}
            </ul>
          </section>`,
      )}
      <button class="fab" onClick=${onAdd} aria-label="Додати запис">+</button>
    </main>`
}

// --- form -------------------------------------------------------------------

const EMPTY = {
  title: '', person: '', location: '', date: '', time: '',
  endTime: '', status: 'planned', note: '', cost: '',
}

function Field({ label, children, error }) {
  return html`
    <label class="field">
      <span class="label">${label}</span>
      ${children}
      ${error && html`<span class="field-error">${error}</span>`}
    </label>`
}

function Form({ initial, persons, onSaved, onCancel }) {
  const isEdit = Boolean(initial && initial.id)
  const [values, setValues] = useState(() => ({
    ...EMPTY,
    date: todayISO(),
    ...(initial || {}),
  }))
  const [error, setError] = useState(null)
  const [saving, setSaving] = useState(false)
  const dirty = useRef(false)

  const set = (name) => (e) => {
    dirty.current = true
    // A half-typed form is worth confirming before Telegram closes the app.
    if (tg && tg.enableClosingConfirmation) tg.enableClosingConfirmation()
    setValues((v) => ({ ...v, [name]: e.target.value }))
  }

  const done = useCallback(() => {
    dirty.current = false
    if (tg && tg.disableClosingConfirmation) tg.disableClosingConfirmation()
  }, [])

  const submit = async (e) => {
    e.preventDefault()
    if (saving) return
    setSaving(true)
    setError(null)
    const body = {
      title: values.title, person: values.person, location: values.location,
      date: values.date, time: values.time, endTime: values.endTime,
      status: values.status, note: values.note, cost: values.cost,
    }
    try {
      if (isEdit) await api(`/appointments/${initial.id}`, { method: 'PUT', body })
      else await api('/appointments', { method: 'POST', body })
      if (tg && tg.HapticFeedback) tg.HapticFeedback.notificationOccurred('success')
      done()
      onSaved()
    } catch (err) {
      setError(err)
      setSaving(false)
    }
  }

  const remove = async () => {
    if (!confirm('Видалити цей запис?')) return
    setSaving(true)
    try {
      await api(`/appointments/${initial.id}`, { method: 'DELETE' })
      done()
      onSaved()
    } catch (err) {
      setError(err)
      setSaving(false)
    }
  }

  const errFor = (field) => (error && error.field === field ? error.message : null)

  return html`
    <form class="form" onSubmit=${submit}>
      <${Field} label="Що" error=${errFor('title')}>
        <input value=${values.title} onInput=${set('title')} placeholder="Ортодонт" autofocus=${!isEdit} />
      <//>

      <${Field} label="Хто">
        <input value=${values.person} onInput=${set('person')} list="persons" placeholder="Демид" />
        <datalist id="persons">
          ${persons.map((p) => html`<option value=${p} key=${p} />`)}
        </datalist>
      <//>

      <div class="row">
        <${Field} label="Дата" error=${errFor('date')}>
          <input type="date" value=${values.date} onInput=${set('date')} />
        <//>
        <${Field} label="Початок" error=${errFor('date')}>
          <input type="time" value=${values.time} onInput=${set('time')} />
        <//>
        <${Field} label="Кінець" error=${errFor('endTime')}>
          <input type="time" value=${values.endTime} onInput=${set('endTime')} />
        <//>
      </div>

      <${Field} label="Де">
        <input value=${values.location} onInput=${set('location')} placeholder="Хрещатик 1" />
      <//>

      <${Field} label="Статус" error=${errFor('status')}>
        <select value=${values.status} onChange=${set('status')}>
          ${STATUSES.map((s) => html`<option value=${s.value} key=${s.value}>${s.label}</option>`)}
        </select>
      <//>

      <${Field} label="Сума" error=${errFor('cost')}>
        <input value=${values.cost} onInput=${set('cost')} inputmode="decimal" placeholder="порожньо — не записано" />
      <//>

      <${Field} label="Нотатка">
        <textarea rows="2" value=${values.note} onInput=${set('note')}></textarea>
      <//>

      ${error && !error.field && html`<p class="error">${error.message}</p>`}

      <div class="actions">
        <button type="submit" class="primary" disabled=${saving}>
          ${saving ? 'Зберігаю…' : 'Зберегти'}
        </button>
        <button type="button" onClick=${() => { done(); onCancel() }}>Скасувати</button>
      </div>
      ${isEdit && html`<button type="button" class="danger" onClick=${remove} disabled=${saving}>Видалити</button>`}
    </form>`
}

// --- app --------------------------------------------------------------------

function App() {
  const [state, setState] = useState({ phase: 'loading' })
  const [screen, setScreen] = useState({ name: 'list' })
  const [persons, setPersons] = useState([])

  const load = useCallback(async () => {
    try {
      const data = await api('/appointments')
      setState({ phase: 'ready', days: data.days || [] })
    } catch (err) {
      setState({ phase: 'error', code: err.code, message: err.message })
    }
  }, [])

  useEffect(() => {
    boot()
    load()
    api('/persons').then((d) => setPersons(d.persons || [])).catch(() => {})

    const onVisible = () => {
      // Only refresh the list; reloading under a half-filled form would throw
      // away what the person is typing.
      if (document.visibilityState === 'visible') load()
    }
    document.addEventListener('visibilitychange', onVisible)
    return () => document.removeEventListener('visibilitychange', onVisible)
  }, [load])

  // The form is a nested view, so Telegram's own back button leaves it.
  useEffect(() => {
    if (!tg || !tg.BackButton) return
    const back = () => setScreen({ name: 'list' })
    if (screen.name === 'list') {
      tg.BackButton.hide()
    } else {
      tg.BackButton.show()
      tg.BackButton.onClick(back)
    }
    return () => tg.BackButton.offClick(back)
  }, [screen.name])

  if (state.phase === 'loading') return html`<div class="center muted">Завантаження…</div>`

  if (state.phase === 'error') {
    // 403 is terminal — nothing the person can do about it.
    const terminal = state.code === 'forbidden'
    return html`
      <div class="center">
        <p class="error">${state.message}</p>
        ${!terminal && html`<button class="primary" onClick=${load}>Спробувати ще</button>`}
      </div>`
  }

  if (screen.name === 'form') {
    return html`
      <${Form}
        initial=${screen.item}
        persons=${persons}
        onSaved=${() => { setScreen({ name: 'list' }); load() }}
        onCancel=${() => setScreen({ name: 'list' })} />`
  }

  return html`
    <${List}
      days=${state.days}
      onOpen=${(item) => setScreen({ name: 'form', item })}
      onAdd=${() => setScreen({ name: 'form', item: null })} />`
}

render(html`<${App} />`, document.getElementById('root'))
