import { html } from '/mini/assets/vendor/preact-htm.module.js'

// Pieces both tabs use.

export function Field({ label, error, children }) {
  return html`
    <label class="field">
      <span class="label">${label}</span>
      ${children}
      ${error && html`<span class="field-error">${error}</span>`}
    </label>`
}

export function Center({ children }) {
  return html`<div class="center">${children}</div>`
}

export function Loading() {
  return html`<div class="center muted">Завантаження…</div>`
}

// 403 is terminal — nothing the person can do about it — so it gets no retry.
export function Failure({ error, onRetry }) {
  return html`
    <div class="center">
      <p class="error">${error.message}</p>
      ${error.code !== 'forbidden' && html`<button class="primary" onClick=${onRetry}>Спробувати ще</button>`}
    </div>`
}

export function Actions({ saving, onCancel, onDelete }) {
  return html`
    <div class="actions">
      <button type="submit" class="primary" disabled=${saving}>${saving ? 'Зберігаю…' : 'Зберегти'}</button>
      <button type="button" onClick=${onCancel}>Скасувати</button>
    </div>
    ${onDelete && html`<button type="button" class="danger" onClick=${onDelete} disabled=${saving}>Видалити</button>`}`
}
