# Фаза 2: реестры-источники и локальные рецепты

Статус: решения приняты, реализация идёт воркерами `phase-2b-core`, `phase-2b-api`, `phase-2b-ui`.

## 1. Требования (финальные)

1. В настройках — форма: ссылки на папки с рецептами в GitHub, несколько через
   запятую (или с новой строки), можно с подпапкой внутри репозитория.
2. Источник по умолчанию — `https://github.com/WaxacaBytes/spark-ai-hub`,
   подпапка `registry/recipes`. Свой репозиторий рецептов не хранит: раньше он их
   копировал, теперь они приходят только из источника.
3. **Единственный источник истины на диске — `~/.config/gospark/registry`.**
   Демон читает рецепты только оттуда, никаких фолбэков на `repo/registry`.
4. Обновление — только по кнопке, синхронизации на старте нет.
5. Правка рецепта из реестра работает как сейчас: правится файл в
   синхронизированной папке, следующая синхронизация его перезапишет.
6. Свой рецепт создаётся явной кнопкой «Создать копию»: копия внешнего рецепта
   с новым слагом в `local/`.
7. Локальный рецепт удаляется кнопкой **с окном подтверждения**.

## 2. Раскладка на диске

```
~/.config/gospark/registry/
  sources.yaml                 # список источников
  vendored/<id>/               # git-клон, sparse: только папка рецептов
    registry/recipes/<slug>/…
    registry/covers/…
  local/<slug>/                # свои рецепты (только через «Создать копию»)
```

```yaml
sources:
  - id: 3f9a1c02                # sha1(url + "#" + subpath)[:8]
    url: https://github.com/WaxacaBytes/spark-ai-hub
    ref: main
    subpath: registry/recipes
    covers: registry/covers     # выводится из subpath, если папка существует
    commit: 4d1c9ab
    synced_at: 2026-10-19T15:20:00Z
    error: ""
```

Слияние: `vendored` всех источников в порядке добавления, затем `local`.
Слаг уникален: копия с занятым слагом → 409. Коллизия двух источников с одним
слагом — побеждает более поздний, в лог `[registry] slug X from <id> shadows <id>`.

## 3. Демон

### 3.1 `internal/registry/source.go` (новый)

```go
type Source struct {
    ID, URL, Ref, Subpath, Covers, Commit string
    SyncedAt time.Time
    Error    string
}

func NewManager(home string) (*Manager, error) // создаёт каталоги, читает sources.yaml,
                                               // при пустом файле пишет источник по умолчанию
func (m *Manager) Home() string
func (m *Manager) List() []Source
func (m *Manager) Add(raw string) ([]Source, []error) // запятые/переводы строк + Sync
func (m *Manager) Remove(id string) error
func (m *Manager) Sync(id string) error
func (m *Manager) SyncAll() []error
func (m *Manager) Roots() []string        // vendored/*/<subpath>, затем local/
func (m *Manager) CoverRoots() []string
func (m *Manager) LocalDir(slug string) string
func (m *Manager) Fork(srcSlug, newSlug string) (string, error)
func (m *Manager) DeleteLocal(slug string) error
```

Синхронизация — `git`:

```
git clone --depth 1 --filter=blob:none --sparse --branch <ref> <url> vendored/<id>
git -C vendored/<id> sparse-checkout set <subpath> [<covers>]
git -C vendored/<id> rev-parse --short HEAD
# обновление
git -C vendored/<id> fetch --depth 1 origin <ref> && git -C vendored/<id> reset --hard FETCH_HEAD
```

Таймаут 120 с; ненулевой код git → текст в `Source.Error`, остальные источники живы.

Парсер URL: `github.com/o/r`, `…/tree/<ref>/<sub>`, `…/o/r/<sub>`, `.git`,
`git@github.com:o/r.git`. Хосты: github.com, gitlab.com, codeberg.org; localhost и
приватные IP отклоняются (форма не должна быть SSRF-дыркой).

### 3.2 `cache.go` — несколько корней

`NewCache(roots ...string)`; загрузка по корням по порядку, `recipes[slug]`
перезаписывается → приоритет из §2 получается сам. `maxModTime` обходит все корни.

```go
func (c *Cache) Origin(slug string) (root, dir string, ok bool)
func (c *Cache) IsLocal(slug string) bool
```

`Origin` нужен `defaultComposeContent` (сейчас корень считается как `dirname^4`,
с внешними папками это врёт) и API.

### 3.3 `internal/config/config.go`

```go
RegistryHome string   // REGISTRY_HOME, default ~/.config/gospark/registry
```

`RegistryPath` остаётся только для тестов, в `main.go` в кэш не попадает.

### 3.4 `cmd/sahd/main.go`

```go
mgr, err := registry.NewManager(cfg.RegistryHome)   // без сети
reg := registry.NewCache(mgr.Roots()...)           // может быть ноль корней
```

Плюс лечится существующий баг: `internal/api/recipes/register.go` создаёт свой
`NewCache` вместо переданного — кэшей сейчас два. `recipes.Deps` получает
`Cache *registry.Cache`, как уже сделано для `dockerx`.

### 3.5 `internal/api/registries` (новый пакет, только админ)

| Метод | Путь | Что делает |
|---|---|---|
| GET | `/api/registries` | источники + `recipes` в каждом, `local_count`, `syncing` |
| POST | `/api/registries` | `{"urls":"a, b"}` → добавить и синхронизировать; ответ — список и ошибки по строкам |
| POST | `/api/registries/sync` | синхронизировать все |
| POST | `/api/registries/{id}/sync` | синхронизировать один |
| DELETE | `/api/registries/{id}` | удалить источник (vendored-папка; `local/` не трогается) |
| GET | `/api/local-recipes` | локальные: слаг, откуда скопирован, mtime |
| POST | `/api/recipes/{slug}/fork` | `{"slug":"my-app"}` → копия в `local/my-app`, 409 если занят |
| DELETE | `/api/recipes/{slug}/local` | удалить локальный рецепт; 409 если установлен/запущен |

### 3.6 Правка внешнего — как сейчас

Никакого автофорка. `PUT /api/recipes/{slug}/compose` правит файл там, где рецепт
лежит. Если он из `vendored/`, следующая синхронизация вернёт версию источника —
это ожидаемое поведение, в UI предупреждение в форме правки.

Свой рецепт: `POST /api/recipes/{slug}/fork` копирует папку в `local/<newSlug>`,
дальше правится свободно и синхронизацией не затрагивается.

## 4. Удаление локального рецепта

Кнопка «Удалить» только у локальных, всегда через модальное окно: название
рецепта, текст «Файлы рецепта будут удалены из ~/.config/gospark/registry/local/<slug>»,
красная кнопка «Удалить», фокус по умолчанию на «Отмена».

`DELETE /api/recipes/{slug}/local` → удаляет каталог, `cache.Load()`, ответ
`{"deleted": true}`. Если рецепт установлен или запущен — 409 «сначала остановите
и удалите приложение», каталог не трогается.

## 5. Фронтенд

- `frontend/src/pages/Settings.jsx` (новый): textarea со ссылками, список
  источников (URL, ветка, коммит, «синхронизировано N назад», ошибка, кнопки
  Sync/Remove), «Синхронизировать все», блок «Локальные рецепты» с удалением.
- `App.jsx`: пункт навигации `Settings` для админа.
- `RecipeDetail.jsx`: бейдж «Локальный» / «Из реестра», кнопка «Создать копию»
  (спрашивает новый слаг), кнопка «Удалить» для локальных с подтверждением.
- `store.js`/`auth.js`: вызовы новых эндпоинтов.
- Обложки: `/cover-sources/` отдаёт из `local`, затем из `vendored/*/covers`.

## 6. Тесты

- парсер URL: 5 форм + мусор + запрещённый хост;
- приоритет: один слаг в vendored и local → побеждает local, `Dir()` туда;
- fork: копия появилась, оригинал не изменился (хеши), занятый слаг → ошибка;
- sync: локальный `git init` во temp (без сети) → clone, sparse-подпапка, commit,
  повторный sync после нового коммита;
- API: 403 не-админу, 400 на плохой URL, 409 на занятый слаг, 404 на неизвестный id;
- удаление: 409 если установлен, каталог на месте; после удаления каталога нет.

## 7. Приёмка

1. Пустой `~/.config/gospark/registry` → старт без сети, 0 рецептов, ошибок нет.
2. Добавить источник через API → 148 рецептов в `vendored/<id>`.
3. «Создать копию» searxng → `local/my-searxng`, правка порта, запуск из UI.
4. Удалить локальную копию через окно подтверждения → исчезла из каталога.
5. `go test ./... -count=1`, parity `19/19`, `-sweep 42/49`.

## 8. Порядок работ

1. `phase-2b-core` — менеджер источников, корни в кэше, конфиг, проводка в main.
2. `phase-2b-api` — пакет `internal/api/registries`, форк/удаление, обложки из
   нескольких корней (стартует после слияния core).
3. `phase-2b-ui` — Settings, бейджи, копия, удаление с подтверждением.
4. Живая проверка §7, ACCEPTANCE, parity, коммит.

## 9. Миграция репозитория

Репозиторий gospark-hub теперь имеет remote `https://github.com/stepinus/gospark-hub`
(private). После того как демон читает рецепты из `~/.config`, каталоги
`registry/recipes` и `registry/covers` из репозитория удаляются — они приходят из
источника. Перед удалением `internal/parity/list_test.go` (грузит все
`registry/recipes/*/recipe.yaml`) переводится на путь из `REGISTRY_HOME` с пропуском,
если каталога нет.
