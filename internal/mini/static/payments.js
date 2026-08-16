import { html, useState } from '/mini/assets/vendor/preact-htm.module.js'
import { api, haptic, guardUnsaved, todayISO, dateLong } from '/mini/assets/api.js'
import { Field, Actions } from '/mini/assets/ui.js'

// Recording money against a course. Opened from the course card, so the course
// is already decided and this form never asks which one — the one question the
// web form has to ask first.

// Nominative month names, for a chip that names a month on its own rather than
// a day inside it ("вересень", not "6 вересня"). Duplicated from Go for the
// same reason dateLong is: the month is picked here, from the phone's own
// clock, so the server never sees it and cannot label it.
const MONTHS = [
  'січень', 'лютий', 'березень', 'квітень', 'травень', 'червень',
  'липень', 'серпень', 'вересень', 'жовтень', 'листопад', 'грудень',
]

// A monthly fee is paid for the month around now: the one running, the next
// one (September settled in late August is the normal shape), one further
// ahead, or the one just gone when it is being caught up on. Offering those
// four beats an <input type="month">, which iOS renders as a bare text box.
//
// Built from a plain year/month pair rather than Date arithmetic so nothing
// can be shifted by a timezone; the offsets are normalised by hand.
function monthOptions(iso) {
  const [y, m] = iso.split('-').map(Number)
  return [-1, 0, 1, 2].map((delta) => {
    const index = m - 1 + delta
    const year = y + Math.floor(index / 12)
    const month = ((index % 12) + 12) % 12
    const value = `${year}-${String(month + 1).padStart(2, '0')}`
    // The year is only worth saying when it is not the one we are in.
    return { value, label: year === y ? MONTHS[month] : `${MONTHS[month]} ${year}` }
  })
}

export function PaymentForm({ course, onSaved, onCancel }) {
  const monthly = course.billing === 'monthly'
  const today = todayISO()
  const [values, setValues] = useState(() => ({
    date: today,
    amount: '',
    lessons: '',
    month: today.slice(0, 7),
    comment: '',
  }))
  const [error, setError] = useState(null)
  const [saving, setSaving] = useState(false)

  const set = (name) => (e) => {
    guardUnsaved(true)
    setValues((v) => ({ ...v, [name]: e.target.value }))
  }
  const pickMonth = (value) => {
    guardUnsaved(true)
    setValues((v) => ({ ...v, month: value }))
  }
  const done = () => guardUnsaved(false)

  const submit = async (e) => {
    e.preventDefault()
    if (saving) return
    setSaving(true)
    setError(null)
    // Both fields always ride along; the server keeps whichever the course's
    // billing calls for and ignores the other.
    const body = {
      date: values.date,
      amount: values.amount,
      lessons: values.lessons,
      month: values.month,
      comment: values.comment,
    }
    try {
      await api(`/courses/${course.id}/payments`, { method: 'POST', body })
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
      <p class="form-head">
        <span class="form-head-title">Нова оплата</span>
        <span class="form-head-when">${course.name} · ${course.person}</span>
      </p>

      ${course.balance && html`<p class="sec-empty">Зараз: <span class="state-${course.state}">${course.balance}</span></p>`}

      <div class="card card-rows">
        <${Field} label="Сума, ₴" error=${errFor('amount')}>
          <input value=${values.amount} onInput=${set('amount')} inputmode="decimal" placeholder="3200" />
        <//>
        ${!monthly &&
        html`
          <${Field} label="Оплачено занять" error=${errFor('lessons')}>
            <input value=${values.lessons} onInput=${set('lessons')} inputmode="numeric" placeholder="8" />
          <//>`}
        <${Field} label="Дата оплати" error=${errFor('date')} help=${dateLong(values.date)}>
          <input type="date" value=${values.date} onInput=${set('date')} />
        <//>
      </div>

      ${monthly &&
      html`
        <div class="card">
          <span class="label">За який місяць</span>
          <div class="chips">
            ${monthOptions(today).map(
              (m) => html`
                <button type="button" key=${m.value}
                  class="chip ${values.month === m.value ? 'chip-on' : ''}"
                  onClick=${() => pickMonth(m.value)}>${m.label}</button>`,
            )}
          </div>
          ${errFor('month') && html`<span class="field-error">${errFor('month')}</span>`}
          <span class="help">Не те саме, що дата переказу вище</span>
        </div>`}

      <div class="card card-rows">
        <${Field} label="Коментар">
          <textarea rows="2" value=${values.comment} onInput=${set('comment')}></textarea>
        <//>
      </div>

      ${error && !error.field && html`<p class="field-error">${error.message}</p>`}
      <${Actions} saving=${saving} onCancel=${() => { done(); onCancel() }} />
    </form>`
}
