# gitlab-search (Go)

[English](README.md) · [Русский](README.ru.md)

Search a string across the code of every project in your GitLab groups

## Build

```powershell
go build -o .build\bin\gitlab-search.exe .\cmd\gitlab-search
```

## Quick start

```powershell
# once: create the configuration for your internal GitLab
gitlab-search setup --token <personal-access-token> --base-url https://git.digital.rt.ru --ignore-ssl
# the token needs the read_api scope

# search
gitlab-search "here-is-my-search-term"
gitlab-search "term" -g my-group -f "*.go" -p services/
```

`setup` has no mandatory arguments: without any flag it writes a complete configuration with
the built-in defaults and `token: ""`. Such a file is valid on its own, but a search started
from it stops at the required-parameter check and reports where a token can be taken from
(`setup --token`, the `token` key, the `GITLAB_SEARCH_TOKEN` variable).

The server and the token can also be given per run, without any configuration file:

```powershell
gitlab-search --base-url https://git.digital.rt.ru --token <pat> "term"
```

## Configuration

The default file is `.gitlab-search.yaml`: it is looked for in the current directory and every
parent, then in the home directory. `setup --full` writes a complete file with every parameter
and its default value; `setup --config-file <path>` writes it anywhere you like.

| Key | Default | Purpose |
|---|---|---|
| `domain` | `gitlab.com` | root URL of GitLab (`http://...` selects HTTP) or a bare domain (HTTPS) |
| `token` | — | personal access token with the `read_api` scope (required) |
| `ignore_ssl` | `false` | skip TLS certificate verification |
| `ca_file` | — | PEM file with your own CA (certificate verification stays enabled) |
| `concurrency` | `25` | maximum concurrent API requests |
| `rate_limit` | `0` | requests per second, `0` disables throttling |
| `retry_count` | `5` | repeats allowed for 429/5xx/network failures |
| `log_level` | `info` | `debug`, `info`, `warn`, `error` |

The remaining search options can also be set once in the configuration file — they apply
whenever the matching flag is not given explicitly:

| Key | Default | Flag | Purpose |
|---|---|---|---|
| `groups` | — | `-g/--groups` | comma-separated group names (empty — every group visible to the token) |
| `filename` | — | `-f/--filename` | only search contents of files matching this glob |
| `extension` | — | `-e/--extension` | only search files with this extension |
| `path` | — | `-p/--path` | only search files under this path |
| `archive` | `all` | `-a/--archive` | `all`, `only` or `exclude` — how to treat archived projects |
| `format` | `plain` | `--format` | output format: `plain` or `json` |
| `output` | — | `-o/--output` | file for the complete run output (parameters, matches, report) |
| `quiet` | `false` | `--quiet` | do not print results to the console (requires `output`) |

```yaml
domain: https://git.digital.rt.ru
token: glpat-xxxx
ignore_ssl: true
concurrency: 25
retry_count: 5
groups: pcp,billing        # search only in these groups
extension: yaml            # only *.yaml
output: found.txt          # write to a file
quiet: true                # ...and do not duplicate it on the console
```

Environment overrides: `GITLAB_SEARCH_TOKEN`, `GITLAB_SEARCH_DOMAIN`,
`GITLAB_SEARCH_CONCURRENCY`, `GITLAB_SEARCH_RATE_LIMIT`, `GITLAB_SEARCH_RETRY_COUNT`,
`GITLAB_SEARCH_IGNORE_SSL`, `GITLAB_SEARCH_CA_FILE`, `GITLAB_SEARCH_LOG_LEVEL`.

Precedence: **CLI flags > environment variables > configuration file > built-in defaults**.
The other side of it: `setup` rewrites the whole file, so keys added by hand are lost on the next
`setup` run (except with `--full`, which writes them with their default values).

## How the search works

1. **Groups.**
   - With `-g/--groups a,b` the names are used directly and the group list is never requested.
   - Otherwise `GET /api/v4/groups?per_page=100`, walking pagination through the `Link` header
     (`rel="next"`) until the pages run out. Every group visible to the token is collected
     (personal projects are not indexed — group projects only).
2. **Group projects.**
   For every group, in parallel, `GET /api/v4/groups/<id>/projects?per_page=100`
   (plus `archived=true|false` for `--archive only|exclude`), paginated the same way through `Link`.
   A group whose listing never succeeds is skipped with a warning while the others continue.
   Projects are deduplicated by id (first occurrence); the final order is groups, then projects within a group.
3. **Per-project search.**
   `GET /api/v4/projects/<id>/search?scope=blobs&per_page=100&search=<expression>`, where
   `<expression>` is the searched string plus GitLab's built-in filters:
   `filename:<glob>` (`-f`), `extension:<ext>` (`-e`), `path:<path>` (`-p`).
   The filters run on the server, there is no local post-filtering. The project's default branch
   is searched and its name comes back with every result (`ref`).
   Workers are bounded by `concurrency`, the pace by `rate_limit`.
4. **Spilling results to disk.**
   Each page of a project's hits is appended to `.gitlab-search-tmp/project-NNNNNN.jsonl` as soon as
   it arrives, so only one page is ever in memory and peak usage does not depend on the total number
   of matches. If a project fails on a middle page, the pages already received are not lost and reach
   the output.
5. **Final pass and output.**
   After all workers finish, the files are read in the original group/project order and streamed into
   the chosen destination: console, file (`-o/--output`), file only (`-o ... --quiet`), `plain` or `json`.
   Both the parameters block and the closing report are part of that output, so the file contains
   exactly what the console showed. Projects without hits are not printed. In plain format every hit
   shows the permalink `<web_url>/blob/<ref>/<filename>#L<startline>` and the code fragment; in json
   the `project`/`hits` pairs. `.gitlab-search-tmp/` is removed on exit.

## Resistance to 429 and failures

- `429`, `500`, `502`, `503`, `504` and network errors are retried; `401/403/404` fail immediately.
- The wait before a repeat grows along the ladder **5s → 15s → 30s → 45s → 60s** (60s from then on)
  plus 0–20 % jitter so the streams do not synchronise; the stated number of seconds is the minimum.
- `Retry-After` is honoured only when the server asks for more than the whole ladder step (>60s).
  GitLab answers every throttled request with `Retry-After: 60`, and without this rule the waits
  would never grow and every repeat would stall for the same minute.
- The number of repeats is `retry_count` (default 5). Once it is exhausted the project is marked
  skipped (`project skipped after retries`), the search continues and the collected results are printed.
- `rate_limit` and `concurrency` keep the server away from 429 in the first place: the first bounds
  the pace, the second the parallelism.
- Exit code: `0` on success and on partial success (there are results), `1` for a fatal error
  (no configuration or token, 401, every group or every project failed).

## Logging

Logs go to stderr and never into the results file. At the default `info` level every phase is visible:
`listing groups` → `groups resolved` → `group projects fetched` (per group) →
`projects resolved` → `search started` → `progress` (every 10 projects) → `search finished`.
`--log-level debug` adds each HTTP request (`Requesting GET ...`) and the group/project lists;
the `DEBUG` variable (any value) enables debug, as in the original package.

## Run parameters and closing report

Two service blocks — the start and the end of the run — are printed regardless of `--log-level`
(unlike the logs) and are part of the output: without `-o` they go to the console, with `-o` to the
console and the file, with `-o --quiet` to the file only (and are echoed to stderr so the run stays
visible without opening the file). The results file therefore holds the complete run record:
parameters, matches, report.

Before the first request, the block shows the values actually applied after resolving
"flags > env > config > defaults", so a wrong configuration is obvious immediately:

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

The token is printed masked (first and last three characters); the full value never reaches stdout,
stderr or any file.

At the end comes the run report: what was searched and where, how many groups and projects went
through, how many were dropped after retries, how many matches were found and how long it took,
where the output was written, and for every match a link to the file plus the quoted line —
all matches, with no limit on the number of lines:

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

The report is printed both on partial success and on a fatal error, in which case an extra `error:`
line gives the reason (`matches: 0` and `no matches found` for an empty result). The file is created
before the first request, so it is never empty: even a failed run contains the parameters and the
report with the error.

With `--format json` the same record lives in one valid JSON document:

```json
{"parameters":{"version":"1.0.0","term":"needle","token":"glp************456","tls":"...","filters":"archive=all","...":"..."},
 "results":[{"project":{"id":10,"name":"p-ok","web_url":"...","archived":false},
             "hits":[{"data":"...","filename":"cmd/main.go","ref":"main","startline":42,"permalink":"..."}]}],
 "report":{"term":"needle","groups":1,"projects_searched":1,"matches":1,"elapsed":"4m3s",
           "written_to":"file found.json only (json)","citations":[{"project":"p-ok","permalink":"...","line":"..."}]}}
```

The token in `parameters.token` is masked exactly as on the console.

## CLI reference

```
gitlab-search [options] <search-term>
  -g, --groups strings     comma-separated group names to search (default: all visible to the token)
  -f, --filename glob      only search files matching this glob (GitLab filter)
  -e, --extension ext      only search files with this extension
  -p, --path path          only search files under this repository path
  -a, --archive[=mode]     archived projects: all (default), only, exclude (written as -a=only)
  -o, --output file        write the whole run record to this file (with --quiet: file only)
      --format plain|json  output format
      --base-url url       root URL of your GitLab instead of the configuration file
      --token tok          token instead of the configuration file
      --ignore-ssl         skip TLS certificate verification
      --ca-file path       PEM file with your own CA
      --config path        explicit path to the configuration file
      --log-level level    debug|info|warn|error

gitlab-search setup [--token pat] [--base-url url] [--ignore-ssl] [--ca-file path]
                    [--dir path] [--config-file path] [--concurrency n]
                    [--retry-count n] [--rate-limit n] [--log-level level]
                    [-g|--groups list] [-f|--filename glob] [-e|--extension ext]
                    [-p|--path path] [-a|--archive all|only|exclude]
                    [--format plain|json] [-o|--output file] [--quiet] [--full]
```

`setup` has a flag for every configuration key, including `--log-level` and the search options
(`--groups`, `--filename`, `--extension`, `--path`, `--archive`, `--format`, `--output`, `--quiet`).
Values outside the allowed list (`--archive bogus`, `--format xml`, `--log-level nope`) never reach
the file: a warning is printed and the default is written instead, so the configuration is always usable.
`--quiet` without `--output` is warned about as well — a search using that configuration will ask for output.

Every search option except `--config` and `--log-level` has an identically named configuration key —
see the tables above; an explicit flag always wins over the file.
