import { html, useState, useEffect, useCallback } from '/mini/assets/vendor/preact-htm.module.js'
import { api, haptic } from '/mini/assets/api.js'
import { Loading, Failure, Empty } from '/mini/assets/ui.js'

// The record of what actually got done.
//
// Occurrences are stored rather than recomputed precisely so this is
// answerable: a pending row in the past is evidence that the moment came and
// nobody closed it. The list tab answers what is open right now; this answers
// how a chore is going over a period, which is the question you have standing
// in a kitchen wondering whether to hand it to somebody else.
//
// Every string comes from the server, the same rule the audit screen follows.
// The client picks a period and renders what comes back — no counting, no
// date arithmetic, and in particular no second opinion about what "часто
// губиться" means.

const RANGES = [
  { id: '30d', label: '30 днів' },
  { id: 'month', label: 'місяць' },
  { id: 'prev', label: 'минулий' },
]

// Tally reads as a line rather than a table: a phone cannot hold four columns,
// and the counts are short enough to sit together.
function Tally({ t }) {
  return html`
    <span class="muted">
      ✓ ${t.done}
      ${t.skipped > 0 ? html` · ✗ ${t.skipped}` : ''}
      ${t.missed > 0 ? html` · ○ ${t.missed}` : ''}
    </span>`
}

// Floor is stated rather than left as a silent gap: before it a missing row
// means nothing was recorded, not that nothing came due.
function Floor({ d }) {
  if (!d.truncated) return ''
  return html`
    <p class="note" style="margin-top:14px">
      Записи ведуться лише з ${d.floor} — за раніший час рядків просто немає,
      і порожнеча там нічого не означає.
    </p>`
}

function Ranges({ range, onPick }) {
  return html`
    <div class="chips">
      ${RANGES.map(
        (r) => html`
          <button class="chip ${range === r.id ? 'chip-on' : ''}" key=${r.id}
            onClick=${() => onPick(r.id)}>${r.label}</button>`,
      )}
    </div>`
}

// ChoreHistory is the overview: every chore with a record in the period,
// worst-kept first. That order is the point — a chore habitually forgotten is
// invisible in a list sorted by name.
export function ChoreHistory({ onOpen }) {
  const [range, setRange] = useState('30d')
  const [state, setState] = useState({ phase: 'loading' })

  const load = useCallback(async (id) => {
    setState({ phase: 'loading' })
    try {
      setState({ phase: 'ready', data: await api(`/reminders/history?range=${id}`) })
    } catch (err) {
      setState({ phase: 'error', error: err })
    }
  }, [])

  useEffect(() => { load('30d') }, [load])

  const pick = (id) => {
    haptic('light')
    setRange(id)
    load(id)
  }

  if (state.phase === 'loading') return html`<${Loading} />`
  if (state.phase === 'error')
    return html`<${Failure} error=${state.error} onRetry=${() => load(range)} />`

  const d = state.data
  return html`
    <main class="screen">
      <h1 class="screen-title">Що зроблено</h1>
      <p class="form-head"><span class="form-head-when">${d.period}</span></p>

      <${Ranges} range=${range} onPick=${pick} />

      ${d.chores && d.chores.length
        ? html`
          <div class="card card-rows">
            ${d.chores.map(
              (c) => html`
                <button class="row row-top row-tap" key=${c.reminderId}
                  onClick=${() => onOpen(c)}>
                  <div class="row-main">
                    <div class="line">
                      <span>${c.title}</span>
                      ${c.oftenMissed &&
                        html`<span class="pill pill-cancelled">часто губиться</span>`}
                    </div>
                    <span class="note">${c.rule}${c.person ? ` · ${c.person}` : ''}</span>
                  </div>
                  <${Tally} t=${c.tally} />
                </button>`,
            )}
          </div>
          <p class="note" style="margin-top:10px">
            Разом: закрито ${d.totals.done} · пропущено ${d.totals.skipped} ·
            не закрито ${d.totals.missed}
          </p>`
        : html`<${Empty} title="Порожньо"
            text="За цей період жодна справа не наставала." />`}

      <${Floor} d=${d} />
    </main>`
}

// OneChoreHistory is the drill-down: the per-moment record, which is the whole
// reason occurrences are written down.
export function OneChoreHistory({ chore, onClose }) {
  const [range, setRange] = useState('30d')
  const [state, setState] = useState({ phase: 'loading' })

  const load = useCallback(async (id) => {
    setState({ phase: 'loading' })
    try {
      setState({
        phase: 'ready',
        data: await api(`/reminders/${chore.reminderId}/history?range=${id}`),
      })
    } catch (err) {
      setState({ phase: 'error', error: err })
    }
  }, [chore.reminderId])

  useEffect(() => { load('30d') }, [load])

  const pick = (id) => {
    haptic('light')
    setRange(id)
    load(id)
  }

  if (state.phase === 'loading') return html`<${Loading} />`
  if (state.phase === 'error')
    return html`<${Failure} error=${state.error} onRetry=${() => load(range)} />`

  const d = state.data
  return html`
    <main class="screen">
      <h1 class="screen-title">${d.title}</h1>
      <p class="form-head">
        <span class="form-head-when">${d.rule} · ${d.period}</span>
      </p>

      <${Ranges} range=${range} onPick=${pick} />

      ${d.occurrences && d.occurrences.length
        ? html`
          <div class="card card-rows">
            ${d.occurrences.map(
              (o, i) => html`
                <div class="row row-top" key=${i}>
                  <div class="row-when row-date">${o.when}</div>
                  <div class="row-main">
                    <div class="line">
                      <span class="pill pill-${o.status}">${o.mark} ${o.label}</span>
                      ${o.doneBy && html`<span class="note">${o.doneBy}</span>`}
                    </div>
                  </div>
                </div>`,
            )}
          </div>
          <p class="note" style="margin-top:10px">
            Закрито ${d.tally.done} · пропущено ${d.tally.skipped} ·
            не закрито ${d.tally.missed}
            ${d.tally.waiting > 0 ? ` · ще не настало ${d.tally.waiting}` : ''}
          </p>`
        : html`<${Empty} title="Порожньо"
            text="За цей період нічого не наставало." />`}

      ${d.ruleChanges && d.ruleChanges.length
        ? html`
          <p class="note" style="margin-top:10px">
            ${d.ruleChanges.join(' ')}
            Рядків за період може бути менше, ніж дає нинішнє правило.
          </p>`
        : ''}

      <${Floor} d=${d} />

      <div class="actions">
        <button class="btn" onClick=${onClose}>Назад</button>
      </div>
    </main>`
}
