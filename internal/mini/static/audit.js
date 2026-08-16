import { html, useState, useEffect, useCallback } from '/mini/assets/vendor/preact-htm.module.js'
import { api, todayISO, dateLong } from '/mini/assets/api.js'
import { Loading, Failure } from '/mini/assets/ui.js'

// The reconciliation for one course: what was paid, what happened, and — when
// the period reaches into the future — which of the coming lessons are already
// paid for. The web has had this behind oauth; this is the same screen with
// the table turned into rows, because a phone cannot hold three columns and a
// running balance.
//
// Every string here comes from the server. The client picks a period and
// renders what comes back.

const RANGES = [
  { id: 'last_payment', label: 'з оплати' },
  { id: 'month', label: 'місяць' },
  { id: 'all', label: 'усе' },
]

function Row({ row }) {
  const future = row.kind === 'future'
  return html`
    <div class="row row-top ${future && !row.covered ? 'row-owed' : ''}">
      <div class="row-when row-date">${row.date}</div>
      <div class="row-main">
        <div class="line">
          ${row.kind === 'visit'
            ? html`<span class="pill pill-${row.status}">${row.label}</span>`
            : html`<span class=${future ? 'muted' : ''}>${row.label}</span>`}
          ${row.amount && html`<span class="row-amount">${row.amount}</span>`}
        </div>
        ${row.comment && html`<span class="note">${row.comment}</span>`}
      </div>
      <div class="row-amount muted">${row.balance}</div>
    </div>`
}

export function Audit({ course, onClose }) {
  const [range, setRange] = useState('last_payment')
  // A custom period is two dates and a button, so it stays folded away until
  // asked for — the three presets answer the everyday question.
  const [custom, setCustom] = useState(null)
  const [state, setState] = useState({ phase: 'loading' })

  const load = useCallback(async (params) => {
    setState({ phase: 'loading' })
    const qs = new URLSearchParams(params).toString()
    try {
      setState({ phase: 'ready', data: await api(`/courses/${course.id}/audit?${qs}`) })
    } catch (err) {
      setState({ phase: 'error', error: err })
    }
  }, [course.id])

  useEffect(() => { load({ range: 'last_payment' }) }, [load])

  const pick = (id) => {
    setRange(id)
    setCustom(null)
    load({ range: id })
  }
  const openCustom = () => {
    setRange('custom')
    setCustom({ from: state.data ? state.data.from || todayISO() : todayISO(), to: todayISO() })
  }
  const showCustom = () => load({ range: 'custom', from: custom.from, to: custom.to })

  const d = state.data
  return html`
    <main class="screen">
      <h1 class="screen-title">Звірка</h1>
      <p class="form-head">
        <span class="form-head-title">${course.name}</span>
        <span class="form-head-when">${course.person}${d ? ` · ${d.period}` : ''}</span>
      </p>

      <div class="chips">
        ${RANGES.map(
          (r) => html`
            <button class="chip ${range === r.id ? 'chip-on' : ''}" key=${r.id}
              onClick=${() => pick(r.id)}>${r.label}</button>`,
        )}
        <button class="chip ${range === 'custom' ? 'chip-on' : ''}" onClick=${openCustom}>свій період</button>
      </div>

      ${custom &&
      html`
        <div class="card card-rows">
          <div class="field-row">
            <label class="field">
              <span class="label">Від</span>
              <input type="date" value=${custom.from}
                onInput=${(e) => setCustom({ ...custom, from: e.target.value })} />
              <span class="help">${dateLong(custom.from)}</span>
            </label>
            <label class="field">
              <span class="label">До</span>
              <input type="date" value=${custom.to}
                onInput=${(e) => setCustom({ ...custom, to: e.target.value })} />
              <span class="help">${dateLong(custom.to)}</span>
            </label>
          </div>
          <!-- A period reaching past today is the only way to see the
               forecast, so the hint says so rather than leaving it to be
               discovered. -->
          <span class="help">Кінець у майбутньому покаже, на скільки вистачить оплаченого</span>
          <div class="actions" style=${{ padding: '10px 0' }}>
            <button class="btn btn-primary" onClick=${showCustom}>Показати</button>
          </div>
        </div>`}

      ${state.phase === 'loading' && html`<${Loading} />`}
      ${state.phase === 'error' && html`<${Failure} error=${state.error} onRetry=${() => pick(range)} />`}

      ${d &&
      html`
        ${d.notice && html`<p class="field-error">${d.notice}</p>`}
        ${d.summary.length > 0 &&
        html`<div class="card"><div class="chips">
          ${d.summary.map((s, i) => html`<span class="chip" key=${i}>${s}</span>`)}
        </div></div>`}
        ${d.forecast.length > 0 &&
        html`<div class="card">
          ${d.forecast.map((f, i) => html`<div class="meta" key=${i}>${f}</div>`)}
        </div>`}
        ${d.rows.length > 0
          ? html`<div class="card card-rows">
              ${d.rows.map((r, i) => html`<${Row} row=${r} key=${i} />`)}
            </div>`
          : html`<p class="sec-empty">За період нічого не було.</p>`}`}

      <div class="actions">
        <button class="btn" onClick=${onClose}>Закрити</button>
      </div>
    </main>`
}
