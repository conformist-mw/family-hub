import { html } from '/mini/assets/vendor/preact-htm.module.js'
import { Bar } from '/mini/assets/ui.js'
import { IconBook, IconCalendar, IconInfo, IconPin, IconRepeat } from '/mini/assets/icons.js'

// "What is going on right now" in one scroll. Almost nothing here is editable
// — it is the screen you open to find out whether anything needs doing, and
// the tabs are where the doing happens. The exception is a payment row: a
// wrong amount is noticed while reading this list, and sending the reader off
// to hunt the same row down under another tab would be the long way round.
//
// The screen opens on the day: lessons from the weekly schedule, one-off
// appointments and chores that came due, in one list in time order. It used to
// show appointments alone, so the two questions it exists to answer — is there
// karate today, and did anybody take the bins out — were the two it could not.
//
// Every string in a row arrives rendered by internal/agenda, which the web hub
// reads too. Nothing here formats a time or names a weekday: that is how the
// phone and the browser would start telling different stories about one day.

function Section({ title, aside, empty, children, onMore, moreLabel }) {
  return html`
    <section>
      <div class="sec-head">
        <h2 class="sec-title">${title}</h2>
        ${onMore && html`<button class="sec-link" onClick=${onMore}>${moreLabel} ›</button>`}
        ${aside && html`<span class="row-amount">${aside}</span>`}
      </div>
      ${empty ? html`<p class="sec-empty">${empty}</p>` : children}
    </section>`
}

const KIND_ICONS = { lesson: IconBook, appointment: IconCalendar, chore: IconRepeat }

function AgendaRow({ item }) {
  const Icon = KIND_ICONS[item.kind] ?? IconCalendar
  return html`
    <div class="row">
      <div class="row-kind"><${Icon} size=${15} /></div>
      <div class="row-when">${item.when}</div>
      <div class="row-main">
        <span>${item.title}${item.person && html`<span class="muted"> · ${item.person}</span>`}</span>
        ${item.place && html`<span class="meta"><${IconPin} /> ${item.place}</span>`}
      </div>
      ${item.status && html`<div class="row-amount muted">${item.status}</div>`}
    </div>`
}

function AgendaList({ items }) {
  return html`<div class="card card-rows">${items.map((it, i) => html`<${AgendaRow} key=${it.kind + it.id + i} item=${it} />`)}</div>`
}

function CourseCard({ course }) {
  return html`
    <div class="card">
      <div class="course-head">
        <div>
          <div class="course-name">${course.name}</div>
          <div class="course-sub">${course.person}</div>
        </div>
        <div class="course-state state-${course.state}">${course.balance}</div>
      </div>
      <${Bar} state=${course.state} />
      ${course.schedule && html`<div class="meta">${course.schedule}</div>`}
      ${course.absence && html`<div class="meta"><${IconInfo} /> ${course.absence}</div>`}
    </div>`
}

export function Home({ data, onOpenVisits, onOpenCourses, onOpenPayment }) {
  const { date = '', today = [], upcoming = [], courses = [], payments = [] } = data

  const attention = courses.filter((c) => c.state !== 'ok')
  const calm = courses.filter((c) => c.state === 'ok')

  return html`
    <main class="screen">
      ${date && html`<h1 class="screen-title">${date}</h1>`}

      <${Section} title="Сьогодні" empty=${today.length === 0 ? 'На сьогодні нічого не заплановано' : null}>
        <${AgendaList} items=${today} />
      <//>

      ${attention.length > 0 &&
      html`
        <${Section} title="Потребує уваги">
          <div class="cards" style=${{ display: 'flex', flexDirection: 'column', gap: '10px' }}>
            ${attention.map((c) => html`<${CourseCard} key=${c.id} course=${c} />`)}
          </div>
        <//>`}

      ${upcoming.length > 0 &&
      html`
        <${Section} title="Найближче" moreLabel="усі записи" onMore=${onOpenVisits}>
          <${AgendaList} items=${upcoming} />
        <//>`}

      <${Section} title="Курси" moreLabel="розклад" onMore=${onOpenCourses}
        empty=${courses.length === 0 ? 'Активних курсів немає' : null}>
        ${calm.length > 0 &&
        html`
          <div class="card card-rows">
            ${calm.map(
              (c) => html`
                <div class="row row-top" key=${c.id}>
                  <div class="row-main">
                    <span>${c.name}<span class="muted"> · ${c.person}</span></span>
                    ${c.schedule && html`<span class="meta">${c.schedule}</span>`}
                    ${c.absence && html`<span class="meta"><${IconInfo} /> ${c.absence}</span>`}
                  </div>
                  <div class="course-state state-ok">${c.balance}</div>
                </div>`,
            )}
          </div>`}
      <//>

      <${Section} title="Останні оплати"
        empty=${payments.length === 0 ? 'Оплат ще не було' : null}>
        <div class="card card-rows">
          ${payments.map(
            (p) => html`
              <button class="row" key=${p.id} onClick=${() => onOpenPayment(p)}>
                <div class="row-when row-date">${p.date}</div>
                <div class="row-main">
                  <span>${p.course}<span class="muted"> · ${p.person}</span></span>
                  ${p.detail &&
                  html`<span class="meta">
                    ${p.kind === 'extra' && html`<span class="pill-extra">доп.</span>`}
                    ${p.detail}
                  </span>`}
                </div>
                <div class="row-amount">${p.amount}</div>
              </button>`,
          )}
        </div>
      <//>
    </main>`
}
