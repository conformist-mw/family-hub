import { html, render, useState, useEffect, useCallback, useRef } from '/mini/assets/vendor/preact-htm.module.js'
import { api, tg, boot } from '/mini/assets/api.js'
import { Loading, Failure } from '/mini/assets/ui.js'
import { AppointmentList, AppointmentForm } from '/mini/assets/appointments.js'
import { CourseList, SlotForm } from '/mini/assets/courses.js'

// Tabs are the top-level navigation; a form is a nested screen inside whichever
// tab opened it, so Telegram's own back button leaves the form rather than the
// app. Only tabs that exist are shown — an empty "coming soon" destination is
// just a dead end.
const TABS = [
  { id: 'appointments', label: 'Записи', icon: '🗓' },
  { id: 'courses', label: 'Заняття', icon: '📚' },
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
            <span class="tab-icon">${t.icon}</span>
            <span class="tab-label">${t.label}</span>
          </button>`,
      )}
    </nav>`
}

function App() {
  const [tab, setTab] = useState('appointments')
  const [screen, setScreen] = useState(null)

  // The visibility handler is registered once, so it reads the open screen
  // through a ref rather than through a closure captured on first render.
  const screenRef = useRef(null)
  screenRef.current = screen

  const [appointments, setAppointments] = useState({ phase: 'loading' })
  const [courses, setCourses] = useState({ phase: 'loading' })
  const [persons, setPersons] = useState([])

  const loadAppointments = useCallback(async () => {
    try {
      const d = await api('/appointments')
      setAppointments({ phase: 'ready', days: d.days || [] })
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
    loadAppointments()
    loadCourses()
    api('/persons').then((d) => setPersons(d.persons || [])).catch(() => {})

    const onVisible = () => {
      // Refresh the lists only. Reloading under a half-filled form would throw
      // away what the person is typing.
      if (document.visibilityState !== 'visible' || screenRef.current) return
      loadAppointments()
      loadCourses()
    }
    document.addEventListener('visibilitychange', onVisible)
    return () => document.removeEventListener('visibilitychange', onVisible)
  }, [loadAppointments, loadCourses])

  useEffect(() => {
    if (!tg || !tg.BackButton) return
    const back = () => setScreen(null)
    if (screen) {
      tg.BackButton.show()
      tg.BackButton.onClick(back)
    } else {
      tg.BackButton.hide()
    }
    return () => tg.BackButton.offClick(back)
  }, [screen])

  const closeScreen = () => setScreen(null)

  if (screen && screen.name === 'appointmentForm') {
    return html`
      <${AppointmentForm}
        initial=${screen.item}
        persons=${persons}
        onSaved=${() => { closeScreen(); loadAppointments() }}
        onCancel=${closeScreen} />`
  }

  if (screen && screen.name === 'slotForm') {
    return html`
      <${SlotForm}
        course=${screen.course}
        slot=${screen.slot}
        weekdays=${courses.weekdays || []}
        onSaved=${() => { closeScreen(); loadCourses() }}
        onCancel=${closeScreen} />`
  }

  let body
  if (tab === 'appointments') {
    if (appointments.phase === 'loading') body = html`<${Loading} />`
    else if (appointments.phase === 'error')
      body = html`<${Failure} error=${appointments.error} onRetry=${loadAppointments} />`
    else
      body = html`
        <${AppointmentList}
          days=${appointments.days}
          onOpen=${(item) => setScreen({ name: 'appointmentForm', item })}
          onAdd=${() => setScreen({ name: 'appointmentForm', item: null })} />`
  } else {
    if (courses.phase === 'loading') body = html`<${Loading} />`
    else if (courses.phase === 'error')
      body = html`<${Failure} error=${courses.error} onRetry=${loadCourses} />`
    else
      body = html`
        <${CourseList}
          courses=${courses.courses}
          weekdays=${courses.weekdays}
          onEditSlot=${(course, slot) => setScreen({ name: 'slotForm', course, slot })}
          onAddSlot=${(course) => setScreen({ name: 'slotForm', course, slot: null })} />`
  }

  return html`
    <div class="app">
      ${body}
      <${TabBar} active=${tab} onSelect=${setTab} />
    </div>`
}

render(html`<${App} />`, document.getElementById('root'))
