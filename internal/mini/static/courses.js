import { html, useState } from '/mini/assets/vendor/preact-htm.module.js'
import { api, haptic, guardUnsaved } from '/mini/assets/api.js'
import { Field, Actions } from '/mini/assets/ui.js'

// Courses and, the point of this tab, their weekly schedule. Moving a lesson
// used to mean adding the new times and deleting the old ones by hand, which
// is why "Логопед, Вівторок Четвер, 13:35" stayed a chat message nobody acted
// on. Here a time is a chip you tap.

export function CourseList({ courses, weekdays, onEditSlot, onAddSlot }) {
  if (courses.length === 0) {
    return html`<div class="center muted">Активних курсів немає</div>`
  }
  return html`
    <main>
      ${courses.map(
        (c) => html`
          <section class="course" key=${c.id}>
            <div class="course-head">
              <div>
                <div class="title">${c.name}</div>
                <div class="person">${c.person}${c.note ? ` · ${c.note}` : ''}</div>
              </div>
            </div>
            <div class="slots">
              ${c.schedule.map(
                (s) => html`
                  <button class="slot" key=${s.id} onClick=${() => onEditSlot(c, s)}>
                    ${weekdays[s.weekday]} ${s.time}
                  </button>`,
              )}
              <button class="slot slot-add" onClick=${() => onAddSlot(c)}>+ час</button>
            </div>
            ${c.schedule.length === 0 && html`<div class="muted small">Розклад не заданий</div>`}
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

  return html`
    <form class="form" onSubmit=${submit}>
      <p class="form-head">${course.name} · ${course.person}</p>
      <${Field} label="День тижня" error=${errFor('weekday')}>
        <select value=${values.weekday} onChange=${set('weekday')}>
          ${weekdays.map((name, i) => html`<option value=${String(i)} key=${i}>${name}</option>`)}
        </select>
      <//>
      <${Field} label="Час" error=${errFor('time')}>
        <input type="time" value=${values.time} onInput=${set('time')} />
      <//>
      <${Field} label="Тривалість, хв" error=${errFor('duration')}>
        <input value=${values.duration} onInput=${set('duration')} inputmode="numeric" />
      <//>
      ${error && !error.field && html`<p class="error">${error.message}</p>`}
      <${Actions} saving=${saving} onCancel=${() => { done(); onCancel() }}
        onDelete=${isEdit ? remove : null} />
    </form>`
}
