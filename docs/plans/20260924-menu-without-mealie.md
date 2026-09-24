# Меню на сьогодні без Mealie

## Overview

Замість Mealie — мінімальний потік «що приготувати сьогодні» прямо у family-hub:
вранці бот пропонує страви кнопками, увечері перепитує, що зрештою їли, раз на
тиждень luna пропонує нові страви. Жодних інгредієнтів, списків покупок і плану на
тиждень наперед.

Проблема, яку це розв'язує:
- Модель Mealie (рецепт → інгредієнти → список покупок → meal plan з entry types)
  за два місяці не знадобилася: інгредієнти ніхто не купує, план ніхто не правив, бо
  для цього треба лізти в Mealie. Потрібна була лише відповідь «що ще приготувати».
- **Історія в Mealie вигадана.** З 89 записів «приготовано» в таймлайні майже всі —
  щоденна автопозначка самого Mealie о 20:45 («Олег made this for lunch/dinner») для
  всього, що стояло в плані; справжніх записів із фото ~3. Планувальник family-hub
  заповнює план о 05:30 → Mealie позначає його з'їденим → планувальник ротує за
  `lastMade`, який сам і вигадав.
- Меню приходить **на завтра**, а в родині ніхто не планує на завтра: готують і
  докуповують сьогодні.

Як інтегрується:
- Нові таблиці `dishes` / `meals` / `menu_messages` у `family-hub.db` — під той самий
  нічний бекап, що й решта.
- Ранкове меню, вечірній перепит і суботні пропозиції — ще три годинники в
  `RunDigests`, як шкільний дайджест. Надсилає сам бот у `NotifyChat`, не HA: натиски
  кнопок приходять боту.
- Журнал готування (фото / `/cooked`) лишається, але розпізнає і пише в локальні
  таблиці замість Mealie.
- `internal/cooking` (разом із планувальником) зникає; `internal/mealie` живе лише
  до одноразового імпорту (див. Post-Completion).
- Web UI і Mini App не чіпаємо.

## Context (from discovery)

Файли/компоненти:
- `internal/bot/digests.go` — хвилинний тікер. **Дві пастки** (як у плані
  school-week-review): ранній вихід на рядках 36-39 і стартовий лог 40-50 — обидва
  треба навчити новим прапорцям. `dueThisMinute` (рядок 131) повертає **5 булів**,
  її кличуть 16 разів (по 8 у `reminders_test.go` і `school_test.go`).
- `internal/bot/bot.go` — `Config.Cooking *cooking.Service` (рядок 100); журнал
  готування ввімкнений лише за `cfg.Cooking != nil && cfg.Dish != nil` (236, 266);
  **усі callback-и реєструються тут** у `New()` через
  `tb.Handle(&tele.Btn{Unique: "..."}, ...)` (200-249), зокрема `ckd_promote`.
  telebot парсить callback як `^\f([-\w]+)(\|(.+))?$`: Unique — лише `[-\w]`, дані —
  через `m.Data(label, unique, args...)`, склеєні `|`.
- `internal/bot/cooked.go` (834 рядки) — картка тарілки, `cookedPending` у пам'яті,
  `recognise` (219-227 збирає `dish.Input`), `writeCooked`, створення рецепта, промоут
  фото, `slotFrom`/`dateFrom`, `shorten`, `redraw` (терпить
  `tele.ErrSameMessageContent`). `onPhoto` (183) у групі ігнорує фото без `/cooked`.
- `internal/bot/awaiting.go` — `awaitingEntry{apptID, field}` жорстко під
  редагування запису; `onText` (`appointments.go:60`) віддає його в `applyEdit`;
  `take()` видаляє запис, TTL 10 хв.
- `internal/bot/*_test.go` — **бот тестується через чисті функції**
  (`buildNagMarkup`, `choreRefsFromMarkup`, `schoolWeekChunks(now, …)`), без
  `tele.Context` і мережі — свідомо (`school.go:612-618`). `Bot.now()`
  (`appointments.go:211`) читає годинник, не інжектиться. `asTelegramSentIt`
  (`reminders_test.go`) — round-trip клавіатури.
- `withAppButton` додає рядок Mini App до кожного групового повідомлення.
- `internal/dish/dish.go` — `Recognizer.Identify`, `Input{Recipes []mealie.Recipe,
  Categories, Tags}`, `Item{Recipe, Alts []mealie.Recipe, Category, Tags}`,
  `Guess.Slot` з enum `"obid"|"vecheria"` у `responseSchema`/`parseGuess`,
  `parseGuess(raw, catalogue)`.
- `internal/cooking/cooking.go` — `Service` поверх Mealie і `Slot` з `Title()`;
  `planner.go` — заповнення плану Mealie.
- `internal/mealie/mealie.go` — `Recipe{ID, Slug, Name}`; `/api/recipes` уже віддає
  `tags` і `recipeCategory`, треба лише додати поля.
- `internal/model` — типи рядків (`model.Balance`, `model.Appointment`); `store`
  повертає їх.
- `internal/db/migrations/` — goose, остання `0012_school_lesson_pupil_notes.sql`;
  `created_at` скрізь з
  `DEFAULT (strftime('%Y-%m-%dT%H:%M:%S','now','localtime'))`; DSN з
  `foreign_keys(1)`; `migrations_test.go` перевіряє наявність таблиць і обмеження
  (`db.prepare` неекспортований — down-тестів немає).
- `internal/actor` — `Roster` з `TELEGRAM_PEOPLE`: хто натиснув/написав.
- `cmd/server/main.go:107-133` — Mealie і планувальник; `:171-191` — `AI_*`.
- `Dockerfile` (6-7) збирає лише `/out/server` і `/out/migrate`.
- `localharness/` — тимчасовий, лише Mini App, не для коміту; бота не покриває.

Дані в проді (перевірено 2026-09-24): 55 рецептів у Mealie; теги прийому їжі
`obid`/`vecheria`, «на вихідні» — `dostavka`/`pokupne`; категорії Гарніри,
Заготовки, Соуси та заправки, Салати тощо.

## Development Approach
- **testing approach**: Regular (спершу код, потім тести в тій самій задачі)
- кожну задачу доводити до кінця перед наступною; **збірка зелена після кожної**
- малі, сфокусовані зміни
- **CRITICAL: кожна задача МАЄ містити нові/оновлені тести** для змін у ній
  - юніт-тести для нових функцій/методів
  - юніт-тести для змінених функцій/методів
  - нові кейси для нових гілок коду
  - оновлення наявних тестів, якщо поведінка змінилась
  - і успішні, і помилкові сценарії
- **CRITICAL: усі тести мають проходити перед наступною задачею** — без винятків
- **CRITICAL: оновлювати цей план, якщо обсяг змінюється під час реалізації**
- `go test ./...` після кожної зміни
- зворотна сумісність не потрібна для Mealie-частини: вона видаляється свідомо

## Testing Strategy
- **unit tests**: обов'язкові для кожної задачі; стиль — як у наявних
  `internal/store/*_test.go` (справжня SQLite у тимчасовому файлі) і
  `internal/bot/*_test.go` (чисті функції, фіксований `now`, без мережі).
- **Шов для бота**, щоб callback-и було чим тестувати без фейкового
  `tele.Context`: кожен потік ділиться на
  1. чисті білдери (`menuView(state) (text, markup)`, `eveningView(...)`,
     `suggestView(...)`) — тестуються напряму, клавіатура — через
     `asTelegramSentIt`;
  2. аплаєри поверх store з явним `now` (`applyMenuTap(now, data, who)`,
     `applyEveningTap(...)`, `applySuggestTap(...)`, `buildMenu(now)`) — тестуються
     на справжній SQLite;
  3. тонкі хендлери, що лише кличуть аплаєр і `c.Edit`/`sendToGroup` — без тестів.
- **e2e tests**: у проєкті немає UI e2e — не застосовується.
- Модель (luna) у тестах не викликається: перевіряються промпт (що в нього потрапило)
  і розбір відповіді на зафіксованих JSON.

## Progress Tracking
- позначати виконане `[x]` одразу
- нові задачі — з префіксом ➕
- блокери — з префіксом ⚠️
- оновлювати план, якщо реалізація відходить від задуманого

## Solution Overview

**Страва — це те, що ставлять на стіл.** «Пюре зі скумбрією» — одна страва, а не
головна + гарнір. Це скасовує рішення з `ARCHITECTURE.md` («Combining the pair into a
'goulash with mash' recipe was considered and rejected»): на практиці комбінацій
небагато, а окремий «гарнір» у меню — шум («Гречка» кнопкою на обід).

**План і факт — різні статуси одного запису.** Ранковий натиск пише `meals` зі
статусом `planned`; вечірня відповідь або фото переводить у `eaten`, замінює або
видаляє. Історія («що їли») — лише `eaten`; ротація — `planned` і `eaten`, інакше
вчорашній невідповідений борщ завтра прийшов би як «давно не їли».

**Залишки без прапорця на страві.** «Доїдаємо» = усе, що вчора підтвердили як
з'їдене. Борщ, доїдений учора, теж «з'їдений учора», тож ланцюжок тягнеться 2-3 дні
сам і обривається, щойно його перестають обирати. Запис має `leftover=1`.

**Нові страви живуть у тій самій таблиці.** `status`: `active` / `proposed` /
`rejected`. 🆕 = рівно `proposed`: «подумаю» лишає страву `proposed`, і вона зрідка
потрапляє в ранкове меню з 🆕; обрали й підтвердили → сама стає `active`. «Ні» =
`rejected` назавжди, і цей список іде luna як виключення. (Імпортовані 55 страв без
історії — звичайні `active`, не 🆕.)

**Показане теж старить.** Страва, яку щоранку показують і не обирають, інакше
ніколи б не старіла й сиділа б у вікні вибору вічно. Тому `menu_messages.shown`
живить ротацію: показане вчора не пропонується сьогодні (поки пул не вичерпано).
Це ж і причина, чому таблиця потрібна; поточні рядки на екрані читаються з самої
клавіатури (як `choreRefsFromMarkup`), а не з `shown`.

**Фото не зберігаються** — лише для розпізнавання.

## Technical Details

### Схема (`0013_menu.sql`)

```sql
CREATE TABLE dishes (
    id         INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,              -- українською
    name_key   TEXT NOT NULL,              -- нормалізована назва, рахується в Go
    meal       TEXT NOT NULL DEFAULT 'any' CHECK (meal IN ('lunch','dinner','any')),
    days       TEXT NOT NULL DEFAULT 'any' CHECK (days IN ('any','weekend')),
    status     TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','proposed','rejected')),
    note       TEXT NOT NULL DEFAULT '',   -- рядок-опис від luna для proposed
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%S','now','localtime'))
);
CREATE UNIQUE INDEX dishes_name ON dishes(name_key);

CREATE TABLE meals (
    id         INTEGER PRIMARY KEY,
    dish_id    INTEGER NOT NULL REFERENCES dishes(id),
    date       TEXT NOT NULL,              -- YYYY-MM-DD, локальна дата
    meal       TEXT NOT NULL CHECK (meal IN ('lunch','dinner')),
    who        TEXT NOT NULL DEFAULT '',
    status     TEXT NOT NULL CHECK (status IN ('planned','eaten')),
    leftover   INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%S','now','localtime'))
);
CREATE UNIQUE INDEX meals_once ON meals(dish_id, date, meal);

CREATE TABLE menu_messages (
    date       TEXT PRIMARY KEY,           -- одне ранкове меню на день
    chat_id    INTEGER NOT NULL,
    message_id INTEGER NOT NULL,
    shown      TEXT NOT NULL DEFAULT '{}'  -- JSON: {"lunch":[ids...], "dinner":[ids...]} — усе показане за день
);
```

- Down: `meals` → `menu_messages` → `dishes` (FK увімкнені в DSN).
- **Унікальність назви:** SQLite `lower()` не згортає кирилицю, тож `name_key`
  (`strings.Fields` → join одним пробілом, `strings.ToLower`, `ʼ`/`'`/`’` → одне)
  рахується в Go. Інакше «Борщ» і «борщ» — дві страви.
- `meals_once` — захист від подвійного запису, як «same recipe, same instant» у
  поточному журналі.

### Семантика записів у `meals`

- `PlanMeal(dish, date, meal, who, leftover)` — у транзакції видаляє інші `planned`
  цього `(date, meal)` і вставляє `planned`; наявний `eaten` не чіпає.
- `RecordEaten(dish, date, meal, who, leftover)` — у транзакції:
  `INSERT ... ON CONFLICT(dish_id,date,meal) DO UPDATE SET status='eaten' WHERE status='planned'`;
  «вже записано» — лише коли рядок уже був `eaten`; видаляє інші `planned` того ж
  `(date, meal)`; страву `proposed` переводить в `active`. Це закриває і «план →
  фото тієї ж страви», і «план → фото іншої страви».
- `ConfirmMeal(id)` = `RecordEaten` для рядка плану.

### Вибір варіантів (`internal/menu`, чиста функція)

```go
type Candidate struct{ Dish model.Dish; LastSeen time.Time /* zero = ніколи */ }
func Pick(cands []Candidate, date time.Time, meal Meal, shownToday, shownYesterday map[int64]bool, n int, rnd *rand.Rand) []model.Dish
func IsWeekend(date time.Time, meal Meal) bool  // пт вечеря, сб, нд
```

- Пул: `active` з `meal ∈ {meal, any}`; у будні без `days=weekend`; мінус `exclude`
  (показане сьогодні + показане вчора). Порожній після виключень → спершу без
  вчорашнього, потім без сьогоднішнього (пішли по колу).
- **Перемішати пул `rnd`, потім стабільно відсортувати за `LastSeen`** — інакше в
  перший день усі 55 з нульовим `LastSeen` стоять у порядку id і щоранку ті самі.
  Далі випадкова вибірка `n` з перших `max(2n, n+3)`.
- 🆕: окремо, з імовірністю (1 з 3 ранків), одна `proposed` замість останньої
  звичайної — не більше однієї на повідомлення.
- `rnd` передається — тести детерміновані.

### Ранкове повідомлення

```
🍽 Що приготувати сьогодні

Доїдаємо: Борщ
Обід: Плов · Деруни · Солянка
Вечеря: Відбивні · Вареники · Удон

[↩ Борщ · обід] [↩ Борщ · вечеря]
[Плов] [Деруни] [🆕 Солянка]
[🔀 обід]
[Відбивні] [Вареники] [Удон]
[🔀 вечеря]
[застосунок]                      ← withAppButton
```

- Мітки «Обід:»/«Вечеря:» — у тексті (у inline-клавіатурі немає не-кнопок); назви
  на кнопках через `shorten()`; остаточну розкладку вирішити на місці за
  читабельністю на телефоні.
- Uniques (реєстрація в `bot.go`, безумовно — меню працює без AI):
  `menu_pick`, `menu_shuf`, `menu_left`; дані `2026-09-24|lunch|17` /
  `2026-09-24|lunch`. Дата в даних — учорашні кнопки не пишуть у сьогодні.
- Натиск страви → `PlanMeal`, перемалювати з ✅ і ім'ям з `actor.Roster`.
- 🔀 → `Pick(exclude=shown∪вчора)`, у транзакції дописати в `shown`, перемалювати;
  рядки, що не міняються, беруться з поточної клавіатури.
- Перемальовка через `c.Edit` із `withAppButton` і терпимістю до
  `ErrSameMessageContent`, як `redraw` у `cooked.go`.
- Рестарт у хвилину відправки не шле вдруге: є рядок `menu_messages` за сьогодні →
  пропустити. Порожній пул для обох прийомів → нічого не слати, лог.

### Вечірній перепит

По кожному прийому без `eaten` за сьогодні; дані кнопок несуть `date|meal|dishID`:
- є `planned`: `Обід: Борщ — так? [Так] [Інше] [Не їли вдома]` (+ `[Ні, не наше]`,
  якщо страва `proposed`).
- немає: кнопки вчорашніх страв + `[Інше]` + `[Не їли вдома]`.
- Uniques: `eve_yes`, `eve_pick` (учорашня страва), `eve_home`, `eve_rej`,
  `eve_other`.
- `Так`/учорашня → `RecordEaten`; `Не їли вдома` → видалити `planned`;
  `Ні, не наше` → видалити `planned`, страва → `rejected`; `Інше` → очікування
  (нижче). `eve_other` реєструється лише з `cfg.Dish != nil`; без AI кнопки
  «Інше» немає.

**Очікування «Інше».** `awaitingEntry` отримує вид: `appt_edit` (як зараз) або
`meal_other{date, meal}`. `onText` диспетчеризує за видом; `onPhoto` перевіряє
очікування **до** гейту `/cooked`, тож фото у групі у відповідь теж працює.
`recognise` отримує необов'язкові зафіксовані дата/прийом замість
`slotFrom`/`dateFrom`. Помилка моделі → «не розпізнав, оберіть кнопкою або
спробуйте пізніше» + повторний `set` (бо `take()` видаляє).

### Суботні пропозиції

- `dish.Recognizer.Suggest(ctx, SuggestInput) ([]Suggestion, error)`: вхід — назви
  `active`, `rejected`, `proposed` + сталий контекст родини; вихід — 3 ×
  `{name, meal, days, note}`.
- Фільтр у Go: збіг `name_key` з будь-якою наявною стравою відкидається.
- Вижилі одразу пишуться `proposed` — тоді «проігнорували» = «подумаю» без коду.
- Повідомлення: рядок на страву + `[➕ Додати] [🤔 Подумаю] [✖ Ні]`; Uniques
  `sugg_add`, `sugg_maybe`, `sugg_no`, дані — id страви; реєструються з
  `cfg.Dish != nil`.
- Запуск асинхронно з атомарним гардом, як `sendSchoolWeekReview`: виклик моделі до
  60 с не має блокувати тікер.
- Помилка моделі / нуль вижилих → лог, без повідомлення.

### Конфіг (env)

| змінна | за замовчуванням | що |
|---|---|---|
| `MENU_TIME` | порожньо = вимкнено | ранкове меню, `HH:MM` |
| `MENU_EVENING_TIME` | порожньо = вимкнено | вечірній перепит |
| `DISH_SUGGEST_DOW` | `-1` = вимкнено | день пропозицій (0 = нд, 6 = сб) |
| `DISH_SUGGEST_TIME` | — | час пропозицій |

`dishSuggestEnabled()` = час заданий && `DOW >= 0` && `Dish != nil` (як
`schoolWeekReviewEnabled` вимагає `School != nil`) — інакше nil-receiver у горутині
`RunDigests` поклав би весь сервер.

Зникають із `main.go`: `MEALPLAN_FILL_TIME`, `MEALPLAN_SLOTS`,
`MEALPLAN_HORIZON_DAYS`, `MEALPLAN_REST_DAYS`, `MEALIE_PUBLIC_URL`.
`MEALIE_URL`/`MEALIE_TOKEN` лишаються лише для команди імпорту.

## What Goes Where
- **Implementation Steps** (`[ ]`): зміни в `~/dev/family-hub` — код, тести, документація.
- **Post-Completion** (без чекбоксів): dotfiles, деплой, імпорт на проді, HA,
  прибирання Mealie.

## Implementation Steps

### Task 1: Міграція `0013_menu` зі стравами, журналом і станом меню

**Files:**
- Create: `internal/db/migrations/0013_menu.sql`
- Modify: `internal/db/migrations_test.go`

- [x] створити таблиці `dishes`, `meals`, `menu_messages` і індекси за схемою вище; Down у порядку `meals` → `menu_messages` → `dishes`
- [x] тест: таблиці існують після `Migrate` (як наявні тести)
- [x] тест: CHECK відкидає невідоме значення, `meals_once` відкидає дубль, FK відкидає неіснуючу страву
- [x] `go test ./...` — має пройти перед задачею 2

### Task 2: Store для страв

**Files:**
- Create: `internal/model/dish.go` (або в наявний файл моделей — за патерном)
- Create: `internal/store/dishes.go`
- Create: `internal/store/dishes_test.go`

- [x] `model.Dish`; `store.NameKey(name)` — `strings.Fields` + join, `ToLower`, уніфікація апострофів
- [x] `CreateDish(d) (model.Dish, existed bool, error)` — при збігу `name_key` повертає наявну
- [x] `EnsureDish(d)` для журналу готування: як `CreateDish`, але наявну `rejected`/`proposed` переводить в `active` (її справді їли)
- [x] `Dishes(statuses ...string)`, `Dish(id)`, `SetDishStatus(id, status)`
- [x] тести: створення, дедуплікація «Борщ»/« борщ »/подвійні пробіли/апострофи, `EnsureDish` реактивує `rejected`, зміна статусу
- [x] тести: невідомий id, невалідний статус
- [x] `go test ./...` — має пройти перед задачею 3

### Task 3: Store для журналу їжі

**Files:**
- Create: `internal/model/meal.go` (тип `model.MealEntry`)
- Create: `internal/store/meals.go`
- Create: `internal/store/meals_test.go`

- [x] `PlanMeal`, `RecordEaten`, `ConfirmMeal` за «Семантикою записів у `meals`»
- [x] `DeleteMeal(id)`, `MealsOn(date) []model.MealEntry`, `EatenOn(date)`
- [x] `LastSeen() map[dishID]date` (planned + eaten)
- [x] тести: план замінює план, план не чіпає `eaten`, план → фото тієї ж страви = `eaten`, план → фото іншої = інша `eaten` + план видалено, повторний `RecordEaten` = «вже записано», `proposed` → `active`, LastSeen враховує planned
- [x] тести: підтвердження неіснуючого, видалення не зачіпає інші дні
- [x] `go test ./...` — має пройти перед задачею 4

### Task 4: Вибір варіантів меню

**Files:**
- Create: `internal/menu/pick.go`
- Create: `internal/menu/pick_test.go`

- [x] тип `Meal` (`lunch`/`dinner`) з українським `Title()` (замінить `cooking.Slot`)
- [x] `IsWeekend(date, meal)` — пт вечеря, сб, нд
- [x] `Pick(...)` за правилами з Technical Details (пул, двоступеневе повернення по колу, shuffle + стабільне сортування, вибірка з найдавніших, одна `proposed` з імовірністю)
- [x] `Leftovers(eatenYesterday []model.MealEntry) []int64` — унікальні страви
- [x] тести (table-driven, фіксований `rand`): будні без `weekend`, пт вечеря з `weekend`, `any` в обох прийомах, never-seen першими, `exclude` + коло, не більше однієї 🆕
- [x] тест: усі страви з нульовим `LastSeen`, кілька «днів» поспіль з exclude вчорашнього — набори різні і покривають пул
- [x] тести: порожній пул → порожній результат, `n` більше пулу
- [x] `go test ./...` — має пройти перед задачею 5

### Task 5: Годинники меню в `RunDigests`

**Files:**
- Modify: `internal/bot/digests.go`
- Modify: `internal/bot/bot.go` (`Config`: `MenuTime`, `MenuEveningTime`, `DishSuggestDOW`, `DishSuggestTime`, `menuEnabled()`, `menuEveningEnabled()`, `dishSuggestEnabled()`)
- Modify: `internal/bot/reminders_test.go`, `internal/bot/school_test.go`
- ➕ Create: `internal/bot/digests_test.go` (тести нових годинників)

- [x] `dueThisMinute` повертає структуру `due{daily, weekly, nag, school, review, menu, evening, suggest bool}`, `last*` — теж структурою (інакше 8 позиційних булів і 16 правок викликів при кожному наступному годиннику)
- [x] ранній вихід і стартовий лог знають про три нові прапорці
- [x] `dishSuggestEnabled()` вимагає `Dish != nil`
- [x] `sendMenu` / `sendEveningCheck` / `sendDishSuggestions` — поки заглушки, що логують
- [x] оновити 16 наявних викликів у тестах під структуру
- [x] тести: кожен новий годинник спрацьовує рівно раз на день у свою хвилину, `DOW=-1` вимикає пропозиції, `Dish == nil` вимикає пропозиції, порожній час вимикає меню
- [x] `go test ./...` — має пройти перед задачею 6

### Task 6: Ранкове меню з вибором, 🔀 і «Доїдаємо»

**Files:**
- Create: `internal/bot/menu.go`
- Create: `internal/bot/menu_test.go`
- Create: `internal/store/menu_messages.go`
- Create: `internal/store/menu_messages_test.go`
- Modify: `internal/bot/bot.go` (реєстрація `menu_pick`, `menu_shuf`, `menu_left`)

- [x] store: `MenuMessage(date)`, `SaveMenuMessage`, `AppendShown(date, meal, ids)` у транзакції (два одночасні 🔀 не гублять одне одного)
- [x] `buildMenu(now) (menuState, ok)`: нема рядка за сьогодні; `Pick` для обох прийомів з exclude показаного вчора; `Leftovers(вчора)`; обидва прийоми порожні → `ok=false`
- [x] `menuView(state) (text, markup)` — розкладка з Technical Details, `shorten`, ✅ + ім'я, `withAppButton`
- [x] `applyMenuTap(now, unique, data, who)` — pick / shuf / left; дата з даних ≠ сьогодні → «це меню вже минуло», нічого не пише; поточні рядки — з клавіатури повідомлення
- [x] тонкі `sendMenu` і хендлери (відправка, `SaveMenuMessage`, `c.Edit` з терпимістю до `ErrSameMessageContent`)
- [x] тести store: `AppendShown` накопичує, `MenuMessage` повертає збережене
- [x] тести `menuView`: з/без залишків, з вибором, порожній прийом; round-trip клавіатури через `asTelegramSentIt`
- [x] тести `buildMenu`/`applyMenuTap` на SQLite: pick пише `planned`, pick іншої — заміна, shuf не повторює показане, застаріла дата не пише, повторний `buildMenu` за той самий день → `ok=false`, порожній каталог → `ok=false`
- [x] `go test ./...` — має пройти перед задачею 7

### Task 7: Журнал готування на локальних стравах

Розпізнавач і картка тарілки міняються разом: типи `dish` використовує лише
`cooked.go`, окремо збірка не пройде.

**Files:**
- Modify: `internal/dish/dish.go`, `internal/dish/dish_test.go`
- Modify: `internal/bot/cooked.go`, `internal/bot/cooked_test.go`
- Modify: `internal/bot/bot.go`
- Modify: `cmd/server/main.go`

- [x] `dish.Input.Recipes` → `Dishes []DishRef{ID int64, Name string}`; прибрати `Categories`/`Tags`; `Item.Recipe`/`Alts` → `DishRef`, `Known()` за `ID != 0`; `Item.Category`/`Tags` → `Meal`, `Days`
- [x] промпт: каталог рядками `- 17 | Борщ`, у схемі `id: integer|null`; прибрати category/tags; «комбінація, яку подають разом (пюре зі скумбрією), — одна страва, якщо вона є такою в списку»; для нової страви — `meal`, `days`; `Guess.Slot` enum → `lunch`/`dinner`
- [x] `parseGuess`: id поза каталогом відкидається, як вигаданий slug зараз
- [x] `cooked.go`: каталог — `store.Dishes("active","proposed")`; підтвердження → `RecordEaten` на кожну страву тарілки (закриває `planned`); «створити» → `EnsureDish(meal, days)` без запису їжі; прибрати фото-промоут, посилання на Mealie, `organizerNames`; `cooking.Slot` → `menu.Meal`
  - ➕ прибрано й «головну страву» (`ckd_main`, ⭐): кожна страва тарілки пишеться окремим `eaten`, тож головна ні на що не впливала; `cookedAt` теж зник (канонічна година була потрібна лише таймлайну Mealie)
- [x] `bot.go`: прибрати `Config.Cooking`, гейт журналу — `cfg.Dish != nil`, прибрати реєстрацію `ckd_promote`, виправити лог «MEALIE_TOKEN or AI_API_KEY not set»
- [x] `main.go`: прибрати `Cooking: cookingSvc` з `bot.Config` (сам `cooking.NewService`/планувальник поки лишаються — див. задачу 11)
  - ➕ `cooking.NewService` і `MEALIE_PUBLIC_URL` прибрано вже тут: без `Cooking:` змінна `cookingSvc` не використовується і не компілюється; планувальник лишається до задачі 11
- [x] тести `dish`: оновити під нові типи; id поза каталогом, комбінація як одна страва, meal/days нової страви, slot `lunch`/`dinner`
- [x] тести картки: оновити під нові типи; запис кількох страв, закриття `planned`, дубль «вже було записано», створення нової страви, реактивація `rejected`
- [x] `go test ./...` — має пройти перед задачею 8

### Task 8: Вечірній перепит

**Files:**
- Modify: `internal/bot/menu.go`, `internal/bot/menu_test.go`
- Modify: `internal/bot/awaiting.go`
- Modify: `internal/bot/appointments.go` (`onText` диспетчеризує за видом очікування)
- Modify: `internal/bot/cooked.go` (`onPhoto` перевіряє очікування до гейту `/cooked`; `recognise` з зафіксованими датою/прийомом)
- Modify: `internal/bot/bot.go` (реєстрація `eve_*`)
- ➕ Modify: `internal/bot/applist.go` (`awaiting.set` → `setEdit`), `internal/bot/digests.go` (прибрано заглушку `sendEveningCheck`)
- ➕ Create: `internal/bot/awaiting_test.go`

- [x] `eveningView(now, meals) (text, markup, ok)` — за правилами з Technical Details; обидва прийоми закриті → `ok=false`
- [x] `applyEveningTap(now, unique, data, who)` — `eve_yes`, `eve_pick`, `eve_home`, `eve_rej`
- [x] `awaitingEntry` з видом `appt_edit` / `meal_other{date, meal}`; `eve_other` ставить `meal_other`; помилка моделі → повідомлення + повторний `set`
- [x] `onText` і `onPhoto` віддають `meal_other` у `recognise` з зафіксованими датою/прийомом
- [x] тонкий `sendEveningCheck`
- [x] тести `eveningView`: план, без плану (учорашні страви), `proposed` дає «Ні, не наше», усе закрито → тиша
- [x] тести `applyEveningTap` на SQLite: Так → `eaten`, `proposed` + Так → `active`, Ні, не наше → `rejected` і план видалено, Не їли вдома → нічого в `meals`
- [x] тести очікування: вид `meal_other` не потрапляє в `applyEdit`; наявні тести редагування записів зелені
- [x] `go test ./...` — має пройти перед задачею 9

### Task 9: Суботні пропозиції нових страв

**Files:**
- Create: `internal/dish/suggest.go`, `internal/dish/suggest_test.go`
- Create: `internal/bot/suggest.go`, `internal/bot/suggest_test.go`
- Modify: `internal/bot/bot.go` (реєстрація `sugg_*` за `cfg.Dish != nil`)
- ➕ Modify: `internal/bot/digests.go` (прибрано заглушку `sendDishSuggestions`)

- [x] `Recognizer.Suggest(ctx, SuggestInput)` — промпт зі сталим контекстом родини (два прийоми, свинина/курка, без баранини, домашня українська кухня, старший не їсть рибу) і списками active/proposed/rejected; JSON 3 × `{name, meal, days, note}`
- [x] `filterSuggestions(sugg, existing)` — відкинути збіги `NameKey`
- [x] `sendDishSuggestions`: асинхронно з атомарним гардом; вижилі → `CreateDish(proposed, note)` → `suggestView` → відправка; помилка / нуль вижилих → лог
- [x] `applySuggestTap(unique, data)` — add → `active`, maybe → без змін (✓ на картці), no → `rejected`; картка перемальовується з відповіддю
  - ➕ `applySuggestTap` отримує ще й клавіатуру повідомлення: з неї читаються страви картки і «подумаю» (✓ на кнопці), якого немає в БД; рядки кнопок лишаються після відповіді — передумати можна ще одним натиском
- [x] тести `Suggest`: промпт містить rejected і proposed, розбір відповіді, кривий JSON → помилка
- [x] тести бота: `filterSuggestions` відкидає дубль, `applySuggestTap` міняє статуси, `suggestView` round-trip
- [x] `go test ./...` — має пройти перед задачею 10

### Task 10: Одноразовий імпорт з Mealie

**Files:**
- Create: `cmd/import-mealie/main.go`, `cmd/import-mealie/main_test.go`
- Modify: `internal/mealie/mealie.go` (`Recipe` + `Tags`, `RecipeCategory` — `/api/recipes` уже їх віддає)
- Modify: `Dockerfile` (збирати й копіювати `/out/import-mealie`)

- [x] чиста функція `mapRecipe(r) (model.Dish, skip string)`: `meal` з `obid`/`vecheria` (обидва/жодного → `any`), `days=weekend` для `dostavka`/`pokupne`; категорії гарніри / заготовки / соуси / напої → skip з причиною
- [x] за замовчуванням dry-run: друкує, що буде імпортовано, і окремо пропущене; `-apply` пише через `CreateDish` (ідемпотентно за `name_key`)
- [x] `Dockerfile`: третій бінар поруч із `server` і `migrate`
- [x] тести `mapRecipe`: кожне правило тегів, кожна skip-категорія, салат без тегу прийому → `any`
- [x] `go test ./...` і `docker build .` — мають пройти перед задачею 11
  - ➕ `Recipe.Tags`/`RecipeCategory` покрито тестом декодування в `internal/mealie/mealie_test.go`; `importDishes` — тестом ідемпотентності на SQLite (повторний запуск не дублює і не чіпає `rejected`)

### Task 11: Прибрати Mealie з сервера

**Files:**
- Modify: `cmd/server/main.go`
- Delete: `internal/cooking/` (`cooking.go`, `planner.go` і їхні тести)
- Modify: `.env.example`

- [x] прибрати `mealie.New` / `cooking.NewService` / `NewPlanner` і змінні `MEALIE_*` / `MEALPLAN_*` з сервера
- [x] прокинути `MENU_TIME`, `MENU_EVENING_TIME`, `DISH_SUGGEST_DOW`, `DISH_SUGGEST_TIME` у `bot.Config`
- [x] `.env.example`: додати нові змінні (старих там немає)
- [x] `go vet ./... && go test ./...`; `grep -r internal/mealie` — лише `cmd/import-mealie`
- [x] наявні тести зелені; нових тестів тут немає — логіка реєстрації вже покрита гейтами в задачах 5 і 7
  - ➕ додано `cmd/server/main_test.go` з тестом `parseDOW`: незаданий `DISH_SUGGEST_DOW` = `-1` (вимкнено), а не неділя; прибрано невикористаний `atoiOr`

### Task 12: Verify acceptance criteria
- [ ] ранкове меню: сьогодні, обід + вечеря, 🔀 по кожному, «Доїдаємо» з учорашнього, вихідні страви лише пт-вечір/сб/нд
- [ ] вибір — `planned`, вечір або фото — `eaten`, невідповідений вечір не ламає ротацію
- [ ] вечірній перепит мовчить, коли все закрито
- [ ] суботні пропозиції: `proposed` з'являється як 🆕 не частіше одного на меню, `rejected` більше не пропонується
- [ ] кнопки ранкового меню працюють після рестарту
- [ ] фото / `/cooked` пишуть у `meals`, нова страва створюється локально
- [ ] `go test ./...`, `go vet ./...`
- [ ] ручний прогін з dev-токеном бота з `.env.example`, polling, приватний чат як `NotifyChat`: меню, натиск, 🔀, рестарт, натиск ще раз, вечірній перепит, «Інше» текстом і фото

### Task 13: [Final] Документація
- [ ] `ARCHITECTURE.md`: переписати «The cooking log» і замінити «Filling the meal plan» на «The menu» — чому не Mealie (вигадана історія), страва = те, що на столі (явно скасувати рішення про комбінації), plan vs eaten, залишки без прапорця, 🆕 = `proposed`, показане старить, стан у БД на відміну від карток тарілки
- [ ] `ARCHITECTURE.md` → «Stack» / «Secrets» і згадки env: прибрати Mealie/`MEALPLAN_*`, згадати, що luna тепер і пропонує
- [ ] `DEPLOY.md`: нові змінні, прибрані змінні, як запустити `import-mealie`
- [ ] `README.md`, якщо там згадано Mealie
- [ ] перенести цей план у `docs/plans/completed/`

## Post-Completion
*Потребує ручних дій або інших систем — без чекбоксів*

**Cutover (у такому порядку):**
- dotfiles `roles/family-hub`: прибрати `MEALPLAN_*` і `MEALIE_PUBLIC_URL`;
  `MEALIE_URL`/`MEALIE_TOKEN` поки лишити — для імпорту. `MENU_*` / `DISH_SUGGEST_*`
  **ще не задавати**.
- Зібрати й запушити образ, прогнати ansible-роль (не руками docker).
- `import-mealie` (dry-run) у контейнері → переглянути пропущене з користувачем: що
  викинути, що перетворити на комбінацію («Котлети з пюре», «Гречка з …») →
  `-apply` → комбінації додати вручну.
- Задати `MENU_TIME=07:00`, `MENU_EVENING_TIME=20:00`, `DISH_SUGGEST_DOW=6`,
  `DISH_SUGGEST_TIME` (напр. `10:00`), прогнати роль.
- Вимкнути HA `automation.meniu_na_zavtra_v_telegram` **до** першого 07:00 нового
  меню, інакше прийдуть два.

**Спостереження 1-2 тижні:** меню приходить, кнопки живі після деплоїв, вечірній
перепит не набридає (якщо набридає — перенести час або слати лише за наявності плану).

**Прибирання Mealie (окремим PR / ранами після спостереження):**
- Фінальний дамп БД Mealie в архів поза сервером.
- HA: видалити інтеграцію mealie (entry `01M0YVAJ338XAECBPHM10KD5E8`) і саму
  вимкнену автоматизацію.
- dotfiles: роль `mealie`, роутер traefik, ACL tinyauth, віджет homepage, ендпоінт
  gatus, записи бекапу; `MEALIE_URL`/`MEALIE_TOKEN` з `roles/family-hub` і SOPS
  (токен `family-hub-bot` зникає разом із Mealie).
- family-hub: видалити `cmd/import-mealie`, `internal/mealie` і третій бінар з
  `Dockerfile`.
- Пам'ять Claude: оновити `mealie_setup.md`, `cooking_log_bot.md` (Mealie
  прибрано, меню живе у family-hub).
