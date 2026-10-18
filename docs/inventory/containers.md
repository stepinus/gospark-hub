# Контейнеры, прокси и мониторинг — контракт реализации

Источник: `~/spark-ai-hub` @ `6eb4372`. Эталон HTTP-схемы: `docs/contract/openapi.json`.
Всё, чего в openapi нет (WebSocket-фрейминг, побочные эффекты, файлы на диске), описано здесь
по коду апстрима.

Покрытие: 17 HTTP-эндпоинтов + 3 WebSocket. Файлы: `daemon/routers/containers.py`,
`daemon/routers/system.py`, `daemon/services/docker_service.py`, `proxy_service.py`,
`monitor_service.py`, `connect_service.py`, `decision_service.py`, `registry_service.py`,
`data/Caddyfile`, фронтенд `RecipeDetail.jsx`, `Running.jsx`, `System.jsx`, `BuildLog.jsx`,
`RunningTray.jsx`, `SystemBar.jsx`, `hooks/useMetrics.js`, `store.js`, `LaunchConflictModal.jsx`.

---

## 0. Инварианты, которые нельзя «улучшать»

1. **Один публичный порт для проксируемых.** Демон слушает `settings.port = 9010`
   (`daemon/config.py:9`, `run.sh:20`), Caddy — `public_port = 9000` (`daemon/config.py:16`,
   `run.sh:15-18`). Из 148 рецептов 141 объявляют `ui.proxy: true` (17 `web` + 124
   `api-only`) и **не публикуют host-портов**: в их compose-файлах слова `ports:` нет,
   они живут в общей docker-сети (`registry/recipes/acestep/docker-compose.yml`, блок
   `networks: spark-ai-hub-net: external: true`). Оставшиеся 7 (`onyx`, `localai`,
   `flowise`, `comfyui-spark`, `langflow`, `ollama-openwebui`, `anythingllm`) порты
   публикуют и открываются как `host:port` — фронтенд различает их по пустому `app_url`
   (`frontend/src/lib/urls.js:34-37`, `Running.jsx:156-162`).
2. **Идентичность контейнера** = compose-проект `spark-ai-hub-{slug}`
   (`docker_service.py:423-424`, `:427-432`) + `container_name:` из самого compose-файла
   (`proxy_service.py:110-127`; имя берётся парсингом строки с отступом ровно 4 пробела,
   `:125`, фолбэк `spark-ai-hub-{slug}` `:127`).
3. **Состояние действий — in-memory.** `_ready_cache`, `_health_tasks`, `_pending_actions`,
   `_startup_errors`, `_last_failure_logs` (`docker_service.py:24-34`) и `_builds`
   (`containers.py:41`) не переживают рестарт демона. В БД живёт только факт установки
   (`installed_recipes`, `docker_service.py:656-664`).
4. **Порядок Caddy-правил** в сгенерированном конфиге обязателен: probe → 308-redirect →
   guarded `handle_path` → fallback (`proxy_service.py:173-222`).

---

## 1. Артефакты на диске

| Путь | Кто пишет | Смысл |
|---|---|---|
| `registry/recipes/{slug}/docker-compose.yml` | репозиторий; **перезаписывается** `PUT /api/recipes/{slug}/compose` (`containers.py:264-268`) | единственный источник истины для образа, `container_name`, `build:`; build-контекст = сам каталог рецепта (`docker_service.py:703-709`) |
| `registry/recipes/{slug}/.env.example` → `.env` | `ensure_runtime_env` (`docker_service.py:469-480`) | генерируется один раз, `chmod 600` (`:479`); заготовки только для `USER_AUTH_SECRET`, `POSTGRES_PASSWORD`, `OPENSEARCH_ADMIN_PASSWORD`, `MINIO_ROOT_PASSWORD`, `S3_AWS_SECRET_ACCESS_KEY` (`:443-453`); `MINIO_ROOT_PASSWORD` и `S3_AWS_SECRET_ACCESS_KEY` — один и тот же секрет (`:444-452`) |
| `data/Caddyfile` | `write_caddyfile` (`proxy_service.py:225-232`) | генерируется целиком, 2975 строк на 141 проксируемый рецепт в текущем registry; bind-mount внутрь Caddy read-only (`:286`) |
| `data/proxy-probe-token` | `probe_token()` (`proxy_service.py:81-97`) | 32 байта `token_urlsafe`, `chmod 600`, читается при каждом рендере (`:171`) |
| `data/hf-token` | `write_token` (`daemon/services/hf_token.py:37-40`) | `chmod 600`; при чтении фолбэк на `~/.cache/huggingface/token` (`:29-35`) |
| `data/spark-ai-hub.db` | sqlite, таблица `installed_recipes(slug, status, compose_project)` (`docker_service.py:658-660`) | «установлено» |

Отдельного «build-каталога» нет: Dockerfile-ов в registry практически нет (один,
`registry/recipes/minimax-music3/runtime/Dockerfile`), 130 рецептов собираются из
`dockerfile_inline` в compose (`registry/recipes/vllm-glm53-flash-exl3-k2/docker-compose.yml`,
блок `build:`), и BuildKit-секрет `hf_token` пробрасывается из env `HF_TOKEN`
(`secrets: hf_token: environment: HF_TOKEN` там же).

---

## 2. HTTP-эндпоинты

Все под `/api` проходят `AuthMiddleware` (`daemon/middleware/auth.py:39,56-59`): нужна
cookie-сессия; API-ключ принимается только на `/v1`, `/mcp` и **ровно** на
`/api/system/connect` (`:48-53`). Значит `/api/system/metrics`, `/api/containers`,
`/api/system/hf-token` с `sah-…` ключом дают 401 `{"detail": "Authentication required."}`
(`:139-161`, заголовок `WWW-Authenticate: Bearer realm="Spark AI Hub"` — `:182`).
WebSocket без сессии закрывается на handshake кодом 1008 (`:142-145`).

### 2.1 `POST /api/recipes/{slug}/install` (`containers.py:44-70`)

* 404 `{"detail": "Recipe not found"}`, если слага нет в registry (`:46-48`).
* Guard: если `_builds[slug]` существует и не `done`, **или** `_pending_actions[slug] ==
  "installing"` — возврат `{"status":"building","slug":slug}` без побочных эффектов (`:50-52`).
  Второе условие специально ловит восстановленное состояние после рестарта UI.
* Иначе: `set_pending(slug,"installing")`, `clear_ready(slug)` (отменяет health-task,
  `docker_service.py:240-244`), `_builds[slug] = {"lines": [], "done": False}` (`:54-56`).
* Работа уходит в `asyncio.create_task` (`:58-69`); ответ **всегда** 200
  `{"status":"building","slug":...}` (`:70`) — до завершения сборки.
* Ошибка сборки не меняет HTTP-код: она дописывается в лог как `[error] {e}`
  (`:63-64`), флаг `done` ставится в `finally` (`:65-67`).

Тело install-генератора (`docker_service.py:607-671`):

1. нет каталога/`docker-compose.yml` → строка `[error] …` и выход (`:609-617`);
2. `ensure_runtime_env`; если `.env.example` есть, а `.env` создать не удалось → `[error]`
   (`:619-626`), иначе строка `[spark-ai-hub] Generated runtime config at …` (`:624`);
3. `build_recipe = recipe.docker.build` (`:627`); build → `docker compose … build`,
   иначе `docker compose … pull` (`:635-638`), затем строка `[spark-ai-hub] Running: <cmd>`
   (`:639`);
4. стриминг через `_stream_proc` (`:643`) с env `_launch_env(api_key, hub_url)` — то есть
   с `HF_TOKEN`, `SAH_API_KEY`, `SAH_HUB_URL` (`:786-814`);
5. ненулевой код → `[spark-ai-hub] Install failed with exit code {rc}` и **выход без записи
   в БД** (`:648-650`);
6. только для build: `docker builder prune -a -f` с логом `Build cache …` (`:652-654`,
   `:514-537`);
7. `INSERT OR REPLACE INTO installed_recipes … ('installed', 'spark-ai-hub-{slug}')`
   (`:656-664`);
8. всегда: prune «осиротевших» образов, лог `Pruning orphaned images... …`
   (`:666-670`, `:540-604`);
9. `[spark-ai-hub] {slug} installed successfully!` (`:671`).

Стриминг строк (`_stream_proc`, `:483-511`): чтение блоками 4096, CRLF→LF, сплит по `\n` **и**
`\r` (прогресс-бары tqdm отдаются сразу, `:497-505`), ANSI вырезается
`_ANSI_RE = \x1b\[[^A-Za-z]*[A-Za-z]` (`:12`, `:503`), пустые строки не отдаются; последним
элементом генератора идёт `("", returncode)` (`:511`).

### 2.2 `POST /api/recipes/{slug}/update` (`containers.py:73-106`)

* 404 как у install; `clear_ready(slug)` **до** проверки guard-а (`:78`).
* Guard только по `_builds` (`:80-81`) — `_pending_actions` не проверяется (см. риск R2).
* `set_pending(slug,"installing")` + новый `_builds[slug]` (`:85-86`), задача в фоне (`:88-105`).
* В `finally`: `done=True`, `clear_ready(slug)` (контейнер пересоздан и должен заново
  заработать ready, `:96-100`), `clear_pending`, и если `is_recipe_running(slug)` —
  `start_health_check(slug)` (`:101-103`).

Поведение по типу рецепта (`docker_service.py:674-783`):

* build: `docker compose up -d --build` (`:698`), построчный стрим, без `_stream_proc`
  (сырой `readline`, `:711-716`), при `rc==0` — prune build-cache + prune образов +
  `{slug} rebuilt successfully!` (`:720-726`), иначе `Rebuild failed with exit code N` (`:728`);
* pull: `compose pull` (`:733`) → при ошибке стоп (`:753-755`) → `compose up -d` (`:759`);
  при успехе prune образов + `{slug} updated successfully!` (`:779-781`).

Отличия от install: `ensure_runtime_env` вызывается так же (`:686-691`), но `INSERT` в
`installed_recipes` не делается — строка уже есть.

### 2.3 `GET /api/recipes/{slug}/build-status` (`containers.py:109-117`)

```
нет записи в _builds → {"status": "idle", "lines": []}
иначе               → {"status": "building"|"done", "lines": ["…", …]}
```

404 нет вообще: неизвестный slug тоже даёт `idle`. `lines` — накопительный массив строк
без обрезки (растёт на всю сборку).

### 2.4 `GET /api/recipes/{slug}/launch-plan` (`containers.py:120-145`)

404 при неизвестном slug (`:127`). Для не-LLM (`recipe.is_llm` — `category == "llm"` и
`ui.type == "api-only"`, `models/recipe.py:314-316`) всегда `{"stop": []}` (`:129`).

Для LLM:

* `need, free, running = memory_plan(slug)` (`:130`) — `memory_plan`
  (`docker_service.py:1065-1084`) считает `need = recipe.memory_gb`, `free =
  available_memory_gb() - loading`, где `loading` — сумма `memory_gb` запущенных, но ещё
  не готовых приложений (`:1078-1082`); `available_memory_gb()` читает `MemAvailable` из
  `/proc/meminfo` в GiB (`:1055-1062`). GPU-память отдельно не измеряется: на Spark она
  общая с CPU, а «сколько займёт» приходит из `--gpu-memory-utilization` /`
  --mem-fraction-static` в compose-команде (`registry_service.py:15,72-78`) и
  `memory_gb = max(requirements.min_memory_gb, reserved_gb)` (`models/recipe.py:309-311`).
* `running` фильтруется по `is_llm` и сортируется по возрастанию `memory_gb` (`:132`).
* если `free < need`: сначала ищется **один** минимальный по памяти LLM, которого хватит
  (`:136-139`), иначе — крупнейшие по одному, пока не наберётся (`:140-144`).
* ответ — `{"stop": [slug, …]}` (`:145`).

### 2.5 `POST /api/recipes/{slug}/launch` (`containers.py:148-184`)

Тело не принимается. Порядок эффектов:

1. 404 (`:157-158`);
2. `set_pending(slug,"launching")`, `clear_ready(slug)`, `clear_startup_error(slug)`
   (последнее чистит и `_last_failure_logs`, `docker_service.py:51-53`) — `:159-163`;
3. если рецепт build-типа и сборка не идёт — создаётся `_builds[slug]` и `on_line`
   зеркалит вывод `compose up` в тот же буфер, что видят `/ws/build` и `/ws/logs`
   (`:168-172`); по завершении `done=True` (`:176-178`);
4. `launch_recipe(...)` (`docker_service.py:817-879`): `ensure_network()`
   (`proxy_service.py:65-74`), `docker compose up -d` (`:832`), построчный стрим с
   `on_line`, `invalidate_docker_lists()` (`:858`), при `rc==0` — `INSERT OR REPLACE` в
   `installed_recipes`, а если вывод содержал `Built` или `DONE` — `_prune_build_cache()`
   (`:875-877`);
5. `"launched"` → `start_health_check(slug)` и `{"status":"launched","slug":slug}` (`:179-182`);
6. любой другой результат → `clear_pending` и **HTTP 500**, `detail` = сырой вывод
   `docker compose up` (или текст `Recipe directory not found for {slug}` /
   `Failed to prepare runtime env for {slug}`) — `:183-184`, `:819-825`.

### 2.6 `POST /api/recipes/{slug}/stop` (`containers.py:187-197`)

`set_pending("stopping")` → `clear_ready` → `clear_startup_error` → `stop_recipe` →
`clear_pending`. Ответ `{"status": "stopped"|"failed"|"<текст ошибки>", "slug": …}` (`:197`).
`stop_recipe` = `docker compose -p spark-ai-hub-{slug} -f … down` (`docker_service.py:887-896`):
удаляет контейнеры и сеть проекта; образы и именованные тома остаются. Код возврата
игнорируется, кроме выбора строки статуса.

### 2.7 `DELETE /api/recipes/{slug}` — remove (`containers.py:200-209`, `docker_service.py:899-959`)

1. `set_pending("removing")`, `clear_ready` (`:205-206`);
2. вычисляются «свои» образы: `own_images = _parse_compose_images(slug)`
   (`:1087-1104`, читает `image:` всех сервисов), `shared_images` — образы **других**
   установленных рецептов; удаляются только `own - shared` (`:910-916`). Причина —
   общий базовый образ (например `full-cuda13-*` у GGUF-рецептов), `:904-909`;
3. `docker compose down --volumes` (`:918`);
4. при `rc==0`: `docker rmi -f` по каждому `removable_images` (`:929-935`), `docker volume rm -f`
   по `_find_project_volumes(slug)` (`:936-942`, `:1107-1126`), `docker builder prune -a -f`
   (`:946`), `DELETE FROM installed_recipes` (`:947-952`), `_prune_orphaned_images()`
   (`:957`), ответ `"removed"`;
5. иначе `"failed"` и **строка в БД остаётся** (`:959`).

`_find_project_volumes` матчит префиксы `spark-ai-hub-{slug}_` и, если `base = slug без
последнего -сегмента` не является самостоятельным рецептом, ещё `spark-ai-hub-{base}_`
(`:1112-1118`) — защита от удаления тома соседнего рецепта. `.env` и каталог рецепта не
удаляются.

### 2.8 `POST /api/recipes/{slug}/purge` (`containers.py:212-218`, `docker_service.py:1170-1204`)

Отдельная кнопка «добить остатки»: `pending`/`ready` **не трогает** (`:217-218`).
Удаляет: все тома с префиксом проекта (`:1175-1186`), затем образы, чей `image:` есть в
compose **и** которые присутствуют в `docker images` **и** не используются ни одним
запущенным контейнером (`:1129-1154`), затем `_prune_orphaned_images()` (`:1203`).
`_prune_build_cache()` не вызывается (в отличие от remove). Ошибки `No such volume` /
`No such image` игнорируются (`:1185`, `:1198`); иначе ответ
`{"status": "partial: <stderr; …>"}`, при отсутствии ошибок — `"purged"` (`:1204`).

### 2.9 Compose: `GET` / `PUT` / `POST …/compose/reset` (`containers.py:221-279`)

* Хелпер `_compose_file_for_slug` (`:225-232`): 404 `Recipe not found`, если каталога нет;
  404 `docker-compose.yml not found`, если файла нет.
* `GET` (`:250-261`) → `{"content": <файл как есть>, "default_content": <git HEAD>}`.
  `default_content` берётся так: `repo_root = compose_file.parents[3]` (то есть корень
  репозитория для `registry/recipes/{slug}/docker-compose.yml`), `git show HEAD:<rel>`
  (`:235-247`); при любом сбое git — **фолбэк на текущее содержимое** (`:254-257`), не 404.
* `PUT` (`:264-268`), тело `{"content": str}` (схема `ComposeBody`, `:221-222`) —
  `write_text` без какой-либо валидации YAML, без перезапуска контейнера и без
  `ensure_proxy`; ответ `{"status":"saved"}`.
* `reset` (`:271-279`) — `git show` (уже без фолбэка: 404 `Default docker-compose.yml not
  available`), `write_text`, ответ `{"status":"reset","content": <записанное>}`.
* Round-trip: `GET.default_content` == то, что запишет `reset` только если рабочее дерево
  чисто по этому файлу; иначе `GET` покажет git-версию, `reset` вернёт именно её и
  UI-состояние `canReset` (`RecipeDetail.jsx:866-867`) станет ложным.
* UI-семантика: `dirty = content !== original`, `Restore Default` активен при
  `original !== defaultContent || content !== defaultContent` (`:866-867`), сохранение
  подтверждается через `setTimeout(…, 2000)` (`:837-838`).

### 2.10 `GET /api/containers` (`containers.py:282-284`, `docker_service.py:962-989`)

`response_model=list[ContainerInfo]`. Источник — `docker ps --filter
label=com.docker.compose.project --format '{{.Names}}\t{{.Status}}\t{{.Image}}\t{{.Ports}}'`
(`:964-969`); при исключении — пустой массив (`:971-972`). Поля: `name`, `status`
(человеческий, как в `docker ps`), `image`, `ports` (`:983-988`).

`_parse_ports` (`:992-1002`): строка режется по `", "`, только элементы с `->`;
`host_part.rsplit(":",1)[-1]` → int, ключ — порт контейнера без `/tcp`. То есть
`{"7860": 7860}`, а опубликованные без host-порта записи (`7860/tcp`) в словарь не попадают.
`cpu_percent` и `memory_mb` объявлены (`models/container.py:10-11`) и **всегда 0** — никто
их не заполняет.

---

## 3. WebSocket

### 3.1 `/ws/build/{slug}` (`containers.py:287-321`)

* `accept()` сразу (`:289`).
* Ожидание до 5 с (50 × 0.1 с) появления `slug` в `_builds` (`:292-296`).
* Если записи нет (рестарт демона, сборки не выживают) → одно сообщение `[done]` и `close`
  (`:314-319`).
* Иначе цикл: доотдать все новые строки как **отдельные text-сообщения** (`:305-308`),
  если `done` → `[done]` + `close` (`:309-312`), иначе `sleep(0.3)` (`:313`).
* Фрейминг: только `text`. Служебные маркеры — `[done]`; ошибки сборки приходят строкой
  `[error] …`, которую дописал router (`:63-64`).
* Разрыв/`RuntimeError` глушатся (`:320-321`).

### 3.2 `/ws/logs/{slug}` (`containers.py:324-453`)

Фаза 1 — ожидание контейнера:

* до 60 тиков по 1 с; на каждом — `get_any_container_name(slug)` = `docker ps -a
  --filter label=com.docker.compose.project=… --format {{.Names}}`, первый результат
  (`docker_service.py:60-81`) — «running **или** exited», чтобы лог умершего контейнера
  был доступен (`:339-344`);
* пока контейнера нет и идёт сборка (`_builds` не `done` или уже что-то отдано) — строки
  сборки уходят в этот же сокет, дедлайн сбрасывается на 60 (`:351-358`);
* иначе один раз `[spark-ai-hub] Waiting for container to start...` (`:359-361`);
* если за 60 с контейнера нет: `_last_failure_logs` (до 400 строк, сохранён при падении,
  `docker_service.py:352`) + `[spark-ai-hub:error] {err}`, либо
  `[spark-ai-hub] Container not running`, затем `close` (`:364-379`).

Фаза 2 — логи:

* дослать остаток сборки (`:384-389`);
* `await start_health_check(slug)` (`:392`);
* параллельная задача `_notify_when_ready`: до 600 с, при `is_ready` → `[spark-ai-hub:ready]`,
  при `get_startup_error` → `[spark-ai-hub:error] {err}` (`:394-409`);
* `docker inspect -f '{{.HostConfig.LogConfig.Type}}'` — если `none`, одно сообщение
  `[spark-ai-hub] This app keeps no logs: logging is turned off in its docker-compose.yml.`,
  ожидание вердикта и `close` (`:414-424`);
* `docker logs -f --tail 200 <container>`, stderr→stdout (`:426-430`);
* каждая строка: `\r` → берётся только последний сегмент (прогресс-бары), пустые не
  отправляются (`:432-438`);
* после естественного конца `docker logs -f` — если есть `get_startup_error`, отдать
  `[spark-ai-hub:error] …` (`:443-445`);
* `finally`: отменить health-task, убить процесс логов (`:448-453`).

Фронтенд (`store.js:68-123`): `[spark-ai-hub:ready]` → `fetchRecipes()`;
`[spark-ai-hub:error] xxx` → `fetchRecipes()` + строка `[spark-ai-hub] xxx` в панель;
строки с `%` и `|`/`it/s`/`Completed`/`Downloading` схлопываются в предыдущую с тем же
префиксом (`:99-112`).

---

## 4. Метрики

### 4.1 `GET /api/system/metrics` → `SystemMetrics` (`system.py:70-72`, `models/container.py:14-32`)

Источники (`monitor_service.py:8-70`), все ошибки глушатся (значения остаются дефолтными):

| Поле | Источник | Единица |
|---|---|---|
| `gpu_utilization` | `nvidia-smi --query-gpu=utilization.gpu,…` (первая строка, поле 0) | % |
| `gpu_memory_used_mb`, `gpu_memory_total_mb` | те же поля 1, 2, `csv,noheader,nounits` | MiB |
| `gpu_temperature` | поле 3 `temperature.gpu` | °C |
| `gpu_name` | поле 4 `name` | строка |
| `gpu_temp_headroom` | поле 5 `temperature.gpu.tlimit`, только если `.isdigit()` | °C |
| `gpu_thermal_slowdown` | `"Active" in (поле6, поле7)` — `clocks_event_reasons.sw/hw_thermal_slowdown` (только при ≥8 полях) | bool |
| `ram_total_gb`, `ram_used_gb` | `/proc/meminfo` `MemTotal`, `MemTotal-MemAvailable`, /1048576, `round(…,1)` | GiB |
| `disk_total_gb/used_gb/free_gb` | `shutil.disk_usage("/home")`, `round(…,1)` | GiB |
| `soc_temperature`, `soc_critical` | `_soc_temperature()`: максимум `temp` по `/sys/class/thermal/thermal_zone*` (//1000), и минимум критических trip-point (`None`, если нет) | °C |

Точность: температура GPU — целые, RAM/диск — один знак после запятой, `headroom`/`soc_*`
могут быть `null` (`monitor_service.py:38-41`, `:73-90`).

### 4.2 `/ws/metrics` (`system.py:75-84`)

`accept()` → бесконечный цикл: `send_json(model_dump())`, `sleep(1)` — каденс 1 Гц, разрыв
только по `WebSocketDisconnect`. Фронтенд (`hooks/useMetrics.js:8-17`): URL
`ws(s)://{location.host}/ws/metrics`, `JSON.parse`, реконнект через 3000 мс, `onerror` →
`close()`. Потребители: `SystemBar.jsx:4-20` (RAM-бар, GPU %, °C при `>0`), `System.jsx:65-78`
(история 60 точек → «Last 60 seconds»), `System.jsx:94-105` (headroom → лимит и цвет),
`System.jsx:142-153` (гейдж температуры, `soc_critical` в подписи), `RunningTray.jsx:86,174-192`
(«сколько осталось» = `total - used`).

---

## 5. `GET /api/system/connect` (`system.py:18-37`, `connect_service.py:249-360`)

Синхронный (`def`, threadpool), потому что шеллит наружу (`system.py:29-30`). Ключ API
допускается (`middleware/auth.py:49`), ключ зрителя уходит в команду установки.

Что запускается:

* `tailscale status --json`, таймаут 3 с → `Self.DNSName` (без точки) + `TailscaleIPs`
  (`connect_service.py:93-109`);
* `ip -o -4 addr show` → список IPv4-подсетей без loopback (`:124-154`);
* UDP-`connect("8.8.8.8", 80)` без пакетов → source IP; фолбэк `gethostbyname(hostname)`
  (`:71-90`);
* импорт `sah/sah` через `SourceFileLoader` (кешируется `functools.cache`) — список агентов
  берётся из `INTEGRATIONS` самого CLI (`:43-63`);
* `request.host`/`Forwarded`/`X-Forwarded-Host` + `X-Forwarded-Proto` → `origin`, валидируется
  регуляркой `^https?://(host|ip)(:port)?$` (`:202`, `:215-246`).

Кандидаты (`:273-322`), каждый — `{host, url, kind, scope, note, recommended}`:

1. `{hostname}.local` — `mdns`, scope `lan`, `recommended: true` (`:291-294`);
2. Tailscale MagicDNS — `tailscale`, scope `vpn` (`:296-300`);
3. LAN IPv4 — `ip`, scope `lan` (`:302-304`);
4. `origin`, если его нет среди первых трёх — вставляется **первым** с
   `kind: "origin"`, scope `remote`, `recommended: true`, у остальных `recommended`
   снимается (`:306-322`).

Ответ (`:339-360`): `hostname`, `port`, `origin`, `external_origin`, `primary`
(`candidates[0].url` или `http://localhost:{port}`), `candidates`, `client_ip`,
`client_local`, `local_url` (первый `lan`-кандидат вида `ip`), `local_urls`, `agents`,
`commands.install` (`curl -fsSL {install_from}/sah/install.sh | sh -s -- --key {api_key}` или
без `-s --`), `commands.set_hub`, `commands.set_key` (null без ключа). `client_ip`
предпочитает `cf-connecting-ip` → `x-forwarded-for` → `x-real-ip` → peer (`:157-169`);
`client_local` — вхождение в собственные подсети, loopback считается локальным (`:172-186`).

---

## 6. HF-токен

* `GET /api/system/hf-token` → `{"has_token": bool}` — только факт, не значение
  (`system.py:40-42`, `hf_token.py:29-35`).
* `POST /api/system/hf-token`, тело `{"token": str}` → `{"status":"saved"}`; значение
  `.strip()`, пишется в `data/hf-token`, `chmod 600` (`system.py:45-52`, `hf_token.py:37-40`).
  Пустая строка валидна и означает «токена нет» (`has_token` станет `false`).
* Токен попадает в сборку/запуск как env `HF_TOKEN`, если он не задан в окружении демона
  (`docker_service.py:806-809`), и как BuildKit-секрет `hf_token` в compose.
* `GET /api/system/hf-access/{slug}` (`system.py:55-67`): 404 при неизвестном slug; иначе
  `{"ok": all(accessible), "repos": [{"repo_id","url","accessible"}]}`. Проверка —
  `HEAD`-подобный GET `https://huggingface.co/{repo}/resolve/main/config.json` с
  `Authorization: Bearer`, 401/403 → `accessible:false`; любая сетевая ошибка → `true`
  (`hf_token.py:46-82`). `repos` = `recipe.gated_repos` (`models/recipe.py:259`).
* Фронтенд вызывает `hf-access` перед install/launch только если у рецепта есть
  `gated_repos`; сетевой сбой проверки не блокирует действие (`store.js:155-170`).

---

## 7. Caddy: один порт (`proxy_service.py`)

### 7.1 Константы

```
NETWORK = "spark-ai-hub-net"        :31
PROXY_CONTAINER = "spark-ai-hub-proxy"  :32
PROXY_IMAGE = "caddy:2-alpine"      :33
CADDYFILE = data_dir/"Caddyfile"    :36
APP_PREFIX = "/run"                 :42
PROBE_HEADER = "X-Sah-Probe"        :77
```

В Go-порте эти имена параметризуются префиксом `SAH_PROJECT` (`docs/PLAN.md:26-28`), но
форма строк в Caddyfile остаётся той же.

### 7.2 Генерируемый конфиг (`render_caddyfile`, `:146-222`)

Глобальный блок: `admin 0.0.0.0:2019`, `auto_https off`, `servers { trusted_proxies static
private_ranges }` (`:151-166`; без последнего Caddy перезапишет `X-Forwarded-Proto` на
`http`, `:155-162`). Сайт — `:{public_port}`, т.е. `:9000` (`:168`).

На каждый проксируемый рецепт (`_proxied_recipes`, `:130-143`): `ui.proxy == true` **и**
`ui.type in ("web","api-only")`, сортировка по slug. Сейчас 141 маршрут (17 web + 124
api-only). Для каждого:

```
# {slug}
@probe-{slug} { path /run/{slug}/* ; header X-Sah-Probe "<token>" }
handle @probe-{slug} { [uri strip_prefix /run/{slug}] reverse_proxy {container}:{ui.port} }
redir /run/{slug} /run/{slug}/ 308
handle_path|handle /run/{slug}/* {
    [forward_auth host.docker.internal:9010 {
        uri /api/auth/me
        @denied status 401 403
        handle_response @denied { redir * / 302 }
    }]
    reverse_proxy {container}:{ui.port}
}
```

Детали, которые нельзя потерять:

* probe-блок идёт **до** guarded-а, иначе health-проба получала бы 302 (`:176-183`);
* `strip_prefix` управляет и probe-блоком (`:184-185`), и выбором директивы:
  `handle_path` (префикс срезается) против `handle` (префикс остаётся, для приложений на
  `FastAPI(root_path=…)`) (`:193-196`, `models/recipe.py:74-85`). В текущем registry
  `strip_prefix: false` только у `minimax-music3` — 140 `handle_path` + 1 `handle` в
  `data/Caddyfile`;
* `redir … 308` нужен потому, что `handle_path` матчит только форму с trailing slash
  (`:190-192`);
* `forward_auth` рендерится только при `settings.auth_enabled` (`:197-209`); отказ (401/403)
  превращается в `302 /`;
* последний `handle` — всё остальное на `host.docker.internal:9010` (`:214-221`).

`write_caddyfile` не пишет файл, если текст не изменился (`:225-232`) — от этого зависит,
будет ли `reload`.

### 7.3 Контейнер Caddy (`ensure_proxy`, `:251-294`)

* `settings.proxy_enabled == False` → выход, ничего не делается (`:258-259`);
* нет `docker` в PATH → лог `[proxy] docker not found — apps will not be reachable under
  /run/` (`:260-262`);
* `ensure_network()` — `docker network inspect` || `create` (`:65-74`); сеть объявлена в
  рецептах как `external: true`, поэтому `compose down` её не сносит;
* `write_caddyfile()` → `True`, если изменился;
* `_container_state()` = `docker inspect -f '{{.State.Status}}'` (`:235-240`);
* `running` → при изменении `docker exec … caddy reload --config /etc/caddy/Caddyfile
  --adapter caddyfile` (`:243-248`, `:269-273`), иначе ничего; hot-reload без обрыва
  соединений;
* существует, но не `running` → `docker rm -f` и создание заново (`:275-276`);
* создание: `docker run -d --name spark-ai-hub-proxy --restart unless-stopped --network
  spark-ai-hub-net --add-host host.docker.internal:host-gateway -p {public}:{public}
  -v {CADDYFILE}:/etc/caddy/Caddyfile:ro caddy:2-alpine` (`:278-288`); ошибка только
  логируется (`:289-290`, `:293-294`) — прокси не может помешать старту демона.

Вызывается **ровно один раз** — в lifespan демона (`main.py:66`), после `load_recipes()`
(`:63`). Комментарий «and whenever the registry changes» (`:254-256`) коду не соответствует:
правка `container_name` в compose или установка рецепта с новым маршрутом требуют
рестарта демона (см. R1).

### 7.4 Проба готовности (`docker_service.py:265-325`)

* `ui_port = recipe.ui.port or 8080`, `health_path = ui.health_path or ui.path`
  (`:276-278`);
* проксируемый рецепт опрашивается **через входную дверь**:
  `http://127.0.0.1:{public_port}/run/{slug}{health_path}` с заголовком `X-Sah-Probe:
  <token>` и `allow_redirects=False` (`:280-303`) — 502 от Caddy вместо редиректа на вход
  читается как «не готово», а не как успех; непроксируемый —
  `http://127.0.0.1:{ui_port}{health_path}` (`:288-290`);
* до 300 тиков по 1 с, таймаут запроса 3 с, успех = `200 ≤ status < 400` → `mark_ready()`
  (`:292-308`); `mark_ready` снимает `_pending_actions` (`:235-237`);
* каждый третий тик (`tick % 3 == 2`) — `_container_exit_info(slug)`: `docker ps -a` по
  метке проекта, затем `docker inspect --format '{{.State.Status}}|{{.State.ExitCode}}|{{.RestartCount}}'`
  (`:156-213`). Провал = `RestartCount >= 2` (`:153`, `:198-200`) **или** все контейнеры
  `exited` и хотя бы один с кодом ≠ 0 (`:205-211`); sidecar, вышедший с 0, и
  `restart: on-failure:N` провалом не считаются;
* при провале — `_report_startup_failure` (`:328-362`): защита от повторного срабатывания
  по уже установленной ошибке (`:340-341`), `docker logs --tail 400` (`:216-232`),
  классификация (`:112-145`): нехватка свободной памяти vLLM, нехватка под KV-кэш,
  generic CUDA OOM; иначе `The app stopped before it finished starting ({why}): <3 последние
  строки, ≤300 символов>` (`:344-347`). Затем: сохранить хвост (400 строк) в
  `_last_failure_logs`, `set_pending("stopping")`, `stop_recipe`, `clear_pending`
  (`:352-362`) — контейнер снимается, чтобы следующий запуск не наследовал труп;
* по истечении 300 тиков — только лог `[health] {slug} health check timed out` (`:323`),
  статус остаётся «starting».

Дедупликация: `start_health_check` выходит, если уже `ready` или живая задача есть
(`:266-270`). Вызывается из launch (`containers.py:181`), из update (`:103`), из
`/ws/logs` (`:392`), из листинга рецептов при `starting` (`daemon/routers/recipes.py:55-56`,
`:98-99`) и на старте демона для всего запущенного (`main.py:35-41`).

### 7.5 Поведение при выключенном прокси / мёртвом Caddy

* `proxy_enabled=false`: `ensure_proxy` — no-op (`:258-259`); проксируемые приложения
  недоступны совсем (портов не публикуют), сам Hub доступен **только** на 9010 — демон
  слушает `settings.port` (`run.sh:20`), порт 9000 никто не занимает. Комментарий в
  `config.py:14-15` («go back to the daemon serving :9000 itself») реализацией не
  поддержан — см. R3.
* Caddy мёртв/сломан: `ensure_proxy` логирует и не падает (`:289-294`), демон остаётся
  доступен напрямую на 9010 — это заявленный путь восстановления (`:17-20`);
  health-пробы проксируемых рецептов при этом всегда неуспешны (502/connection refused),
  приложение остаётся в статусе `starting` до 5 минут, затем «timeout».

---

## 8. Конкурентность и кэши

* `_builds` — per-slug, без глобальной блокировки: две сборки разных рецептов идут
  параллельно; повторный install того же slug отсекается guard-ом (`containers.py:50-52`).
* `_pending_actions` — «одно действие на slug», значения `installing|launching|stopping|removing`
  (`docker_service.py:29-30`); выставляется/снимается в каждом роутере.
* Кэш docker-листингов (`docker_service.py:366-419`): TTL 3 с (`:377`), ключи
  `running_projects`, `volumes`, `images`, `ps_images`; конкурентные вызовы делят один
  in-flight `Task` через `asyncio.shield` (`:402-411`); поколение `_docker_lists_gen`
  отбрасывает результат, начатый до инвалидации (`:396-398`); `invalidate_docker_lists`
  вызывается из `set_pending`/`clear_pending` (`:253`, `:258`), launch (`:858`), stop
  (`:895`), remove (`:926`), purge (`:1173`). Причина — до ~250 процессов `docker` на
  каждый poll `/api/recipes` (`:366-372`).
* Prune-операции не сериализованы: `docker builder prune -a -f` после одной сборки может
  идти одновременно с другой (в коде это признано: «Records still in use by a concurrent
  build are in-use and survive», `:524-525`).

---

## 9. Переменные окружения и настройки

`daemon/config.py:5-39`, префикс `SPARK_AI_HUB_` (`:36`), читаются из env/`.env` через
pydantic-settings:

| Переменная | Поле | Дефолт | Влияние |
|---|---|---|---|
| `SPARK_AI_HUB_PORT` | `port` | `9010` | bind демона; он же в `forward_auth` и в fallback-`handle` Caddyfile (`:9`, `proxy_service.py:149`, `:202`) |
| `SPARK_AI_HUB_HOST` | `host` | `0.0.0.0` | описано, но `run.sh` передаёт `--host 0.0.0.0` явно |
| `SPARK_AI_HUB_PUBLIC_PORT` | `public_port` | `9000` | порт Caddy, `:9000 { }`, публикация `-p`, URL проб (`:16`, `:107`) |
| `SPARK_AI_HUB_PROXY_ENABLED` | `proxy_enabled` | `true` | выключает `ensure_proxy` целиком (`:258`) |
| `SPARK_AI_HUB_BASE_DIR` | `base_dir` | корень репо | не используется в этих файлах |
| `SPARK_AI_HUB_REGISTRY_PATH` | `registry_path` | `<repo>/registry/recipes` | `get_recipe_dir` (`registry_service.py:115-117`) |
| `SPARK_AI_HUB_DATA_DIR` | `data_dir` | `<repo>/data` | `Caddyfile`, `proxy-probe-token`, `hf-token` |
| `SPARK_AI_HUB_DB_PATH` | `db_path` | `<repo>/data/spark-ai-hub.db` | `installed_recipes` |
| `SPARK_AI_HUB_AUTH_ENABLED` | `auth_enabled` | `true` | `forward_auth` в Caddyfile (`proxy_service.py:197`), middleware |
| `SPARK_AI_HUB_SESSION_COOKIE_NAME` | `session_cookie_name` | `spark_ai_hub_session` | имя cookie сессии |
| `SPARK_AI_HUB_SESSION_TTL_DAYS` | `session_ttl_days` | `30` | TTL сессии |
| `SPARK_AI_HUB_FORCE_SECURE_COOKIE` | `force_secure_cookie` | `false` | Secure-флаг cookie |

Env, который демон передаёт **дочерним** процессам (`_launch_env`, `docker_service.py:786-814`):

* `HF_TOKEN` — из окружения или из `data/hf-token` (`:806-809`); нужен и для `build`
  (BuildKit secret), и для `pull` приватных образов;
* `SAH_API_KEY` — ключ аккаунта, инициировавшего запуск (`:810-811`);
* `SAH_HUB_URL` — origin вызывающего, без trailing slash (`:812-813`).

---

## 10. Приоритетный чек-лист паритета

1. `POST /api/recipes/{slug}/install` для неизвестного slug → 404 `{"detail":"Recipe not found"}`.
2. Повторный `install` при активной сборке → 200 `{"status":"building","slug":…}`, второй сборки нет.
3. `install` при `_pending_actions[slug]=="installing"` (восстановленное состояние) → 200 без запуска сборки.
4. `install` build-рецепта выполняет `docker compose -p spark-ai-hub-{slug} -f registry/recipes/{slug}/docker-compose.yml build`; pull-рецепта — `… pull`.
5. В лог сборки попадают строки `[spark-ai-hub] Starting install for …`, `[spark-ai-hub] Running: <cmd>`, `[spark-ai-hub] <slug> installed successfully!`.
6. Строки сборки отдаются без ANSI-последовательностей и с разбиением по `\r` (прогресс-бары не склеиваются).
7. Неуспешная сборка: строка `[spark-ai-hub] Install failed with exit code N`, запись в `installed_recipes` отсутствует, HTTP был 200.
8. После успешной build-сборки вызывается `docker builder prune -a -f`, в логе строка `Pruning build cache` + `Build cache …`.
9. После любой успешной установки вызывается prune осиротевших образов и в логе есть `Pruning orphaned images...`.
10. Установка пишет `installed_recipes(slug, 'installed', 'spark-ai-hub-{slug}')`.
11. `GET …/build-status` без сборки → `{"status":"idle","lines":[]}`; во время — `building`; после — `done` с полными строками.
12. `GET …/launch-plan` для не-LLM → `{"stop":[]}`; для LLM, который влезает в `MemAvailable` → `{"stop":[]}`.
13. `GET …/launch-plan` при нехватке памяти возвращает **ровно один** минимальный по `memory_gb` LLM, которого хватает; если такого нет — несколько крупнейших по убыванию до покрытия дефицита.
14. `memory_plan` вычитает из свободной памяти `memory_gb` запущенных, но ещё не `ready` приложений.
15. `POST …/launch` неизвестного slug → 404; успех → 200 `{"status":"launched","slug":…}`.
16. `POST …/launch` при ошибке compose → 500, `detail` содержит вывод `docker compose up`, `pending` снят.
17. `launch` build-рецепта без готового образа: вывод `compose up` доступен в `/ws/build/{slug}` и помечается `[done]` по завершении.
18. `launch` выставляет `pending=launching` и сбрасывает прошлую `startup_error` и `_last_failure_logs`.
19. `POST …/stop` → `{"status":"stopped","slug":…}` и `docker compose … down` (образы и тома на месте).
20. `DELETE /api/recipes/{slug}` удаляет только образы, которых нет у других установленных рецептов (`own − shared`), плюс `down --volumes`, тома по префиксу проекта, build-cache, строку БД.
21. `DELETE` при ненулевом коде `compose down` → `{"status":"failed"}`, строка БД сохраняется.
22. `POST …/purge` не меняет `pending`/`ready`, удаляет тома и неиспользуемые образы, вызывает prune осиротевших, но не `builder prune`.
23. `purge` при ошибках удаления → `{"status":"partial: …"}`; без ошибок → `{"status":"purged"}`.
24. `GET …/compose` → `{content, default_content}`; `default_content` = `git show HEAD:registry/recipes/{slug}/docker-compose.yml`; при недоступном git — равен `content`.
25. `PUT …/compose` с `{"content": "..."}` пишет файл байт в байт, без валидации YAML, без рестарта контейнера, отвечает `{"status":"saved"}`.
26. `POST …/compose/reset` возвращает `{"status":"reset","content": <записанное>}` и делает файл равным git-версии; при недоступном git — 404 `Default docker-compose.yml not available`.
27. `GET /api/containers` возвращает по элементу на каждый контейнер с меткой `com.docker.compose.project`, `ports` — `{"<container_port>": <host_port>}`, `cpu_percent` и `memory_mb` всегда `0`.
28. `/ws/build/{slug}`: новые строки — отдельные text-сообщения; в конце ровно одно `[done]` и закрытие.
29. `/ws/build/{slug}` при отсутствии `_builds[slug]` после 5 с ожидания отдаёт `[done]` и закрывается (без стрима).
30. `/ws/logs/{slug}` до появления контейнера отдаёт `[spark-ai-hub] Waiting for container to start...` не более одного раза.
31. `/ws/logs/{slug}` при отсутствии контейнера и сохранённом хвосте отдаёт хвост + `[spark-ai-hub:error] <текст>`; иначе `[spark-ai-hub] Container not running`.
32. `/ws/logs/{slug}` для контейнера с `LogConfig.Type == "none"` отдаёт сообщение про отключённые логи, ждёт вердикт и закрывается.
33. `/ws/logs/{slug}` для готового приложения отдаёт `[spark-ai-hub:ready]` в пределах 600 с.
34. `/ws/logs/{slug}` при падении контейнера до готовности отдаёт `[spark-ai-hub:error] …` (из `_startup_errors`) и стрим `docker logs -f --tail 200` завершается.
35. Health-check: успех = 2xx/3xx на `/run/{slug}{health_path}` с `X-Sah-Probe`, для проксируемых — без follow redirect.
36. Health-check падает при `RestartCount >= 2` и при «все exited, хотя бы один с кодом ≠ 0»; sidecar с кодом 0 — не падение.
37. Падение до готовности: текст классифицируется (нехватка свободной памяти vLLM / KV-кэш / CUDA OOM) либо `The app stopped before it finished starting (…): <3 строки>`; затем приложение останавливается.
38. `GET /api/system/metrics` отдаёт все 14 полей `SystemMetrics`, `headroom`/`soc_*` — `null` при отсутствии данных.
39. `/ws/metrics` отдаёт JSON-объект раз в секунду, соединение не закрывается само.
40. `GET /api/system/connect` содержит `candidates` с `scope` (`lan|vpn|remote`), `primary`, `client_local`, `local_urls`, `agents`, `commands.install/set_hub/set_key`; при внешнем origin он первый и `recommended`.
41. `GET /api/system/connect` принимает API-ключ (`Authorization: Bearer sah-…`), остальные `/api/system/*` — только сессию.
42. `GET /api/system/hf-token` → `{"has_token": bool}` без значения токена; `POST` сохраняет `.strip()`-значение в `data/hf-token` с правами 600.
43. `GET /api/system/hf-access/{slug}` → `{"ok": bool, "repos":[{"repo_id","url","accessible"}]}`; 404 для неизвестного slug.
44. Генерируемый Caddyfile начинается с `# Generated by Spark AI Hub`, содержит `admin 0.0.0.0:2019`, `auto_https off`, `servers { trusted_proxies static private_ranges }`, сайт `:9000`.
45. На каждый проксируемый рецепт в Caddyfile ровно один probe-блок (`path /run/{slug}/*` + `header X-Sah-Probe "<token>"`), один `redir /run/{slug} /run/{slug}/ 308`, один guarded-блок с `forward_auth host.docker.internal:9010`, `uri /api/auth/me`, `@denied status 401 403` → `redir * / 302`.
46. `strip_prefix: true` → `handle_path` и `uri strip_prefix /run/{slug}` в probe; `false` → `handle` и без strip.
47. Файл `data/Caddyfile` не перезаписывается, если текст не изменился (`write_caddyfile` → false).
48. `ensure_proxy`: нет контейнера → `docker run -d --name spark-ai-hub-proxy --restart unless-stopped --network spark-ai-hub-net --add-host host.docker.internal:host-gateway -p 9000:9000 -v <data>/Caddyfile:/etc/caddy/Caddyfile:ro caddy:2-alpine`.
49. `ensure_proxy`: контейнер `running` + конфиг изменился → `docker exec … caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile`; конфиг не изменился → ни одного вызова.
50. `ensure_proxy`: контейнер существует но не `running` → `docker rm -f` и создание заново.
51. `proxy_enabled=false` → ни сети, ни контейнера, ни файла; демон продолжает работать на 9010.
52. Ошибка Caddy не роняет демон: `ensure_proxy` только логирует, `/healthz` и API доступны на 9010.
53. Сеть `spark-ai-hub-net` создаётся, если отсутствует, и не удаляется при `compose down` (рецепты объявляют её `external: true`).
54. Не-проксируемый рецепт получает health-пробу на `http://127.0.0.1:{ui.port}{health_path}` (дефолт `ui.port` = 8080).
55. `X-Sah-Probe`-токен читается из `data/proxy-probe-token` и переиспользуется между рестартами (не регенерируется, если файл непуст).
56. Все `/api/*`, `/ws/*` требуют сессии; WebSocket без сессии закрывается кодом 1008 до `accept`.

---

## 11. Риски и открытые вопросы

* **R1.** `ensure_proxy` вызывается только из lifespan (`main.py:66`), хотя docstring
  обещает вызов «whenever the registry changes» (`proxy_service.py:254-256`). После
  установки рецепта, правки `container_name` или `ui.proxy` маршрут в Caddyfile не
  появляется до рестарта демона. Для Go-порта нужно решить: тянуть паритет (рестарт) или
  вызывать `ensure_proxy` после каждого install/update/compose PUT.
* **R2.** `update` не проверяет `_pending_actions == "installing"` (`containers.py:80-81`),
  поэтому update поверх идущего install перезаписывает `_builds[slug]` и теряет лог первой
  сборки; при этом обе задачи пишут в один `installed_recipes`. `install` защищён от
  обратного случая.
* **R3.** `proxy_enabled=false` в комментарии описан как «демон сам обслуживает :9000»
  (`config.py:14-15`), но `run.sh:20` всегда биндит 9010, и ничего не слушает 9000. При
  выключенном прокси Hub доступен только на 9010, приложения — нигде.
* **R4.** `ContainerInfo.cpu_percent`/`memory_mb` (`models/container.py:10-11`) всегда 0:
  схема обещает больше, чем отдаёт реализация. Если Go-порт начнёт их заполнять, ответ
  разойдётся с апстримом (это допустимо по схеме, но не побайтово).
* **R5.** `/api/recipes/{slug}/build-status` не проверяет существование рецепта
  (`containers.py:109-117`) — 404 тут не будет, а `idle` вернётся и для мусорного slug.
  Фронтенд на это опирается (`store.js:407-427`), поэтому «улучшать» нельзя.
* **R6.** `GET …/compose` использует `compose_file.parents[3]` (`containers.py:236`) —
  предположение о раскладке `registry/recipes/{slug}/`. При смене `REGISTRY_PATH` на путь
  другой глубины `git show` молча деградирует до `content` (фолбэк `:254-257`), а `reset`
  начнёт отдавать 404 (`:245-246`).
* **R7.** `PUT …/compose` не валидирует YAML и не перезапускает контейнер
  (`containers.py:264-268`): битый compose проявится только на следующем install/launch.
  Сохранять это поведение — осознанное решение (фронт показывает ошибку сборки, а не
  ошибку редактора).
* **R8.** `_prune_orphaned_images` удаляет **все** локальные образы, не входящие в
  `image:` установленных рецептов и не используемые контейнерами (`docker_service.py:581-590`).
  На машине с посторонними образами это деструктивно; для тестового порта
  (`SAH_PROJECT`-префикс) область видимости надо ограничить, иначе первый же install
  вычистит окружение разработчика.
* **R9.** `stop_recipe`/`remove_recipe` не читают вывод `compose`, только код возврата
  (`:894-896`, `:925`); причина отказа пользователю не видна — `status: "failed"` без деталей.
* **R10.** Падение health-check останавливает приложение (`:358-362`), но `pending`
  выставляется в `"stopping"` в обход `clear_ready` специально (`:353-357`) — при
  переписывании на Go легко получить «залипший» `stopping` после отмены задачи.
