# Реестр рецептов, медиа, MCP и прокси — контракт реализации

Источник: `/home/nvidia/spark-ai-hub` (Python/FastAPI). Эталон HTTP-схемы: `docs/contract/openapi.json`.
Все ссылки `file:line` — по текущему состоянию исходников. Ничего, кроме этого файла, не изменялось.

Порядок разделов: рецепт → обнаружение/кэш → `/api/recipes` → статика реестра → медиахранилище →
`/api/uploads` → `/link/*` → `/mcp` → `/v1` (OpenAI) → `/v1/messages` (Anthropic) → сервисы mesh/web →
чеклист парности.

---

## 1. Схема recipe.yaml

Модель — `daemon/models/recipe.py`, единственный Pydantic-класс `Recipe` (строка 208) и вложенные модели.
**Валидация — целиком Pydantic v2 (2.13.4), своего кода валидации нет.** Практические следствия, которые
обязан воспроизвести порт:

* Обязательны ровно два поля: `name` и `slug` (recipe.py:209-210). Отсутствие любого → `ValidationError`,
  рецепт не загружается (registry_service.py:101-102). Проверено: `Recipe(slug='x')` → ошибка `name: missing`.
* Лишние ключи **молча игнорируются** (у моделей нет `model_config`, Pydantic v2 по умолчанию `extra='ignore'`).
  В реестре таких ключей два массовых: `docker.build_time_minutes` (145 файлов, напр.
  `registry/recipes/sglang-qwen-image-21/recipe.yaml:37`) и `tested_on` (62 файла, напр.
  `registry/recipes/vllm-glm53-flash-exl3-k2/recipe.yaml:51-53`). В JSON-ответе их нет — проверено
  `model_dump()`, `tested_on` отсутствует, `docker` = `{"build":…, "gpu":…}`.
* Слабое приведение типов включено: `params_b: "7"` → `7.0` (проверено).
* Перечислений (enum) нет ни для `ui.type`, ни для `category`, ни для `source`, ни для `status`, ни для
  `arch`/`quantization` — любая строка принимается (проверено: `ui.type: "weird"` проходит).

### 1.1. `Recipe` — верхний уровень

| ключ | строка | тип | дефолт | смысл / правило |
|---|---|---|---|---|
| `name` | 209 | str | **обязателен** | отображаемое имя |
| `slug` | 210 | str | **обязателен** | идентификатор; **ключ словаря рецептов** (registry_service.py:100), не имя каталога |
| `version` | 211 | str | `"1.0.0"` | |
| `description` | 212 | str | `""` | |
| `author` | 213 | str | `""` | |
| `website` | 214 | str | `""` | |
| `upstream` | 215 | str | `""` | |
| `fork` | 216 | str | `""` | |
| `category` | 217 | str | `"llm"` | одиночная категория |
| `categories` | 218 | list[str] | `[]` | если непусто — заменяет `category` при фильтрации (recipes.py:64) |
| `tags` | 219 | list[str] | `[]` | источник всех вычисляемых возможностей (§1.6) |
| `icon` | 220 | str | `""` | эмодзи/глиф, не URL (`RecipeCard.jsx:76`) |
| `logo` | 221 | str | `""` | URL-путь, обычно `/logos/<vendor>.png` (147 из 148 рецептов) |
| `cover` | 222 | `RecipeCover` | пустой объект | §1.2 |
| `requirements` | 223 | `RecipeRequirements` | см. §1.2 | |
| `ui` | 224 | `RecipeUI` | см. §1.2 | |
| `docker` | 225 | `RecipeDocker` | см. §1.2 | |
| `integration` | 226 | `RecipeIntegration \| null` | `null` | §1.2; **наличие определяет `capabilities`** |
| `image_defaults` | 227 | `RecipeImageDefaults \| null` | `null` | §1.3 |
| `video_defaults` | 228 | `RecipeVideoDefaults \| null` | `null` | §1.3 |
| `audio_defaults` | 229 | `RecipeAudioDefaults \| null` | `null` | §1.3 |
| `source` | 230 | str | `"community"` | `spark-ai-hub` / `official` / `community` (соглашение, не enum) |
| `status` | 231 | str | `"experimental"` | |
| `release_date` | 232 | str | `""` | `YYYY-MM` или `YYYY-MM-DD`, для сортировки каталога |
| `engine` | 234 | str | `""` | `vLLM` / `llama.cpp` / `SGLang` / … |
| `params_b` | 235 | float \| null | `null` | млрд параметров |
| `active_params_b` | 236 | float \| null | `null` | только MoE |
| `arch` | 237 | str | `""` | `dense` / `moe` |
| `quantization` | 238 | str | `""` | `BF16`/`FP8`/`NVFP4`/… |
| `speculative_method` | 244 | str | `""` | снимается с compose-команды вручную автором, не вычисляется |
| `artificial_analysis_index` | 250 | float \| null | `null` | внешний балл |
| `weights_gb` | 251 | float \| null | `null` | |
| `depends_on` | 252 | list[str] | `[]` | слаги связанных рецептов |
| `requires_hf_token` | 253 | bool | `false` | |
| `gated_repos` | 259 | list[str] | `[]` | репозитории с terms-gate |
| `runtime_env_path` | 260 | str | `""` | **выставляется роутером**, не из yaml (§3.4) |
| `tokens_per_second` | 261 | float \| null | `null` | |
| `tokens_per_second_editing` | 272 | float \| null | `null` | |
| `editing_workload` | 273 | str | `""` | напр. `code-edit` |
| `benchmarks` | 277 | dict[str,float] | `{}` | ключи `code`, `explainer`, `prose`, `code-edit` |
| `render_seconds` | 285 | float \| null | `null` | медиамодели |
| `render_workload` | 286 | str | `""` | напр. `"1024x1024, 40 steps"` |
| `load_seconds` | 287 | float \| null | `null` | |
| `arena` | 288 | list[`RecipeArenaScore`] | `[]` | §1.4 |
| `context_tokens` | 289 | int \| null | `null` | если `null` — дозаполняется загрузчиком (§2.2) |
| `reserved_gb` | 294 | float | `0.0` | **всегда** перезаписывается загрузчиком (§2.2) |

### Runtime-поля (не из yaml, recipe.py:296-305)

`installed=false`, `running=false`, `ready=false`, `starting=false`, `installing=false`,
`has_leftovers=false`, `error: str|null = null`. Всегда присутствуют в ответе; пересчитываются роутером
на каждый запрос (§3.3).

### 1.2. Вложенные модели

`RecipeRequirements` (59-63): `min_memory_gb: int = 8`, `recommended_memory_gb: int|null = null`,
`disk_gb: int = 10`, `cuda_compute: str = "12.1"`. В реестре заполнены все четыре ключа во всех 148 файлах.

`RecipeUI` (66-84): `type: str = "web"` (используются `web` и `api-only`; `api-only` = «у модели нет своего
UI», на этом строится `is_llm`/`isModel`), `port: int = 8080` (**порт внутри контейнера**), `path: str = "/"`,
`health_path: str|null = null`, `proxy: bool = false`, `strip_prefix: bool = true`.

`RecipeDocker` (87-89): `build: bool = false`, `gpu: bool = true`. Больше ничего: `build_time_minutes` из
yaml отбрасывается.

`RecipeIntegration` (92-98), все строки, дефолт `""`, весь объект `null` по умолчанию:
`api_url`, `model_id`, `api_key`, `max_context`, `max_output_tokens`, `curl_example`.
`max_output_tokens` в реестре не встречается ни разу, но в схеме и на фронте (`RecipeDetail.jsx:690`) есть.

`RecipeCover` (186-205): `image: str = ""` (имя файла в `registry/covers/`), `caption: str = ""`,
`fit: str = "cover"` (`cover`|`contain`), `backdrop_fit: str = ""`, `focus_x: float = 0.5`,
`focus_y: float = 0.5`, `credit: str = ""`, `source: str = ""`, `grade: bool = true`,
`backdrop: RecipeBackdrop|null = null`.
`RecipeBackdrop` (172-183): `image: str = ""`, `fit: str = ""`, `focus_x: float|null = null`,
`focus_y: float|null = null`.

### 1.3. Дефолты генерации

`RecipeImageDefaults` (101-116): `steps: int|null`, `guidance_scale: float|null`,
`true_cfg_scale: float|null`, `preset: str|null`, `notes: str = ""`, `source: str = ""`.
Семантика `preset` — «сервер сам выводит steps/guidance, напрямую их слать нельзя»; при заданном
preset `steps` не отправляется (image_service.py:144-146), а в тело уходит `preset`
(image_service.py:254-255). `guidance_scale`/`true_cfg_scale` попадают в тело как есть
(image_service.py:235-239).

`RecipeVideoDefaults` (119-143): `steps`, `guidance_scale`, `fps`, `seconds`, `num_frames` (модели,
считающие кадры; обязано быть 4k+1 — video_service.py:174-179), `size` (`"WIDTHxHEIGHT"`, ландшафт;
для 9:16 переворачивается — video_service.py:162-168), `flow_shift`, `extra_params: dict|null`,
`send_aspect_ratio: bool = false`, `aspect_ratios: list[str]|null`, `max_seconds: int|null`,
`notes`, `source`. Все числовые/строковые — `None` по умолчанию.
Незаданные `aspect_ratios` → `["16:9","9:16"]`, `max_seconds` → `10` (video_service.py:80-86).

`RecipeAudioDefaults` (146-150): `seconds: int|null`, `notes: str = ""`, `source: str = ""`.

### 1.4. `RecipeArenaScore` (153-169)

`board: str`, `task: str`, `elo: float`, `entry: str`, `read: str` — **все обязательны** (нет дефолтов);
`votes: int|null = null`, `url: str = ""`, `base: bool = false`.
Одна неполная запись `arena` валит загрузку всего рецепта (проверено: `arena: [{board: 'b'}]` → три
`missing`); загрузчик это ловит и печатает `[registry] Failed to load …` (registry_service.py:101-102).

### 1.5. Вычисляемые поля (всегда в JSON)

* `memory_gb` (307-311) = `max(float(requirements.min_memory_gb), reserved_gb)` → float, всегда.
* `capabilities` (318-328): `[]` если `integration is None`; иначе множество, начиная с `"completion"`,
  плюс отображение тегов `TAG_CAPABILITIES` (9-15): `vision`→`vision`, `multimodal`→`vision`,
  `video`→`video`, `tool-use`→`tools`, `reasoning`→`thinking`; результат **отсортирован**.
* `media_capabilities` (330-334): список по порядку словаря `MEDIA_CAPABILITIES` (31-56), отфильтрованный
  по тегам: `text-to-image`→`image-generation`, `image-edit`→`image-editing`, `multi-image`→`multi-image-input`,
  `text-to-video`→`text-to-video`, `image-to-video`→`image-to-video`, `last-frame`→`first-last-frame`,
  `multi-reference`→`multi-reference`, `audio-reference`→`audio-reference`, `video-edit`→`video-editing`,
  `faceswap`→`face-swap`, `video-audio`→`video-with-audio`, `orbit-360`→`orbit-360`,
  `image-to-3d`→`image-to-3d`, `multi-view`→`multi-view`, `text-to-music`→`music-generation`.
* `app_url` (336-353): `"/run/{slug}/"` при `ui.proxy and ui.type=="web"`;
  `"/run/{slug}{ui.path}"` при `ui.proxy and ui.type=="api-only"`; иначе `""`.
* `is_llm` (313-316) — **не** сериализуется (`@property`, без `computed_field`): `category=="llm" and ui.type=="api-only"`.
  Порт обязан воспроизвести это правило в своём коде, но не в JSON.

`media_figures()` (20-28) — отдельный помощник (не поле Recipe): отдаёт `render_seconds`,
`render_workload`, `load_seconds` (только непустые) и `arena` как `model_dump(exclude_defaults=True)`;
используется в MCP-списках моделей (image_service.py:172, video_service.py:138, mesh_service.py:105).

---

## 2. Обнаружение и кэширование рецептов

### 2.1. Загрузчик

`daemon/services/registry_service.py:81-104`:

1. `settings.registry_path` (`daemon/config.py:19` → `<repo>/registry/recipes`); нет каталога → пустой словарь.
2. `sorted(registry.iterdir())` — **порядок по имени каталога**, каждый подкаталог с `recipe.yaml`.
3. `yaml.safe_load` → `Recipe(**data)`; любое исключение — печать и **пропуск** рецепта (`:101-102`).
4. Ключ словаря — `recipe.slug` (`:100`), а `get_recipe_dir()` ищет каталог **по slug** (`:115-117`).
   Расхождение slug и имени каталога сломает доступ к compose/`.env` — в реестре они совпадают.
5. Один прогон при старте: `load_recipes()` вызывается в lifespan (`daemon/main.py:63`). **Горячей
   перезагрузки нет**; изменение yaml требует рестарта демона.

### 2.2. Дозаполнение из compose

* `context_tokens`: если в yaml `null` → `data.get("context_length")` (сырой ключ, **не поле модели**;
  12 файлов, напр. `registry/recipes/sglang-qwen38-27b-bf16-dflash2/recipe.yaml:54`) → иначе
  `_context_from_compose()` (registry_service.py:95-98, 53-61).
* `_CONTEXT_FLAGS` = `--max-model-len`, `--max-seq-len`, `--max-total-tokens`, `--ctx-size`, `-c` (:11).
* `_RESERVE_FLAGS` = `--gpu-memory-utilization`, `--mem-fraction-static` (:15);
  `reserved_gb = round(fraction * MemTotal_GiB, 1)` (:72-78), `MemTotal` из `/proc/meminfo` (:64-69).
  Нет флага → `0.0`; нечисловое значение → `0.0` (`:74-77`).
* Разбор команд (`_compose_commands`, :18-32): читается `docker-compose.yml` рядом с `recipe.yaml`,
  берутся `command` **только в виде списка** у всех сервисов; строка-шелл не разбирается.
* `_flag_value` (:35-50): поддерживаются `--flag value` и `--flag=value`; `${VAR:-default}` → `default`;
  возвращается **первое** совпадение (первый сервис, первый флаг). `--flag value`, где value начинается
  с `-`, не отличит флаг от значения.
* `context_tokens` принимается только если `value.isdigit()` (:61) — `262144` ок, `256k` нет.

---

## 3. `/api/recipes`

Роутер `daemon/routers/recipes.py`, префикс `/api/recipes` (:16). Аутентификация — `AuthMiddleware`
закрывает весь `/api` (`daemon/middleware/auth.py:39`), API-ключ здесь **не** принимается (`API_KEY_PREFIXES = ("/v1", "/mcp")`, :48).

### 3.1. `GET /api/recipes` (recipes.py:30-72)

Query: `category: str|None`, `search: str|None`. Ответ — `list[Recipe]` (тот же полный объект, что и деталь).

Порядок — порядок словаря рецептов (отсортированные каталоги, §2.1). Фильтры:

* `category` (64-66): `recipe_categories = categories or [category]`; `"all"` и пустое значение —
  без фильтра; иначе точное вхождение строки.
* `search` (67-70): `q = search.lower()`; пропуск, если `q not in name.lower()` **и**
  ни один тег не содержит `q`. Теги сравниваются **как есть**, без приведения к нижнему регистру —
  тег `Vision` не найдётся по `vision`.

Тело ответа отдаёт `response_model=list[Recipe]`, поэтому в каждом элементе присутствуют и вычисляемые
поля (§1.5), и runtime-поля (§3.3), а `null`-поля сериализуются как `null`, а не опускаются.

### 3.2. `GET /api/recipes/{slug}` (recipes.py:75-107)

404 → `{"detail": "Recipe not found"}` (:78-79). Иначе тот же объект + runtime-состояние.
`GET /api/recipes/{slug}` DELETE/`/install`/`/launch` и т.д. живут в других роутерах
(`docs/contract/openapi.json`, пути `/api/recipes/{slug}/…`) — вне этого документа.

### 3.3. Runtime-состояние (recipes.py:33-63 = 81-106, логика идентична)

```
installed  = slug in SELECT slug FROM installed_recipes        (docker_service.py:1045-1052)
pending    = get_pending(slug)   # None | "installing" | "launching" | "stopping"  (docker_service.py:261-262)
error      = get_startup_error(slug)                            (docker_service.py:41-42)
```

Установленный рецепт:

* `installing = (pending == "installing")`;
* `container_running = await is_recipe_running(slug)`; `ready = is_ready(slug)` если контейнер запущен, иначе `false`;
* `pending == "launching"` → `starting=true, running=false`;
  `pending == "stopping"` → `running=false, starting=false`;
  иначе `starting = container_running and not ready`, `running = container_running and ready`;
* если `starting` или (`container_running and not ready`) — фоновый `asyncio.create_task(start_health_check(slug))`
  (:55-56), т.е. **GET сам запускает опрос здоровья**.

Неустановленный: `running=false, ready=false`, `starting = pending=="launching"`,
`installing = pending=="installing"`, и только если ни то ни другое — `has_leftovers =
await has_recipe_leftovers(slug)` (проверка образов/томов, docker_service.py:1157+).

Важные следствия для порта:

* `error` и `installed` перезаписываются на каждом запросе, но `has_leftovers` при `true` **обратно не
  сбрасывается** (присваивание только в ветке «не занят», :62-63) — значение липкое до рестарта.
* Обработчики мутируют **глобальные объекты** `Recipe` из `get_recipes()` (общий словарь),
  а не копии (:37-63, :80-106). В Go при конкурентных запросах это гонка: нужен либо мьютекс,
  либо сборка ответа из неизменяемого прототипа + локальных полей.

### 3.4. `runtime_env_path` (recipes.py:19-27)

Если в каталоге рецепта есть `.env` **или** `.env.example`, полю присваивается путь к `<dir>/.env`
(именно `.env`, даже когда существует только `.env.example` — файл может отсутствовать).
В реестре сейчас есть `registry/recipes/onyx/.env.example`; проверка существования не повторяется.

### 3.5. Поля, которые реально читает фронтенд

| поле | потребитель |
|---|---|
| `slug`, `name`, `icon`, `logo`, `author`, `description` | `components/RecipeCard.jsx:70-89`, `PosterCard.jsx:42,77,156` |
| `installed`, `running`, `ready`, `starting`, `installing` | `PosterCard.jsx:48-52,173-192`, `store.js:246-297` |
| `params_b`, `active_params_b`, `quantization`, `weights_gb` | `RecipeCard.jsx:16-17`, `PosterCard.jsx:60-62` |
| `tokens_per_second`, `release_date` | `PosterCard.jsx:63-69` |
| `categories` \| `category`, `tags`, `source` | `Catalog.jsx:23,32-38`, `RecipeCard.jsx:47-49` |
| `requirements.min_memory_gb`, `.disk_gb` | `RecipeDetail.jsx:280-281` |
| `engine`, `arch`, `media_capabilities`, `render_seconds`, `render_workload`, `load_seconds`, `arena` | `RecipeDetail.jsx:271-294` |
| `benchmarks` | `RecipeDetail.jsx:510-522` |
| `runtime_env_path`, `integration.*`, `capabilities` | `RecipeDetail.jsx:659-707` |
| `depends_on`, `website`, `upstream`, `fork`, `has_leftovers` | `RecipeDetail.jsx:608-610,770` |
| `context_tokens`, `tokens_per_second_editing`, `editing_workload`, `artificial_analysis_index` | `components/ModelList.jsx:430-462` |
| `cover.*` | `covers.js:17-43` |
| `app_url` | `lib/urls.js`, `store.js` |

`RecipeDetail.jsx` также рисует capability-блок только при непустом `integration` и непустом
`capabilities` (:672-690) — то есть согласовано с правилом §1.5.

---

## 4. Статика реестра: `/covers`, `/cover-sources`, `/logos`

* `/covers/<stem>-poster.jpg` (600×900) и `/covers/<stem>-backdrop.jpg` (2560×1000) — **производные**,
  генерируются `tools/build_covers.py` в `frontend/public/covers/` (`tools/build_covers.py:24-25,51`),
  оттуда попадают в `frontend/dist` и раздаются общим статическим обработчиком
  (`daemon/main.py:154-159`). Имя: `stem` = `cover.image` без расширения; нет `cover.image` → `generic`
  (`frontend/src/covers.js:15-29`).
* `/cover-sources/<cover.image>` — **оригинал без обработки**, монтируется из `registry/covers`
  (`daemon/main.py:143-146`), ссылка строится как `/cover-sources/${cover.image}` (`covers.js:34-37`).
* `/logos/...` — обычные файлы `frontend/public/logos`, раздаются тем же статическим обработчиком
  (отдельного маршрута нет). Тематические варианты: `useThemedLogo.js:6` подменяет
  `/logos/x.png` → `/logos/x-dark.png`/`x-light.png`.
* Любой отсутствующий ассет с суффиксом из `ASSET_SUFFIXES` (png/jpg/svg/…, `main.py:28-31`) обязан
  вернуть **404**, а не `index.html` (`main.py:160-165`); `index.html` отдаётся с `Cache-Control: no-cache`
  (:170-172).

---

## 5. Медиахранилище

### 5.1. Таблицы (`daemon/db.py:91-114`)

```sql
CREATE TABLE media (
    name TEXT PRIMARY KEY,          -- '<32 hex>.<ext>'
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    model TEXT NOT NULL DEFAULT '', -- slug рецепта; '' для загрузки
    seconds REAL                    -- время генерации; NULL для загрузки
);
CREATE INDEX idx_media_user ON media(user_id);

CREATE TABLE media_links (
    token_hash TEXT PRIMARY KEY,    -- sha256(token)
    kind TEXT NOT NULL,             -- 'upload' | 'download'
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name TEXT NOT NULL DEFAULT '',  -- файл для download-ссылки
    expires_at INTEGER NOT NULL,    -- unix seconds
    used_at INTEGER                 -- ставится при использовании upload-ссылки
);
```

Миграция добавляет `media.model` и `media.seconds`, если таблица уже существует (db.py:127-139).

### 5.2. Имена, каталоги, шаблоны (`media_store.py:56-68`)

| вид | каталог | regex имени | публичный префикс | загрузки |
|---|---|---|---|---|
| image | `data/images` | `^([0-9a-f]{32})\.png$` | `/images` | нет |
| image upload | `data/images/uploads` | тот же | `/images` | да |
| video | `data/videos` | `^([0-9a-f]{32})\.mp4$` | `/videos` | нет |
| video upload | `data/videos/uploads` | тот же | `/videos` | да |
| audio | `data/audio` | `^([0-9a-f]{32})\.wav$` | `/audio` | нет |
| audio upload | `data/audio/uploads` | тот же | `/audio` | да |
| mesh | `data/meshes` | `^([0-9a-f]{32})\.glb$` | `/meshes` | нет |

Константы: `IMAGE_DIR`/`IMAGE_NAME_RE` (image_service.py:42-47), `VIDEO_DIR` (video_service.py:42-45),
`AUDIO_DIR` (audio_service.py:29-32), `MESH_DIR` (mesh_service.py:47-49); `data_dir` — `daemon/config.py:20`.
Идентификатор — `secrets.token_hex(16)` = 128 бит (напр. image_service.py:483).

Прочее:

* `MEDIA_TYPES = {".png":"image/png", ".mp4":"video/mp4", ".wav":"audio/wav", ".glb":"model/gltf-binary"}`
  (media_store.py:95-96).
* `_URL_NAME_RE` (:99) — распознаёт имя файла в URL **любого** хоста: `/(images|videos|audio|meshes)/<32hex>.<ext>`;
  `name_in_url()` (:102-105). Это то, что позволяет передавать URL, полученный по одному адресу, в запрос с другого.
* `render_of(path)` = `<id>.render.png` (:108-111); `picture(path)` = сам файл, для `.glb` — render,
  иначе `<id>.png` (кадр-вырезка) (:114-120).
* `find(name)` перебирает все локации и возвращает первый существующий файл с именем, прошедшим regex (:123-129).

### 5.3. Владение (`media_store.py:43-53, 154-160, 27-40`)

Единое правило `_visible(owner, user)`:

* `settings.auth_enabled == false` → видно всем;
* нет пользователя → нет;
* `owner is not None` → `owner == user["id"]`;
* `owner is None` (строка потеряна / файл старше учёта) → только `user["role"] == "admin"`.

`can_read` читает `media.user_id` по имени файла. `record(name, user_id, model, seconds)` — `INSERT OR REPLACE`;
**при `user_id is None` ничего не пишет** (:32-33), т.е. файлы, созданные при выключенной авторизации,
остаются без владельца и позже видны только админам.

### 5.4. `GET /api/media` (`files.py:35-40`, сборка — `media_store.py:163-179`)

Требует пользователя при `auth_enabled` → иначе `401 {"detail":"Authentication required."}`.
Ответ: `{"files": [ … ]}`. Элемент:

| поле | источник |
|---|---|
| `name` | имя файла |
| `kind` | `image`\|`video`\|`audio`\|`mesh` |
| `upload` | bool (каталог `uploads`) |
| `path` | `"{prefix}/{name}"`, напр. `/images/<id>.png` |
| `bytes` | `st_size` |
| `created` | `int(st_mtime)` — unix-секунды |
| `model` | имя рецепта, если слаug ещё в каталоге, иначе slug; `""` для загрузки (`_model_fields`, :141-151) |
| `orbit` | `true` если у рецепта тег `orbit-360` (:151) |
| `seconds` | `round(media.seconds, 1)` или `null` (:172) |
| `rendered` | **только для `kind=="mesh"`**: есть ли `<id>.render.png` (:173-174) |
| `width`, `height` | `.png` через PIL; `.mp4`/`.wav` через `ffprobe -select_streams v:0` (:185-212) |
| `duration` | `.mp4`/`.wav`: `format.duration`, округление 2 знака; для wav — единственное, что вернётся |

Сортировка — по `created` **по убыванию** (:179). Формы кэшируются в `_shapes[(path, bytes)]` навсегда
(:182, 189-191) — при перезаписи файла того же размера форма останется старой.

### 5.5. `DELETE /api/media/{name}` (`files.py:43-48`)

Успех → `{"deleted": name}`; нет файла/чужой → `404 {"detail":"No such file."}`. Удаляет файл, для `.glb`
дополнительно `<id>.png` и `<id>.render.png` (media_store.py:233-236), строку `media` (:237-242) и запись
из кэша миниатюр (files.py:47).

### 5.6. Миниатюры

`GET /api/media/{name}/thumb` (files.py:94-105):

* принимаются только имена на `.png` и `.glb` (:96), иначе 404 (в т.ч. для mp4/wav);
* для `.glb` источник — `media_store.picture(path)`: render, иначе кадр-вырезка (:100, media_store.py:114-120);
* `THUMB_EDGE = 320` по длинной стороне, JPEG quality 80, альфа накладывается на серый `(236,236,236)`
  (:26, 51-62);
* LRU `_thumbs` на `MAX_THUMBS = 500` записей, ключ — имя файла (:27-28, 99-103);
* заголовок `Cache-Control: private, max-age=86400`, `media_type=image/jpeg` (:104-105).

`PUT /api/media/{name}/thumb` (files.py:76-91) — браузерный снимок GLB:

* только `name.endswith(".glb")` и только владелец, иначе пустой `404` (:80-82);
* тело — сырой PNG; пустое или `> MAX_RENDER_BYTES = 8 MiB` → `400 {"detail":"Send one PNG of the model."}`
  (:65, 83-85);
* `MAX_RENDER_EDGE = 2048` по длинной стороне, иначе `400 {"detail":"Not a readable image of a sensible size."}`
  (:66, 69-73, 86-89); сохраняется как PNG `<id>.render.png`;
* успех → `{"rendered": name}`, кэш миниатюры сбрасывается (:90-91).

---

## 6. `POST /api/uploads` (`daemon/routers/uploads.py`, `daemon/services/upload_service.py`)

**Мультипарта нет.** Тело запроса — сам файл, читается потоком (`uploads.py:36-42`). Никаких имён полей
(`file`, `image`) не существует; фронтенд шлёт `body: file` (`Files.jsx:82`).

Порядок:

1. Потоковое чтение, накопление в память; при `size > upload_service.MAX_BYTES` → `413`
   `{"detail":"Uploads can be at most 200 MB."}` (`uploads.py:37-41`). `MAX_BYTES = max(20 MB image,
   200 MB video, 50 MB audio) = 200 MB` (`upload_service.py:26-29`; `image_service.py:61`,
   `video_service.py:60-61`).
2. `upload_service.save(body)` в треде (`uploads.py:44`), ошибки `UploadError` → `400 {"detail": str}` (:45-46).
3. `media_store.record(<name>, user_id)` — модель `""`, `seconds` = NULL (`uploads.py:47`).
4. `url = origin + path`, где `origin = request_origin(request)` (учитывает X-Forwarded-*) либо
   `scheme://netloc` (`uploads.py:48-49`).
5. Ответ: `{"kind","id","path","url"}` + `width`,`height` только для изображений (`upload_service.py:86-112`,
   `uploads.py:49`). Поля `name` нет — фронт перезапрашивает список.

Определение типа (`upload_service.py:76-112`), порядок важен:

* **аудио** (`_is_audio`, :55-58): `RIFF…WAVE`, `fLaC`, `OggS`, `ID3`, или MPEG-frame sync `0xFFEx`;
  лимит 50 MB; всё, что не RIFF, прогоняется через `ffmpeg -vn` и сохраняется как `<id>.wav` (:61-73, 80-87);
* **видео** (`_is_mp4`, :45-52): `raw[4:8] == b"ftyp"` и major/compatible brand **не** из `IMAGE_BRANDS`
  (`mif1,msf1,heic,heix,heim,heis,hevc,hevx,avif,avis`, :37-38); лимит 200 MB; пишется как есть в
  `<id>.mp4` (:88-95);
* **изображение**: лимит 20 MB; `ImageOps.exif_transpose(...).convert("RGB")` → PNG, `<id>.png` (:97-112).
  Альфа при конвертации в RGB **теряется**, EXIF (включая GPS) отбрасывается;
* иначе `UploadError("Not an image, MP4/MOV video or sound file the Hub can read.")` (:108-109).
* пустое тело → `"The upload is empty."` (:78-79).

Аутентификация: `/api/uploads` под `AuthMiddleware`, ключ API здесь не действует
(`middleware/auth.py:48`); агент пользуется `create_upload` (§8).

---

## 7. Одноразовая загрузка и короткоживущая выгрузка

`daemon/services/link_service.py`, роуты — `daemon/routers/links.py`. Оба пути **вне** `/api`, `/v1`, `/mcp`,
поэтому `AuthMiddleware` их не трогает: единственный credential — токен в пути (`links.py:1-6`).

* `LINK_TTL = 15*60` (900 c), `UPLOAD_PREFIX="/link/upload/"`, `DOWNLOAD_PREFIX="/link/download/"` (:27-29).
* Токен — `secrets.token_urlsafe(32)`; в БД только `sha256(token)` (:32-33, 36-48).
* `create(kind, user_id, name="")` → `(token, expires_at)`; `name` заполняется только для download (:36-48).

### 7.1. `POST /link/upload/{token}` (links.py:18-31)

* `claim_upload(token)` — **один атомарный UPDATE**: `used_at = now WHERE token_hash=? AND kind='upload'
  AND used_at IS NULL AND expires_at > now`; `rowcount != 1` → ссылка непригодна (:58-72).
  Гонка двух запросов невозможна.
* Владелец берётся join'ом с `users` при `status='active'` (:51-55); если аккаунт заблокирован —
  `used_at` откатывается и ссылка считается негодной (:74-77).
* Тело — тот же приём, что `/api/uploads` (`receive(request, owner)`, links.py:12, 26), поэтому лимиты,
  типы и формат ответа идентичны §6, включая `url`.
* При **любой** неудаче (ответ `JSONResponse`, т.е. 400/413, или исключение) вызывается
  `release_upload(token)` — `used_at = NULL` (:29-31, 82-89), ссылка остаётся рабочей.
* Негодная ссылка → `404 {"detail":"This upload link is used, expired or invalid. Call create_upload for a new one."}`
  (:21-23).

### 7.2. `GET /link/download/{token}` (links.py:34-44)

* `download_target(token)` → `(name, owner)` при `kind='download' AND expires_at > now` и активном
  пользователе (:92-105). **Многоразовая** до истечения TTL — сознательно, выгрузки ретраятся
  (:11-12 в docstring).
* Файл обязан существовать и быть доступным владельцу (`media_store.find` + `can_read`), иначе 404:
  `{"detail":"This download link is expired or invalid, or the file was deleted. Call create_download for a new one."}`
  (:39-44).
* Отдача: `FileResponse` с `media_type=media_store.MEDIA_TYPES[path.suffix]`, `filename=name`,
  `Cache-Control: no-store` (:41-42).
* `purge_expired()` удаляет строки с `expires_at <= now` (:108-114) — раз в час из
  `_session_janitor` (`daemon/main.py:44-57`).

---

## 8. MCP: `POST /mcp`

### 8.1. Транспорт и протокол (`daemon/routers/mcp.py:1-18, 45-61, 1065-1101`)

* Streamable HTTP, **stateless**: один JSON-RPC объект на POST, без `Mcp-Session-Id`, без
  серверного стрима. `GET /mcp` и `DELETE /mcp` → `405` c `Allow: POST` (:1099-1101).
* `PROTOCOL_VERSIONS = ("2025-11-25","2025-06-18","2025-03-26")` (:45). `initialize` возвращает
  запрошенную версию, если она в списке, иначе **первую** (`2025-11-25`) (:1083-1089).
* `capabilities = {"tools": {"listChanged": false}}`; `serverInfo = {name:"spark-ai-hub",
  title:"Spark AI Hub", version:"1.0.0", icons:[data:image/png;base64,…128x128]}`, иконка читается из
  `frontend/public/icon-128.png` и вшивается в ответ (:49-54); плюс `instructions` — длинный
  текст-подсказка (:63-101, 1088).
* Прочие методы: `ping` → `{}`; `tools/list` → `{"tools": TOOLS}`; `tools/call` → §8.3;
  неизвестный метод → `-32601 "Method not found: <m>"` (:1090-1096).
* Ошибки разбора: невалидный JSON → `400` + `-32700 "Parse error"`; не объект/не `jsonrpc:"2.0"` → `400` +
  `-32600 "Expected one JSON-RPC 2.0 message"` (:1067-1073). Сообщение без `id` или без `method`
  (уведомление/ответ) → `202` без тела (:1075-1077).
* Аутентификация — общая с Хабом: API-ключ (`Authorization: Bearer`, `x-api-key`, `api-key`) или OAuth-токен;
  `/mcp` входит в `API_KEY_PREFIXES` (`middleware/auth.py:48`). Пользователь кладётся в `request.state.user`
  (:1025), `auth_source` — в `request.state.auth_source`.

### 8.2. `tools/list` — 21 инструмент (`mcp.py:126-570`)

`_TOOL_NAMES` строится из списка (:571). У всех схем `"additionalProperties": false`, если не указано иное.
Read-only инструменты помечены `annotations.readOnlyHint: true`; `delete_file` — `destructiveHint+idempotentHint`
(:458); `start_model`/`stop_model` — `idempotentHint: true` (:533, 550).

| # | tool | обязательные | параметры (тип, границы, дефолт) | строки |
|---|---|---|---|---|
| 1 | `web_search` | `query` | `query: str`; `max_results: int 1..20`, default 8 | 127-148 |
| 2 | `web_fetch` | `url` | `url: str` (https) | 149-165 |
| 3 | `generate_image` | `prompt` | `aspect_ratio` enum `["1:1","16:9","9:16","4:3","3:4","3:2","2:3"]` default `1:1`; `negative_prompt: str`; `seed: int ≥0`; `steps: int 1..60`; `model: str` | 166-192 |
| 4 | `edit_image` | `prompt`, `images` | `images: array[str] 1..3`; `seed ≥0`; `steps 1..60`; `model` | 193-216 |
| 5 | `get_image` | `job_id` | `job_id: str` | 217-232 |
| 6 | `generate_video` | `prompt` | `image: str`; `last_image: str`; `images: array[str] max 8`; `audio: str`; `video: str`; `seconds: int 1..15`; `aspect_ratio` enum `["16:9","9:16","21:9","4:3","1:1","3:4"]`; `seed ≥0`; `steps 1..100`; `model: str` | 233-286 |
| 7 | `get_video` | `job_id` | — | 287-302 |
| 8 | `list_video_models` | — | пустая схема | 303-311 |
| 9 | `generate_3d` | `image` | `views: object{left,back,right}`; `seed ≥0`; `steps 1..100`; `model` | 312-351 |
| 10 | `get_3d` | `job_id` | — | 352-367 |
| 11 | `list_3d_models` | — | пустая схема | 368-375 |
| 12 | `generate_music` | `lyrics`, `style` | `seconds: int 5..360`; `seed ≥0`; `model` | 376-399 |
| 13 | `get_music` | `job_id` | — | 400-414 |
| 14 | `create_upload` | — | пустая схема | 415-426 |
| 15 | `create_download` | `url` | `url: str` | 427-443 |
| 16 | `delete_file` | `url` | `url: str` | 444-459 |
| 17 | `decide` | `state`, `questions` | `state: any (text/JSON)`; `questions: object` без фиксированной схемы, `additionalProperties` = `{type: enum["choice","score","noul"], instructions: str, criteria: object\|array}`, `required:["type"]`, до 64 вопросов; `images: array max 4`; `videos: array max 2`; `model` | 460-514 |
| 18 | `start_model` | `model` | `_MODEL_ARG` (строка — id модели или `"searxng"`) | 515-534 |
| 19 | `stop_model` | `model` | то же | 535-551 |
| 20 | `list_decision_models` | — | пустая схема | 552-560 |
| 21 | `list_image_models` | — | пустая схема | 561-569 |

Общие фрагменты описаний: `_STEPS_HINT` (108-112) — «не задавать steps, брать рекомендованные»;
`_IMAGE_REFS` (119-124) — «только URL, не пути и не base64»; `_MODEL_ARG` (114-117).

### 8.3. Поведение инструментов (`call_tool`, mcp.py:603-725)

Диспетчеризация — цепочкой `if` по имени; неизвестное имя и неверные аргументы отсекаются раньше (:1019-1023):
`-32602 "Unknown tool: <n>"`, `-32602 "arguments must be an object"`.

* `web_search` (611-615): пустой `query` → ошибка; `web_service.search(query, max_results)` → текст (§11).
* `web_fetch` (616-620): `web_service.fetch(url, user)`; права `user` влияют на доступ к LAN.
* `list_image_models` / `list_video_models` / `list_3d_models` / `list_decision_models` (621-624, 660-663):
  `_with_memory(models)` (787-797) → JSON-строка `{"memory_free_gb","memory_total_gb","models":[…]}`,
  где остановленным моделям с известным `min_memory_gb` добавляется `fits_now: bool`.
* `create_upload` / `create_download` → `_link_tool` (§8.4).
* `delete_file` (627-631): `_own_file(args,user)` → имя; `media_store.delete`; ответ
  `content:[text "Deleted <name>."]`, `structuredContent:{"deleted":name}`.
* `decide` (632-642): строки `images`/`videos` нормализуются в списки, вызов `decision_service.decide(state,
  questions, images, videos, model, user)`; ответ — JSON в тексте и он же в `structuredContent`.
* `start_model` / `stop_model` (643-646) → §8.6.
* `generate_music` (647-656): обязательны оба поля; `audio_service.pick_backend` → slug;
  `image_service.start_job("music", …)`; ответ `_started("get_music", job_id, slug)`.
* `get_image` / `get_music` (657-659): `image_service.check_job(job_id, CHECK_WAIT=20, user)`,
  затем `_music_result` или `_image_result` по `info["kind"]` — **один и тот же** `get_image` собирает и музыку.
* `generate_3d` (664-677): требует `image`; `views` обязан быть объектом; `mesh_service.pick_backend(...,
  multi_view=bool(views))`; `start_job("mesh", …)` → `_started("get_3d", …)`.
* `get_3d` (678-679) → `_mesh_result`; `get_video` (680-682) → `video_service.check(job_id, CHECK_WAIT,
  on_progress, user)` → `_video_result`.
* `generate_video` (683-697): `video_service.start(...)` синхронно ставит задачу на сервере и возвращает
  локальный `job_id`; ответ `_started("get_video", …)`.
* `generate_image` / `edit_image` (699-721): `prompt` обязателен; `edit_image` требует 1..3 изображений;
  backend выбирается **сразу** (`pick_backend`), чтобы «нет запущенной модели» вернулось из этого вызова,
  а не из первого `get_*`; `image_service.start_job(kind, …)`.
* Ошибки: `ImageError`/`WebError` → `{"content":[{"type":"text","text":str}], "isError":true}` (722-723);
  любое другое исключение → `"<tool> failed: <Type>: <msg>"` с `isError:true` (724-725). Исключения
  наружу не выходят.

Формы ответов:

* `_not_done(tool, job_id, status, info)` (740-748) — текст «Not done yet: <status>. … call <tool> with
  job_id <id>», `structuredContent = {"status":"rendering", **info, "job_id", "done":false,
  "next_call":{"tool","arguments":{"job_id"}}}`.
* `_started` (751-753) — тот же `_not_done` со статусом `"job <id> started on <model>"` и `"model"`.
* `_image_result` (871-905): `url = origin + result.path`; текст `Image:/Model:/Size:/Seed:/Steps:/Preset:`;
  вторым блоком `{"type":"image","data":<preview JPEG b64>,"mimeType":"image/jpeg"}`;
  `structuredContent = {url, model, width, height, seed, steps}`.
* `_video_result` (908-929): `url = origin + "/videos/<video_id>.mp4"`; текст с `Size/Seconds/Seed/Steps` и
  `Render time`; постер — блок `image`, если есть; `structuredContent` — `info` без `poster` + `url`.
* `_mesh_result` (932-950): `url = origin + "/meshes/<mesh_id>.glb"`; `structuredContent` — `mesh` без
  `preview` + `url`; второй блок — `preview` (JPEG вырезки).
* `_music_result` (953-965): `url = origin + "/audio/<audio_id>.wav"`; `structuredContent` — `song` + `url`.
* `origin` — `request_origin(request)` или `scheme://netloc` (:1013-1014).

### 8.4. `create_upload` / `create_download` (`_link_tool`, mcp.py:983-1010)

Требуют пользователя (:985-986). `create_upload`: `link_service.create("upload", user.id)`; текст с
ссылкой, curl `curl -sS --data-binary @<your file> '<link>'` и лимитами; `structuredContent =
{"upload_url","method":"POST","curl","expires_at"}` (:988-1000). `create_download`: сначала `_own_file`
(имя из URL, файл существует и доступен — иначе `ImageError`), затем `create("download", user.id, name)`;
curl `curl -sSfL -o <name> '<link>'`; `structuredContent = {"download_url","file","curl","expires_at"}`
(:1002-1010). `_own_file` (968-980) отвергает не-Hub-URL и чужие/удалённые файлы, а при `user is None`
говорит «Files need a signed-in Hub account…».

### 8.5. Асинхронные задачи (`image_service.py:551-608`, `video_service.py:272-497`)

* `MAX_JOBS = 200` (image_service.py:558, video_service.py:69); при переполнении вытесняется задача с
  минимальным `created` — **в том числе ещё выполняющаяся** (image_service.py:562-564, video_service.py:272-274).
* `start_job(kind, work, model, user)` (567-590): `job_id = token_hex(16)`, запись
  `{job_id,status:"rendering",created,model,kind,result,error,user_id,done:Event}`, задача в
  `asyncio.create_task`; по завершении `status ∈ {"completed","failed"}`, `error` = str или `"Type: msg"`,
  `elapsed`.
* `owns_job` (593-595): при включённой авторизации собрать задачу может только её владелец; иначе
  `ImageError("No job …")` (601-602), для видео — «Unknown job_id. Video jobs are forgotten when the Hub
  restarts…» (video_service.py:449-450).
* `check_job(job_id, wait, user)` ждёт `min(wait, …)` и возвращает `{job_id,status,model,kind,result,error,
  elapsed}` (598-608). `video_service.check` опрашивает сервер каждые 5 c (video_service.py:67, 456-497),
  при `completed` копирует MP4 в `data/videos/<id>.mp4`, пишет `media` с фактическим временем
  (`completed_at - created`), для тега `orbit-360` перекодирует во all-keyframes, делает постер
  (`ffmpeg -ss 0.5 -frames:v 1 scale=512:-2`) (:469-488, 426-443, 410-423).

### 8.6. `start_model` / `stop_model` (mcp.py:756-868)

* Разрешены только **установленные** медиамодели (image/video/mesh/audio/decision) и `searxng`
  (`_startable_slugs`, :770-773; `MCP_APPS = {web_service.SEARXNG}`, :767). Иначе `ImageError` со списком
  установленных (:776-784). LLM и прочие приложения — только через UI.
* Старт: `_start_lock` (один старт за раз, :764), проверка `is_recipe_running`/`get_pending`, затем
  `_check_memory(slug)` (:800-816): `memory_plan` → при нехватке текст «<slug> needs about N GB and only
  M GB is free.» + перечисление работающих, отдельно те, что можно погасить `stop_model`.
  Затем `containers.launch_app(slug, user or {}, origin)`; `HTTPException` → `ImageError` (:819-830).
* Ожидание: до `CHECK_WAIT = 20` c, опрос `is_ready` каждые `READY_POLL_SECONDS = 2` с параллельным
  `start_health_check(slug)`; по таймауту — «Not done yet» с `next_call`, при остановке контейнера —
  ошибка «stopped before it finished loading» (:832-851).
* Стоп: уже остановлена → `status:"stopped"` без ошибки; если `image_service.renders_on(slug) +
  await video_service.rendering_on(slug) > 0` → отказ «is rendering N jobs right now…»; иначе
  `containers.stop(slug)`, при `status != "stopped"` — ошибка (:854-868).

### 8.7. Потоковая выдача `tools/call` (`_tools_call`, mcp.py:1017-1059)

* SSE включается **только** если `Accept` содержит `text/event-stream` (:1028-1029); иначе обычный JSON.
* Прогресс: `progressToken` берётся из `params._meta` (:1026). Уведомления
  `notifications/progress {progressToken, progress:<counter>, message}` шлются только при наличии токена;
  `progress` — счётчик 1,2,3… (:1041-1046).
* Каждые `KEEPALIVE_SECONDS = 10` без событий отправляется комментарий `": keepalive\n\n"` (:55, 1049-1050).
* Формат события: `event: message\ndata: <json>\n\n` (:586-587); финальный результат — обычный
  JSON-RPC `result` внутри `message`.
* Заголовки: `Cache-Control: no-cache`, `X-Accel-Buffering: no` (:1058-1059). При обрыве клиента задача
  отменяется, но рендер на сервере модели уже поставлен и доигрывается (:1052-1056).

### 8.8. Отдача артефактов `/images|/videos|/audio|/meshes/{name}` (mcp.py:1104-1153)

| путь | суффикс | media type |
|---|---|---|
| `/images/{name}` | `.png` | `image/png` |
| `/videos/{name}` | `.mp4` | `video/mp4` |
| `/audio/{name}` | `.wav` | `audio/wav` |
| `/meshes/{name}` | `.glb` | `model/gltf-binary` |

* Аноним при `auth_enabled`: при `Accept: text/html` — HTML-страница «This file is private…» со
  статусом 401, иначе `401` JSON `{"detail":"Authentication required: sign in to the Hub, or ask the MCP
  tools for a create_download link."}` и `WWW-Authenticate: Bearer realm="Spark AI Hub"` (:1112-1128).
* Имя обязано заканчиваться нужным суффиксом, иначе 404; чужой файл отвечает **ровно как отсутствующий**
  (`404` без тела) (:1129-1132).
* Кэш: `private, max-age=3600` для загрузок (каталог `uploads`), `private, max-age=86400, immutable`
  для результатов (:1104-1109).

---

## 9. OpenAI-совместимый прокси: `/v1/{path}`

Файл `daemon/routers/openai_proxy.py`, префикс `/v1` (:30), catch-all на методы
GET/POST/PUT/PATCH/DELETE/OPTIONS (:305-308). Монтируется **после** anthropic-роутера, иначе `/v1/messages`
поглотился бы (`daemon/main.py:102-105`).

### 9.1. Разрешение целевой модели

* `_running_slugs()` — один `docker ps --format '{{.Label "com.docker.compose.project"}}'`, берутся
  проекты с префиксом `spark-ai-hub-` (:72-87).
* Кандидаты — рецепты этих слагов, у которых `is_llm` (`category=="llm" and ui.type=="api-only"`, :114).
* `_upstream(session, recipe)` (:90-105): `GET {internal_url(slug)}/v1/models` с probe-заголовком;
  не-200/исключение → `None` (модель ещё грузится). Каждой записи добавляются/заполняются
  `capabilities` (из `recipe.capabilities`), `params_b`, `recipe=slug`. `model` апстрима = `entries[0]["id"]`.
* `_upstreams()` (:108-130): кэш `_UPSTREAM_CACHE` c TTL **5 c** (:68-69); сортировка «largest first» по
  `(params_b or 0, memory_gb)` по убыванию (:117-118). Если один и тот же `id` отдают несколько сборок,
  **все** такие записи переименовываются в slug рецепта (:121-126), иначе по имени была бы достижима
  только одна.
* `route(requested)` (:133-146): совпадение по `slug`, по `model` апстрима или по любому `entries[].id`
  → этот апстрим; иначе — **самый крупный** (`ups[0]`). `None` только если ничего не запущено.
* `internal_url(slug) = http://127.0.0.1:{public_port}/run/{slug}` (`proxy_service.py:104-107`),
  `probe_headers() = {"X-Sah-Probe": <token>}` (:77-101) — токен из `data/proxy-probe-token`.

### 9.2. `GET /v1/models` (openai_proxy.py:310-313)

Собственный ответ Хаба: `{"object":"list","data":[<все entries всех живых апстримов>]}`. Поля записи:
`id`, `object`, `created`, `owned_by` (что отдал сервер) + `capabilities: list[str]`,
`params_b: float|null`, `recipe: str`. Порядок — порядок апстримов (крупные первыми).
Фронтенд читает только `data` (`pages/Agents.jsx:57-63`).

### 9.3. Трансформация запроса (openai_proxy.py:315-375)

1. Тело читается целиком; JSON парсится для POST/PUT/PATCH (:315-321).
2. `route(body.get("model"))`; ничего не запущено → `503` и тело
   `{"error":{"type":"no_model_running","message":"No model is currently running. Launch one from the Spark AI Hub first."}}`
   (:149-158, 323-325).
3. `url = {upstream.base}/{path}` (:327) — путь и query пробрасываются как есть (:402).
4. `model` в теле **перезаписывается** на id апстрима (:333-334).
5. PDF: part `{"type":"file","file":{"file_data":"data:application/pdf;base64,…"}}` превращается в
   `{"type":"text","text":"[PDF attachment: <filename>]\n<текст>"}` через `pdftotext -layout`
   (таймаут 30 c, нет бинаря/ошибка — часть остаётся как была) (:251-302, 336-337).
6. `role:"developer"` → `"system"` в `messages` (:342-347); элементы `input[]` с `role:"developer"`
   вынимаются и их текст дописывается в `instructions` (через `\n\n`), `input` — без них (:348-371).
7. Заголовки вверх: исходные минус hop-by-hop и минус **все** credential-заголовки
   `authorization`, `x-api-key`, `api-key`, `cookie` (:33-41, 165-167), плюс probe-заголовок (:375).

### 9.4. Учёт токенов (openai_proxy.py:170-248)

* `_usage_from(payload)` (:172-196): читает `usage.prompt_tokens|input_tokens`,
  `completion_tokens|output_tokens`, `total_tokens` (иначе сумма), `model`; при отсутствии `usage`
  рекурсивно смотрит `payload["response"]` (событие `response.completed`).
* `record_call(request, endpoint, usage)` (:230-248): без пользователя — no-op; иначе
  `auth_service.record_usage(user_id, model, endpoint, prompt/completion/total, source=request.state.auth_source)`.
  `endpoint` = `"openai:{path.strip('/')}"` (:403).
* Стрим: если клиент не просил `stream_options.include_usage`, прокси **сам** включает его
  (`include_usage: true`) и потом вырезает лишний usage-only чанк (:377-400, 199-227). Удаляется строго
  чанк с `object=="chat.completion.chunk"`, `usage` — объект и пустыми `choices`; терминальное событие
  Responses API не трогается (:199-227).

### 9.5. Стриминг и обычный ответ

* Стрим (:405-432): `aiohttp` без таймаутов (`total=None, sock_read=None`), построчная буферизация по
  `\n`, каждый SSE-чанк проходит через `_inspect_sse_line`; usage запоминается, лишний чанк не отдаётся;
  в конце — `record_call`; `media_type="text/event-stream"`.
* Не-стрим (:434-454): таймаут 600 c; тело и **статус апстрима** возвращаются как есть, заголовки —
  минус hop-by-hop, `media_type` = `content-type` апстрима; usage пишется только при `status==200` и
  методе POST.

---

## 10. Anthropic-совместимые ручки

`daemon/routers/anthropic_proxy.py`. Маршрутизация — та же `route()` из openai_proxy (:22, 437).

### 10.1. `POST /v1/messages` (426-488)

Запрос:

* невалидный JSON → `400 {"type":"error","error":{"type":"invalid_request_error","message":"invalid JSON body"}}` (:428-434);
* `requested_model = body.model or "claude"` (:436) — любое имя Claude уходит на самый крупный апстрим;
* конвертация `_anthropic_to_openai` (76-196):
  * `model` = id апстрима; `stream = bool(body.stream)`;
  * `max_tokens`, `temperature`, `top_p` — как есть; `stop_sequences` → `stop`; при стриме
    принудительно `stream_options.include_usage = true` (:79-87);
  * `system` (строка или блоки `text`) → ведущее сообщение `system` (:90-91, 37-47);
  * `user`: блоки `text`/`image` → части `text`/`image_url` (base64 → `data:<media_type>;base64,…`,
    иначе `source.url`); `tool_result` → отдельное сообщение `role:"tool"` с `tool_call_id` и текстом,
    а изображения из него — следующим `user`-сообщением с префиксом `[tool_result images]`
    (:103-143, 199-223). Если изображений в user-сообщении нет — контент склеивается в строку (:226-229);
  * `assistant`: `text` → content, `tool_use` → `tool_calls[{id,type:"function",function:{name,
    arguments: json.dumps(input)}}]`, `content: null` при пустом тексте (:145-168);
  * `_hoist_system_messages` (50-73): все `system`-сообщения склеиваются в одно ведущее через `\n\n`
    (иначе SGLang отвечает `System message must be at the beginning.`);
  * `tools[].input_schema` → `function.parameters` (дефолт `{"type":"object","properties":{}}`) (:172-183);
  * `tool_choice`: `auto`→`"auto"`, `any`→`"required"`, `tool`→`{"type":"function","function":{"name"}}`,
    `none`→`"none"` (:185-194).
* `url = {upstream.base}/chat/completions`, заголовки — только probe (:443-444).

Ответ, не-стрим (:467-488): `data = await r.json()` (при `ContentTypeError` — `{"error":{"message":<text[:500]>}}`);
не-200 → тот же статус и `{"type":"error","error":{"type":"api_error","message":<сообщение из тела, иначе
json.dumps(data)[:800], иначе "upstream error">}}`. Успех — `_openai_to_anthropic` (242-275):
`id` (или `msg_<24hex>`), `type:"message"`, `role:"assistant"`, `model` = **запрошенное** имя,
блоки: `thinking` (из `reasoning_content` или `reasoning`), `text`, `tool_use` (`input` из
`json.loads(arguments)`, при ошибке `{}`), `stop_reason` по `_FINISH_TO_STOP`
(`stop→end_turn`, `length→max_tokens`, `tool_calls→tool_use`, `content_filter→end_turn`, `None→end_turn`,
28-34), `stop_sequence:null`, `usage{input_tokens, output_tokens}`. Usage пишется в `record_call` с
endpoint `"anthropic:messages"` (:487).

Стрим (:446-465): при не-200 апстрима отдаётся **200 + SSE** `event: error` c
`{"type":"error","error":{"type":"api_error","message":<text[:500]>}}` и стрим закрывается (:454-461).
Иначе `_stream_translate` (282-423) генерирует последовательность Anthropic-событий:
`message_start` (пустой content, usage 0/0) → `content_block_start`/`content_block_delta`
(`thinking_delta`, `text_delta`, `input_json_delta`) → `content_block_stop` для каждого блока →
`message_delta` (`stop_reason`, `usage.output_tokens`) → `message_stop`. Индексы блоков выделяются
по мере появления (`next_idx`), thinking-блок закрывается при первом тексте/tool_call (:361-363).
Usage собирается в `usage_sink` и пишется после закрытия стрима (:334-337, 450-464).

### 10.2. `POST /v1/messages/count_tokens` (491-506)

Тело не валидируется (при ошибке парсинга — `{}`). Возврат:
`{"input_tokens": max(1, len(json.dumps({"system","messages","tools"})) // 4)}` — оценка по символам,
картинки не учитываются. Нужна, чтобы Claude Code не получал 404 на каждом ходу.

---

## 11. Сервисы mesh и web

### 11.1. 3D (`daemon/services/mesh_service.py`)

* Backend'ы: теги `openai-meshes` + `image-to-3d`; `multi-view` → принимает `views`;
  `background-removal` → вырезает фон сам (:76-81, 51-54).
* `VIEWS = ("left","back","right")` (:54); неизвестный вид → `ImageError` (:181-182).
* Вырезка фона (`cut_out`, 129-162): если прозрачных пикселей ≥ 5 % — картинка берётся как есть;
  иначе flood-fill от краёв с `CUT_TOLERANCE = 35` и шагом `EDGE_STEP = 8`, альфа = инверсия разницы,
  эрозия `MinFilter(3)`; доля фона обязана быть в `[0.05, 0.97]`, иначе отказ с инструкцией
  «one object on a plain background…» (:156-160).
* Запрос: multipart `image`, поля видов (`left`/`back`/`right`), `seed`, `output_format=glb`,
  `num_inference_steps` при заданных steps (:194-202); опрос `GET /v1/meshes/{id}` каждые 3 c,
  затем `/content` (:208-222).
* Файлы: `<id>.png` (вырезка) и `<id>.glb` (:224-227); `media.record` с временем (:228-229).
* Возврат `generate`: `{mesh_id, model, seed, steps, views:["front",…], bytes, inference_time_s, preview}`
  (:230-233); `list_models` — `{model,name,accepts,views?,output,background,state,min_memory_gb,
  render_seconds…}` (:89-107).

### 11.2 Web (`daemon/services/web_service.py`)

* `search` (124-142): `POST {internal_url("searxng")}/search` с формой `{q, format:"json"}` (POST, чтобы
  запрос не попал в логи); `count = clamp(max_results or 8, 1, 20)` (:30-32, 125). Ошибка/не-200 →
  `WebError` с подсказкой про `start_model("searxng")`. Формат ответа (`format_results`, 145-167):
  инфобоксы, затем нумерованный список `N. title` / URL / сниппет / `Published: YYYY-MM-DD`,
  в конце «Read a result in full with web_fetch. Cite the URLs you use.»; пусто → `"No results."` +
  перечень неответивших движков.
* `fetch` (177-229): только `http`/`https`; проверка адреса через `reach()` (:80-107) — публично можно
  всем, LAN/tailnet только `role=="admin"`, сам Spark (loopback, свои интерфейсы, подсети docker-бриджей)
  запрещён всем; `http` вне LAN запрещён (:197-199); до 5 редиректов вручную (:33, 188-212);
  лимит тела 5 MiB (:34, 206), обрезка текста 80 000 символов с примечанием (:35, 225-229);
  cookie не отправляются и не сохраняются (`DummyCookieJar`), TLS — стоковый контекст Python (:183-187);
  User-Agent — Chrome 140 (:40-41); HTML → markdown (`BeautifulSoup` + `markdownify`, вырезаются
  script/style/noscript/svg/nav/header/footer/aside/form/iframe/button, ссылки абсолютизируются,
  приоритет `main`→`article`→`body`, заголовок `# <title>`) (:232-243, 44-45); не-HTML/не-текст →
  `WebError` (:221-223).

### 11.3 Прочие медиасервисы (для полноты контракта MCP-инструментов)

* **image** (`image_service.py`): backend'ы по тегам `openai-images` / `openai-chat-images` /
  `hidream-app` (+ `text-to-image`/`image-edit`) (:102-121). Размеры `OPENAI_SIZES` — 1:1 1024²,
  16:9 1344×768, 9:16 768×1344, 4:3 1152×864, 3:4 864×1152, 3:2 1216×832, 2:3 832×1216 (:95-99);
  enum `aspect_ratio` в MCP = эти 7 ключей. Edit: размер ~1 МП, кратный 32 (:222-232).
  Тело генерации: `n=1, size, response_format:"b64_json", output_format:"png", seed`, guidance,
  `preset` **или** `num_inference_steps`, `negative_prompt` (:242-260). Результат: PNG в
  `data/images/<id>.png` (альфа сохраняется, если была), inline-превью JPEG 512 px q85 на белом
  (:481-506); seed генерируется на стороне Хаба (`secrets.randbelow(2**31-1)`) (:535).
* **video** (`video_service.py`): backend'ы по тегу `openai-videos`; `TAG_KINDS` — какие входы
  принимает модель (:47-54). Ограничения: `MAX_SECONDS = 15` в схеме инструмента, фактические
  `max_seconds` — из рецепта (дефолт 10) (:56-59, 84-86). Две формы запроса: multipart
  (`input_reference`/`input_references`) и JSON с `extra_params.conditions` (data URI, keyframe/reference)
  (:333-361); конвертация секунд в кадры 4k+1 (:174-179).
* **audio** (`audio_service.py`): теги `openai-music` + `text-to-music`; `POST /v1/audio/speech` с
  `{input: lyrics, instructions: style, seed, max_new_tokens: seconds*25, response_format:"wav"}`;
  секунды зажимаются в 5..360 (:36-37, 72-81); файл `data/audio/<id>.wav`, длина из WAV-заголовка (:107-114).

---

## 12. Чеклист парности (проверяемые поведения)

1. `GET /api/recipes` → 200, массив; в каждом элементе **всегда** есть `name`, `slug`, `memory_gb`,
   `capabilities`, `media_capabilities`, `app_url`; отсутствующие опциональные поля — `null`, не пропущены.
2. `memory_gb == max(requirements.min_memory_gb, reserved_gb)`; без флагов reserve → равен `min_memory_gb`.
3. `capabilities == []` при `integration: null`; иначе начинается с `completion`, содержит `vision` для
   тегов `vision`/`multimodal`, `tools` для `tool-use`, `thinking` для `reasoning`, и отсортирован.
4. `media_capabilities` соответствует таблице §1.5 и идёт в порядке словаря, а не в порядке тегов.
5. `app_url` = `/run/{slug}/` (proxy+web), `/run/{slug}{ui.path}` (proxy+api-only), `""` иначе.
6. YAML-ключи `docker.build_time_minutes` и `tested_on` **не появляются** в JSON.
7. `context_tokens` берётся из yaml, иначе из `context_length`, иначе из compose-флага
   (`--max-model-len`, `--max-seq-len`, `--max-total-tokens`, `--ctx-size`, `-c`), включая резолв `${VAR:-default}`.
8. `reserved_gb == round(fraction*MemTotal_GiB, 1)` для `--gpu-memory-utilization`/`--mem-fraction-static`;
   `0.0`, если флага нет.
9. Рецепт без `name`/`slug` или с неполной записью `arena` пропускается, сервис стартует; лог содержит
   `[registry] Failed to load`.
10. Ключ рецепта — `slug` из yaml, и он же используется для поиска каталога (`get_recipe_dir`).
11. `?category=all` не фильтрует; фильтр сравнивает с `categories`, а при пустом — с `category`.
12. `?search=` ищет по `name` без регистра и по тегам **с** регистром; комбинация — И.
13. `GET /api/recipes/{unknown}` → 404 `{"detail":"Recipe not found"}`.
14. Установленный и работающий контейнер при `pending=="launching"` даёт `starting=true, running=false`;
    при `pending=="stopping"` — оба `false`; при `pending=="installing"` — `installing=true`.
15. `runtime_env_path` заполняется, если есть `.env` **или** `.env.example`, и всегда указывает на `.env`.
16. `/covers/<stem>-poster.jpg`, `/covers/<stem>-backdrop.jpg`, `/cover-sources/<cover.image>` отдаются
    статикой; отсутствующий ассет с известным суффиксом → 404, не `index.html`.
17. `GET /api/media` возвращает только свои файлы (`{"files":[…]}`), с `name,kind,upload,path,bytes,
    created,model,orbit,seconds` и `width/height/duration` там, где файл читается; сортировка по
    `created` desc.
18. `rendered` присутствует **только** у `kind=="mesh"`; при наличии `<id>.render.png` — `true`.
19. `DELETE /api/media/{name}` → `{"deleted":name}`; чужой/несуществующий → 404 `{"detail":"No such file."}`;
    удаление `.glb` уносит `<id>.png` и `<id>.render.png`.
20. `GET /api/media/{name}/thumb` для `.png`/`.glb` → JPEG, длинная сторона 320, фон `(236,236,236)`,
    `Cache-Control: private, max-age=86400`; для `.mp4`/`.wav` → 404.
21. `PUT /api/media/{name}/thumb` принимает только `.glb` и только у владельца; тело > 8 MiB → 400;
    сторона > 2048 → 400; успех → `{"rendered":name}` и файл `<id>.render.png`.
22. `POST /api/uploads` принимает **сырое тело** без multipart и без имён полей; > 200 MiB → 413
    `"Uploads can be at most 200 MB."`; пустое тело → 400 `"The upload is empty."`.
23. Загрузка нормализуется: звук → `<id>.wav` (не-RIFF через ffmpeg), видео → `<id>.mp4` при `ftyp` с
    не-изображенческим brand, изображение → `<id>.png` RGB с применённым EXIF-поворотом; ответ содержит
    `kind,id,path,url` (+`width`,`height` для изображений), `url` абсолютный с учётом X-Forwarded-*.
24. `/api/uploads` не принимает API-ключ (только сессия) → 401 при анониме и включённой авторизации.
25. `POST /link/upload/{token}` — ровно один успешный аплоад; повтор → 404 с текстом «used, expired or
    invalid»; отклонённый аплоад (400/413) ссылку **не** расходует.
26. `GET /link/download/{token}` многоразовый до истечения TTL=900 c, отдаёт `Cache-Control: no-store` и
    `filename=<name>`; после истечения/удаления файла → 404 с текстом «expired or invalid, or the file
    was deleted».
27. В БД лежит только `sha256(token)`; просроченные строки `media_links` чистятся фоновой задачей раз в час.
28. `POST /mcp` `initialize` возвращает запрошенную версию из `{"2025-11-25","2025-06-18","2025-03-26"}`,
    иначе `2025-11-25`; `capabilities.tools.listChanged == false`; есть `instructions`.
29. `GET /mcp` и `DELETE /mcp` → 405 с `Allow: POST`; уведомление без `id` → 202; битый JSON → 400 `-32700`;
    не JSON-RPC 2.0 → 400 `-32600`; неизвестный метод → `-32601`.
30. `tools/list` отдаёт ровно 21 инструмент с указанными именами, `inputSchema` (`additionalProperties:false`
    и границы из §8.2) и `annotations` там, где они есть.
31. `tools/call` с неизвестным именем → `-32602 "Unknown tool: <n>"`; `arguments` не объект → `-32602`.
32. `generate_*` возвращает `job_id` немедленно, `structuredContent.done == false` и `next_call` с
    `{tool, arguments}`; `get_*` ждёт до 20 c и затем отдаёт «Not done yet» либо результат/ошибку.
33. Задачу может собрать только её владелец: чужой `job_id` → ошибка «No job …» (изображения/музыка/3D)
    или «Unknown job_id…» (видео).
34. `start_model` отказывает для неустановленной/не медиамодели, перечисляя установленные; при нехватке
    памяти текст содержит «needs about N GB and only M GB is free»; `stop_model` отказывает, пока модель
    рендерит задачу, и является идемпотентным для уже остановленной.
35. `GET /images|videos|audio|meshes/{name}`: аноним при включённой авторизации → 401 (HTML при
    `Accept: text/html`, иначе JSON с `WWW-Authenticate`); чужой файл → пустой 404; кэш
    `private,max-age=3600` для загрузок и `private,max-age=86400,immutable` для результатов.
36. `GET /v1/models` — объединённый список живых апстримов с `capabilities`, `params_b`, `recipe`;
    дублирующиеся `id` переименованы в slug рецепта.
37. `/v1/{path}` маршрутизируется по `slug`, `model` или `id` из списка, иначе — на самый крупный апстрим
    (`(params_b, memory_gb)` desc); при отсутствии живых моделей — 503 `no_model_running`.
38. Поле `model` в JSON-теле перезаписывается на id апстрима; заголовки `authorization`, `x-api-key`,
    `api-key`, `cookie` и hop-by-hop **не** уходят на контейнер; добавляется `X-Sah-Probe`.
39. `role:"developer"` в `messages` → `system`; `input[]` с `role:"developer"` переносится в `instructions`;
    `data:application/pdf` part → текст `[PDF attachment: …]`.
40. Стрим `/v1/*`: при отсутствии `stream_options.include_usage` прокси добавляет его, usage-only чанк
    клиенту не отдаётся, usage записывается с endpoint `openai:{path}`; Responses-API событие
    `response.completed` не вырезается.
41. Не-стрим `/v1/*`: статус и тело апстрима возвращаются без изменений; usage пишется только при 200 POST;
    таймаут 600 c.
42. `POST /v1/messages`: конвертация system/user/assistant/tool_result/tools/tool_choice по §10.1;
    ответ — Anthropic-форма с `stop_reason` по таблице и `usage{input_tokens,output_tokens}`.
43. `POST /v1/messages` со `stream:true` отдаёт последовательность `message_start … message_stop`;
    не-200 апстрима превращается в SSE `event: error` со статусом 200.
44. `POST /v1/messages/count_tokens` → `{"input_tokens": max(1, len(json)/4)}` и никогда не 404.
45. `web_search` клампит `max_results` в 1..20 (дефолт 8) и шлёт POST-форму `{q, format:"json"}` на SearXNG;
    `web_fetch` отказывает на `http` вне LAN, на непубличные адреса и на не-HTML/текст, обрезает до 80 000
    символов и не отправляет cookie.
46. `generate_3d` при непрозрачном фоне без «плоского» фона (доля 5–97 %) отказывает с инструкцией; при
    прозрачном PNG отправляет картинку как есть; сохраняет `<id>.glb` и `<id>.png`.

---

## Риски и открытые вопросы (для родительской задачи)

* Гонка на общем состоянии: `recipes.py` мутирует глобальные `Recipe` (§3.3) — в Go нужен снимок на запрос
  или блокировка; иначе `installed/running/error` будут «мигать» между пользователями.
* `has_leftovers` липкий (§3.3), `_shapes` в `media_store` не инвалидируется при перезаписи файла (§5.4),
  `_thumbs` инвалидируется только на delete/PUT (§5.6) — поведение придётся воспроизвести намеренно.
* `record()` не пишет строку при `user_id is None` (§5.3): файлы, созданные без авторизации, позже
  становятся «админскими». Это же поведение задаёт и `/api/uploads` без сессии (если auth выключен).
* Вытеснение задач по `MAX_JOBS` убивает самую старую, даже выполняющуюся (§8.5) — «исчезающий» job_id
  это ожидаемое поведение, а не баг порта.
* Не воспроизведено (и не должно): `strip_prefix`, `proxy`, `health_path` используются Caddy-конфигом
  `proxy_service`, а не Python-кодом реестра; их парность проверяется отдельно по `/run/{slug}/`.
