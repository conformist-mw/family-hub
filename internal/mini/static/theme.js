// Which of the two themes to paint. Telegram is the source of truth when the
// app runs inside it — the client knows whether the person is in a dark chat —
// and it can change while the app is open, so the event is worth listening to.
// Outside Telegram there is no client, and the OS preference decides through
// the media query in style.css; nothing is written to <html> in that case.
import { tg } from '/mini/assets/api.js'

function apply(scheme) {
  const dark = scheme === 'dark'
  document.documentElement.dataset.theme = dark ? 'dark' : 'light'
  if (!tg) return
  // The client draws the header and the area under the app itself. Left
  // alone they keep Telegram's own background, which is a different shade
  // than the app's and reads as a seam across the top of the screen.
  const bg = dark ? '#17181c' : '#f2f3f7'
  if (tg.setBackgroundColor) tg.setBackgroundColor(bg)
  if (tg.setHeaderColor) tg.setHeaderColor(bg)
  if (tg.setBottomBarColor) tg.setBottomBarColor(bg)
}

export function watchTheme() {
  if (!tg) return
  apply(tg.colorScheme)
  tg.onEvent('themeChanged', () => apply(tg.colorScheme))
}
