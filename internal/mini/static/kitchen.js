import { html, useState, useEffect, useMemo } from '/mini/assets/vendor/preact-htm.module.js'
import { api, haptic, guardUnsaved } from '/mini/assets/api.js'
import { Loading, Failure, Actions } from '/mini/assets/ui.js'
import { IconChevron } from '/mini/assets/icons.js'

// Кухня: the week of meals, newest first; the morning menu, where today's
// plan is made; and the picker that records what one meal turned out to be.
// Every label arrives from the server; the client never parses a date.

function MealRow({ meal, onOpen }) {
  let main
  if (meal.eaten.length > 0) main = html`<span class="title">${meal.eaten.join(' · ')}</span>`
  else if (meal.planned.length > 0) main = html`<span class="note">план: ${meal.planned.join(' · ')}</span>`
  else main = html`<span class="note">—</span>`
  return html`
    <button class="row" onClick=${onOpen}>
      <span class="meal-name">${meal.title}</span>
      <div class="row-main">${main}</div>
      <span class="chev"><${IconChevron} /></span>
    </button>`
}

export function Kitchen({ days, onOpen, onPlan }) {
  return html`
    <main class="screen">
      <h1 class="screen-title">Кухня</h1>
      <button class="btn btn-primary" style=${{ flex: 'none' }} onClick=${() => onPlan(days[0].date)}>Меню на сьогодні</button>
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

const dishLabel = (d) => (d.proposed ? `🆕 ${d.name}` : d.name)

// A set of dishes being chosen: ids from the catalogue plus dishes it does
// not have yet ({name, meal, days}). Both screens hold one per meal.
function useDishSet(initial) {
  const [picked, setPicked] = useState(initial)
  const [fresh, setFresh] = useState([])
  const touch = () => guardUnsaved(true)
  return {
    picked, fresh,
    reset(ids) { setPicked(ids); setFresh([]) },
    toggle(id) { touch(); setPicked((p) => (p.includes(id) ? p.filter((x) => x !== id) : [...p, id])) },
    pick(id) { touch(); setPicked((p) => (p.includes(id) ? p : [...p, id])) },
    addFresh(d) {
      const name = d.name.replace(/\s+/g, ' ').trim()
      if (!name) return
      touch()
      setFresh((f) => (f.some((x) => nameKey(x.name) === nameKey(name)) ? f : [...f, { ...d, name }]))
    },
    dropFresh(name) { touch(); setFresh((f) => f.filter((x) => x.name !== name)) },
    body() { return { dishIds: picked, newDishes: fresh } },
  }
}

// What was typed is read by the model that reads a photo of the plate: it
// matches the catalogue ("морковь по-корейски" is the "Морква по-корейськи"
// already there), names a new dish in Ukrainian, and splits a line naming
// several. Without a model the name goes in as typed.
async function resolveTyped(set, text) {
  try {
    const { items } = await api('/dishes/resolve', { method: 'POST', body: { text } })
    for (const it of items) {
      if (it.id) set.pick(it.id)
      else set.addFresh({ name: it.name, meal: it.meal, days: it.days })
    }
    return items.length > 0 ? null : 'Не зрозумів, що це за страва'
  } catch (err) {
    if (err.code === 'no_model') {
      set.addFresh({ name: text })
      return null
    }
    return err.message || 'Не вдалося розпізнати'
  }
}

// DishChooser is the chips for one set: the chosen ones on top, which untick
// on a tap; under them the offer — or, once something is typed, the catalogue
// searched — and a "+" chip for a name the catalogue does not have.
// `marked` are offered dishes drawn with an outline (the plan, on the evening
// picker); `prefix` puts a mark before some of them (↩ for yesterday's pot);
// `extra` is one more chip at the end of the offer (more suggestions).
function DishChooser({ set, byId, dishes, offered, marked = [], prefix = {}, extra, hint }) {
  const [query, setQuery] = useState('')
  const [resolving, setResolving] = useState(false)
  const [miss, setMiss] = useState(null)
  const q = nameKey(query)
  const resolve = async () => {
    if (resolving) return
    setResolving(true)
    setMiss(null)
    const problem = await resolveTyped(set, query.trim())
    setResolving(false)
    if (problem) setMiss(problem)
    else setQuery('')
  }
  let shown = q
    ? dishes.filter((d) => nameKey(d.name).includes(q)).slice(0, MAX_RESULTS).map((d) => d.id)
    : offered
  shown = shown.filter((id) => !set.picked.includes(id))
  const exact = q && (dishes.some((d) => nameKey(d.name) === q) || set.fresh.some((f) => nameKey(f.name) === q))
  const nothing = set.picked.length === 0 && set.fresh.length === 0

  return html`
    ${!nothing && html`
      <div class="chips">
        ${set.picked.map((id) => {
          const d = byId.get(id)
          return d && html`
            <button type="button" key=${id} class="chip chip-on" onClick=${() => set.toggle(id)}>
              ${dishLabel(d)} ✕
            </button>`
        })}
        ${set.fresh.map(
          (d) => html`
            <button type="button" key=${'new:' + d.name} class="chip chip-on" onClick=${() => set.dropFresh(d.name)}>
              + ${d.name} ✕
            </button>`,
        )}
      </div>`}
    <input class="search" type="search" value=${query} placeholder="Пошук або нова страва"
      onInput=${(e) => { setQuery(e.target.value); setMiss(null) }}
      onKeyDown=${(e) => {
        if (e.key !== 'Enter') return
        e.preventDefault()
        if (shown.length === 1) { set.toggle(shown[0]); setQuery('') }
        else if (q && !exact) resolve()
      }} />
    <div class="chips">
      ${shown.map((id) => {
        const d = byId.get(id)
        return d && html`
          <button type="button" key=${id}
            class="chip ${marked.includes(id) ? 'chip-plan' : ''}"
            onClick=${() => { set.toggle(id); setQuery('') }}>${prefix[id] || ''}${dishLabel(d)}</button>`
      })}
      ${q && !exact &&
      html`<button type="button" class="chip chip-add" disabled=${resolving} onClick=${resolve}>
        ${resolving ? 'Розпізнаю…' : `+ ${query.trim()}`}
      </button>`}
      ${!q && extra}
    </div>
    ${!q && hint && html`<span class="help">${hint}</span>`}
    ${q && shown.length === 0 && exact && html`<span class="help">Уже вибрано</span>`}
    ${miss && html`<span class="field-error">${miss}</span>`}`
}

function useCatalogue(data) {
  return useMemo(() => {
    const m = new Map()
    if (data) for (const d of data.dishes) m.set(d.id, d)
    return m
  }, [data])
}

// The evening picker: what one meal turned out to be, any number of dishes.
// It exists because the group's keyboard answers one dish per tap and then
// closes the meal.
export function MealPicker({ date, meal, onSaved, onCancel }) {
  const [state, setState] = useState({ phase: 'loading' })
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState(null)
  const set = useDishSet([])

  const load = async () => {
    setState({ phase: 'loading' })
    try {
      const data = await api(`/meals/${date}/${meal}`)
      set.reset(data.selected || [])
      setState({ phase: 'ready', data })
    } catch (err) {
      setState({ phase: 'error', error: err })
    }
  }
  useEffect(() => { load() }, [date, meal])
  useEffect(() => () => guardUnsaved(false), [])
  const byId = useCatalogue(state.data)

  if (state.phase === 'loading') return html`<${Loading} />`
  if (state.phase === 'error') return html`<${Failure} error=${state.error} onRetry=${load} />`
  const data = state.data

  const submit = async (e) => {
    e.preventDefault()
    if (saving) return
    setSaving(true)
    setError(null)
    try {
      await api(`/meals/${date}/${meal}`, { method: 'PUT', body: set.body() })
      haptic()
      guardUnsaved(false)
      onSaved()
    } catch (err) {
      setError(err)
      setSaving(false)
    }
  }

  return html`
    <form class="form" onSubmit=${submit}>
      <p class="form-head">
        <span class="form-head-title">${data.title}</span>
        <span class="form-head-when">${data.dayLabel}</span>
      </p>
      <div class="card">
        <span class="label">Що їли</span>
        <${DishChooser} set=${set} byId=${byId} dishes=${data.dishes}
          offered=${data.quick} marked=${data.planned}
          hint="План, вчорашнє й те, що їдять найчастіше. Решта — через пошук." />
      </div>
      ${error && html`<p class="field-error">${error.message}</p>`}
      <${Actions} saving=${saving} onCancel=${() => { guardUnsaved(false); onCancel() }} />
    </form>`
}

// One meal of the morning menu. A meal already eaten is not planned — the
// server would refuse it too — so it says what was eaten instead.
function PlannerMeal({ meal, set, byId, dishes, leftovers, offered, onMore, more }) {
  if (meal.eaten.length > 0) {
    return html`
      <div class="card">
        <span class="label">${meal.title}</span>
        <span>✅ ${meal.eaten.join(' · ')}</span>
        <span class="help">Уже їли — змінити можна в Кухні</span>
      </div>`
  }
  const prefix = {}
  for (const id of leftovers) prefix[id] = '↩ '
  const offer = [...leftovers, ...offered.filter((id) => !leftovers.includes(id))]
  return html`
    <div class="card">
      <span class="label">${meal.title}</span>
      <${DishChooser} set=${set} byId=${byId} dishes=${dishes} offered=${offer} prefix=${prefix}
        extra=${html`
          <button type="button" class="chip chip-add" disabled=${more === 'loading'} onClick=${onMore}>
            ${more === 'empty' ? 'Більше нічого' : '🔀 Ще варіанти'}
          </button>`} />
    </div>`
}

// The morning menu: today's plan, both meals on one screen. It opens with
// what the group message offered — yesterday's pot (↩) and the suggestions —
// and each meal takes any number of dishes. Saving writes the plan and has the
// bot redraw its message.
export function MenuPlanner({ date, onSaved, onCancel }) {
  const [state, setState] = useState({ phase: 'loading' })
  const [offered, setOffered] = useState({ lunch: [], dinner: [] })
  const [more, setMore] = useState({})
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState(null)
  const lunch = useDishSet([])
  const dinner = useDishSet([])
  const sets = { lunch, dinner }

  const load = async () => {
    setState({ phase: 'loading' })
    try {
      const data = await api(`/menu/${date}`)
      const off = {}
      for (const m of data.meals) {
        off[m.meal] = m.offered
        sets[m.meal].reset(m.planned)
      }
      setOffered(off)
      setMore({})
      setState({ phase: 'ready', data })
    } catch (err) {
      setState({ phase: 'error', error: err })
    }
  }
  useEffect(() => { load() }, [date])
  useEffect(() => () => guardUnsaved(false), [])
  const byId = useCatalogue(state.data)

  if (state.phase === 'loading') return html`<${Loading} />`
  if (state.phase === 'error') return html`<${Failure} error=${state.error} onRetry=${load} />`
  const data = state.data

  const askMore = async (meal) => {
    setMore((m) => ({ ...m, [meal]: 'loading' }))
    const alsoOut = [...data.leftovers, ...Object.entries(offered).filter(([k]) => k !== meal).flatMap(([, v]) => v)]
    try {
      const { ids } = await api(`/menu/${date}/${meal}/more`, {
        method: 'POST', body: { offer: offered[meal], alsoOut },
      })
      if (ids.length === 0) {
        setMore((m) => ({ ...m, [meal]: 'empty' }))
        return
      }
      setOffered((o) => ({ ...o, [meal]: [...o[meal], ...ids] }))
      setMore((m) => ({ ...m, [meal]: null }))
    } catch (err) {
      setError(err)
      setMore((m) => ({ ...m, [meal]: null }))
    }
  }

  const submit = async (e) => {
    e.preventDefault()
    if (saving) return
    setSaving(true)
    setError(null)
    const body = {}
    for (const m of data.meals) if (m.eaten.length === 0) body[m.meal] = sets[m.meal].body()
    try {
      await api(`/menu/${date}`, { method: 'PUT', body })
      haptic()
      guardUnsaved(false)
      onSaved()
    } catch (err) {
      setError(err)
      setSaving(false)
    }
  }

  return html`
    <form class="form" onSubmit=${submit}>
      <p class="form-head">
        <span class="form-head-title">Меню на сьогодні</span>
        <span class="form-head-when">${data.dayLabel}</span>
      </p>
      ${data.meals.map(
        (m) => html`
          <${PlannerMeal} key=${m.meal} meal=${m} set=${sets[m.meal]} byId=${byId} dishes=${data.dishes}
            leftovers=${data.leftovers} offered=${offered[m.meal] || []}
            more=${more[m.meal]} onMore=${() => askMore(m.meal)} />`,
      )}
      <span class="help">↩ — доїдаємо вчорашнє. Тапни скільки завгодно страв.</span>
      ${error && html`<p class="field-error">${error.message}</p>`}
      <${Actions} saving=${saving} onCancel=${() => { guardUnsaved(false); onCancel() }} />
    </form>`
}
