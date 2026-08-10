import { html, useState, useRef } from '/mini/assets/vendor/preact-htm.module.js'
import { api, haptic, guardUnsaved, todayISO } from '/mini/assets/api.js'
import { Field, Actions, Empty } from '/mini/assets/ui.js'
import { IconChevron, IconPin, IconPlus } from '/mini/assets/icons.js'

const STATUSES = [
  { value: 'planned', label: 'заплановано' },
  { value: 'done', label: 'було' },
  { value: 'cancelled', label: 'скасовано' },
]

const statusLabel = (v) => (STATUSES.find((s) => s.value === v) || { label: v }).label

// The server writes day headers as "Сьогодні, 6 серпня" — one string, because
// the client must never parse a date (the phone can be in another zone). It is
// still one string here; it is only split on the comma so the relative half
// can be set in the accent colour.
function splitLabel(label) {
  const at = label.indexOf(', ')
  if (at < 0) return [label, '']
  return [label.slice(0, at), label.slice(at + 2)]
}

function Item({ item, date, dayLabel, onOpen }) {
  const off = item.status === 'cancelled'
  return html`
    <button class="row row-top ${off ? 'dim' : ''}" onClick=${() => onOpen({ ...item, date, dayLabel })}>
      <div class="time">
        <span class="start ${off ? 'start-off' : ''}">${item.time}</span>
        ${item.endTime && html`<span class="end">${item.endTime}</span>`}
      </div>
      <div class="row-main">
        <div class="line">
          <span class="title">${item.title}</span>
          ${item.status !== 'planned' &&
          html`<span class="pill pill-${item.status}">${statusLabel(item.status)}</span>`}
        </div>
        ${item.person && html`<div class="person">${item.person}</div>`}
        ${item.location && html`<div class="meta"><${IconPin} /> ${item.location}</div>`}
        ${item.note && html`<div class="note">${item.note}</div>`}
      </div>
      <span class="chev"><${IconChevron} /></span>
    </button>`
}

export function AppointmentList({ days, truncated, onOpen, onAdd }) {
  if (days.length === 0) {
    return html`
      <main>
        <${Empty} title="Попереду візитів немає"
          text="Додай запис або напиши боту в чат"
          action="Новий запис" onAction=${onAdd} />
      </main>`
  }
  return html`
    <main class="screen">
      <h1 class="screen-title">Записи</h1>
      ${days.map((day) => {
        const [head, rest] = splitLabel(day.label)
        const today = head === 'Сьогодні'
        return html`
          <section class="day" key=${day.date}>
            <h2 class="day-label">
              <span class=${today ? 'day-today' : ''}>${head}</span>
              ${rest && html`<span class="day-date">${rest}</span>`}
            </h2>
            <div class="card card-rows">
              ${day.items.map(
                (it) => html`<${Item} key=${it.id} item=${it} date=${day.date} dayLabel=${day.label} onOpen=${onOpen} />`,
              )}
            </div>
          </section>`
      })}
      ${truncated && html`<p class="sec-empty">Показано перші 100 записів — далі є ще</p>`}
      <button class="fab" onClick=${onAdd} aria-label="Додати запис"><${IconPlus} /> Запис</button>
    </main>`
}

// Location and status have no inputs here. Location was never once filled in,
// and everything entered on a phone is something being planned. They stay in
// the payload all the same: this form PUTs the whole appointment, so dropping
// them would wipe whatever the web UI or the bot had recorded.
const EMPTY = {
  title: '', person: '', location: '', date: '', time: '',
  endTime: '', duration: '', status: 'planned', note: '', cost: '',
  // Display only: which day header the row was tapped under.
  dayLabel: '',
}

// "How long does it take" is a tap; "when does it end" is arithmetic the
// person has to do themselves. The end time is computed server-side.
const DURATIONS = [
  { min: '30', label: '30 хв' },
  { min: '45', label: '45 хв' },
  { min: '60', label: '1 год' },
  { min: '120', label: '2 год' },
]

// Wall-clock arithmetic on two values the person just typed — no Date, no
// timezone, nothing that could reinterpret a stored time. It exists so the
// chosen duration says when the thing actually ends; the value that gets
// stored is still computed on the server.
function endPreview(time, duration) {
  const minutes = parseInt(duration, 10)
  if (!/^\d{1,2}:\d{2}$/.test(time) || !Number.isFinite(minutes) || minutes <= 0) return null
  const [h, m] = time.split(':').map(Number)
  const total = h * 60 + m + minutes
  const pad = (n) => String(n).padStart(2, '0')
  return { at: `${pad(Math.floor(total / 60) % 24)}:${pad(total % 60)}`, nextDay: total >= 1440 }
}

// "1 год 30 хв" reads better than "90" in a sentence about a visit.
function humanDuration(minutes) {
  const h = Math.floor(minutes / 60)
  const m = minutes % 60
  if (!h) return `${m} хв`
  if (!m) return `${h} год`
  return `${h} год ${m} хв`
}

function DurationPicker({ value, startTime, onPick, error }) {
  const minutes = parseInt(value, 10)
  const known = Number.isFinite(minutes) && minutes > 0
  const end = endPreview(startTime, value)
  const custom = known && !DURATIONS.some((d) => d.min === value)

  let summary = 'Тривалість не задана'
  let ok = false
  if (known) {
    if (end) { summary = `Закінчиться о ${end.at}${end.nextDay ? ' наступного дня' : ''}`; ok = true }
    else summary = `Триває ${humanDuration(minutes)} · вкажи час початку, щоб побачити закінчення`
  }

  return html`
    <div class="card">
      <span class="label">Скільки триває</span>
      <div class="chips">
        ${DURATIONS.map(
          (d) => html`
            <button type="button" key=${d.min}
              class="chip ${value === d.min ? 'chip-on' : ''}"
              onClick=${() => onPick(value === d.min ? '' : d.min)}>${d.label}</button>`,
        )}
        <!-- Always mirrors the value, including one a chip just set. Blanking
             it the moment a typed number matched a preset made the text vanish
             mid-keystroke. -->
        <input class="chip ${custom ? 'chip-on' : ''}" style=${{ width: '72px', textAlign: 'center' }}
          inputmode="numeric" value=${custom ? value : ''} placeholder="хв"
          onInput=${(e) => onPick(e.target.value)} />
      </div>
      ${error && html`<span class="field-error">${error}</span>`}
      <span class=${ok ? 'help-ok' : 'help'}>${summary}</span>
    </div>`
}

function costLabel(cost) {
  if (cost === '' || cost === undefined) return 'не записували'
  if (Number(cost) === 0) return 'безкоштовно'
  return `${cost} ₴`
}

// Tapping a row opens this, not the form. It answers "what did I just pick"
// before offering to change it, and it keeps Delete off the screen a person
// lands on by accident. It also shows the fields the form no longer has —
// location and status are still stored, just not edited from a phone.
export function AppointmentCard({ item, onEdit, onDelete, onClose }) {
  const when = [item.dayLabel, item.time].filter(Boolean).join(', ')
  const rows = [
    ['Хто', item.person],
    ['Де', item.location],
    ['Статус', item.status !== 'planned' ? statusLabel(item.status) : ''],
    ['Сума', costLabel(item.cost)],
    ['Нотатка', item.note],
  ].filter(([, v]) => v)

  return html`
    <div class="form">
      <div class="card" style=${{ padding: '18px', gap: '14px' }}>
        <div>
          <h1 class="card-title" style=${{ margin: 0, fontSize: '26px', fontWeight: 650, lineHeight: 1.15 }}>${item.title}</h1>
          <div class="person" style=${{ fontSize: '15px' }}>
            ${when}${item.endTime ? ` – ${item.endTime}` : ''}
          </div>
        </div>
        <dl style=${{ margin: 0, display: 'flex', flexDirection: 'column' }}>
          ${rows.map(
            ([label, value]) => html`
              <div class="row" key=${label} style=${{ gap: '12px' }}>
                <dt style=${{ flex: '0 0 80px', color: 'var(--hint)', fontSize: '14px' }}>${label}</dt>
                <dd style=${{ margin: 0, flex: 1, minWidth: 0, fontSize: '15px' }}>${value}</dd>
              </div>`,
          )}
        </dl>
      </div>
      <div class="actions">
        <button type="button" class="btn btn-primary" onClick=${onEdit}>Редагувати</button>
        <button type="button" class="btn" onClick=${onClose}>Закрити</button>
      </div>
      <button type="button" class="btn-danger" onClick=${onDelete}>Видалити запис</button>
    </div>`
}

// The edit form still says what it is editing: the card is behind it, not
// beside it, so the context would otherwise be gone.
function FormHead({ initial }) {
  if (!initial || !initial.id) {
    return html`<p class="form-head"><span class="form-head-title">Новий запис</span></p>`
  }
  const when = [initial.dayLabel, initial.time].filter(Boolean).join(', ')
  return html`
    <p class="form-head">
      <span class="form-head-title">${initial.title}</span>
      ${when && html`<span class="form-head-when">${when}</span>`}
    </p>`
}

export function AppointmentForm({ initial, persons, onSaved, onCancel }) {
  const isEdit = Boolean(initial && initial.id)
  const [values, setValues] = useState(() => ({ ...EMPTY, date: todayISO(), ...(initial || {}) }))
  const [error, setError] = useState(null)
  const [saving, setSaving] = useState(false)
  const dirty = useRef(false)

  const set = (name) => (e) => {
    dirty.current = true
    guardUnsaved(true)
    setValues((v) => ({ ...v, [name]: e.target.value }))
  }
  const done = () => {
    dirty.current = false
    guardUnsaved(false)
  }

  const submit = async (e) => {
    e.preventDefault()
    if (saving) return
    setSaving(true)
    setError(null)
    const body = {
      title: values.title, person: values.person, location: values.location,
      date: values.date, time: values.time,
      // The chips own the end of the appointment, so the stored end time is
      // recomputed from them rather than carried along stale.
      endTime: '', duration: values.duration,
      status: values.status, note: values.note, cost: values.cost,
    }
    try {
      if (isEdit) await api(`/appointments/${initial.id}`, { method: 'PUT', body })
      else await api('/appointments', { method: 'POST', body })
      haptic()
      done()
      onSaved()
    } catch (err) {
      setError(err)
      setSaving(false)
    }
  }

  const errFor = (f) => (error && error.field === f ? error.message : null)

  return html`
    <form class="form" onSubmit=${submit}>
      <${FormHead} initial=${initial} />

      <div class="card card-rows">
        <${Field} label="Що" error=${errFor('title')}>
          <input value=${values.title} onInput=${set('title')} placeholder="Ортодонт" />
        <//>
        <${Field} label="Хто">
          <input value=${values.person} onInput=${set('person')} list="persons" placeholder="Демид" />
          <datalist id="persons">${persons.map((p) => html`<option value=${p} key=${p} />`)}</datalist>
        <//>
        <div class="field-row">
          <${Field} label="Дата" error=${errFor('date')}>
            <input type="date" value=${values.date} onInput=${set('date')} />
          <//>
          <${Field} label="Початок" error=${errFor('date')}>
            <input type="time" value=${values.time} onInput=${set('time')} />
          <//>
        </div>
      </div>

      <${DurationPicker} value=${values.duration} startTime=${values.time} error=${errFor('duration')}
        onPick=${(min) => { guardUnsaved(true); setValues((v) => ({ ...v, duration: min })) }} />

      <div class="card card-rows">
        <${Field} label="Сума, ₴" error=${errFor('cost')}
          help="0 — безкоштовно · порожньо — не записували">
          <input value=${values.cost} onInput=${set('cost')} inputmode="decimal" placeholder="—" />
        <//>
        <${Field} label="Нотатка">
          <textarea rows="2" value=${values.note} onInput=${set('note')}></textarea>
        <//>
      </div>

      ${error && !error.field && html`<p class="field-error">${error.message}</p>`}
      <${Actions} saving=${saving} onCancel=${() => { done(); onCancel() }} />
    </form>`
}
