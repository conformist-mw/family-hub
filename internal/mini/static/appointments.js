import { html, useState, useRef } from '/mini/assets/vendor/preact-htm.module.js'
import { api, haptic, guardUnsaved, todayISO } from '/mini/assets/api.js'
import { Field, Actions } from '/mini/assets/ui.js'

const STATUSES = [
  { value: 'planned', label: 'заплановано' },
  { value: 'done', label: 'було' },
  { value: 'cancelled', label: 'скасовано' },
]

const statusLabel = (v) => (STATUSES.find((s) => s.value === v) || { label: v }).label

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

export function AppointmentList({ days, onOpen, onAdd }) {
  return html`
    <main>
      ${days.length === 0 && html`<div class="center muted">Попереду візитів немає</div>`}
      ${days.map(
        (day) => html`
          <section class="day" key=${day.date}>
            <h2 class="day-label">${day.label}</h2>
            <ul class="items">
              ${day.items.map((it) => html`<${Item} key=${it.id} item=${it} date=${day.date} onOpen=${onOpen} />`)}
            </ul>
          </section>`,
      )}
      <button class="fab" onClick=${onAdd} aria-label="Додати запис">+</button>
    </main>`
}

// Location and status have no inputs here. Location was never once filled in,
// and everything entered on a phone is something being planned. They stay in
// the payload all the same: this form PUTs the whole appointment, so dropping
// them would wipe whatever the web UI or the bot had recorded.
const EMPTY = {
  title: '', person: '', location: '', date: '', time: '',
  endTime: '', duration: '', status: 'planned', note: '', cost: '',
}

// "How long does it take" is a tap; "when does it end" is arithmetic the
// person has to do themselves. The end time is computed server-side.
const DURATIONS = [
  { min: '30', label: '30 хв' },
  { min: '45', label: '45 хв' },
  { min: '60', label: '1 год' },
  { min: '120', label: '2 год' },
]

function DurationPicker({ value, onPick, error }) {
  const custom = value !== '' && !DURATIONS.some((d) => d.min === value)
  return html`
    <div class="field">
      <span class="label">Скільки триває</span>
      <div class="chips">
        ${DURATIONS.map(
          (d) => html`
            <button type="button" key=${d.min}
              class="chip ${value === d.min ? 'chip-on' : ''}"
              onClick=${() => onPick(value === d.min ? '' : d.min)}>${d.label}</button>`,
        )}
        <input class="chip-input ${custom ? 'chip-on' : ''}" inputmode="numeric"
          value=${custom ? value : ''} placeholder="свої хв"
          onInput=${(e) => onPick(e.target.value)} />
      </div>
      ${error && html`<span class="field-error">${error}</span>`}
      <span class="help">Порожньо — тривалість не задана</span>
    </div>`
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

  const errFor = (f) => (error && error.field === f ? error.message : null)

  return html`
    <form class="form" onSubmit=${submit}>
      <${Field} label="Що" error=${errFor('title')}>
        <input value=${values.title} onInput=${set('title')} placeholder="Ортодонт" />
      <//>
      <${Field} label="Хто">
        <input value=${values.person} onInput=${set('person')} list="persons" placeholder="Демид" />
        <datalist id="persons">${persons.map((p) => html`<option value=${p} key=${p} />`)}</datalist>
      <//>
      <div class="row">
        <${Field} label="Дата" error=${errFor('date')}>
          <input type="date" value=${values.date} onInput=${set('date')} />
        <//>
        <${Field} label="Початок" error=${errFor('date')}>
          <input type="time" value=${values.time} onInput=${set('time')} />
        <//>
      </div>
      <${DurationPicker} value=${values.duration} error=${errFor('duration')}
        onPick=${(min) => { guardUnsaved(true); setValues((v) => ({ ...v, duration: min })) }} />
      <${Field} label="Сума" error=${errFor('cost')}>
        <input value=${values.cost} onInput=${set('cost')} inputmode="decimal" placeholder="—" />
        <span class="help">0 — безкоштовно · порожньо — не записували</span>
      <//>
      <${Field} label="Нотатка">
        <textarea rows="2" value=${values.note} onInput=${set('note')}></textarea>
      <//>
      ${error && !error.field && html`<p class="error">${error.message}</p>`}
      <${Actions} saving=${saving} onCancel=${() => { done(); onCancel() }}
        onDelete=${isEdit ? remove : null} />
    </form>`
}
