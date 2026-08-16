import { html, useState } from '/mini/assets/vendor/preact-htm.module.js'
import { api, haptic, guardUnsaved, todayISO, dateLong } from '/mini/assets/api.js'
import { Field, Actions } from '/mini/assets/ui.js'

// Recording money against a course, and fixing it afterwards. Opened from the
// course card, so a new payment never asks which course; opened from a row on
// the home screen, so an existing one is already the row that looked wrong.

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
// can be shifted by a timezone; the offsets are normalised by hand. An edit
// whose month falls outside the four is added, so the form can always show
// what the row actually holds.
function monthOptions(iso, extra) {
  const [y, m] = iso.split('-').map(Number)
  const values = [-1, 0, 1, 2].map((delta) => {
    const index = m - 1 + delta
    return { year: y + Math.floor(index / 12), month: ((index % 12) + 12) % 12 }
  })
  if (extra && !values.some((v) => monthValue(v) === extra)) {
    const [ey, em] = extra.split('-').map(Number)
    if (Number.isFinite(ey) && Number.isFinite(em)) values.unshift({ year: ey, month: em - 1 })
  }
  return values.map((v) => ({
    value: monthValue(v),
    // The year is only worth saying when it is not the one we are in.
    label: v.year === y ? MONTHS[v.month] : `${MONTHS[v.month]} ${v.year}`,
  }))
}

const monthValue = ({ year, month }) => `${year}-${String(month + 1).padStart(2, '0')}`

export function PaymentForm({ course, payment, onSaved, onCancel }) {
  const isEdit = Boolean(payment && payment.id)
  // A new payment takes the billing from the course card it was opened from;
  // an existing one from the row, which carries its course's billing type.
  const monthly = (isEdit ? payment.billing : course.billing) === 'monthly'
  const today = todayISO()
  const [values, setValues] = useState(() => ({
    date: isEdit ? payment.dateISO : today,
    amount: isEdit ? payment.value : '',
    lessons: isEdit ? payment.lessons : '',
    month: (isEdit && payment.month) || today.slice(0, 7),
    comment: (isEdit && payment.comment) || '',
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
      if (isEdit) await api(`/payments/${payment.id}`, { method: 'PUT', body })
      else await api(`/courses/${course.id}/payments`, { method: 'POST', body })
      haptic()
      done()
      onSaved()
    } catch (err) {
      setError(err)
      setSaving(false)
    }
  }

  const remove = async () => {
    // Deleting a payment moves the balance, and unlike a visit it leaves no
    // row behind — hence the question.
    if (!confirm('Видалити цю оплату? Баланс перерахується.')) return
    setSaving(true)
    try {
      await api(`/payments/${payment.id}`, { method: 'DELETE' })
      done()
      onSaved()
    } catch (err) {
      setError(err)
      setSaving(false)
    }
  }

  const errFor = (f) => (error && error.field === f ? error.message : null)
  const head = isEdit ? `${payment.course} · ${payment.person}` : `${course.name} · ${course.person}`

  return html`
    <form class="form" onSubmit=${submit}>
      <p class="form-head">
        <span class="form-head-title">${isEdit ? 'Оплата' : 'Нова оплата'}</span>
        <span class="form-head-when">${head}</span>
      </p>

      ${!isEdit && course.balance &&
      html`<p class="sec-empty">Зараз: <span class="state-${course.state}">${course.balance}</span></p>`}

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
            ${monthOptions(today, isEdit ? payment.month : '').map(
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
      <${Actions} saving=${saving} onCancel=${() => { done(); onCancel() }}
        onDelete=${isEdit ? remove : null} deleteLabel="Видалити оплату" />
    </form>`
}
