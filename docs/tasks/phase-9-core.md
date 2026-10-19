# Задача воркера: ядро фазы 9 (движки и нативные рецепты)

Читай `docs/tasks/phase-9-engines-native.md` — это спецификация. Ниже только твой кусок.

## Файлы, которые ты создаёшь

### `internal/engines/engines.go`

Один движок, зашитый в код (не реестр, не yaml):

```go
const TensorFold = "tensorfold"

type Engine struct { Name, Repo, AssetPattern, Binary string }  // AssetPattern: "tensorfold-%s-linux-aarch64.tar.gz"

type Status struct {
    Name      string `json:"name"`
    Installed bool   `json:"installed"`
    Version   string `json:"version,omitempty"`
    Latest    string `json:"latest,omitempty"`
    Path      string `json:"path,omitempty"`
}
```

`type Manager struct { Home string; HTTP *http.Client; GitHubAPI string }` — `Home` из
`cfg.EngineHome`, `GitHubAPI` по умолчанию `https://api.github.com` (в тестах подменяется на
`httptest`).

Методы:
- `List(ctx) ([]Status, error)` — статус всех движков; `Latest` берётся из
  `GET {api}/repos/ashhart/TensorFold/releases/latest` (`tag_name` без `v`). Ошибка сети или
  404 — не ошибка вызова: `Latest` пустой, кэш 5 минут на процесс.
- `Install(ctx, name, version) (Status, error)` — `version == ""` значит последнюю. Скачивание
  `https://github.com/ashhart/TensorFold/releases/download/v<v>/<asset>` и `...sha256`, проверка
  суммы по первой строке файла, распаковка tar.gz во временный каталог под `Home`, перенос
  `tensorfold-<v>-linux-aarch64/` в `Home/tensorfold/<v>/`, переустановка симлинка
  `Home/tensorfold/current` атомарно (создать `current.tmp`, `os.Rename`), удаление прошлых
  версий. Уже установленная та же версия — `ErrAlreadyInstalled`.
- `Delete(name) error` — `os.RemoveAll(Home/<name>)`.
- `Path(name) (string, error)` — путь к `current/bin/tensorfold-native`, `ErrNotInstalled` если нет.
- `DetectArch()` — `linux-aarch64` для `arm64`, `linux-x86_64` для `amd64`, иначе ошибка.

Ошибки — экспортируемые переменные (`ErrNotInstalled`, `ErrAlreadyInstalled`, `ErrUnknownEngine`),
чтобы API отвечал 404/409 без разбора строк.

### `internal/native/native.go`

```go
type Runner struct { LogDir string; UnitPrefix string; LookPath func(string)(string,error); Run func(ctx, args...) (string,int,error) }
```

`Run` по умолчанию запускает `systemctl --user` (и `systemd-run --user` для старта) через
`exec.CommandContext`, возвращает вывод и код — как `runDocker` в `internal/proxy`.

- `ResolveModel(spec string) (string, error)` — `hf:` подстановка (см. спецификацию), `~`, обычный путь.
- `Start(ctx context.Context, slug string, spec Spec) error`, где
  `Spec{EnginePath, Model, Args []string, Env map[string]string, Port int, ServedName, LogPath string}`.
  Собирает аргументы: `serve <model>` + `Args` + `--port <port>` + `--name <ServedName>`, если их
  ещё нет в `Args`. Юнит `gospark-native-<slug>`; если юнит уже активен — `ErrAlreadyRunning`.
  Флаги `systemd-run`: `--user --unit=... --collect --quiet`, `StandardOutput/StandardError` в
  `append:<LogPath>`, каждый env через `--setenv=K=V`.
- `Stop(ctx, slug) error` — `systemctl --user stop <unit>`; неактивный юнит — не ошибка.
- `Running(ctx, slug) (bool, error)` — `systemctl --user is-active` → 0 = да.

Тесты: подменяй `Run` и проверяй собранные аргументы (юнит, env, `--port`), `ResolveModel` на
временном HF-кэше (`HF_HUB_CACHE` в t.Setenv).

### Рецепты

- `internal/registry/models.go`: `Kind string \`json:"kind,omitempty"\`` и
  `Native *RecipeNative \`json:"native,omitempty"\``;
  `type RecipeNative struct { Engine string \`json:"engine"\`; Model string \`json:"model"\`; Args []string \`json:"args,omitempty"\`; Env map[string]string \`json:"env,omitempty"\`; Port int \`json:"port,omitempty"\` }`.
- `parse.go`: `Kind` из `kind` (пусто → `container`), `Native` из блока `native` (нет блока → nil;
  `port` из `ui.port`, если не задан).
- Метод `(Recipe) IsNative() bool { return r.Kind == "native" }`.
- Контейнерные рецепты в JSON не должны получить ни `kind`, ни `native` — паритет с апстримом.

### API движков: `internal/api/engines/register.go`

Регистрация как у `internal/api/registries` (`Register(mux, Deps)`, `Deps{Engines *engines.Manager, Auth, Native *native.Runner, Reg *registry.Cache}`).
Только админ (иначе 403), без сессии 401. Маршруты из спецификации.

- `DELETE` и `update` проверяют, что ни один нативный рецепт движка не запущен (`Native.Running`);
  запущен → 409 `{"detail":"A native recipe is running on this engine"}`.
- `install` при `ErrAlreadyInstalled` → 409 `{"detail":"Engine tensorfold 1.1.1 is already installed"}`.
- `update` при той же версии → 200 `{"name","version","changed":false}`.

### Запуск нативных рецептов

В `internal/api/containers/register.go` (там же, где контейнерные launch/stop):

- `Deps` получает `Engines *engines.Manager` и `Native *native.Runner`.
- `install`: если `rec.IsNative()` → 200 `{"slug":slug,"status":"installed"}` без докера.
- `launch`: если `rec.IsNative()` → проверить движок (`Engines.Path`), собрать `native.Spec`
  (лог `<cfg.DataDir>/logs/<slug>.log`, `ServedName` = `rec.UI`/`rec.Name`), вызвать
  `Native.Start`, ответ `{"slug","status":"launched"}`. Ошибки: движок не стоит → 409,
  уже запущен → 409.
- `stop`: нативный → `Native.Stop`, `{"slug","status":"stopped"}`.
- `remove`/`purge` для нативного: остановить процесс и ответить `{"status":"removed"}` (образов нет).
- Runtime-флаги (`applyRuntime`) для нативного рецепта: `installed` = движок установлен,
  `running`/`ready` = `Native.Running`, `starting` = false, `error` = пусто. Поменяй
  `recipes.Deps`, если понадобится ещё одна зависимость.

### Прокси

`internal/proxy/render.go`: у нативного рецепта апстрим `host.docker.internal:<native.port>`
вместо `<container>:<ui.port>`. Тест на рендер Caddyfile с нативным рецептом.

### Конфиг и сборка

- `internal/config/config.go`: `EngineHome` (`ENGINE_HOME`, по умолчанию `~/.config/gospark/engines`).
- `cmd/sahd/main.go`: создать `engines.Manager` и `native.Runner`, передать в API.
- `go build ./... && go vet ./... && go test ./... -count=1` — всё зелёное, `staticcheck` чистый.

## Чего не делать

- Не трогать `frontend/` — это второй воркер.
- Не менять поведение контейнерных рецептов, схему SQLite, паритетные пути.
- Не добавлять новых зависимостей: `net/http`, `archive/tar`, `compress/gzip`, `crypto/sha256` хватает.
- Не писать миграций и не читать секреты.

## Готово, когда

`go test ./...` зелёный, есть тесты на: разбор `kind`/`native`, `ResolveModel`, аргументы
`systemd-run`, установку движка с поддельного GitHub (httptest + локальный tar.gz), 409 при
запущенном рецепте, рендер Caddyfile для нативного рецепта. Один коммит, сообщение по-русски.
