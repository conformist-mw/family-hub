# Moodle: оцінки та тижневий огляд школи

## Overview
- Нова школа — дистанційна, на Moodle 5.3. Інтеграція заміняє видалену school-today (#115), але інша за змістом: розкладу уроків немає, є **оцінки** та **прогрес по тижнях**.
- Три речі в сімейну групу (`TELEGRAM_NOTIFY_CHAT`):
  1. **нові оцінки** — фоновий синк кожні ~2 год (не вночі), нові оцінки однією пачкою на синк; курс, який синк бачить уперше, запам'ятовується мовчки;
  2. **`/grades`** — поточна зведена: середнє по предмету + оцінки по порядку;
  3. **п'ятничний огляд** (у проді Пт 19:45) — по предмету: пройдено X/Y елементів поточного «Тиждень N», оцінки за тиждень, середнє.
- Повідомлення українською.
- Репозиторій **публічний**: у коді, фікстурах, плані й комітах немає імені дитини, назви/адреси школи, email чи Moodle userid. Адреса школи — тільки в env/dotfiles, без дефолту в коді.

## Context (from discovery)
- API перевірено вручну 25.11:
  - `POST /login/token.php` (`username`, `password`, `service=moodle_mobile_app`) → `{"token","privatetoken"}`; помилка → `{"error","errorcode"}`.
  - `POST /webservice/rest/server.php` з `wstoken`, `wsfunction`, `moodlewsrestformat=json` — усе в тілі POST, ніколи в query (інакше токен потрапить у `*url.Error` і в логи). Помилка — HTTP 200 з `{"exception","errorcode","message"}`.
  - `core_webservice_get_site_info` → `userid`.
  - `core_enrol_get_users_courses(userid)` → ~20 курсів (`id`, `fullname` на кшталт «Алгебра 9»).
  - `gradereport_overview_get_course_grades(userid)` → середнє по курсу (`grade` "5,5" або "-").
  - `gradereport_user_get_grade_items(courseid,userid)` → `gradeitems[]`: `id`, `itemname`, `itemtype` (`mod`/`course`/`category`), `itemmodule`, `iteminstance`, `graderaw` (null, поки не оцінено), `gradeformatted` ("7,8" / "-"), `gradedategraded`.
  - `core_course_get_contents(courseid)` → секції «Тиждень N» з модулями (`id`, `modname` lesson/quiz/label/book, `instance`).
  - `core_completion_get_activities_completion_status(courseid,userid)` → `statuses[]` (`cmid`, `state` 0..3) — лише модулі з відстеженням.
  - Календар, завдання (assign), дати відкриття квізів порожні; формат курсів `topics`, у секцій дат немає.
- «Тиждень 0» = тиждень 31.08.2026 (дати оцінок ОВ 10/11/12 лягають на тижні 10/11/12). Номер тижня рахуємо від якоря `SCHOOL_MOODLE_WEEK0`.
- Шаблон видаленої інтеграції: `git show 67c470c^:internal/schooltoday/sync.go` (Service: store, client, cfg, loc, now, м'ютекс) і `git show 67c470c^:internal/bot/school.go` (~L529-560: огляд у горутині з `atomic.Bool` і `context.WithTimeout`); той самий патерн зараз у `sendDishSuggestions` (`internal/bot/suggest.go`).
- Годинники: `internal/bot/digests.go` — `RunDigests`, `due`/`lastFired`, `dueThisMinute`, `anyDigestEnabled`, стартовий лог; `countDue` у `digests_test.go`.
- Команди: `internal/bot/bot.go` (`SetCommands`, реєстрація з гейтом як `/cooked` на `cfg.Dish`), `internal/bot/handlers.go` (`cmdHelp`, `cmdStart`).
- Відправка в групу: `b.NotifyHTML` уже ріже по рядках у межах ліміту Telegram; для відповіді на команду — `audit.SplitMessage` (`internal/audit/text.go`).
- Міграції: `internal/db/migrations`, остання `0016_log.sql` (конвенції: шапка-коментар «чому», `CHECK (x <> '')`, індекси `idx_<table>_<col>`), тест у `internal/db/migrations_test.go`.
- Env-парсинг: `cmd/server/main.go`, патерн `withMenuEnv(cfg, getenv)` + `main_test.go`; конкретний `*Service` у конфігу бота (як колишній `Config.School`), бо nil-вказівник в інтерфейсі не nil.
- Секрети: `~/dev/dotfiles/host_vars/hetzner/secrets.sops.yaml` (`family_hub_*`), env у `roles/family-hub/tasks/main.yml`.
- `localharness/` у корені не відстежується й не в `.gitignore` — не комітити.

## Development Approach
- **testing approach**: Regular (код, потім тести в тій самій задачі)
- кожну задачу завершувати повністю перед наступною; малі фокусовані зміни
- **CRITICAL: кожна задача містить нові/оновлені тести**
- **CRITICAL: усі тести проходять перед наступною задачею**
- **CRITICAL: оновлювати цей план, якщо скоуп змінюється**
- клієнт і синк тестуються на `httptest`-сервері з фікстурами у форматі реальних відповідей, але з вигаданими предметами/ID/іменами

## Testing Strategy
- unit-тести на кожну задачу: клієнт (логін, розбір відповідей і помилок, токен не в URL), store (upsert/seed по курсу), синк (seed-кейси, фільтри Moodle), форматування, годинник огляду (`dueThisMinute`, `anyDigestEnabled`, `countDue`), `WeekProgress`, розбір env.
- e2e-тестів у проєкті немає.

## Progress Tracking
- `[x]` одразу після виконання; ➕ для нових задач; ⚠️ для блокерів

## Solution Overview
- `internal/moodle` — клієнт Moodle (токен мобільного API + REST) і `Service` синку. Логін на початку кожного синку/огляду (раз на ~2 год) — без логіки перелогіну по `invalidtoken`.
- Оцінки в `school_grades` (ключ — grade item id), курси в `school_courses`. Синк робить upsert у транзакції й повертає нові/змінені оцінки лише для курсів, які вже були відомі; курс, якого ще не було в `school_courses`, засівається мовчки. Так порожній перший синк, курс, що впав при першому синку, і новий курс посеред року не дають ні мовчання, ні флуду.
- Відправка не відстежується (`notified_at` немає): якщо Telegram упав, ці оцінки втрачено для сповіщення, але вони є в `/grades`. Свідомо простіше.
- Курси, яких немає в успішному синку, видаляються разом з оцінками (новий навчальний рік не змішується зі старим).
- Огляд іде в окремій горутині з `atomic.Bool` і таймаутом, як `sendDishSuggestions`; HTTP-клієнт з таймаутом 30 с.
- Без налаштування (порожній `SCHOOL_MOODLE_URL`/логін) — сервіс nil: синку немає, `/grades` не реєструється, годинник огляду не входить в `anyDigestEnabled`. Без `TELEGRAM_BOT_TOKEN` синк усе одно працює з nil-нотифікатором (оцінки накопичуються).

## Technical Details
- Env: `SCHOOL_MOODLE_URL` (обов'язково, без дефолту), `SCHOOL_MOODLE_USERNAME`, `SCHOOL_MOODLE_PASSWORD`, `SCHOOL_MOODLE_SYNC_INTERVAL` (default `2h`), `SCHOOL_MOODLE_WEEK0` (`YYYY-MM-DD`, понеділок тижня 0; без нього огляд вимкнено), `SCHOOL_MOODLE_REVIEW_DOW` / `SCHOOL_MOODLE_REVIEW_TIME` (unset = вимкнено). Синк пропускає 22:00–07:00 у `cfg.Loc`.
- Міграція `0017_school_moodle.sql`:
  - `school_courses(id INTEGER PRIMARY KEY, name TEXT NOT NULL CHECK (name <> ''), position INTEGER NOT NULL, average REAL, average_text TEXT, synced_at TEXT NOT NULL)` — `name` без хвостового ` \d+`;
  - `school_grades(item_id INTEGER PRIMARY KEY, course_id INTEGER NOT NULL, item_module TEXT, item_instance INTEGER, name TEXT NOT NULL CHECK (name <> ''), grade_raw REAL NOT NULL, grade_text TEXT NOT NULL, graded_at TEXT NOT NULL)`, `idx_school_grades_course_id`.
- Фільтри Moodle:
  - зберігаються лише items з `itemtype = 'mod'` і `graderaw != null`;
  - «нова/змінена» = item_id не було або `grade_raw` змінився;
  - `graded_at` = `gradedategraded`, а якщо 0/null — час синку.
- Прогрес тижня (`WeekProgress`):
  - секція — за префіксом «Тиждень N» (`^Тиждень\s+N\b`);
  - Y — модулі секції, що є в `statuses` completion (label і невідстежувані не рахуються);
  - X — `state` 1 або 2 (3 = «завершено, не склав» — не пройдено);
  - оцінки тижня — зі store за `graded_at` у межах Пн–Нд;
  - якщо секції немає — рядок «секції «Тиждень N» немає» (сигнал підправити якір після канікул).
- Скорочення: «Оцінка вчителя N» → «ОВ N», «Контрольна робота N» → «КР N», інші назви як є (до 40 символів); оцінка без «,0». ⚠️ якщо середнє < 4 (порівняння по `average REAL`).
- Формати (HTML, кожен рядок із закритими тегами, щоб розрізання по рядках було безпечне):
  - нові оцінки: `🆕 Нові оцінки` + `<b>Алгебра</b>: ОВ 12 — 2,2 (сер. 5,5)`;
  - `/grades`: `📚 Оцінки` / `<b>Предмет</b> — 5,5[ ⚠️]` / `   <i>ОВ 7,8 · КР 10</i>`, внизу — скільки предметів без оцінок; відповідь ріжеться `audit.SplitMessage`;
  - огляд: `📅 Тиждень N (дд.мм–дд.мм)`, по предмету `пройдено X/Y`, оцінки тижня, середнє; предмети без відстежуваних елементів у тижні й без оцінок не показуються; надсилається через `NotifyHTML`.
- Клієнт не логує `username`, `fullname`, токен.

## What Goes Where
- **Implementation Steps** — код, тести, документація в цьому репо.
- **Post-Completion** — секрети й env у dotfiles, деплой, перевірка в проді.

## Implementation Steps

### Task 1: Moodle client

**Files:**
- Create: `internal/moodle/client.go`
- Create: `internal/moodle/client_test.go`
- Create: `internal/moodle/testdata/*.json`

- [x] `NewClient(baseURL)` з `http.Client{Timeout: 30s}`; `Login(ctx, user, pass)` через `/login/token.php`; помилка містить `errorcode`, але не логін/пароль
- [x] внутрішній `call(ctx, fn, params, out)`: усе в POST-тілі; `{"exception",...}` → типізована помилка
- [x] методи: `SiteInfo`, `Courses(userid)`, `CourseAverages(userid)`, `GradeItems(courseid,userid)`, `Contents(courseid)`, `Completion(courseid,userid)` з мінімальними структурами
- [x] тести (httptest + вигадані фікстури): успішний логін, неправильний пароль, розбір кожного методу, токен ніколи не в URL
- [x] тести помилок: HTTP 5xx, битий JSON, exception-відповідь
- [x] `go test ./internal/moodle/...` — зелено

### Task 2: Storage for courses and grades

**Files:**
- Create: `internal/db/migrations/0017_school_moodle.sql`
- Create: `internal/model/school_moodle.go`
- Create: `internal/store/school_moodle.go`
- Create: `internal/store/school_moodle_test.go`
- Modify: `internal/db/migrations_test.go`

- [x] міграція `school_courses`, `school_grades` за конвенціями 0016
- [x] `model.SchoolCourse`, `model.SchoolGrade` (+ `ShortItemName`, `FormatGrade`, `CourseName` зі зрізанням хвостового ` \d+`)
- [x] `store.ApplySchoolSync(courses, grades, now) (fresh []SchoolGrade, err)` в одній транзакції: свіже лише для вже відомих курсів; нові курси засіваються мовчки; курси, яких немає в синку, видаляються з оцінками
- [x] `store.SchoolSummary()` (у порядку `position`) і `store.SchoolGradesBetween(from,to)`
- [x] тести: перший синк = мовчки; порожній перший синк, потім одна оцінка → вона свіжа; новий курс посеред року — мовчки; змінена оцінка свіжа; повтор без змін — нічого; зниклий курс видаляється
- [x] тест міграції
- [x] `go test ./internal/store/... ./internal/db/...` — зелено

### Task 3: Sync service

**Files:**
- Create: `internal/moodle/sync.go`
- Create: `internal/moodle/sync_test.go`

- [x] `Config{Username,Password,Interval,Week0}`, `Service` (store, client, loc, logger, now, м'ютекс навколо клієнта, `SetNotify(func([]model.SchoolGrade))`)
- [x] `SyncOnce(ctx)`: логін → site info → курси + середні → grade items усіх курсів (фільтри з Technical Details) → `ApplySchoolSync` → notify, якщо є свіжі й notify задано. Якщо впав хоча б один курс — синк не застосовується (щоб не видалити курс через тимчасову помилку), лише лог
- [x] `Run(ctx)`: одразу синк, далі по інтервалу; пропуск 22:00–07:00; помилки логуються
- [x] `Week(t)` — номер тижня від `Week0` і межі Пн–Нд
- [x] тести на httptest: фільтр null/`course`/`category`, `graded_at` fallback, нічний пропуск, падіння курсу не застосовує синк, `Week` на межах і до якоря
- [x] `go test ./internal/moodle/...` — зелено

### Task 4: Week progress

**Files:**
- Modify: `internal/moodle/sync.go`
- Create: `internal/moodle/progress_test.go`

- [x] `Service.WeekProgress(ctx, week)` → по курсу: X/Y (Technical Details), оцінки тижня, середнє, ознака «секції немає»
- [x] тести: label/невідстежувані не рахуються, state 3 не пройдено, збіг за префіксом, секції немає
- [x] `go test ./internal/moodle/...` — зелено

### Task 5: Message formatting

**Files:**
- Create: `internal/bot/school_moodle.go`
- Create: `internal/bot/school_moodle_test.go`

- [x] `formatNewGrades`, `formatGradesSummary`, `formatWeekReview`; HTML-екранування назв
- [x] тести: формати з Technical Details, ⚠️ < 4, «,0» прибирається, порожні стани, кожен рядок має збалансовані теги
- [x] `go test ./internal/bot/...` — зелено

### Task 6: /grades command and new-grade notifications

**Files:**
- Modify: `internal/bot/bot.go`
- Modify: `internal/bot/handlers.go`
- Modify: `internal/bot/school_moodle.go`
- Modify: `internal/bot/school_moodle_test.go`

- [x] `Config.Moodle *moodle.Service` (nil = вимкнено); `/grades` реєструється лише з ним; додати в `SetCommands`, `cmdHelp`, `cmdStart` (умовно)
- [x] `/grades` читає зі store, розрізає відповідь `audit.SplitMessage`
- [x] `Bot.NotifyNewGrades(grades)` через `NotifyHTML` (callback для синку)
- [x] тести: `/grades` з даними/без даних, довга відповідь розрізається, без `NotifyChat` нічого не шле
- [x] `go test ./internal/bot/...` — зелено

### Task 7: Friday week review clock

**Files:**
- Modify: `internal/bot/digests.go`
- Modify: `internal/bot/digests_test.go`
- Modify: `internal/bot/school_moodle.go`

- [x] годинник `schoolReview` у `due`/`lastFired`/`dueThisMinute`/`anyDigestEnabled` (DOW+TIME+Week0 **і** `Config.Moodle != nil`), у стартовому лозі й тексті «disabled»
- [x] `sendSchoolReview` — горутина з `atomic.Bool` і `context.WithTimeout` (як `sendDishSuggestions`), `WeekProgress` поточного тижня → `formatWeekReview` → `NotifyHTML`
- [x] тести: спрацьовує один раз у свою хвилину, вимкнений без сервісу/якоря, `anyDigestEnabled` true лише з ним, `countDue` враховує нове поле, повторний запуск під час виконання пропускається
- [x] `go test ./internal/bot/...` — зелено

### Task 8: Wiring in cmd/server

**Files:**
- Modify: `cmd/server/main.go`
- Modify: `cmd/server/main_test.go`
- Modify: `.env.example`

- [x] `withSchoolMoodleEnv(getenv)` → конфіг сервісу (nil без URL/логіна/пароля), DOW/TIME у `bot.Config`
- [x] сервіс створюється до блоку бота й передається як `Config.Moodle`; після `bot.New` — `svc.SetNotify(b.NotifyNewGrades)`; `go svc.Run(ctx)` лише після цього (і без токена бота — з nil notify)
- [x] `.env.example` — блок `SCHOOL_MOODLE_*` з поясненнями, без реальної адреси
- [x] тести розбору env: дефолт інтервалу, вимкнено без URL, огляд вимкнено без WEEK0/DOW, кривий WEEK0
- [x] `go build ./... && go test ./...` — зелено

### Task 9: Verify acceptance criteria
- [ ] нові оцінки, `/grades`, огляд реалізовані; seed-кейси покриті
- [ ] живий прогін у scratchpad сесії (не в репо): харнесс робить `SyncOnce` у тимчасову БД і друкує `/grades` та огляд у stdout, облікові дані з env
- [ ] `go vet ./... && go test ./...`
- [ ] `git grep` по диффу: немає імені, адреси школи, email, userid; `localharness/` не в коміті

### Task 10: [Final] Update documentation
- [ ] `ARCHITECTURE.md` — розділ про Moodle (токен мобільного API, логін на синк, seed по курсу, якір тижня, нічна пауза, втрата сповіщення при збої Telegram)
- [ ] `DEPLOY.md`/`README.md` — env і секрети
- [ ] перенести план у `docs/plans/completed/`

## Post-Completion
**External system updates:**
- dotfiles: у `host_vars/hetzner/secrets.sops.yaml` — `family_hub_school_moodle_url`, `_username`, `_password`; у `roles/family-hub/tasks/main.yml` env `SCHOOL_MOODLE_*`, `SCHOOL_MOODLE_WEEK0: "2026-08-31"`, `SCHOOL_MOODLE_REVIEW_DOW: "5"`, `SCHOOL_MOODLE_REVIEW_TIME: "19:45"`
- деплой: PR → merge → `docker build/push` → `just deploy-hetzner-tag family-hub`

**Manual verification:**
- у логах — засів курсів без повідомлення; `/grades` у групі; при наступній новій оцінці — одне повідомлення
- після канікул звірити номер тижня з назвою секції; якщо огляд пише «секції немає», підправити `SCHOOL_MOODLE_WEEK0`
