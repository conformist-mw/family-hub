import { html } from '/mini/assets/vendor/preact-htm.module.js'
import { Bar } from '/mini/assets/ui.js'
import { IconClock, IconInfo } from '/mini/assets/icons.js'

// "What is going on right now" in one scroll. Almost nothing here is editable
// — it is the screen you open to find out whether anything needs doing, and
// the tabs are where the doing happens. The exception is a payment row: a
// wrong amount is noticed while reading this list, and sending the reader off
// to hunt the same row down under another tab would be the long way round.
//
// The order changed: the next visit is a card rather than the first line of a
// list, and the courses that are running out are lifted above the ones that
// are fine. Reading the old screen meant scanning three lists to find the one
// coloured word in them.

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

function Next({ visit }) {
  return html`
    <div class="hero">
      <div class="hero-kicker"><${IconClock} /> ${visit.when}</div>
      <div class="hero-body">
        <div class="hero-main">
          <div class="hero-title">${visit.title}</div>
          ${visit.person && html`<div class="hero-sub">${visit.person}</div>`}
        </div>
      </div>
      ${visit.location && html`<div class="hero-foot">${visit.location}</div>`}
    </div>`
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
  const { today = '', upcoming = [], courses = [], payments = [] } = data

  // The first upcoming visit is the card; the rest stay a list. Splitting it
  // this way means the screen answers "what is next" before it answers
  // "what else is there".
  const [next, ...later] = upcoming
  const attention = courses.filter((c) => c.state !== 'ok')
  const calm = courses.filter((c) => c.state === 'ok')

  return html`
    <main class="screen">
      ${today && html`<h1 class="screen-title">${today}</h1>`}

      ${next ? html`<${Next} visit=${next} />` : html`<p class="sec-empty">Попереду візитів немає</p>`}

      ${attention.length > 0 &&
      html`
        <${Section} title="Потребує уваги">
          <div class="cards" style=${{ display: 'flex', flexDirection: 'column', gap: '10px' }}>
            ${attention.map((c) => html`<${CourseCard} key=${c.id} course=${c} />`)}
          </div>
        <//>`}

      ${later.length > 0 &&
      html`
        <${Section} title="Найближче" moreLabel="усі записи" onMore=${onOpenVisits}>
          <div class="card card-rows">
            ${later.map(
              (v) => html`
                <div class="row" key=${v.id}>
                  <div class="row-when">${v.when}</div>
                  <div class="row-main"><span>${v.title}${v.person && html`<span class="muted"> · ${v.person}</span>`}</span></div>
                </div>`,
            )}
          </div>
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
                  ${p.detail && html`<span class="meta">${p.detail}</span>`}
                </div>
                <div class="row-amount">${p.amount}</div>
              </button>`,
          )}
        </div>
      <//>
    </main>`
}
