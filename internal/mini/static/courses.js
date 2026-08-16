import { html, useState } from '/mini/assets/vendor/preact-htm.module.js'
import { api, haptic, guardUnsaved } from '/mini/assets/api.js'
import { Field, Actions, Empty, Bar } from '/mini/assets/ui.js'
import { IconInfo } from '/mini/assets/icons.js'

// Courses and, the point of this tab, their weekly schedule. Moving a lesson
// used to mean adding the new times and deleting the old ones by hand, which
// is why "Логопед, Вівторок Четвер, 13:35" stayed a chat message nobody acted
// on. Here a time is a chip you tap.

export function CourseList({ courses, weekdays, onEditSlot, onAddSlot, onAddPayment, onOpenAudit }) {
  if (courses.length === 0) {
    return html`<main><${Empty} title="Активних курсів немає" /></main>`
  }
  return html`
    <main class="screen">
      <h1 class="screen-title">Заняття</h1>
      ${courses.map(
        (c) => html`
          <section class="card" key=${c.id}>
            <div class="course-head">
              <div>
                <div class="course-name">${c.name}</div>
                <div class="course-sub">${c.person}${c.note ? ` · ${c.note}` : ''}</div>
              </div>
              ${c.balance && html`<div class="course-state state-${c.state}">${c.balance}</div>`}
            </div>
            ${c.state && html`<${Bar} state=${c.state} />`}
            ${c.absence && html`<div class="meta"><${IconInfo} /> ${c.absence}</div>`}
            <div class="chips">
              ${c.schedule.map(
                (s) => html`
                  <button class="chip" key=${s.id} onClick=${() => onEditSlot(c, s)}>
                    ${weekdays[s.weekday]} ${s.time}
                  </button>`,
              )}
              <button class="chip chip-add" onClick=${() => onAddSlot(c)}>+ час</button>
            </div>
            ${c.schedule.length === 0 && html`<div class="meta">Розклад не заданий</div>`}
            <!-- Its own row, not another chip beside the times: the row above
                 is when the course happens, this is money. -->
            <div class="chips">
              <button class="chip chip-add" onClick=${() => onAddPayment(c)}>₴ оплата</button>
              <button class="chip chip-add" onClick=${() => onOpenAudit(c)}>звірка</button>
            </div>
          </section>`,
      )}
    </main>`
}

export function SlotForm({ course, slot, weekdays, onSaved, onCancel }) {
  const isEdit = Boolean(slot && slot.id)
  const [values, setValues] = useState(() => ({
    weekday: String(slot ? slot.weekday : 1),
    time: slot ? slot.time : '',
    duration: String(slot ? slot.durationMin : 60),
  }))
  const [error, setError] = useState(null)
  const [saving, setSaving] = useState(false)

  const set = (name) => (e) => {
    guardUnsaved(true)
    setValues((v) => ({ ...v, [name]: e.target.value }))
  }
  const pickDay = (i) => {
    guardUnsaved(true)
    setValues((v) => ({ ...v, weekday: String(i) }))
  }
  const done = () => guardUnsaved(false)

  const submit = async (e) => {
    e.preventDefault()
    if (saving) return
    setSaving(true)
    setError(null)
    try {
      if (isEdit) await api(`/slots/${slot.id}`, { method: 'PUT', body: values })
      else await api(`/courses/${course.id}/slots`, { method: 'POST', body: values })
      haptic()
      done()
      onSaved()
    } catch (err) {
      setError(err)
      setSaving(false)
    }
  }

  const remove = async () => {
    if (!confirm('Прибрати цей час із розкладу?')) return
    setSaving(true)
    try {
      await api(`/slots/${slot.id}`, { method: 'DELETE' })
      done()
      onSaved()
    } catch (err) {
      setError(err)
      setSaving(false)
    }
  }

  const errFor = (f) => (error && error.field === f ? error.message : null)

  // Weekdays arrive Sunday-first (Go's own order) but a school week starts on
  // Monday, so the buttons are reordered for reading while the value stays
  // the index the server sends.
  const order = [1, 2, 3, 4, 5, 6, 0]

  return html`
    <form class="form" onSubmit=${submit}>
      <p class="form-head">
        <span class="form-head-title">${isEdit ? 'Час у розкладі' : 'Новий час'}</span>
        <span class="form-head-when">${course.name} · ${course.person}</span>
      </p>

      <div class="card">
        <span class="label">День тижня</span>
        <div class="days">
          ${order.map(
            (i) => html`
              <button type="button" key=${i}
                class="day-btn ${values.weekday === String(i) ? 'day-on' : ''}"
                onClick=${() => pickDay(i)}>${weekdays[i]}</button>`,
          )}
        </div>
        ${errFor('weekday') && html`<span class="field-error">${errFor('weekday')}</span>`}
      </div>

      <div class="card card-rows">
        <div class="field-row">
          <${Field} label="Час" error=${errFor('time')}>
            <input type="time" value=${values.time} onInput=${set('time')} />
          <//>
          <${Field} label="Триває, хв" error=${errFor('duration')}>
            <input value=${values.duration} onInput=${set('duration')} inputmode="numeric" />
          <//>
        </div>
      </div>

      ${error && !error.field && html`<p class="field-error">${error.message}</p>`}
      <${Actions} saving=${saving} onCancel=${() => { done(); onCancel() }}
        onDelete=${isEdit ? remove : null} deleteLabel="Прибрати з розкладу" />
    </form>`
}
