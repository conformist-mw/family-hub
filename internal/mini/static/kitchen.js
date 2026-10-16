import { html, useState, useEffect, useMemo } from '/mini/assets/vendor/preact-htm.module.js'
import { api, haptic, guardUnsaved } from '/mini/assets/api.js'
import { Loading, Failure, Actions } from '/mini/assets/ui.js'
import { IconChevron } from '/mini/assets/icons.js'

// Кухня: the week of meals, newest first, and the picker that records one.
// Every label arrives from the server; the client never parses a date.

function MealRow({ meal, onOpen }) {
  let main
  if (meal.eaten.length > 0) main = html`<span class="title">${meal.eaten.join(' · ')}</span>`
  else if (meal.planned) main = html`<span class="note">план: ${meal.planned}</span>`
  else main = html`<span class="note">—</span>`
  return html`
    <button class="row" onClick=${onOpen}>
      <span class="meal-name">${meal.title}</span>
      <div class="row-main">${main}</div>
      <span class="chev"><${IconChevron} /></span>
    </button>`
}

export function Kitchen({ days, onOpen }) {
  return html`
    <main class="screen">
      <h1 class="screen-title">Кухня</h1>
      ${days.map((day) => {
        const at = day.label.indexOf(', ')
        const head = at < 0 ? day.label : day.label.slice(0, at)
        const rest = at < 0 ? '' : day.label.slice(at + 2)
        return html`
          <section class="day" key=${day.date}>
            <h2 class="day-label">
              <span class=${head === 'Сьогодні' ? 'day-today' : ''}>${head}</span>
              ${rest && html`<span class="day-date">${rest}</span>`}
            </h2>
            <div class="card card-rows">
              ${day.meals.map(
                (m) => html`<${MealRow} key=${m.meal} meal=${m} onOpen=${() => onOpen(day.date, m.meal)} />`,
              )}
            </div>
          </section>`
      })}
    </main>`
}

// The same folding the server's store.NameKey does, near enough to decide
// whether a typed name is already a dish: case, spacing and the three
// apostrophes a keyboard can produce.
const nameKey = (s) =>
  s.toLowerCase().replace(/[ʼ’]/g, "'").replace(/\s+/g, ' ').trim()

const MAX_RESULTS = 30

// The picker is many dishes for one meal, which is why it exists: the group's
// keyboard answers one dish per tap and then closes the meal. Ticked dishes
// sit on top and untick on a tap; below them are the shortcuts — the plan,
// yesterday's pot, the usual — or, once something is typed, the catalogue
// searched. A name the catalogue does not have becomes a "+" chip and is
// created on save.
export function MealPicker({ date, meal, onSaved, onCancel }) {
  const [state, setState] = useState({ phase: 'loading' })
  const [picked, setPicked] = useState([]) // dish ids
  const [fresh, setFresh] = useState([]) // new names
  const [query, setQuery] = useState('')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState(null)

  const load = async () => {
    setState({ phase: 'loading' })
    try {
      const data = await api(`/meals/${date}/${meal}`)
      setPicked(data.selected || [])
      setFresh([])
      setState({ phase: 'ready', data })
    } catch (err) {
      setState({ phase: 'error', error: err })
    }
  }
  useEffect(() => { load() }, [date, meal])
  useEffect(() => () => guardUnsaved(false), [])

  const byId = useMemo(() => {
    const m = new Map()
    if (state.data) for (const d of state.data.dishes) m.set(d.id, d)
    return m
  }, [state.data])

  if (state.phase === 'loading') return html`<${Loading} />`
  if (state.phase === 'error') return html`<${Failure} error=${state.error} onRetry=${load} />`
  const data = state.data

  const touch = () => guardUnsaved(true)
  const toggle = (id) => {
    touch()
    setPicked((p) => (p.includes(id) ? p.filter((x) => x !== id) : [...p, id]))
  }
  const addFresh = (name) => {
    touch()
    const clean = name.replace(/\s+/g, ' ').trim()
    if (clean && !fresh.some((f) => nameKey(f) === nameKey(clean))) setFresh((f) => [...f, clean])
    setQuery('')
  }
  const dropFresh = (name) => {
    touch()
    setFresh((f) => f.filter((x) => x !== name))
  }

  const q = nameKey(query)
  let offered
  if (q) {
    offered = data.dishes.filter((d) => nameKey(d.name).includes(q)).slice(0, MAX_RESULTS).map((d) => d.id)
  } else {
    offered = data.quick
  }
  offered = offered.filter((id) => !picked.includes(id))
  const exact = q && (data.dishes.some((d) => nameKey(d.name) === q) || fresh.some((f) => nameKey(f) === q))

  const label = (d) => (d.proposed ? `🆕 ${d.name}` : d.name)

  const submit = async (e) => {
    e.preventDefault()
    if (saving) return
    setSaving(true)
    setError(null)
    try {
      await api(`/meals/${date}/${meal}`, { method: 'PUT', body: { dishIds: picked, newNames: fresh } })
      haptic()
      guardUnsaved(false)
      onSaved()
    } catch (err) {
      setError(err)
      setSaving(false)
    }
  }

  const nothing = picked.length === 0 && fresh.length === 0
  return html`
    <form class="form" onSubmit=${submit}>
      <p class="form-head">
        <span class="form-head-title">${data.title}</span>
        <span class="form-head-when">${data.dayLabel}</span>
      </p>

      <div class="card">
        <span class="label">Що їли</span>
        ${nothing
          ? html`<span class="help">Нічого не вибрано — тапни страву нижче</span>`
          : html`
            <div class="chips">
              ${picked.map((id) => {
                const d = byId.get(id)
                return d && html`
                  <button type="button" key=${id} class="chip chip-on" onClick=${() => toggle(id)}>
                    ${label(d)} ✕
                  </button>`
              })}
              ${fresh.map(
                (name) => html`
                  <button type="button" key=${'new:' + name} class="chip chip-on" onClick=${() => dropFresh(name)}>
                    + ${name} ✕
                  </button>`,
              )}
            </div>`}
      </div>

      <div class="card">
        <input class="search" type="search" value=${query} placeholder="Пошук або нова страва"
          onInput=${(e) => setQuery(e.target.value)}
          onKeyDown=${(e) => {
            if (e.key !== 'Enter') return
            e.preventDefault()
            if (offered.length === 1) { toggle(offered[0]); setQuery('') }
            else if (q && !exact) addFresh(query)
          }} />
        <div class="chips">
          ${offered.map((id) => {
            const d = byId.get(id)
            return d && html`
              <button type="button" key=${id}
                class="chip ${id === data.planned ? 'chip-plan' : ''}"
                onClick=${() => { toggle(id); setQuery('') }}>${label(d)}</button>`
          })}
          ${q && !exact &&
          html`<button type="button" class="chip chip-add" onClick=${() => addFresh(query)}>+ ${query.trim()}</button>`}
        </div>
        ${!q && html`<span class="help">План, вчорашнє й те, що їдять найчастіше. Решта — через пошук.</span>`}
        ${q && offered.length === 0 && exact && html`<span class="help">Уже вибрано</span>`}
      </div>

      ${error && html`<p class="field-error">${error.message}</p>`}
      <${Actions} saving=${saving} onCancel=${() => { guardUnsaved(false); onCancel() }} />
    </form>`
}
