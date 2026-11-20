# gitlab-search (Go)

[English](README.md) · Русский

Поиск строки в коде всех проектов GitLab-групп

## Сборка

```powershell
go build -o .build\bin\gitlab-search.exe .\cmd\gitlab-search
```

## Быстрый старт

```powershell
# один раз: создать конфиг для внутреннего GitLab
gitlab-search setup --token <personal-access-token> --base-url https://git.digital.rt.ru --ignore-ssl
# токен должен иметь область read_api

# поиск
gitlab-search "here-is-my-search-term"
gitlab-search "term" -g my-group -f "*.go" -p services/
```

`setup` не требует обязательных аргументов: без флагов он пишет конфиг целиком со значениями
по умолчанию и `token: ""`. Такой конфиг сам по себе рабочий, но поиск из него остановится
на проверке обязательных параметров и выдаст ошибку с указанием, где взять токен
(`setup --token`, ключ `token`, переменная `GITLAB_SEARCH_TOKEN`).

Можно и без конфиг-файла, указав сервер и токен флагами:

```powershell
gitlab-search --base-url https://git.digital.rt.ru --token <pat> "term"
```

## Конфигурация

По умолчанию — `.gitlab-search.yaml`: ищется в текущей директории и всех родительских,
затем в домашней директории. Полный файл со всеми параметрами и значениями по умолчанию
создаётся командой `setup --full`, в произвольное место — `setup --config-file <путь>`.

| Ключ | По умолчанию | Назначение |
|---|---|---|
| `domain` | `gitlab.com` | root URL GitLab (`http://...` включает HTTP) или голый домен (HTTPS) |
| `token` | — | personal access token с областью `read_api` (обязательно) |
| `ignore_ssl` | `false` | пропускать проверку TLS-сертификата |
| `ca_file` | — | PEM-файл собственного CA (проверка сертификата остаётся включённой) |
| `concurrency` | `25` | максимум одновременных запросов к API |
| `rate_limit` | `0` | запросов в секунду, `0` — без троттлинга |
| `retry_count` | `5` | число повторов при 429/5xx/сетевых сбоях |
| `log_level` | `info` | `debug`, `info`, `warn`, `error` |

Остальные опции поиска тоже можно задать один раз в конфиг-файле — они применяются,
если соответствующий флаг не передан явно:

| Ключ | По умолчанию | Флаг | Назначение |
|---|---|---|---|
| `groups` | — | `-g/--groups` | список групп через запятую (пусто — все видимые токену) |
| `filename` | — | `-f/--filename` | искать только в файлах, подходящих под glob |
| `extension` | — | `-e/--extension` | искать только в файлах с этим расширением |
| `path` | — | `-p/--path` | искать только файлам под этим путём |
| `archive` | `all` | `-a/--archive` | `all`, `only` или `exclude` — как поступать с архивными проектами |
| `format` | `plain` | `--format` | формат вывода: `plain` или `json` |
| `output` | — | `-o/--output` | файл для полного вывода запуска (параметры, найденное, отчёт) |
| `quiet` | `false` | `--quiet` | не печатать результаты в консоль (нужен `output`) |

```yaml
domain: https://git.digital.rt.ru
token: glpat-xxxx
ignore_ssl: true
concurrency: 25
retry_count: 5
groups: pcp,billing        # искать только в этих группах
extension: yaml            # только *.yaml
output: found.txt          # писать в файл
quiet: true                # ...и не дублировать в консоль
```

Переопределение через окружение: `GITLAB_SEARCH_TOKEN`, `GITLAB_SEARCH_DOMAIN`,
`GITLAB_SEARCH_CONCURRENCY`, `GITLAB_SEARCH_RATE_LIMIT`, `GITLAB_SEARCH_RETRY_COUNT`,
`GITLAB_SEARCH_IGNORE_SSL`, `GITLAB_SEARCH_CA_FILE`, `GITLAB_SEARCH_LOG_LEVEL`.

Приоритет: **флаги CLI > переменные окружения > конфиг-файл > встроенные значения по умолчанию**.
Обратная сторона: `setup` перезаписывает файл целиком, поэтому ключи, дописанные вручную,
при следующем `setup` теряются (кроме случая `--full`, где они пишутся со значениями по умолчанию).

## Алгоритм поиска

1. **Группы.**
   - Если передан `-g/--groups a,b` — имена используются напрямую, запрос к списку групп не выполняется.
   - Иначе `GET /api/v4/groups?per_page=100` и обход пагинации по заголовку `Link` (`rel="next"`),
     пока страницы не закончатся. Собираются все группы, видимые токену (личные проекты не индексируются — только групповые).
2. **Проекты групп.**
   Для каждой группы параллельно `GET /api/v4/groups/<id>/projects?per_page=100`
   (+ `archived=true|false` для `--archive only|exclude`). Пагинация — так же по `Link`.
   Группа, для которой листинг так и не удался, пропускается с предупреждением, остальные продолжают работу.
   Проекты дедуплицируются по id (первое вхождение), итоговый порядок — группы, затем проекты внутри группы.
3. **Поиск по каждому проекту.**
   `GET /api/v4/projects/<id>/search?scope=blobs&per_page=100&search=<выражение>`,
   где `<выражение>` — искомая строка плюс встроенные фильтры GitLab:
   `filename:<glob>` (`-f`), `extension:<ext>` (`-e`), `path:<путь>` (`-p`).
   Фильтры выполняются сервером, локальной пост-фильтрации нет. Ищется в ветке по умолчанию проекта;
   имя ветки возвращается в каждом результате (`ref`).
   Воркеры ограничены `concurrency`, темп — `rate_limit`.
4. **Сбор результатов на диск (spill).**
   Страницы хитов каждого проекта дописываются в JSONL-файл `.gitlab-search-tmp/project-NNNNNN.jsonl`
   по мере получения — в памяти одновременно находится только одна страница,
   поэтому потребление памяти не зависит от общего числа совпадений.
   Если проект «упал» на средней странице, уже полученные страницы не теряются и попадут в вывод.
5. **Финальный проход и вывод.**
   После завершения всех воркеров файлы читаются в исходном порядке групп/проектов и стримятся
   в выбранный вывод: консоль, файл (`-o/--output`), только файл (`-o ... --quiet`), формат `plain` или `json`.
   В вывод идут и блок параметров, и итоговый отчёт — файл содержит ровно то же, что показала консоль.
   Проекты без хитов не печатаются. В plain-формате на каждый хит выводятся permalink
   `<web_url>/blob/<ref>/<filename>#L<startline>` и фрагмент кода; в json — пары `project`/`hits`.
   Директория `.gitlab-search-tmp/` удаляется при выходе.

## Устойчивость к 429 и сбоям

- Повторяются ответы `429`, `500`, `502`, `503`, `504` и сетевые ошибки; `401/403/404` — сразу ошибка без повторов.
- Пауза перед повтором растёт по лестнице **5s → 15s → 30s → 45s → 60s** (дальше всегда 60s)
  плюс джиттер 0–20 %, чтобы потоки не синхронизировались; минимум — ровно указанное число секунд.
- `Retry-After` соблюдается только тогда, когда сервер просит больше, чем весь шаг лестницы (>60s).
  GitLab отвечает `Retry-After: 60` на каждый ограниченный запрос, и без этого правила паузы
  не росли бы, а все повторы ждали бы одну и ту же минуту.
- Число повторов — `retry_count` (по умолчанию 5). После исчерпания проект помечается пропущенным
  (`project skipped after retries`), поиск продолжается, собранные результаты выводятся.
- `rate_limit` и `concurrency` позволяют не доводить сервер до 429: первый ограничивает темп, второй — параллельность.
- Код возврата: `0` при успехе и при частичном успехе (есть результаты), `1` — фатальная ошибка
  (нет конфига/токена, 401, упали все группы или все проекты).

## Логирование

Логи идут в stderr и не попадают в файл результатов. По умолчанию `info` показывает все фазы:
`listing groups` → `groups resolved` → `group projects fetched` (на каждую группу) →
`projects resolved` → `search started` → `progress` (каждые 10 проектов) → `search finished`.
`--log-level debug` добавляет каждый HTTP-запрос (`Requesting GET ...`) и списки групп/проектов;
переменная `DEBUG` (любое значение) включает debug как в оригинальном пакете.

## Параметры запуска и итоговый отчёт

Две служебные полосы — начало и конец запуска — печатаются независимо от `--log-level`
(в отличие от логов) и входят в вывод наравне с результатами: без `-o` они в консоли,
с `-o` — в консоли и в файле, с `-o --quiet` — только в файле (в консоль они при этом
дублируются в stderr, чтобы запуск был видно и без открытия файла). Файл результатов
содержит полную запись запуска: параметры, найденное, отчёт.

В начале, ещё до первого запроса, показаны фактически применённые значения после разрешения
приоритета «флаги > env > конфиг > defaults» — по ним сразу видно, что конфигурация прочиталась не та:

```
──── run parameters ────
version:   1.0.0
term:      "here-is-my-search-term"
config:    C:\work\.gitlab-search.yaml
api:       https://git.digital.rt.ru/api/v4
token:     glp*******************abc
tls:       certificate checks SKIPPED (--ignore-ssl)
groups:    pcp,billing
filters:   extension=yaml  path=roles  archive=all
output:    console and file found.txt (plain)
limits:    concurrency=25 rate_limit=0/s retry_count=5 (5s, 15s, 30s, 45s, 60s) log_level=info
```

Токен выводится замаскированным (первые и последние 3 символа), полное значение не попадает ни в stdout,
ни в stderr, ни в файлы.

В конце — отчёт по запуску: что и где искалось, сколько групп/проектов прошло и сколько отброшено
после повторов, сколько найдено, сколько заняло, куда записан вывод, и по каждому вхождению ссылка
на файл плюс процитированная строка — все вхождения, без ограничения по числу строк:

```
──── report ────
searched:  "here-is-my-search-term"
filters:   extension=yaml  path=roles  archive=all
groups:    2
projects:  3172 searched, 0 skipped after retries
matches:   57
time:      4m3s
results:   console and file found.txt (plain)
citations (57):
  https://git.digital.rt.ru/pcp/billing/blob/master/roles/app/values.yaml#L18
      here-is-my-search-term: true
  ...
```

Отчёт печатается и при частичном успехе, и при фатальной ошибке; в этом случае добавляется
строка `error:` с причиной (`matches: 0` и `no matches found` при пустом результате).
Файл создаётся до первого запроса, поэтому он никогда не бывает пустым: даже упавший запуск
содержит параметры и отчёт с ошибкой.

В `--format json` та же запись лежит в одном валидном JSON-документе:

```json
{"parameters":{"version":"1.0.0","term":"needle","token":"glp************456","tls":"...","filters":"archive=all","...":"..."},
 "results":[{"project":{"id":10,"name":"p-ok","web_url":"...","archived":false},
             "hits":[{"data":"...","filename":"cmd/main.go","ref":"main","startline":42,"permalink":"..."}]}],
 "report":{"term":"needle","groups":1,"projects_searched":1,"matches":1,"elapsed":"4m3s",
           "written_to":"file found.json only (json)","citations":[{"project":"p-ok","permalink":"...","line":"..."}]}}
```

Токен в `parameters.token` замаскирован так же, как в консоли.

## Справка CLI

```
gitlab-search [options] <search-term>
  -g, --groups strings     группы для поиска (по умолчанию — все доступные токену)
  -f, --filename glob      ограничить имена файлов (фильтр GitLab)
  -e, --extension ext      ограничить расширение файлов
  -p, --path path          ограничить путь в репозитории
  -a, --archive[=mode]     архивные проекты: all (default), only, exclude (в форме -a=only)
  -o, --output file        писать весь вывод запуска в файл (с --quiet — только в файл)
      --format plain|json  формат вывода
      --base-url url       root URL вашего GitLab вместо конфиг-файла
      --token tok          токен вместо конфиг-файла
      --ignore-ssl         пропуск проверки сертификата
      --ca-file path       PEM собственного CA
      --config path        явный путь к конфиг-файлу
      --log-level level    debug|info|warn|error

gitlab-search setup [--token pat] [--base-url url] [--ignore-ssl] [--ca-file path]
                    [--dir path] [--config-file path] [--concurrency n]
                    [--retry-count n] [--rate-limit n] [--log-level level]
                    [-g|--groups list] [-f|--filename glob] [-e|--extension ext]
                    [-p|--path path] [-a|--archive all|only|exclude]
                    [--format plain|json] [-o|--output file] [--quiet] [--full]
```

У `setup` есть флаг для каждого ключа конфиг-файла, включая `--log-level` и опции поиска
(`--groups`, `--filename`, `--extension`, `--path`, `--archive`, `--format`, `--output`, `--quiet`).
Значения вне списка допустимых (`--archive bogus`, `--format xml`, `--log-level nope`) не попадают
в файл: печатается предупреждение и пишется значение по умолчанию, поэтому конфиг всегда рабочий.
`--quiet` без `--output` тоже отмечается предупреждением — поиск с таким конфигом запросит output.

Все опции поиска (кроме `--config` и `--log-level`) имеют одноимённый ключ в конфиг-файле —
см. таблицы выше; явный флаг всегда перебивает значение из файла.
