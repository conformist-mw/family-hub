import { html, useState, useEffect } from '/mini/assets/vendor/preact-htm.module.js'
import { api, haptic, guardUnsaved, todayISO, dateLong } from '/mini/assets/api.js'
import { Field, Actions, Empty } from '/mini/assets/ui.js'
import { IconCheck, IconInfo } from '/mini/assets/icons.js'

// Recurring chores — the things that are neither lessons nor appointments:
// "enable cashback on the 1st", "log the mileage", "water the cactus every
// other week". This replaces what the family kept in iOS Reminders.
//
// The screen answers the daily question first — what is still open — and only
// then lists the chores themselves. Managing a rule is rare; closing today's
// item is what happens every morning.

// PRESETS are the rules worth a single tap. Anything else goes in the free
// field beside them: the store keeps a full RRULE, so an odd schedule is a
// typing job, not a missing feature.
const PRESETS = [
  { label: 'щодня', rrule: 'FREQ=DAILY' },
  { label: 'через день', rrule: 'FREQ=DAILY;INTERVAL=2' },
  { label: 'щотижня', rrule: 'FREQ=WEEKLY' },
  { label: 'раз на 2 тижні', rrule: 'FREQ=WEEKLY;INTERVAL=2' },
  { label: 'щомісяця', rrule: 'FREQ=MONTHLY' },
  { label: 'останній день місяця', rrule: 'FREQ=MONTHLY;BYMONTHDAY=-1' },
]

// dayLabel names a date the way the appointments tab does, so the two lists
// read as one app.
function dayLabel(iso, today) {
  if (iso === today) return 'Сьогодні'
  const t = new Date(`${today}T00:00`)
  t.setDate(t.getDate() + 1)
  const pad = (n) => String(n).padStart(2, '0')
  const tomorrow = `${t.getFullYear()}-${pad(t.getMonth() + 1)}-${pad(t.getDate())}`
  if (iso === tomorrow) return 'Завтра'
  return dateLong(iso).replace(/ \d{4}$/, '')
}

function groupByDay(items, today) {
  const days = []
  for (const it of items) {
    const last = days[days.length - 1]
    if (last && last.date === it.date) last.items.push(it)
    else days.push({ date: it.date, label: dayLabel(it.date, today), items: [it] })
  }
  return days
}

export function ReminderList({ data, onOpen, onAdd, onHistory, onMarked }) {
  const today = todayISO()
  const [busy, setBusy] = useState(null)

  const open = data.occurrences.filter((o) => o.canMark && o.status === 'pending')
  const ahead = data.occurrences.filter((o) => !o.canMark)
  const byId = new Map(data.reminders.map((r) => [r.id, r]))

  const mark = async (occ, status) => {
    if (busy) return
    setBusy(`${occ.reminderId}:${occ.dueAt}`)
    try {
      await api(`/reminders/${occ.reminderId}/occurrences`, {
        method: 'POST',
        body: { dueAt: occ.dueAt, status },
      })
      haptic()
      await onMarked()
    } catch (e) {
      haptic('error')
    } finally {
      setBusy(null)
    }
  }

  if (data.reminders.length === 0) {
    return html`
      <main class="screen">
        <h1 class="screen-title">Нагадування</h1>
        <${Empty}
          title="Нагадувань немає"
          text="Регулярні справи: кешбек 1-го числа, пробіг авто, полити кактус."
          action="Додати"
          onAction=${onAdd} />
      </main>`
  }

  return html`
    <main class="screen">
      <h1 class="screen-title">Нагадування</h1>

      ${open.length > 0 &&
      html`
        <h2 class="sec-title">Не закрито</h2>
        ${groupByDay(open, today).map(
          (day) => html`
            <section class="day" key=${day.date}>
              <h3 class="day-label ${day.date === today ? 'day-today' : ''}">
                ${day.label}
                ${day.date !== today && html`<span class="day-date">${dateLong(day.date).replace(/ \d{4}$/, '')}</span>`}
              </h3>
              ${day.items.map(
                (o) => html`
                  <div class="occ ${day.date !== today ? 'occ-late' : ''}" key=${o.dueAt + o.reminderId}>
                    <button
                      class="occ-check"
                      disabled=${busy !== null}
                      aria-label="Зроблено"
                      onClick=${() => mark(o, 'done')}><${IconCheck} /></button>
                    <div class="occ-body">
                      <div class="occ-title">${o.title}</div>
                      <div class="occ-meta">
                        ${o.time}${o.person ? ` · ${o.person}` : ''}
                      </div>
                    </div>
                    <button
                      class="occ-skip"
                      disabled=${busy !== null}
                      onClick=${() => mark(o, 'skipped')}>пропустити</button>
                  </div>`,
              )}
            </section>`,
        )}`}

      ${ahead.length > 0 &&
      html`
        <h2 class="sec-title">Далі</h2>
        ${groupByDay(ahead.slice(0, 20), today).map(
          (day) => html`
            <section class="day" key=${'a' + day.date}>
              <h3 class="day-label ${day.date === today ? 'day-today' : ''}">
                ${day.label}
                ${day.date !== today && html`<span class="day-date">${dateLong(day.date).replace(/ \d{4}$/, '')}</span>`}
              </h3>
              ${day.items.map(
                (o) => html`
                  <div class="occ occ-ahead" key=${o.dueAt + o.reminderId}>
                    <span class="occ-time">${o.time}</span>
                    <div class="occ-body">
                      <div class="occ-title">${o.title}</div>
                      ${o.person && html`<div class="occ-meta">${o.person}</div>`}
                    </div>
                  </div>`,
              )}
            </section>`,
        )}`}

      <h2 class="sec-title">Справи</h2>
      ${data.reminders.map(
        (r) => html`
          <section class="card ${r.active ? '' : 'card-off'}" key=${r.id}>
            <button class="course-head course-head-btn" onClick=${() => onOpen(r)}>
              <div>
                <div class="course-name">${r.title}</div>
                <div class="course-sub">
                  ${r.rule.text} · ${r.rule.time}${r.person ? ` · ${r.person}` : ''}
                </div>
              </div>
              ${!r.active && html`<div class="course-state state-empty">пауза</div>`}
            </button>
            ${r.note && html`<div class="meta"><${IconInfo} /> ${r.note}</div>`}
          </section>`,
      )}
      <div class="chips">
        <button class="chip chip-add" onClick=${onAdd}>+ нагадування</button>
        <button class="chip" onClick=${onHistory}>що зроблено</button>
      </div>
    </main>`
}

export function ReminderForm({ item, onSaved, onCancel }) {
  const isEdit = Boolean(item && item.id)
  const [values, setValues] = useState(() => ({
    title: item ? item.title : '',
    person: item ? item.person : '',
    note: item ? item.note : '',
    rrule: item ? item.rule.rrule : 'FREQ=MONTHLY',
    date: item ? item.rule.date : todayISO(),
    time: item ? item.rule.time : '08:00',
    active: item ? item.active : true,
  }))
  // How a changed rule should be applied. "forward" appends a version and
  // leaves the record of what already came due alone; "amend" rewrites the
  // version in place, for a rule that was mistyped rather than changed.
  const [ruleMode, setRuleMode] = useState('forward')
  const [error, setError] = useState(null)
  const [saving, setSaving] = useState(false)
  const [preview, setPreview] = useState(null)

  const ruleChanged =
    isEdit &&
    (values.rrule !== item.rule.rrule || values.date !== item.rule.date || values.time !== item.rule.time)

  const set = (name) => (e) => {
    guardUnsaved(true)
    setValues((v) => ({ ...v, [name]: e.target.value }))
  }
  const pickRule = (rrule) => {
    guardUnsaved(true)
    setValues((v) => ({ ...v, rrule }))
  }
  const done = () => guardUnsaved(false)

  // The preview is answered by the server, using the same library that will
  // expand the rule for real — so what this shows and what the calendar later
  // holds cannot disagree.
  useEffect(() => {
    let cancelled = false
    if (!values.rrule || !values.date || !values.time) {
      setPreview(null)
      return
    }
    const t = setTimeout(async () => {
      try {
        const out = await api('/reminders/preview', {
          method: 'POST',
          body: { rrule: values.rrule, date: values.date, time: values.time },
        })
        if (!cancelled) setPreview({ ok: true, next: out.next })
      } catch (e) {
        if (!cancelled) setPreview({ ok: false, message: e.message })
      }
    }, 250)
    return () => {
      cancelled = true
      clearTimeout(t)
    }
  }, [values.rrule, values.date, values.time])

  const fieldError = (name) => (error && error.field === name ? error.message : null)

  const submit = async (e) => {
    e.preventDefault()
    if (saving) return
    setSaving(true)
    setError(null)
    try {
      if (isEdit) {
        await api(`/reminders/${item.id}`, {
          method: 'PUT',
          body: {
            title: values.title,
            person: values.person,
            note: values.note,
            durationMin: 15,
            active: values.active,
          },
        })
        if (ruleChanged) {
          const body = { rrule: values.rrule, date: values.date, time: values.time }
          if (ruleMode === 'amend') {
            await api(`/reminders/${item.id}/rules/${item.rule.id}`, { method: 'PUT', body })
          } else {
            await api(`/reminders/${item.id}/rules`, { method: 'POST', body })
          }
        }
      } else {
        await api('/reminders', {
          method: 'POST',
          body: {
            title: values.title,
            person: values.person,
            note: values.note,
            durationMin: 15,
            rrule: values.rrule,
            date: values.date,
            time: values.time,
          },
        })
      }
      haptic()
      done()
      await onSaved()
    } catch (err) {
      setError(err)
      haptic('error')
    } finally {
      setSaving(false)
    }
  }

  const remove = async () => {
    if (!confirm(`Видалити «${item.title}»?`)) return
    setSaving(true)
    try {
      await api(`/reminders/${item.id}`, { method: 'DELETE' })
      haptic()
      done()
      await onSaved()
    } catch (err) {
      setError(err)
      setSaving(false)
    }
  }

  const custom = !PRESETS.some((p) => p.rrule === values.rrule)

  return html`
    <main class="screen">
      <h1 class="screen-title">${isEdit ? 'Нагадування' : 'Нове нагадування'}</h1>
      <form onSubmit=${submit}>
        <${Field} label="Що зробити" error=${fieldError('title')}>
          <input value=${values.title} onInput=${set('title')}
            placeholder="Виставити кешбек" />
        <//>

        <${Field} label="Хто">
          <input value=${values.person} onInput=${set('person')} placeholder="необов'язково" />
        <//>

        <${Field} label="О котрій" error=${fieldError('time')}>
          <input type="time" value=${values.time} onInput=${set('time')} />
        <//>

        <div class="card">
          <span class="label">Як часто</span>
          <div class="chips">
            ${PRESETS.map(
              (p) => html`
                <button type="button" key=${p.rrule}
                  class="chip ${values.rrule === p.rrule ? 'chip-on' : ''}"
                  onClick=${() => pickRule(p.rrule)}>${p.label}</button>`,
            )}
          </div>
          <!-- The full RRULE, for the schedules no chip covers. Shown always
               rather than behind a toggle: it is also where a rule copied from
               somewhere else gets pasted. -->
          <input class="chip ${custom ? 'chip-on' : ''}"
            style=${{ width: '100%', textAlign: 'left', marginTop: '8px' }}
            value=${values.rrule} onInput=${set('rrule')}
            placeholder="FREQ=MONTHLY;BYMONTHDAY=1" />
          ${fieldError('rrule') && html`<span class="field-error">${fieldError('rrule')}</span>`}
        </div>

        <${Field} label="Відлік з" error=${fieldError('date')}
          help="Задає фазу — з якого саме тижня рахувати «раз на 2 тижні». Для «щомісяця» це просто число.">
          <input type="date" value=${values.date} onInput=${set('date')} />
        <//>

        ${preview &&
        html`
          <div class="card">
            <span class="label">Найближчі</span>
            ${preview.ok
              ? html`<div class="chips">
                  ${preview.next.map(
                    (n) => html`<span class="chip" key=${n.dueAt}>
                      ${dateLong(n.date).replace(/ \d{4}$/, '')}, ${n.time}
                    </span>`,
                  )}
                </div>`
              : html`<span class="field-error">${preview.message}</span>`}
          </div>`}

        ${ruleChanged &&
        html`
          <div class="card">
            <span class="label">Правило змінилось</span>
            <div class="chips">
              <button type="button" class="chip ${ruleMode === 'forward' ? 'chip-on' : ''}"
                onClick=${() => setRuleMode('forward')}>з цього моменту</button>
              <button type="button" class="chip ${ruleMode === 'amend' ? 'chip-on' : ''}"
                onClick=${() => setRuleMode('amend')}>виправити запис</button>
            </div>
            <span class="help">
              ${ruleMode === 'forward'
                ? 'Те, що вже настало, лишиться як було.'
                : 'Виправляє саме правило — для випадку, коли його ввели з помилкою.'}
            </span>
          </div>`}

        <${Field} label="Нотатка">
          <input value=${values.note} onInput=${set('note')} placeholder="необов'язково" />
        <//>

        ${isEdit &&
        html`
          <div class="card">
            <span class="label">Стан</span>
            <div class="chips">
              <button type="button" class="chip ${values.active ? 'chip-on' : ''}"
                onClick=${() => { guardUnsaved(true); setValues((v) => ({ ...v, active: true })) }}>активне</button>
              <button type="button" class="chip ${!values.active ? 'chip-on' : ''}"
                onClick=${() => { guardUnsaved(true); setValues((v) => ({ ...v, active: false })) }}>пауза</button>
            </div>
            <span class="help">
              Пауза не видаляє історію. Коли ввімкнеш назад, за час паузи нічого не додасться.
            </span>
          </div>`}

        ${error && !error.field && html`<p class="field-error">${error.message}</p>`}

        <${Actions} saving=${saving} onCancel=${() => { done(); onCancel() }}
          onDelete=${isEdit ? remove : null} />
      </form>
    </main>`
}
