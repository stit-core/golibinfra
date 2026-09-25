#!/usr/bin/env bash
# validate-plan.sh — валидатор и сводка планов `.claude/plans/` (канон формата — skill `/plan`).
#
# Модель:
#   .claude/plans/<slug>.md  — один активный план: frontmatter (status / parent / blocked-by),
#                              H1 `# <slug> — <заголовок>`, разделы `## …`;
#   .claude/plans/backlog.md — кандидаты в работу, по пункту `- **<id>** — …`.
# Закрытый план удаляется — история в git log.
#
# Использование:
#   validate-plan.sh              проверить все планы и бэклог; exit 1 при ошибках
#   validate-plan.sh <file>...    проверить указанные файлы (план или backlog.md)
#   validate-plan.sh --summary    сводка для status/smartdev: план · статус · прогресс · следующий шаг
#   validate-plan.sh --handoff    содержимое разделов `## Handoff` активных планов (для release)
#
# Ошибки ломают формат, на который опираются скрипты и возобновление после /clear.
# Предупреждения — сигнал к уборке (размер, покрытие критериев, план готов к закрытию).
# Зависимости: bash + POSIX awk (mawk/BSD awk/gawk).

set -uo pipefail
ROOT=$(git rev-parse --show-toplevel 2>/dev/null || pwd)
cd "$ROOT" || exit 1

PLANS_DIR=.claude/plans
MODE=validate
declare -a FILES=()

case "${1:-}" in
  --summary) MODE=summary; shift ;;
  --handoff) MODE=handoff; shift ;;
  -h|--help) sed -n '2,19p' "$0"; exit 0 ;;
esac
if [ "$#" -gt 0 ]; then
  FILES=("$@")
else
  if [ -d "$PLANS_DIR" ]; then
    for f in "$PLANS_DIR"/*.md; do
      [ -e "$f" ] && FILES+=("$f")
    done
  fi
fi

# Один проход awk на файл: печатает записи `E|<msg>`, `W|<msg>`, `S|<line>`, `H|<line>`, `B|<n>`.
check_file() {
  local file=$1 base slug kind
  base=$(basename "$file")
  slug=${base%.md}
  kind=plan
  [ "$base" = "backlog.md" ] && kind=backlog
  awk -v KIND="$kind" -v SLUG="$slug" '
function e(msg) { print "E|" msg }
function w(msg) { print "W|" msg }
function trim(s) { sub(/^[ \t]+/, "", s); sub(/[ \t]+$/, "", s); return s }
# Текст после метки до разделителя « · » или конца строки.
function after(line, label,   p, rest, q) {
  p = index(line, label); if (p == 0) return ""
  rest = substr(line, p + length(label))
  q = index(rest, " · "); if (q > 0) rest = substr(rest, 1, q - 1)
  return trim(rest)
}
function refs(text, pat, arr,   n, i, parts, c) {
  n = split(text, parts, /[ ,;]+/); c = 0
  for (i = 1; i <= n; i++) if (parts[i] ~ pat) arr[++c] = parts[i]
  return c
}
# Закрыть текущий пункт (критерий или шаг).
function close_item(   i, n, r) {
  if (itype == "") return
  if (itype == "AC") {
    if (!ihas_check)                     e(iid ": у критерия нет «Проверка:»")
    if (idone && !ihas_confirm)          e(iid ": критерий отмечен [x] без «Подтверждено:»")
  } else {
    if (!ihas_check)                     e(iid ": у шага нет «Проверка:»")
    if (idone && !ihas_done)             e(iid ": шаг отмечен [x] без «Сделано:»")
    n = refs(icloses, "^AC[0-9]+$", r)
    for (i = 1; i <= n; i++) { closes[++nclose] = iid SUBSEP r[i]; covered[r[i]] = 1 }
    n = refs(ideps, "^S[0-9]+$", r)
    for (i = 1; i <= n; i++) deps[++ndep] = iid SUBSEP r[i]
  }
  itype = ""
}
function start_item(type, line,   id) {
  close_item()
  id = line; sub(/^- \[[ xX]\] \*\*/, "", id); sub(/\*\*.*$/, "", id)
  dup = ((type SUBSEP id) in ids)
  if (dup) e(id ": идентификатор повторяется")
  ids[type SUBSEP id] = 1
  itype = type; iid = id
  idone = (line ~ /^- \[[xX]\]/)
  ihas_check = 0; ihas_confirm = 0; ihas_done = 0; icloses = ""; ideps = ""
  title = line; sub(/^- \[[ xX]\] \*\*[A-Z]+[0-9]+\*\*[ \t]*(—|-)?[ \t]*/, "", title)
  if (type == "AC") { nac++; if (idone) nac_done++; if (!dup) acorder[++nacu] = id }
  else {
    nst++; if (idone) nst_done++
    if (!idone && next_step == "") next_step = id " — " title
  }
  scan_item_line(line)
}
function scan_item_line(line,   t) {
  if (index(line, "Проверка:"))     ihas_check = 1
  if (index(line, "Подтверждено:")) ihas_confirm = 1
  if (index(line, "Сделано:"))      ihas_done = 1
  t = after(line, "Закрывает:");  if (t != "") icloses = icloses " " t
  t = after(line, "Зависит от:"); if (t != "") ideps = ideps " " t
}

BEGIN {
  nreq = split("Цель|Контекст|Критерии приёмки|Шаги|Где мы сейчас", req, "|")
  nopt = split("Не входит|Решения|Открытые вопросы|Handoff|Фазы", opt, "|")
  for (i = 1; i <= nreq; i++) known[req[i]] = 1
  for (i = 1; i <= nopt; i++) known[opt[i]] = 1
  sec = ""; itype = ""; next_step = ""; nlines = 0; nitems = 0
}

KIND == "backlog" {
  nlines++
  if (nlines == 1 && $0 != "# Backlog") w("первая строка бэклога — «# Backlog»")
  if ($0 ~ /^- /) {
    if (cur_len > 6) w("пункт бэклога «" cur_id "» длиннее 6 строк — это уже план или его надо сжать")
    nitems++; cur_len = 1
    cur_id = $0; sub(/^- \*\*/, "", cur_id); sub(/\*\*.*$/, "", cur_id)
    if ($0 !~ /^- \*\*[^*]+\*\* (—|-) /) e("пункт бэклога без формы «- **<id>** — <что и зачем>»: " substr($0, 1, 60))
    next
  }
  if ($0 ~ /^[ \t]+[^ \t]/ && cur_len > 0) { cur_len++; next }
  if ($0 ~ /^#/ || $0 ~ /^[ \t]*$/) { next }
  next
}
KIND == "backlog" { next }

# ---- план ----
{ nlines++ }
nlines == 1 {
  if ($0 != "---") { e("нет frontmatter: первая строка должна быть ---"); fm_state = 2 }
  else fm_state = 1
  next
}
fm_state == 1 {
  if ($0 == "---") { fm_state = 2; next }
  if (match($0, /^[a-z-]+:/)) {
    key = substr($0, 1, RLENGTH - 1); val = substr($0, RLENGTH + 1)
    sub(/[ \t]+#.*$/, "", val); fm[key] = trim(val)
  }
  next
}
/^# / && h1 == "" {
  h1 = substr($0, 3); split(h1, hp, " ")
  if (hp[1] != SLUG) e("H1 «# " hp[1] " …» не совпадает с именем файла " SLUG ".md")
  next
}
/^## / {
  close_item()
  sec = trim(substr($0, 4)); seen[sec] = 1
  if (!(sec in known)) w("неизвестный раздел «## " sec "»")
  next
}
/^#/ { close_item(); next }
sec == "Критерии приёмки" && /^- \[[ xX]\] \*\*AC[0-9]+\*\*/ { start_item("AC", $0); next }
sec == "Шаги"             && /^- \[[ xX]\] \*\*S[0-9]+\*\*/  { start_item("S", $0); next }
sec == "Фазы" && /^\|[ \t]*P[0-9]+[ \t]*\|/ {
  n = split($0, cells, "|"); st = ""
  for (i = n; i >= 1; i--) { c = trim(cells[i]); gsub(/`/, "", c); if (c != "") { st = c; break } }
  if (st !~ /^(todo|in-progress|blocked|done)$/) e("фаза " trim(cells[2]) ": статус «" st "» не из todo|in-progress|blocked|done")
  nphase++; if (st == "done") nphase_done++
  next
}
/^- / { close_item() }
itype != "" && /^[ \t]+[^ \t]/ { scan_item_line($0) }
sec == "Handoff" { hand[++nhand] = $0 }
sec == "Где мы сейчас" && /[^ \t]/ { has_now = 1 }

END {
  if (KIND == "backlog") {
    if (cur_len > 6) w("пункт бэклога «" cur_id "» длиннее 6 строк — это уже план или его надо сжать")
    if (nitems > 30) w("в бэклоге " nitems " пунктов — пора прополоть: неактуальное удалить")
    print "B|" nitems
    exit
  }
  close_item()
  if (fm_state == 1) e("frontmatter не закрыт строкой ---")
  st = fm["status"]; par = fm["parent"]; bb = fm["blocked-by"]
  if (st == "")                                   e("нет status во frontmatter")
  else if (st !~ /^(planning|in-progress|blocked)$/) e("status «" st "» не из planning|in-progress|blocked")
  if (st == "blocked" && (bb == "" || bb == "—" || bb == "-")) e("status: blocked без blocked-by")
  if (par != "" && par != "—" && par != "-" && par !~ /^[a-z0-9][a-z0-9-]*:[a-z0-9][a-z0-9-]*$/)
    e("parent «" par "» не в форме <репо>:<slug>")
  if (h1 == "") e("нет заголовка «# " SLUG " — <заголовок>»")
  for (i = 1; i <= nreq; i++) if (!(req[i] in seen)) e("нет раздела «## " req[i] "»")
  if (("Критерии приёмки" in seen) && nac == 0) e("нет критериев приёмки (- [ ] **AC1** — …)")
  if (("Шаги" in seen) && nst == 0)             e("нет шагов (- [ ] **S1** — …)")
  if (("Где мы сейчас" in seen) && !has_now)     w("раздел «Где мы сейчас» пуст — после /clear не с чего продолжить")
  for (i = 1; i <= nclose; i++) { split(closes[i], p, SUBSEP); if (!(("AC" SUBSEP p[2]) in ids)) e(p[1] ": «Закрывает: " p[2] "» — такого критерия нет") }
  for (i = 1; i <= ndep; i++)   { split(deps[i], p, SUBSEP);   if (!(("S" SUBSEP p[2]) in ids))  e(p[1] ": «Зависит от: " p[2] "» — такого шага нет") }
  for (i = 1; i <= nacu; i++) if (!(acorder[i] in covered)) w(acorder[i] ": ни один шаг его не закрывает (Закрывает:)")
  if (nlines > 300) w("план длиннее 300 строк (" nlines ") — раздели на последовательные планы или фазы")
  if (nst > 20)     w("шагов больше 20 (" nst ") — раздели на последовательные планы или фазы")
  if (st == "in-progress" && nst > 0 && nst_done == nst && nac_done == nac)
    w("все шаги и критерии закрыты — план готов к закрытию: хвосты в backlog.md, файл удалить")

  line = "  " SLUG " · " st
  if (st == "blocked") line = line " (blocked by: " bb ")"
  if (par != "" && par != "—" && par != "-") line = line " · фаза " par
  line = line " · шаги " (nst_done + 0) "/" (nst + 0) " · AC " (nac_done + 0) "/" (nac + 0)
  if (nphase > 0) line = line " · фазы " (nphase_done + 0) "/" nphase
  if (next_step != "") line = line " · дальше: " next_step
  rank = (st == "in-progress") ? 1 : (st == "blocked") ? 2 : 3
  print "S|" rank "|" line
  body = 0
  for (i = 1; i <= nhand; i++) { t = trim(hand[i]); if (t != "" && t != "—" && t != "-") body = 1 }
  if (body) {
    first = 1; last = nhand
    while (first <= last && trim(hand[first]) == "") first++
    while (last >= first && trim(hand[last]) == "") last--
    print "H|--- " SLUG " ---"
    for (i = first; i <= last; i++) print "H|" hand[i]
  }
}
' "$file"
}

errors=0
warnings=0
plans=0
backlog_items=-1
summary=""
handoff=""
report=""

if [ "$#" -eq 0 ] && [ -f "$PLANS_DIR/roadmap.md" ]; then
  report+="E $PLANS_DIR/roadmap.md: такого файла в модели планов нет — план живёт в $PLANS_DIR/<slug>.md, бэклог в $PLANS_DIR/backlog.md (skill /plan)"$'\n'
  errors=$((errors + 1))
fi
if [ "$#" -eq 0 ] && [ ! -f "$PLANS_DIR/backlog.md" ]; then
  report+="E $PLANS_DIR/backlog.md: файла нет — бэклог обязателен (пустой — строка «_пусто_»)"$'\n'
  errors=$((errors + 1))
fi

for f in "${FILES[@]}"; do
  [ -f "$f" ] || { report+="E $f: файл не найден"$'\n'; errors=$((errors + 1)); continue; }
  case "$(basename "$f")" in roadmap.md) continue ;; esac
  out=$(check_file "$f")
  [ "$(basename "$f")" = "backlog.md" ] || plans=$((plans + 1))
  while IFS= read -r rec; do
    [ -n "$rec" ] || continue
    tag=${rec%%|*}
    msg=${rec#*|}
    case "$tag" in
      E) report+="E $f: $msg"$'\n'; errors=$((errors + 1)) ;;
      W) report+="W $f: $msg"$'\n'; warnings=$((warnings + 1)) ;;
      S) summary+="$msg"$'\n' ;;
      H) handoff+="$msg"$'\n' ;;
      B) backlog_items=$msg ;;
    esac
  done <<< "$out"
done

case "$MODE" in
  summary)
    if [ -n "$summary" ]; then printf '%s' "$summary" | sort -t'|' -k1,1n -s | cut -d'|' -f2-; else echo "  none (нет активных планов)"; fi
    [ "$backlog_items" -ge 0 ] && echo "  backlog: $backlog_items"
    [ "$errors" -gt 0 ] && echo "  ATTENTION: формат планов — $errors ошибк(и); подробности — проверка планов"
    exit 0
    ;;
  handoff)
    printf '%s' "$handoff"
    exit 0
    ;;
esac

printf '%s' "$report"
if [ "$errors" -gt 0 ]; then
  echo "FAIL: $errors ошибк(и), $warnings предупрежд. — планов: $plans"
  exit 1
fi
echo "OK: планов $plans, предупреждений $warnings"
