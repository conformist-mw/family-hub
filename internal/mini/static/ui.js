import { html } from '/mini/assets/vendor/preact-htm.module.js'
import { IconCalendar, IconAlert } from '/mini/assets/icons.js'

// Pieces every tab uses.

export function Field({ label, error, children, help }) {
  return html`
    <label class="field">
      <span class="label">${label}</span>
      ${children}
      ${error && html`<span class="field-error">${error}</span>`}
      ${help && html`<span class="help">${help}</span>`}
    </label>`
}

// A gauge with three positions. The server sends ok | low | empty, not a
// number, so the bar says which of the three it is and nothing more precise.
const FILL = { ok: '100%', low: '30%', empty: '0%' }

export function Bar({ state }) {
  return html`
    <div class="bar bar-${state}">
      <div class="bar-fill" style=${{ width: FILL[state] || '0%' }}></div>
    </div>`
}

export function Loading() {
  return html`<div class="state"><p class="state-text">Завантаження…</p></div>`
}

export function Empty({ title, text, action, onAction }) {
  return html`
    <div class="state">
      <span class="state-icon"><${IconCalendar} size=${34} /></span>
      <p class="state-title">${title}</p>
      ${text && html`<p class="state-text">${text}</p>`}
      ${action && html`<button class="btn btn-primary" onClick=${onAction}>${action}</button>`}
    </div>`
}

// 403 is terminal — nothing the person can do about it — so it gets no retry.
export function Failure({ error, onRetry }) {
  return html`
    <div class="state">
      <span class="state-icon" style=${{ color: 'var(--danger)' }}><${IconAlert} /></span>
      <p class="state-title error">${error.message}</p>
      ${error.code !== 'forbidden' &&
      html`<button class="btn btn-primary" onClick=${onRetry}>Спробувати ще</button>`}
    </div>`
}

export function Actions({ saving, onCancel, onDelete, deleteLabel = 'Видалити' }) {
  return html`
    <div class="actions">
      <button type="submit" class="btn btn-primary" disabled=${saving}>${saving ? 'Зберігаю…' : 'Зберегти'}</button>
      <button type="button" class="btn" onClick=${onCancel}>Скасувати</button>
    </div>
    ${onDelete &&
    html`<button type="button" class="btn-danger" onClick=${onDelete} disabled=${saving}>${deleteLabel}</button>`}`
}
