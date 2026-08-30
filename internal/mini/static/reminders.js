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

// describeRule turns a rule into Ukrainian. The stored form is a full RRULE
// because that is what expands correctly, but it is machinery: nobody reading
// a list of chores should have to parse FREQ=WEEKLY;INTERVAL=2;BYDAY=SA.
//
// It reads the parts it knows and says so plainly; a rule built from parts it
// does not know is called what it is rather than described wrongly. The raw
// text still lives in the form, which is where a rule is meant to be edited.

const DAY_SHORT = { SU: 'нд', MO: 'пн', TU: 'вт', WE: 'ср', TH: 'чт', FR: 'пт', SA: 'сб' }
// The "every Tuesday" form reads better than "weekly, Tue" when there is only
// one day, and Ukrainian builds it per weekday rather than from a suffix.
const EVERY_DAY_OF_WEEK = {
  SU: 'щонеділі', MO: 'щопонеділка', TU: 'щовівторка', WE: 'щосереди',
  TH: 'щочетверга', FR: 'щоп\'ятниці', SA: 'щосуботи',
}

// plural picks the Ukrainian form: 1 день, 2 дні, 5 днів.
function plural(n, forms) {
  const mod10 = n % 10
  const mod100 = n % 100
  if (mod10 === 1 && mod100 !== 11) return forms[0]
  if (mod10 >= 2 && mod10 <= 4 && (mod100 < 12 || mod100 > 14)) return forms[1]
  return forms[2]
}

function parseRule(rrule) {
  const parts = {}
  for (const chunk of String(rrule || '').replace(/^RRULE:/, '').split(';')) {
    const [k, v] = chunk.split('=')
    if (k) parts[k.toUpperCase()] = (v || '').toUpperCase()
  }
  return parts
}

function describeRule(rrule) {
  const p = parseRule(rrule)
  const every = Number(p.INTERVAL || 1)
  const days = p.BYDAY ? p.BYDAY.split(',').filter((d) => DAY_SHORT[d]) : []
  // A positional day ("2SU" — the second Sunday) is a shape this does not
  // describe; fall through rather than call it plain Sunday.
  const positional = p.BYDAY && p.BYDAY.split(',').some((d) => /\d/.test(d))
  const dayList = days.map((d) => DAY_SHORT[d]).join(', ')

  if (p.FREQ === 'DAILY' && !p.BYDAY) {
    if (every === 1) return 'щодня'
    if (every === 2) return 'через день'
    return `кожні ${every} ${plural(every, ['день', 'дні', 'днів'])}`
  }

  if (p.FREQ === 'WEEKLY' && !positional) {
    if (every === 1) {
      if (days.length === 1) return EVERY_DAY_OF_WEEK[days[0]]
      if (days.length > 1) return `щотижня: ${dayList}`
      return 'щотижня'
    }
    const base = every === 2
      ? 'раз на 2 тижні'
      : `кожні ${every} ${plural(every, ['тиждень', 'тижні', 'тижнів'])}`
    return days.length > 0 ? `${base}, ${dayList}` : base
  }

  if (p.FREQ === 'MONTHLY' && !p.BYDAY) {
    const day = p.BYMONTHDAY ? Number(p.BYMONTHDAY) : null
    if (day === -1) return every === 1 ? 'останній день місяця' : `останній день кожні ${every} місяці`
    const on = day ? `, ${day}-го` : ''
    if (every === 1) return `щомісяця${on}`
    if (every === 2) return `раз на 2 місяці${on}`
    return `кожні ${every} ${plural(every, ['місяць', 'місяці', 'місяців'])}${on}`
  }

  if (p.FREQ === 'YEARLY' && !p.BYDAY && !p.BYMONTH) {
    return every === 1 ? 'щороку' : `кожні ${every} ${plural(every, ['рік', 'роки', 'років'])}`
  }

  return 'за власним правилом'
}

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

export function ReminderList({ data, onOpen, onAdd, onMarked }) {
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
                  ${describeRule(r.rule.rrule)} · ${r.rule.time}${r.person ? ` · ${r.person}` : ''}
                </div>
              </div>
              ${!r.active && html`<div class="course-state state-empty">пауза</div>`}
            </button>
            ${r.note && html`<div class="meta"><${IconInfo} /> ${r.note}</div>`}
          </section>`,
      )}
      <div class="chips">
        <button class="chip chip-add" onClick=${onAdd}>+ нагадування</button>
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
