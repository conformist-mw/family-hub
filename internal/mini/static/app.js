import { html, render, useState, useEffect, useCallback, useRef } from '/mini/assets/vendor/preact-htm.module.js'
import { api, tg, boot } from '/mini/assets/api.js'
import { watchTheme } from '/mini/assets/theme.js'
import { Loading, Failure } from '/mini/assets/ui.js'
import { IconHome, IconCalendar, IconBook } from '/mini/assets/icons.js'
import { AppointmentList, AppointmentCard, AppointmentForm } from '/mini/assets/appointments.js'
import { CourseList, SlotForm } from '/mini/assets/courses.js'
import { PaymentForm } from '/mini/assets/payments.js'
import { Home } from '/mini/assets/home.js'

// Tabs are the top-level navigation; a form is a nested screen inside whichever
// tab opened it, so Telegram's own back button leaves the form rather than the
// app. Only tabs that exist are shown — an empty "coming soon" destination is
// just a dead end.
const TABS = [
  { id: 'home', label: 'Головна', Icon: IconHome },
  { id: 'appointments', label: 'Записи', Icon: IconCalendar },
  { id: 'courses', label: 'Заняття', Icon: IconBook },
]

function TabBar({ active, onSelect }) {
  return html`
    <nav class="tabbar">
      ${TABS.map(
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
  const [tab, setTab] = useState('home')

  // Screens stack on top of the tab: list -> card -> edit. Telegram's back
  // button pops one, so leaving the editor lands on the card it was opened
  // from rather than all the way out.
  const [stack, setStack] = useState([])
  const screen = stack[stack.length - 1] || null
  const push = (s) => setStack((st) => [...st, s])
  const pop = () => setStack((st) => st.slice(0, -1))
  const closeAll = () => setStack([])

  // The visibility handler is registered once, so it reads the open screen
  // through a ref rather than through a closure captured on first render.
  const screenRef = useRef(null)
  screenRef.current = screen

  const [home, setHome] = useState({ phase: 'loading' })
  const [appointments, setAppointments] = useState({ phase: 'loading' })
  const [courses, setCourses] = useState({ phase: 'loading' })
  const [persons, setPersons] = useState([])

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

  useEffect(() => {
    boot()
    watchTheme()
    loadHome()
    loadAppointments()
    loadCourses()
    api('/persons').then((d) => setPersons(d.persons || [])).catch(() => {})

    const onVisible = () => {
      // Refresh the lists only. Reloading under a half-filled form would throw
      // away what the person is typing.
      if (document.visibilityState !== 'visible' || screenRef.current) return
      loadHome()
      loadAppointments()
      loadCourses()
    }
    document.addEventListener('visibilitychange', onVisible)
    return () => document.removeEventListener('visibilitychange', onVisible)
  }, [loadHome, loadAppointments, loadCourses])

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

  let body
  if (tab === 'home') {
    if (home.phase === 'loading') body = html`<${Loading} />`
    else if (home.phase === 'error') body = html`<${Failure} error=${home.error} onRetry=${loadHome} />`
    else
      body = html`
        <${Home}
          data=${home.data}
          onOpenVisits=${() => setTab('appointments')}
          onOpenCourses=${() => setTab('courses')}
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
          onAddPayment=${(course) => push({ name: 'paymentForm', course })} />`
  }

  return html`
    <div class="app">
      ${body}
      <${TabBar} active=${tab} onSelect=${(id) => { closeAll(); setTab(id) }} />
    </div>`
}

render(html`<${App} />`, document.getElementById('root'))
