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

Копии знака лежат у обеих морд, потому что отдаются они по-разному: `/static/`
веб-интерфейса сидит за oauth, а mini app обязан грузиться без него, так что
поделить один каталог нельзя.

| Куда | Файлы | Откуда взято |
| --- | --- | --- |
| `internal/web/static/` | `favicon.svg`, `favicon-32.png`, `apple-touch-icon.png` | `icon-small.svg` → `favicon.svg` |
| `internal/mini/static/` | `icon.svg`, `favicon-32.png`, `apple-touch-icon.png` | то же, `icon.svg` — упрощённый знак |

Теги прописаны в `internal/web/templates/base.html` и
`internal/mini/static/index.html`.

`/favicon.ico` в корне намеренно не отдаётся: корневой роутер за `auth-chain`,
браузер получит на него 401. Иконка приходит из тега, а не из корня.

Манифест, если понадобится PWA (пока не подключён):

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

`banner.png` стоит первой строкой README. Растр, а не `banner.svg`, потому что
в баннере настоящий `<text>` с системным шрифтовым стеком: у каждого читателя
он разрешится в свой шрифт. Для логотипа предсказуемость дороже вектора,
1280×640 и так хватает на retina.

`banner.png` — в Settings → General → Social preview: это картинка, которую
видно, когда ссылку на репозиторий вставляют в чат. Через API её не поставить,
только руками.
