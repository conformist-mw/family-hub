import { html } from '/mini/assets/vendor/preact-htm.module.js'

// "What is going on right now" in one scroll: what is coming up, which courses
// are running out of paid lessons, what was paid lately. Nothing here is
// editable — it is the screen you open to find out whether anything needs
// doing, and the tabs are where the doing happens.

const STATE_DOT = { ok: 'dot-ok', low: 'dot-low', empty: 'dot-empty' }

function Section({ title, empty, children, onMore, moreLabel }) {
  return html`
    <section class="hs">
      <h2 class="hs-title">
        ${title}
        ${onMore && html`<button class="hs-more" onClick=${onMore}>${moreLabel} ›</button>`}
      </h2>
      ${empty ? html`<p class="hs-empty">${empty}</p>` : children}
    </section>`
}

export function Home({ data, onOpenVisits, onOpenCourses }) {
  const { upcoming = [], courses = [], payments = [] } = data

  return html`
    <main class="home">
      <${Section} title="Найближче" moreLabel="усі записи" onMore=${onOpenVisits}
        empty=${upcoming.length === 0 ? 'Попереду візитів немає' : null}>
        <ul class="hs-list">
          ${upcoming.map(
            (v) => html`
              <li class="hs-row" key=${v.id}>
                <div class="hs-when">${v.when}</div>
                <div class="hs-main">
                  <span class="title">${v.title}</span>
                  ${v.person && html`<span class="person"> · ${v.person}</span>`}
                </div>
              </li>`,
          )}
        </ul>
      <//>

      <${Section} title="Курси" moreLabel="розклад" onMore=${onOpenCourses}
        empty=${courses.length === 0 ? 'Активних курсів немає' : null}>
        <ul class="hs-list">
          ${courses.map(
            (c) => html`
              <li class="hs-row" key=${c.id}>
                <span class="dot ${STATE_DOT[c.state] || ''}"></span>
                <div class="hs-main">
                  <div class="line">
                    <span class="title">${c.name}</span>
                    <span class="person">${c.person}</span>
                  </div>
                  <div class="hs-balance bal-${c.state}">${c.balance}</div>
                  ${c.schedule && html`<div class="muted small">${c.schedule}</div>`}
                  ${c.absence && html`<div class="hs-absence">${c.absence}</div>`}
                </div>
              </li>`,
          )}
        </ul>
      <//>

      <${Section} title="Останні оплати"
        empty=${payments.length === 0 ? 'Оплат ще не було' : null}>
        <ul class="hs-list">
          ${payments.map(
            (p) => html`
              <li class="hs-row" key=${p.id}>
                <div class="hs-when">${p.date}</div>
                <div class="hs-main">
                  <div class="line">
                    <span class="title">${p.course}</span>
                    <span class="person">${p.person}</span>
                  </div>
                  ${p.detail && html`<div class="muted small">${p.detail}</div>`}
                </div>
                <div class="hs-amount">${p.amount}</div>
              </li>`,
          )}
        </ul>
      <//>
    </main>`
}
