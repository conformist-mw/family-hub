import { html, render, useState, useEffect, useCallback, useRef } from '/mini/assets/vendor/preact-htm.module.js'
import { api, tg, boot } from '/mini/assets/api.js'
import { watchTheme } from '/mini/assets/theme.js'
import { Loading, Failure } from '/mini/assets/ui.js'
import { IconHome, IconCalendar, IconBook, IconRepeat, IconBack } from '/mini/assets/icons.js'
import { AppointmentList, AppointmentCard, AppointmentForm } from '/mini/assets/appointments.js'
import { CourseList, SlotForm } from '/mini/assets/courses.js'
import { PaymentForm } from '/mini/assets/payments.js'
import { Audit } from '/mini/assets/audit.js'
import { Home } from '/mini/assets/home.js'
import { ReminderList, ReminderForm } from '/mini/assets/reminders.js'
import { ChoreHistory, OneChoreHistory } from '/mini/assets/history.js'

// Navigation has two tiers, the same two the web grew: a space groups the tabs
// that belong together, and a tab is a screen inside it. The hub is a space
// like any other — Головна, Записи and Справи live in it — so entering one is
// always the same operation rather than a second kind of navigation to keep in
// step.
//
// A form is still a nested screen inside whichever tab opened it, so Telegram's
// own back button leaves the form rather than the app or the space.
const HUB = 'hub'
const LESSONS = 'lessons'

// Only spaces that have screens are here. Комуналка joins this table together
// with its own screens and not before: an empty "coming soon" destination is a
// dead end, and a flag guarding one is a second thing to remember to remove.
const SPACES = {
  [HUB]: [
    { id: 'home', label: 'Головна', Icon: IconHome },
    { id: 'appointments', label: 'Записи', Icon: IconCalendar },
    { id: 'reminders', label: 'Справи', Icon: IconRepeat },
  ],
  [LESSONS]: [{ id: 'courses', label: 'Заняття', Icon: IconBook }],
}

function TabBar({ space, active, onSelect, onHub }) {
  return html`
    <nav class="tabbar">
      ${space !== HUB &&
      html`
        <button class="tab tab-back" onClick=${onHub}>
          <${IconBack} />
          <span class="tab-label">Хаб</span>
        </button>`}
      ${SPACES[space].map(
        (t) => html`
          <button
            key=${t.id}
            class="tab ${active === t.id ? 'tab-active' : ''}"
            onClick=${() => onSelect(t.id)}>
            <${t.Icon} />
            <span class="tab-label">${t.label}</span>
          </button>`,
      )}
    </nav>`
}

function App() {
  const [space, setSpace] = useState(HUB)
  const [tab, setTab] = useState('home')

  // Screens stack on top of the tab: list -> card -> edit. Telegram's back
  // button pops one, so leaving the editor lands on the card it was opened
  // from rather than all the way out.
  const [stack, setStack] = useState([])
  const screen = stack[stack.length - 1] || null
  const push = (s) => setStack((st) => [...st, s])
  const pop = () => setStack((st) => st.slice(0, -1))
  const closeAll = () => setStack([])

  // Changing space closes the form stack. A form belongs to the tab that
  // opened it, and carrying it across would leave Telegram's back button
  // pointing at a screen that is no longer reachable.
  const enterSpace = (id) => {
    closeAll()
    setSpace(id)
    setTab(SPACES[id][0].id)
  }

  // The visibility handler is registered once, so it reads the open screen
  // through a ref rather than through a closure captured on first render.
  const screenRef = useRef(null)
  screenRef.current = screen

  const [home, setHome] = useState({ phase: 'loading' })
  const [appointments, setAppointments] = useState({ phase: 'loading' })
  const [courses, setCourses] = useState({ phase: 'loading' })
  const [persons, setPersons] = useState([])
  const [reminders, setReminders] = useState({ phase: 'loading' })

  const loadHome = useCallback(async () => {
    try {
      setHome({ phase: 'ready', data: await api('/home') })
    } catch (err) {
      setHome({ phase: 'error', error: err })
    }
  }, [])

  const loadAppointments = useCallback(async () => {
    try {
      const d = await api('/appointments')
      setAppointments({ phase: 'ready', days: d.days || [], truncated: Boolean(d.truncated) })
    } catch (err) {
      setAppointments({ phase: 'error', error: err })
    }
  }, [])

  const loadCourses = useCallback(async () => {
    try {
      const d = await api('/courses')
      setCourses({ phase: 'ready', courses: d.courses || [], weekdays: d.weekdays || [] })
    } catch (err) {
      setCourses({ phase: 'error', error: err })
    }
  }, [])

  const loadReminders = useCallback(async () => {
    try {
      const d = await api('/reminders')
      setReminders({
        phase: 'ready',
        data: { reminders: d.reminders || [], occurrences: d.occurrences || [] },
      })
    } catch (err) {
      setReminders({ phase: 'error', error: err })
    }
  }, [])

  const loadPersons = useCallback(async () => {
    try {
      setPersons((await api('/persons')).persons || [])
    } catch {
      // The list only fills a form's dropdown; a failure here must not take
      // the space down with it.
    }
  }, [])

  // What each space needs, named once, so opening a space and refreshing it
  // cannot drift apart.
  const loadSpace = useCallback(
    (id) => {
      if (id === LESSONS) {
        loadCourses()
        return
      }
      loadHome()
      loadAppointments()
      loadReminders()
      loadPersons()
    },
    [loadHome, loadAppointments, loadReminders, loadPersons, loadCourses],
  )

  // Five requests used to go out on boot no matter which tab was open. Now the
  // open space loads, and any other loads the first time it is entered — kept
  // in a ref, because a re-render must not count as a first entry.
  const loadedRef = useRef(new Set())
  useEffect(() => {
    if (loadedRef.current.has(space)) return
    loadedRef.current.add(space)
    loadSpace(space)
  }, [space, loadSpace])

  // Read through a ref for the same reason the open screen is: the handler is
  // registered once and must not answer to the space captured on first render.
  const spaceRef = useRef(space)
  spaceRef.current = space

  useEffect(() => {
    boot()
    watchTheme()

    const onVisible = () => {
      // Refresh the lists only. Reloading under a half-filled form would throw
      // away what the person is typing.
      if (document.visibilityState !== 'visible' || screenRef.current) return
      loadSpace(spaceRef.current)
    }
    document.addEventListener('visibilitychange', onVisible)
    return () => document.removeEventListener('visibilitychange', onVisible)
  }, [loadSpace])

  useEffect(() => {
    if (!tg || !tg.BackButton) return
    const back = () => pop()
    if (screen) {
      tg.BackButton.show()
      tg.BackButton.onClick(back)
    } else {
      tg.BackButton.hide()
    }
    return () => tg.BackButton.offClick(back)
  }, [screen])

  const removeAppointment = async (item) => {
    if (!confirm('Видалити цей запис?')) return
    try {
      await api(`/appointments/${item.id}`, { method: 'DELETE' })
      closeAll()
      loadAppointments()
      loadHome()
    } catch (err) {
      setAppointments({ phase: 'error', error: err })
      closeAll()
    }
  }

  if (screen && screen.name === 'appointmentCard') {
    return html`
      <${AppointmentCard}
        item=${screen.item}
        onEdit=${() => push({ name: 'appointmentForm', item: screen.item })}
        onDelete=${() => removeAppointment(screen.item)}
        onClose=${pop} />`
  }

  if (screen && screen.name === 'appointmentForm') {
    return html`
      <${AppointmentForm}
        initial=${screen.item}
        persons=${persons}
        onSaved=${() => { closeAll(); loadAppointments(); loadHome() }}
        onCancel=${pop} />`
  }

  if (screen && screen.name === 'slotForm') {
    return html`
      <${SlotForm}
        course=${screen.course}
        slot=${screen.slot}
        weekdays=${courses.weekdays || []}
        onSaved=${() => { closeAll(); loadCourses(); loadHome() }}
        onCancel=${pop} />`
  }

  if (screen && screen.name === 'paymentForm') {
    return html`
      <${PaymentForm}
        course=${screen.course}
        payment=${screen.payment}
        onSaved=${() => { closeAll(); loadCourses(); loadHome() }}
        onCancel=${pop} />`
  }

  if (screen && screen.name === 'reminderForm') {
    return html`
      <${ReminderForm}
        item=${screen.item}
        onSaved=${() => { closeAll(); loadReminders() }}
        onCancel=${pop} />`
  }

  if (screen && screen.name === 'audit') {
    return html`<${Audit} course=${screen.course} onClose=${pop} />`
  }

  if (screen && screen.name === 'choreHistory') {
    return html`
      <${ChoreHistory}
        onOpen=${(chore) => push({ name: 'oneChoreHistory', chore })} />`
  }

  if (screen && screen.name === 'oneChoreHistory') {
    return html`<${OneChoreHistory} chore=${screen.chore} onClose=${pop} />`
  }

  let body
  if (tab === 'home') {
    if (home.phase === 'loading') body = html`<${Loading} />`
    else if (home.phase === 'error') body = html`<${Failure} error=${home.error} onRetry=${loadHome} />`
    else
      body = html`
        <${Home}
          data=${home.data}
          onOpenVisits=${() => setTab('appointments')}
          onOpenCourses=${() => enterSpace(LESSONS)}
          onOpenPayment=${(payment) => push({ name: 'paymentForm', payment })} />`
  } else if (tab === 'appointments') {
    if (appointments.phase === 'loading') body = html`<${Loading} />`
    else if (appointments.phase === 'error')
      body = html`<${Failure} error=${appointments.error} onRetry=${loadAppointments} />`
    else
      body = html`
        <${AppointmentList}
          days=${appointments.days}
          truncated=${appointments.truncated}
          onOpen=${(item) => push({ name: 'appointmentCard', item })}
          onAdd=${() => push({ name: 'appointmentForm', item: null })} />`
  } else if (tab === 'reminders') {
    if (reminders.phase === 'loading') body = html`<${Loading} />`
    else if (reminders.phase === 'error')
      body = html`<${Failure} error=${reminders.error} onRetry=${loadReminders} />`
    else
      body = html`
        <${ReminderList}
          data=${reminders.data}
          onOpen=${(item) => push({ name: 'reminderForm', item })}
          onAdd=${() => push({ name: 'reminderForm', item: null })}
          onHistory=${() => push({ name: 'choreHistory' })}
          onMarked=${loadReminders} />`
  } else {
    if (courses.phase === 'loading') body = html`<${Loading} />`
    else if (courses.phase === 'error')
      body = html`<${Failure} error=${courses.error} onRetry=${loadCourses} />`
    else
      body = html`
        <${CourseList}
          courses=${courses.courses}
          weekdays=${courses.weekdays}
          onEditSlot=${(course, slot) => push({ name: 'slotForm', course, slot })}
          onAddSlot=${(course) => push({ name: 'slotForm', course, slot: null })}
          onAddPayment=${(course) => push({ name: 'paymentForm', course })}
          onOpenAudit=${(course) => push({ name: 'audit', course })} />`
  }

  return html`
    <div class="app">
      ${body}
      <${TabBar}
        space=${space}
        active=${tab}
        onSelect=${(id) => { closeAll(); setTab(id) }}
        onHub=${() => enterSpace(HUB)} />
    </div>`
}

render(html`<${App} />`, document.getElementById('root'))
