# Брендинг

Знак «Дах над днем». Один зелёный `#127a68`, крыша белая, три полосы —
день семьи. Мелкий кегль (16–32 px) использует упрощённую версию: две полосы
вместо трёх, штрих толще.

## Файлы

| Файл | Куда |
| --- | --- |
| `telegram-512.png` | аватар бота в BotFather |
| `whatcanido.png` | 640×360, блок «What can this bot do?» в BotFather |
| `favicon.ico` | корень сайта: `/favicon.ico` (16 + 32 в одном файле) |
| `favicon-32.png`, `favicon-16.png` | то же по отдельности |
| `icon-small.svg` | favicon в SVG, упрощённый |
| `apple-touch-icon.png` | 180×180, iOS «на экран Домой» |
| `icon-192.png`, `icon-512.png` | манифест PWA |
| `icon-maskable-512.png` | манифест, `purpose: maskable` — знак внутри safe zone |
| `banner.png` | шапка README и Social preview репозитория |
| `icon.svg`, `icon-maskable.svg`, `banner.svg`, `whatcanido.svg` | исходники, из них пересобирается всё остальное |

## Telegram

BotFather → `/setuserpic` → отправить `telegram-512.png` файлом, не сжатым
фото. Аватар обрезается в круг: у знака поля рассчитаны под это.

Иконку кнопки mini app там же — `/setmenubutton`.

Картинка «What can this bot do?» — там же, файл `whatcanido.png`, ровно
640×360 (Telegram другие размеры не примет). Имени бота на ней нет намеренно:
знак и три строки о деле переживут переименование, а подпись пришлось бы
перерисовывать. Текст в SVG — настоящий `<text>`, так что рендерить нужно тем,
что умеет шрифты: скриншот headless-браузера с вьюпортом 640×360, не
`convert` из ImageMagick.

## Web

Иконки кладутся в `internal/mini/static/`, отдаются тем же файловым сервером.
В `index.html`:

```html
<link rel="icon" href="/favicon.ico" sizes="32x32">
<link rel="icon" href="/mini/assets/icon-small.svg" type="image/svg+xml">
<link rel="apple-touch-icon" href="/mini/assets/apple-touch-icon.png">
```

`/favicon.ico` браузер просит в корне сам, без тега — если сервер отдаёт
статику только под `/mini/`, добавить отдельный маршрут на этот файл.

Манифест, если нужен PWA:

```json
{
  "name": "family-hub",
  "short_name": "Родина",
  "background_color": "#f2f3f7",
  "theme_color": "#127a68",
  "icons": [
    { "src": "/mini/assets/icon-192.png", "sizes": "192x192", "type": "image/png" },
    { "src": "/mini/assets/icon-512.png", "sizes": "512x512", "type": "image/png" },
    { "src": "/mini/assets/icon-maskable-512.png", "sizes": "512x512", "type": "image/png", "purpose": "maskable" }
  ]
}
```

## GitHub

`banner.png` в репозиторий, первой строкой README:

```markdown
![family-hub](brand/banner.png)
```

Он же в Settings → General → Social preview: это картинка, которую видно,
когда ссылку на репозиторий вставляют в чат.
