# Додаткові оплати курсу (payment extras)

## Overview

Курс іноді вимагає грошей, які не купують ні заняття, ні місяць абонемента: кімоно на
карате, форма на футбол, внесок за аттестацію, поїздка на змагання. Зараз такий платіж
записати нікуди — `payments` за визначенням означає «або пакет занять (`lessons_paid`),
або оплачений місяць (`covers_from`/`covers_until`)», і третього стану в ній немає.

Наслідок: витрата або не записується взагалі (і статистика по курсу занижена), або
записується як оплата занять і **бреше в балансі** — дашборд покаже +N занять, яких
ніхто не купував, бот замовкне про потрібну оплату, а прогноз розкладе неіснуючі
заняття по майбутніх датах.

Що робимо: `payments` отримує третій вид рядка — `kind = 'extra'` з вільнотекстовим
`label` («за що»). Він видимий у списку оплат, у статистиці та в ленті аудиту, і
**навмисно невидимий для балансу**.

Тільки факт оплати. Ні планування, ні дедлайнів, ні нагадувань «купити кімоно до
жовтня» — це була б інша сутність (мінізадача з ціною і статусом), і вона свідомо не
входить у скоуп.

Як інтегрується:
- **Баланс, покриття і прогноз не змінюються ані на рядок** — див. Solution Overview.
- **Бот**: доп. оплата їде в сімейну групу тим самим повідомленням, що й звичайна.
- **Нагадування про оплату** (`payment_notice_min`, `billing_reminders`) не чіпаємо:
  доп. оплата нічого не покриває, тож нічого й не відкладає.

## Context (from discovery)

Файли/компоненти:
- `internal/db/migrations/` — 10 наявних міграцій, goose, `-- +goose Up/Down`.
  Нова буде `0011_payment_extras.sql`. Конвенція тестів — `internal/db/migrations_test.go`,
  хелпер `migrated(t)` (рядок 12).
- `internal/model/model.go:207` — `type Payment`. Коментар на рядках 213-215 описує
  `Billing` як «which half of this row is meaningful — LessonsPaid or the coverage
  range». Після цієї зміни половин три, і коментар доведеться переписати.
- `internal/payments/payments.go` — `Form` (рядок 25) і `Form.Parse` (рядок 36),
  **єдині двері для запису на обох поверхнях**. Саме тут живе інваріанта, а не в БД.
- `internal/payments/notify.go:55` — `covers(p)` каже, що купили; doc-коментар на
  52-54 пише: пусто, «which the forms do not allow but an old imported row might».
  Після зміни ця гілка стає законною.
- `internal/store/payments.go` — `ListPayments`, `PaymentsForEnrollment` (рядок 59),
  `TotalPaid` (82), `GetPayment`, `CreatePayment` (109), `UpdatePayment` (120).
- `internal/store/audit.go` — `AuditData` (payQ на рядку 22) і `LastPaymentDate` (115).
- `internal/store/stats.go` — `CourseSpend` (рядок 13), `Stats` (21), `spendByMonth`
  (39), ByCourse-запит (124).
- `internal/audit/ledger.go` — константи `KindVisit`/`KindPayment`/`KindFuture`
  (14-16), `Summary` (35), `BuildLedger` (48).
- `internal/store/billing_test.go:12` — хелпер `testStore(t)`, **перевикористати**.
  Увага: файл у пакеті `store_test` (як усі `internal/store/*_test.go`), тож
  неекспортовані функції стора з тестів недосяжні.

**Гілки, які вже захищають баланс від рядка без `lessons_paid`/`covers_*`** (перевірено).
Механізми різні, і плутати їх не варто:
- SQL-фільтри `IS NOT NULL` — `internal/store/store.go:42` (сума занять у балансі),
  `internal/store/store.go:92` (`coveragePeriods`), `internal/store/audit.go:80`
  (opening balance). Це справжні guard'и: рядок відсіюється запитом.
- Go nil-check — `internal/audit/forecast.go:128` (`RemainingPacks` пропускає
  `LessonsPaid == nil`) і `internal/audit/ledger.go:61`/`:65` (running balance ленти
  зростає лише коли `LessonsPaid != nil`).
- **НЕ guard**: `internal/store/stats.go:93` — `COALESCE(strftime('%Y-%m',
  covers_from), strftime('%Y-%m', date))`. Він нікого не відсіює, а лише відносить
  рядок без покриття до місяця переказу. Через нього графік «за оплачені періоди»
  виходить коректним даром, але захисту він не дає.

**Пастка №1: платіж рендериться в ЧОТИРЬОХ місцях, не в трьох.** Усі чотири треба
навчити новому виду рядка:
- `internal/web/templates/payments.html:29` — тригілкове
  `{{if .LessonsPaid}}…{{else if .CoversUntil}}…{{else}}—{{end}}`, готова точка вставки.
- `internal/web/templates/dashboard.html:57` — блок «Останні оплати» (44-65), який
  живиться `ListPayments` з `internal/web/router.go:281`. **`{{else}}` тут немає
  взагалі**, тож доп. оплата вже зараз дала б порожню клітинку «за що» на головному
  екрані балансу — саме той провал, який план і має закрити.
- `internal/mini/home.go:223` — `switch` у `homePaymentRows` **без `default`**:
  готовий третій `case`.
- `internal/audit` + її три рендерери — див. пастку №2.

**Пастка №2: ленту аудиту рендерять ТРИ місця**, і всі три треба навчити новому рядку:
- `internal/audit/text.go:88` (`case KindPayment`) — текст для Telegram
- `internal/web/templates/audit.html:53-57` (клас рядка — `internal/web/static/style.css:421`)
- `internal/mini/audit.go:212` (`case audit.KindPayment`)

Підсумок ленти рендериться теж у трьох місцях і теж потребує нового числа:
`internal/audit/text.go:54`, `internal/web/templates/audit.html:31-32`,
`internal/mini/audit.go:158`.

**Пастка №3: `Format` не екранує те, що повертає `covers()`.**
`internal/payments/notify.go:41`/`:43` екранують `p.Class` і `p.Person` кожен на
своєму місці, а `covers(p)` приклеюється **сирим** (рядки 46-48) — бо сьогодні він
віддає лише згенеровані застосунком рядки (числівник або назва місяця). Щойно він
почне віддавати `label`, `&` в назві завалить усе повідомлення (HTML parse mode, як і
попереджає doc-коментар на 38-39). Запис при цьому вже успішний, тож група просто
нічого не почує.

**Пастка №4: явний `INSERT` обходить `DEFAULT`.** Див. Solution Overview — це причина,
чому `Form.Parse` мусить виставляти `Kind` і на курсовому шляху.

Інші готові точки вставки:
- `internal/web/templates/payment_form.html` — файл на 79 рядків, `<script>` на 63-78,
  `toggle()` на 68-74; зараз дивиться лише на `data-billing` вибраного курсу.
- `internal/mini/static/style.css:228-239` — `.chips`/`.chip`/`.chip-on`, які вже
  використовує чипсовий вибір місяця (`payments.js:144-151`). Перемикач виду в Mini App
  робимо на них — тоді CSS чіпати не треба.

Патерни, які повторюємо:
- Одна колонка = один сенс (`readings.tariff_id`, `reminder_rules` як список версій).
  Тому `label` окремо, а не в `comment`.
- `DEFAULT` у міграції замість `UPDATE` наявних рядків (як `0002`, `0003`, `0005`).
- Форми віддають рядки (`Form` — усі поля `string`), бо це те, що дає `<input>`.
- Тести міграцій через `migrated(t)`; `Down` пишеться, але не тестується.

## Development Approach

- **testing approach**: Regular (код, потім тести в межах тієї ж задачі) — як у решті
  репозиторію.
- виконувати задачу повністю, перш ніж братися за наступну
- **CRITICAL: кожна задача містить нові/оновлені тести**
- **CRITICAL: усі тести проходять до початку наступної задачі**
- **CRITICAL: оновлювати цей файл, якщо скоуп змінюється під час роботи**
- зворотна сумісність: наявні оплати, баланс, прогноз і нагадування не ламаються.
  Порожній `Form.Kind` мусить і далі означати `course` — це те, що захищає всі наявні
  виклики й тести.

## Testing Strategy

- **unit tests**: обов'язкові в кожній задачі
- **e2e**: UI-e2e (Playwright/Cypress) у проєкті немає, не застосовно
- **регресійний тест №1 (найважливіший): курсові оплати не зникають.** Після того, як
  задача 3 додасть `AND kind='course'` у `PaymentsForEnrollment` і `LastPaymentDate`,
  треба довести, що звичайна оплата, записана через `CreatePayment`, **знаходиться**
  цими запитами. Це той тест, який ловить пастку №4; без нього фіча тихо вимикає
  `/packs` у боті і зсуває період аудиту на «весь час».
- **регресійний тест №2: доп. оплата не рухає баланс.** Створити курс, оплатити пакет
  занять, додати `extra`, перевірити `Balance.Paid`, `Balance.Remaining`, а для
  monthly-курсу — `Balance.CoveredNow` / `Balance.CoversUntil` / `Balance.DaysLeft`.
  Саме через `Balance`, а не через `coveragePeriods`: остання неекспортована
  (`internal/store/store.go:89`), а тести стора живуть у пакеті `store_test`.
- **регресійний тест №3**: `Form.Parse` з порожнім `Kind` поводиться як `course` на
  обох типах біллінгу і виставляє `Kind = "course"` у результаті.
- **міграція**: після `migrated(t)` голий `INSERT INTO payments (enrollment_id, date,
  amount, lessons_paid)` дає `kind='course'`, `label=''`. Перевіряти «наявний рядок до
  міграції» неможливо — `db.Migrate` проганяє всі 11 міграцій разом.
- **чого не тестуємо**: `Service.announce` наскрізь (Telegram) — як і зараз.
  Перевіряємо натомість `payments.Format`/`covers` на рядку з `label`, включно з
  екрануванням.

## Progress Tracking

- позначати виконане `[x]` одразу
- нові задачі — з префіксом ➕
- блокери — з префіксом ⚠️
- тримати план синхронним із реальною роботою

## Solution Overview

**Ключове рішення: новий вид рядка в наявній `payments`, а не окрема таблиця.**

Причина — асиметрія цін, яку показала розвідка. Уся математика по заняттях і покриттю
**вже** відсіює рядок без `lessons_paid` і без `covers_*` — трьома SQL-фільтрами і
трьома nil-check'ами, перелічені в Context. Натомість історія і статистика сумують
`amount` **без жодного фільтра** у восьми місцях (`stats.go:41` з двох call site'ів,
`:68`, `:71`, `:74`, `:100`, `:125`, плюс `payments.go:82`) і підхоплять новий рядок
без правок.

Окрема таблиця (`enrollment_extras`) дала б жорсткішу гарантію ізоляції, але ціною
`UNION ALL` у всіх цих запитах плюс повний дубль CRUD у `store`, `web`, `mini`,
шаблонах і тестах. Це робота проти власного дизайну: розділити зберігання, щоб потім
склеювати назад у восьми місцях. Доп. оплата — це буквально гроші, що пішли на курс, і
у звітах вона мусить стояти поруч з рештою.

**`kind` існує не для математики, а для намірів.** Технічно nil-guard'и вже все роблять;
`kind` робить задум явним, дає точку опори в UI («Кімоно» з бейджем, а не в колонці, де
«8 зан.») і чесний фільтр там, де сенс функції — саме про заняття.

**⚠️ `DEFAULT 'course'` НЕ рятує від забутого `Kind`.** Спокусливо думати, що голий
`INSERT` дасть `course` сам. Це так лише поки колонку не названо явно — а задача 3
називає її в `CreatePayment` (`internal/store/payments.go:109`) і `UpdatePayment`
(`:120`). З цієї миті туди йде `p.Kind`, і якщо `Form.Parse` не виставив його на
курсовому шляху, там буде `''`. У парі з `AND kind='course'` із тієї ж задачі це
означає, що **всі нові оплати зникають** з `/packs` і з періоду аудиту, а редагування
старого рядка через веб переписує його `kind` з `'course'` на `''`. Тому `Form.Parse`
виставляє `Kind` на **обох** шляхах, і задача 3 має тест «курсова оплата знаходиться».
`DEFAULT` лишається — але як страховка для міграції наявних рядків, не для коду.

**Чому `label` окремою колонкою, а не в `comment`.** Це два різних факти. `label` —
чим оплата **є**; він у списку й у статистиці. `comment` — примітка збоку («переказала
Оля, чек у неї»); він у таблиці взагалі не показується, тільки у формі й у ленті.
Звалити їх в одну колонку — це або втратити примітку, або показувати її як назву.

**Чому підсумок ленти розходиться на два числа.** `Summary.PaidAmount` — пара до
`PaidLessons`: «оплачено за період: 8 занять (3200 ₴)» (`internal/audit/text.go:56`).
Додати туди кімоно означає, що сума перестає ділитися на кількість, і читач, який
перевірить арифметику, знайде її невірною. Тому доп. оплати їдуть у нове `ExtrasAmount`.

**Чому `CourseSpend.Extras` лишається, попри те, що `Amount` уже включає доп. оплати.**
Це прямий запит: хочеться бачити, що на курс пішли гроші **ще й** на форму, а не лише
дізнатися, що сума побільшала. Розбивка є лише в розрізі курсів і свідомо не робиться
в `ByPerson`/`ByMonth`: курс — єдиний розріз, де доп. оплата атрибутується конкретній
речі («кімоно на карате»), а «скільки доп. витрат було в березні» — інше питання, якого
ніхто не ставив. Асиметрія тут навмисна, а не недороблена.

**Чому без `CHECK` у схемі — і в чому це відступ від місцевої конвенції.** Репозиторій
`CHECK` для такої форми **використовує**: `internal/db/migrations/0010_school_lesson_details.sql:75`
має `kind TEXT NOT NULL CHECK (kind IN ('homework', 'lesson'))`. Тут ми свідомо
відступаємо: інваріанта складніша за перелік значень («у `extra` порожні і
`lessons_paid`, і `covers_*`, і непорожній `label`»), вона вже живе в `Form.Parse` —
єдиних дверях для запису, — а SQLite не вміє зняти `CHECK` без перебудови таблиці.
Перелік значень `kind` можна було б закріпити `CHECK`ом окремо, але тоді інваріанта
опиниться в двох місцях, і сильнішу половину все одно доведеться тримати в Go.

## Technical Details

Схема:

```sql
ALTER TABLE payments ADD COLUMN kind  TEXT NOT NULL DEFAULT 'course';
ALTER TABLE payments ADD COLUMN label TEXT NOT NULL DEFAULT '';
```

Модель:

```go
const (
    PaymentKindCourse = "course"
    PaymentKindExtra  = "extra"
)
```

`model.Payment` += `Kind string`, `Label string`.

`payments.Form` += `Kind string` (порожньо == `course`), `Label string`.

`Form.Parse` після розбору дати й суми, **перед** гілкою біллінгу:

```go
if f.Kind == model.PaymentKindExtra {
    p.Kind = model.PaymentKindExtra
    p.Label = strings.TrimSpace(f.Label)
    if p.Label == "" {
        return p, valid.FieldError{Field: "label", Message: "вкажи, за що оплата"}
    }
    return p, nil
}
p.Kind = model.PaymentKindCourse   // ⚠️ обов'язково: явний INSERT обходить DEFAULT
```

Нуль у сумі лишається дозволеним (наявне правило «заняття могли дати безкоштовно»);
окремого правила для доп. оплат не заводимо.

Повідомлення в групу — `covers()` віддає **екранований** label:

```go
if p.Kind == model.PaymentKindExtra {
    return html.EscapeString(p.Label)
}
```

Статистика — другий агрегат у ByCourse-запиті:

```sql
SUM(CASE WHEN pm.kind='extra' THEN pm.amount ELSE 0 END) AS extras
```

`CourseSpend` += `Extras float64`.

Лента — нове поле рядка називається `Row.What` (не `Label`): на шар вище
`auditRowDTO.Label` (`internal/mini/audit.go:216-220`) уже означає інше — **відрендерений**
рядок («оплата +8»). Два `Label` з різним сенсом через одну межу — саме те, від чого
застерігає власне правило «не змішувати сенси». `Covers` теж не перевикористовуємо.

Потік запису (не змінюється): форма → `payments.Form` → `Service.Prepare` →
`Form.Parse(billingType)` → `store.CreatePayment` → `announce`.

## What Goes Where

- **Implementation Steps**: усе в цьому репозиторії — міграція, код, шаблони, тести,
  `ARCHITECTURE.md`.
- **Post-Completion**: перевірка на живих даних після деплою і сам деплой.

## Implementation Steps

### Task 1: Міграція 0011 і поля моделі

**Files:**
- Create: `internal/db/migrations/0011_payment_extras.sql`
- Modify: `internal/model/model.go`
- Modify: `internal/db/migrations_test.go`

- [x] створити міграцію з двома `ALTER TABLE payments ADD COLUMN` (`kind`, `label`)
      і `-- +goose Down`, що знімає обидві колонки
- [x] додати константи `PaymentKindCourse` / `PaymentKindExtra` в `internal/model/model.go`
- [x] додати `Kind` і `Label` у `model.Payment`
- [x] переписати коментар `model.go:213-215`: спершу `Kind`, і тільки в `course` далі
      вирішує `Billing`; у `extra` не значима жодна з половин
- [x] написати тест міграції: після `migrated(t)` голий
      `INSERT INTO payments (enrollment_id, date, amount, lessons_paid)` дає
      `kind='course'` і `label=''`
- [x] запустити тести — мусять пройти до задачі 2

### Task 2: Гілка extra у Form.Parse

**Files:**
- Modify: `internal/payments/payments.go`
- Modify: `internal/payments/payments_test.go`

- [x] додати `Kind` і `Label` у `payments.Form` з коментарем, що порожній `Kind`
      означає `course`
- [x] додати гілку `Kind == PaymentKindExtra` у `Form.Parse` перед гілкою біллінгу:
      вимагає непорожній `Label`, повертає без уроків і місяця
- [x] **виставити `p.Kind = model.PaymentKindCourse` на курсовому шляху** з
      коментарем чому (явний `INSERT` у задачі 3 обходить `DEFAULT`)
- [x] ➕ `Kind`/`Label` виставляються **до** валідації дати й суми: форма з
      помилкою перерендерюється з того, що повернув `Parse`, тож інакше доп.
      оплата з опискою в сумі перемалювалася б як курсова і без label
      (+ тест `TestParseKeepsTheExtraKindThroughAValidationError`)
- [x] написати тести: `extra` з `label` парситься на обох типах біллінгу; уроки й
      місяць не вимагаються
- [x] написати тест помилки: `extra` з порожнім/пробільним `label` →
      `valid.FieldError{Field: "label"}`
- [x] **написати регресійний тест: порожній `Kind` дає `Kind == "course"`** у
      результаті `Parse`, на `per_lesson` і на `monthly`
- [x] запустити тести — мусять пройти до задачі 3

### Task 3: Store — CRUD, фільтри, регрес балансу

**Files:**
- Modify: `internal/store/payments.go`
- Modify: `internal/store/audit.go`
- Create: `internal/store/payments_test.go`

- [x] додати `kind`/`label` у `SELECT`/`INSERT`/`UPDATE`: `GetPayment`,
      `ListPayments`, `CreatePayment` (рядок 109), `UpdatePayment` (120)
- [x] додати `AND kind='course'` у `PaymentsForEnrollment` (рядок 59) — функція за
      сенсом про пакети занять; дописати чому в doc-коментар
- [x] додати `AND kind='course'` у `LastPaymentDate` (рядок 115) — інакше покупка
      кімоно зсуне початок періоду аудиту
- [x] тягнути `kind`/`label` у `AuditData.payQ` (рядок 22)
- [x] написати тест round-trip `kind`/`label` через `testStore(t)`
- [x] **написати регресійний тест №1: курсова оплата, записана `CreatePayment`,
      знаходиться `PaymentsForEnrollment` і `LastPaymentDate`** (ловить пастку №4)
- [x] **написати регресійний тест №2: доп. оплата не рухає баланс** — `Balance.Paid`,
      `Balance.Remaining`, а для monthly-курсу `Balance.CoveredNow` /
      `Balance.CoversUntil` / `Balance.DaysLeft`
- [x] написати тест: `LastPaymentDate` ігнорує `extra`
- [x] ➕ `courseByDefault` у `store`: пустий `Kind` при записі означає `course`.
      Знайдено на тестах — усі наявні сіди збирають `model.Payment{}` напряму,
      без `Kind`, і після задачі 3 випадали з `kind='course'`-фільтрів
      (3 падіння в `internal/mini` і `internal/web`). Правити двадцять сідів
      означало б лишити міну для будь-якого майбутнього прямого виклику;
      натомість store став тотальним. + тест
      `TestAPaymentWrittenWithoutAKindIsACoursePayment`
- [x] запустити тести — мусять пройти до задачі 4

### Task 4: Повідомлення в групу

**Files:**
- Modify: `internal/payments/notify.go`
- Modify: `internal/payments/payments_test.go`

- [ ] додати в `covers(p)` гілку, що повертає **`html.EscapeString(p.Label)`** —
      `Format` (рядки 46-48) приклеює результат `covers` сирим, на відміну від
      `p.Class`/`p.Person`
- [ ] переписати doc-коментар `covers` (рядки 52-54): гілка «ні те, ні те» тепер
      законна, це `extra`; додати, чому екранування саме тут
- [ ] не змінювати заголовок «💸 Оплата» і не заводити четвертий шаблон тексту
- [ ] написати тест `Format` на рядку з `label`: назва курсу, дитина, сума, «за що»
- [ ] написати тест екранування: `label` з `&` і `<` не ламає рядок
- [ ] запустити тести — мусять пройти до задачі 5

### Task 5: Лента аудиту — новий вид рядка

**Files:**
- Modify: `internal/audit/ledger.go`
- Create: `internal/audit/ledger_test.go` (у `internal/audit/` тестів ще немає — це перший)

- [ ] додати константу `KindExtra` поруч з `KindVisit`/`KindPayment`/`KindFuture`
- [ ] додати `What string` у `audit.Row` (не `Label` — конфлікт сенсу з
      `auditRowDTO.Label`; не перевикористовувати `Covers`)
- [ ] додати `ExtrasAmount float64` у `Summary` з коментарем, чому не в `PaidAmount`
- [ ] у `BuildLedger` віддавати `extra` як `KindExtra` у таймлайні по даті, **не
      торкаючись running balance**, і сумувати в `ExtrasAmount`
- [ ] написати тест: `extra` стоїть у таймлайні по даті, `Balance` після неї не
      змінюється
- [ ] написати тест: `PaidAmount` не містить доп. оплату, `ExtrasAmount` містить
- [ ] написати тест на порядок при однаковій даті (оплата перед візитом — наявне правило)
- [ ] запустити тести — мусять пройти до задачі 6

### Task 6: Три рендерери ленти

**Files:**
- Modify: `internal/audit/text.go`
- Modify: `internal/web/templates/audit.html`
- Modify: `internal/web/static/style.css`
- Modify: `internal/mini/audit.go`
- Create: `internal/audit/text_test.go`
- Modify: `internal/mini/audit_test.go`

- [ ] `text.go:88` — додати `case KindExtra` з рендером «за що» і сумою
- [ ] `text.go:54` — додати рядок підсумку про доп. оплати, окремо від
      «Оплачено за період»
- [ ] `audit.html:53-57` — додати гілку `{{if eq .Kind "extra"}}` і власний клас рядка
- [ ] `style.css:421` — додати правило для нового класу поруч з `tr.ledger-payment`
- [ ] `audit.html:31-32` — вивести `ExtrasAmount` окремим `<span>`
- [ ] `mini/audit.go:212` — додати `case audit.KindExtra`, заповнити `Label` з `What`
- [ ] `mini/audit.go:158` — додати доп. оплати в підсумковий рядок
- [ ] написати тести на текстовий рендер і на mini-DTO для рядка `extra`
- [ ] запустити тести — мусять пройти до задачі 7

### Task 7: Статистика по курсах

**Files:**
- Modify: `internal/store/stats.go`
- Modify: `internal/web/templates/stats.html`
- Modify: `internal/store/stats_test.go`

- [ ] додати `Extras float64` у `CourseSpend`
- [ ] додати `SUM(CASE WHEN pm.kind='extra' THEN pm.amount ELSE 0 END)` у
      ByCourse-запит (рядок 124) і в `Scan`
- [ ] `stats.html:70-74` — показати «з них N доп.» у рядку курсу, коли `Extras > 0`
- [ ] написати тест: `ByCourse` віддає повну суму в `Amount` і доп. частину в `Extras`;
      курс без доп. оплат має `Extras == 0`
- [ ] **написати тест, що доп. оплата включена в `TotalAll`, `ByMonth`, `ByPerson` і
      `TotalPaid`** — це закріплює задум «видно в статистиці» у всіх розрізах
- [ ] запустити тести — мусять пройти до задачі 8

### Task 8: Веб — форма, список, дашборд

**Files:**
- Modify: `internal/web/payments.go`
- Modify: `internal/web/templates/payment_form.html`
- Modify: `internal/web/templates/payments.html`
- Modify: `internal/web/templates/dashboard.html`
- Modify: `internal/web/static/style.css`
- Modify: `internal/web/routes_test.go`

- [ ] `web/payments.go` — читати `r.FormValue("kind")` і `r.FormValue("label")`
      у `payments.Form`
- [ ] `payment_form.html` — перемикач виду згори («За заняття» / «Додатково») і блок
      `#block-extra` з полем «За що»
- [ ] **`payment_form.html` — префіл при редагуванні**: `value="{{$p.Label}}"` і
      перемикач за `$p.Kind`; інакше наявна доп. оплата відкриється як курсова і
      збережеться зі втратою або з помилкою «вкажи кількість оплачених занять».
      Те саме для шляху повторного рендеру з помилкою (`renderPaymentFormError`,
      `web/payments.go:177`)
- [ ] `payment_form.html:68-74` — розширити `toggle()`: спершу вибраний вид, при
      `extra` ховати обидва блоки біллінгу; при `course` — наявна логіка по `data-billing`
- [ ] `payments.html:29` — додати гілку `{{if eq .Kind "extra"}}{{.Label}}` **першою**,
      плюс бейдж (правило в `style.css`), щоб «Кімоно» не читалося як оплата занять
- [ ] `dashboard.html:57` — додати ту саму гілку в блок «Останні оплати»
      (там немає навіть `{{else}}`, тож зараз клітинка була б порожня)
- [ ] написати тест: POST форми з `kind=extra` створює рядок з `label`
- [ ] написати тест: POST з `kind=extra` без `label` повертає форму з помилкою поля
- [ ] **написати тест edit round-trip**: GET форми наявної доп. оплати містить `label`
      і вид `extra`; PUT без змін не перетворює її на курсову
- [ ] запустити тести — мусять пройти до задачі 9

### Task 9: Mini App

**Files:**
- Modify: `internal/mini/home.go`
- Modify: `internal/mini/payments.go`
- Modify: `internal/mini/static/payments.js`
- Modify: `internal/mini/home_test.go`
- Modify: `internal/mini/payments_test.go`

- [ ] `home.go` — додати `Kind` і `Label` у `homePaymentDTO`
- [ ] `home.go:223` — третій `case` у `switch`: `Detail = p.Label`
- [ ] `payments.go` — додати `kind` і `label` у `paymentForm` і в `form()`
- [ ] `payments.js` — сегмент виду згори форми **на наявних `.chips`/`.chip`/`.chip-on`**
      (`mini/static/style.css:228-239`, той самий ідіом, що чипси місяця на
      `payments.js:144-151`) — CSS не чіпаємо
- [ ] `payments.js` — при `extra` не рендерити ні «Оплачено занять», ні чипи місяців,
      замість них поле «За що»
- [ ] `payments.js` — додати `kind`/`label` у тіло запиту і в початковий `useState`
      для режиму редагування
- [ ] написати тест: `homePaymentRows` дає `Detail = label` для `extra`
- [ ] написати тест: POST/PUT `mini` API з `kind=extra` (успіх + помилка поля `label`)
- [ ] запустити тести — мусять пройти до задачі 10

### Task 10: Verify acceptance criteria

- [ ] перевірити, що всі вимоги з Overview реалізовані
- [ ] перевірити наскрізь: доп. оплата видна в `/lessons/payments`, на дашборді, у
      статистиці по курсу, в ленті аудиту і на головній Mini App
- [ ] перевірити, що баланс курсу після доп. оплати не змінився (веб + Mini App)
- [ ] **перевірити, що звичайна оплата, створена після міграції, видна в `/packs`
      бота і що період аудиту «з оплати» відкривається на ній** — наскрізна перевірка
      пастки №4
- [ ] перевірити, що бот надіслав повідомлення з «за що»
- [ ] запустити повний набір тестів: `go test ./...`
- [ ] `go vet ./...` — лінтера в репозиторії не налаштовано (немає `.golangci.yml`),
      тож це і є реальний гейт

### Task 11: [Final] Update documentation

- [ ] `ARCHITECTURE.md:41` — переписати опис `payments` («money in. Either prepaid
      lessons or a monthly pass»): третій вид рядка і явна фраза, що він навмисно
      невидимий для балансу, з переліком guard'ів, які це забезпечують
- [ ] `ARCHITECTURE.md:452-456` — там сказано, що повідомлення в групу називає
      «a pack of lessons or a named month»; додати третій варіант
- [ ] `ARCHITECTURE.md` — згадати, що інваріанта живе у `payments.Form.Parse`, а не в
      схемі, і що це свідомий відступ від `CHECK`-конвенції `0010`
- [ ] перенести цей план у `docs/plans/completed/`

## Post-Completion

*Потребує ручних дій або зовнішніх систем — без чекбоксів, інформативно*

**Ручна перевірка після деплою:**
- записати справжню оплату кімоно на карате Демида і подивитися, що баланс карате не
  зрушився, а сума за місяць виросла
- перевірити на телефоні, що сегмент виду в Mini App не ламає форму на вузькому екрані

**Зовнішні системи:**
- деплой через dotfiles: `just deploy-hetzner-tag family-hub`
- міграція застосовується автоматично на старті (goose, вбудований у бінар) —
  окремої дії не потребує, але варто глянути лог першого старту

**Поза скоупом (окрема робота):**
- баг з byline «(веб)» замість імені: `actorName` (`internal/web/appointments.go`)
  читав заголовки oauth2-proxy, а в проді tinyauth. Це issue #67; фікс уже лежить у
  робочому дереві окремо від цього плану. Разом з ним застаріє й
  `ARCHITECTURE.md:451` («whatever oauth2-proxy forwards») — правити там, не тут.
