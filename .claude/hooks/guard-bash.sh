#!/usr/bin/env bash
# .claude/hooks/guard-bash.sh — PreToolUse(Bash): не пускает из сессии Claude обходы git-хуков.
# exit 2 блокирует вызов, stderr уходит Claude.
#
# Блокирует: git commit с -n; git с --no-veri… (любая подкоманда; флаг в кавычках — тот же флаг);
# core.hooksPath в команде с git (прочитать — git config --list); команду с путём git-hooks,
# pre-push или commit-msg, в которой не узнал чтение, и запись туда перенаправлением. Команда
# проверяется построчно; тела heredoc и строки в кавычках — данные (сообщение коммита может
# упоминать флаги). Это страховка от «удобного» обхода, а не от обфускации: формы, которые guard
# держит, — TestGuardBash (scripts/gate_test.go), чего не видит — README § Гейт. Окружение команды
# guard не разбирает: переменные make (`MAKEFLAGS=i make check`) и оболочки (`BASH_ENV=… git push`,
# экспортированные функции, SHELLOPTS) отвергает или снимает сам гейт — make check, хуки и
# шаги-скрипты стартуют `bash -p`; подставной бинарь в PATH не ловит никто.
set -uo pipefail

block() {
    echo "guard-bash: $1 — обход гейта. Почини причину падения; обойти гейт может только человек." >&2
    exit 2
}

# Значение "command" из JSON без jq: \" — вручную, остальное экранирование — printf %b.
raw=$(sed -nE 's/.*"command"[[:space:]]*:[[:space:]]*"(([^"\\]|\\.)*)".*/\1/p' | head -n 1)
cmd=$(printf '%b' "$(printf '%s' "$raw" | sed 's/\\"/"/g')")

# Без тел heredoc: от строки с `<<WORD` до строки-терминатора WORD.
text=$(printf '%s\n' "$cmd" | awk '
    body != "" { line = $0; sub(/^\t+/, "", line); if (line == body) body = ""; next }
    { print }
    match($0, /(^|[^<])<<-?[ \t]*["'\'']?[A-Za-z_][A-Za-z0-9_]*/) {
        body = substr($0, RSTART, RLENGTH); sub(/^[^<]*<<-?[ \t]*["'\'']?/, "", body)
    }')

# Ключ конфига опасен только рядом с git: grep по нему не блокируется.
re_git='(^|[^[:alnum:]_.-])git([[:space:]]|$)'
[[ $(printf '%s' "$text" | tr '[:upper:]' '[:lower:]') == *core.hookspath* && $text =~ $re_git ]] &&
    block "core.hooksPath"

# Слова команды с учётом кавычек. Вне кавычек `\x` — буква, пробел — граница слова (код 037); в
# кавычках и после `\` разделители команд прячутся в коды 001–006 и возвращаются, когда строка уже
# разрезана на команды: слово в кавычках остаётся одним словом, и его текст виден целиком. Перед
# тем, что оболочка раскроет сама (`$…`, обратная кавычка, вне кавычек — и `{`, `*`, `?`, `[`),
# встаёт код 007: что получится из такого слова, guard не знает. Перенаправление, приклеенное к
# слову (`хук>/dev/null`), — отдельное слово; номер дескриптора остаётся при нём (`2>…`). Кавычка,
# не закрытая до конца строки, кончается вместе со строкой: следующая строка — снова код.
split_words() {
    awk '
        function hide(c,    k) { k = index(";&|()`", c); return k ? substr("\001\002\003\004\005\006", k, 1) : c }
        {
            out = ""; q = ""; n = length($0); word = ""
            for (i = 1; i <= n; i++) {
                c = substr($0, i, 1)
                if (q == "") {
                    if (c == "\\") { out = out c; word = word c; i++; c = hide(substr($0, i, 1)) }
                    else if (c == "\"") q = c
                    else if (c == "\047") q = (i > 1 && substr($0, i - 1, 1) == "$") ? "$" : c
                    else if (c == " " || c == "\t") { c = "\037"; word = "" }
                    else if (index("$`{*?[", c)) out = out "\007"
                    else if ((c == "<" || c == ">") && word ~ /[^0-9<>]/) { out = out "\037"; word = "" }
                    if (c != "\037") word = word c
                } else if (q == "\047") {
                    if (c == q) q = ""; else c = hide(c)
                } else if (c == "\\") { out = out c; i++; c = hide(substr($0, i, 1)) }
                else if (c == (q == "$" ? "\047" : q)) q = ""
                else { if (q == "\"" && index("$`", c)) out = out "\007"; c = hide(c) }
                out = out c
            }
            print out
        }'
}

# Изменённый, неисполняемый или удалённый хук git молча пропускает. Команда с путём git-hooks,
# pre-push или commit-msg проходит, только если в ней узнано чтение: readers, git без правки дерева,
# bash -n, запуск самого хука (и bash <хук>) и «читатели», которые умеют писать и запускать, — без
# таких опций и программ; остальное — отказ, как и запись туда перенаправлением. cd туда — отказ:
# дальше путь в команде не виден.
hook='(^|[[:space:]/"'\''=])(git-hooks|pre-push|commit-msg)([[:space:]/"'\'']|$)'
run_hook='^([.[:alnum:]_~-]*/)*(pre-push|commit-msg)$'
readers='cat|head|tail|less|more|grep|egrep|fgrep|ls|stat|file|wc|diff|cmp|md5sum|sha256sum|shellcheck|test|\[|echo|printf'
git_readers='add|blame|cat-file|commit|diff|grep|log|ls-files|ls-tree|push|rev-parse|show|status|tag'
bel=$'\007'
re_redirect='^[0-9]*[<>]'
re_no_target='[<>]$'
re_path='^[[:alnum:]_./~-]+$'
re_awk_opts='^(-F.*|-v.*|--field-separator(=.*)?|--assign(=.*)?|--)$'
re_awk_io='[>|@]|system'
re_cp_opts='^(-[adfHLnPpRrsuvx]+|--(archive|recursive|force|verbose|parents|dereference|no-dereference|update|no-clobber|preserve(=.*)?)|--)$'

# parse <regex опций с отдельным аргументом> — слова команды: opts — опции, optargs — их аргументы,
# ops — операнды; все — без кавычек и `\` ("-delete" и --out""put — те же опции). Перенаправления с
# целями и хвост-комментарий — не слова команды.
parse() {
    opts=() optargs=() ops=()
    local j skip=0 arg=0 w b
    for ((j = i + 1; j < ${#words[@]}; j++)); do
        w=${words[j]} b=${bare[j]}
        if ((skip)); then
            skip=0
            continue
        fi
        [[ $w == \#* ]] && break
        if [[ $w =~ $re_redirect ]]; then
            [[ $w =~ $re_no_target ]] && skip=1
            continue
        fi
        if ((arg)); then
            arg=0
            optargs+=("$b")
        elif [[ $b == -?* ]]; then
            opts+=("$b")
            [[ -n $1 && $b =~ $1 ]] && arg=1
        else
            ops+=("$b")
        fi
    done
}
has() { # has <regex> — среди опций команды есть такая
    local o
    for o in ${opts[@]+"${opts[@]}"}; do [[ $o =~ $1 ]] && return 0; done
    return 1
}
only() { # only <regex> — все опции команды такие
    local o
    for o in ${opts[@]+"${opts[@]}"}; do [[ $o =~ $1 ]] || return 1; done
    return 0
}

while IFS= read -r seg; do
    plain=${seg//$'\037'/ }
    plain=${plain//$bel/}
    flat=${plain//[\"\'\\]/} # кавычки и `\` внутри пути его не прячут: git-ho""oks
    [[ $plain =~ $hook || $flat =~ $hook ]] || continue
    while IFS= read -r target; do
        [[ $target =~ $hook ]] && block "запись в scripts/git-hooks"
    done <<<"$(printf '%s\n%s' "$plain" "$flat" | grep -oE '>>?[[:space:]]*[^[:space:]>]+' | sed -E 's/^>>?[[:space:]]*//')"
    IFS=$'\037' read -r -a raw <<<"$seg"
    words=() bare=()
    for w in ${raw[@]+"${raw[@]}"}; do
        [ -n "$w" ] || continue
        words+=("$w")
        w=${w//$bel/}
        bare+=("${w//[\"\'\\\$]/}")
    done
    # cp, sort, find и awk проходят только в виде, который guard разобрал целиком: без подстановок.
    expands=0
    [[ $seg == *$bel* ]] && expands=1
    i=0
    while ((i < ${#words[@]})) && [[ ${words[i]} == *=* || ${bare[i]} =~ ^(sudo|env|command)$ ]]; do
        i=$((i + 1))
    done
    cmd=${bare[i]-}
    name=${cmd##*/}
    case "$name" in
        git)
            sub="" skip=0
            for w in "${bare[@]:i+1}"; do # подкоманда — первое слово не-флаг, кроме значений -C/-c…
                case "$w" in
                    -C | -c | --git-dir | --work-tree | --namespace) skip=1 ;;
                    -*) ;;
                    *) if [ "$skip" = 1 ]; then skip=0; else sub=$w && break; fi ;;
                esac
            done
            parse ''
            # --output пишет вывод в файл, add --chmod=-x снимает право на запуск, grep -O запускает команду.
            [[ $sub =~ ^($git_readers)$ ]] && ! has '^(--ou|--chmod=-)' &&
                ! { [ "$sub" = grep ] && has '^(-O|--open-files-in-pager)'; } && continue
            ;;
        sed)
            # -i правит файл на месте; команды w и e скрипта пишут в файл и запускают команду — скрипт
            # (-e или первый операнд) не называет каталог хуков.
            parse '^(-[a-zA-Z]*[efl]|--(e[a-z]*|fi[a-z]*|l[a-z-]*))$'
            scripts=("${ops[0]-}")
            has '^(-[a-zA-Z]*e|--e)' && scripts=(${opts[@]+"${opts[@]}"} ${optargs[@]+"${optargs[@]}"})
            has '^(-[a-zA-Z]*i|--i)' || [[ "${scripts[*]}" == *git-hooks* ]] || continue
            ;;
        awk | gawk)
            # Программа awk пишет и запускает через `>`, `|` и system, а файл-операнд знает как FILENAME;
            # опции — только разделитель и переменные: остальные читают программу из файла или пишут.
            parse '^(-F|-v|--field-separator|--assign)$'
            prog=${ops[0]-}
            ((!expands)) && only "$re_awk_opts" && [[ -n $prog && ! $prog =~ $re_awk_io ]] && continue
            ;;
        sort)
            parse ''
            ((expands)) || has '^(-[a-zA-Z]*o|--o|--co)' || continue # вывод в файл и --compress-program
            ;;
        find)
            parse ''
            ((expands)) || has '^-(exec|execdir|ok|okdir|delete|fprint|fprint0|fprintf|fls)$' || continue
            ;;
        rg)
            parse ''
            has '^--pre' || continue # --pre запускает команду на каждом файле
            ;;
        cp)
            # Копия из хука — чтение. Назначение — последний операнд; опции — только без аргумента:
            # иначе последним словом встал бы аргумент опции (-S x, -t каталог).
            parse ''
            ((!expands && ${#ops[@]} > 1)) && only "$re_cp_opts" && ! [[ ${ops[${#ops[@]} - 1]} =~ $hook ]] && continue
            ;;
        bash | sh)
            # bash -n только разбирает скрипт; с другими опциями (+n, -c) он исполняет.
            parse ''
            if [[ ${bare[i + 1]-} == -n ]]; then
                plain_ops=1
                for o in ${ops[@]+"${ops[@]}"}; do [[ $o =~ $re_path ]] || plain_ops=0; done
                ((${#opts[@]} == 1 && plain_ops)) && continue
            elif [[ ${bare[i + 1]-} =~ $run_hook ]]; then
                continue
            fi
            ;;
        *) [[ $name =~ ^($readers)$ || $cmd =~ $run_hook ]] && continue ;;
    esac
    block "${name:-команда} по scripts/git-hooks"
done <<<"$(printf '%s' "$text" | tr -d '\001-\007\037' | sed -e ':a' -e '/\\$/{N;s/\\\n//;ba' -e '}' | split_words |
    sed -e 's/>|/>/g' -e 's/>&/>/g' | tr ';&|()`' '\n\n\n\n\n\n' | tr '\001\002\003\004\005\006' ';&|()`')"

# Флаги git ищем вне кавычек, по сегментам: строки (с учётом `\` в конце) и части между
# ; & | ( ) и обратными кавычками. Слово-флаг в кавычках ("--no-verify", '-n') — тот же флаг.
segments=$(printf '%s' "$text" | sed -e ':a' -e '/\\$/{N;s/\\\n//;ba' -e '}' | tr '\n' ';' |
    sed -E -e "s/(^|[[:space:]])[\"'](-[^\"'[:space:]]*)[\"']/\1\2/g" -e 's/"([^"\\]|\\.)*"//g' -e "s/'[^']*'//g" |
    tr ';&|()`' '\n\n\n\n\n\n')
while read -r -a words; do
    git=0 sub="" skip=0
    for w in ${words[@]+"${words[@]}"}; do
        if [ "$git" = 0 ]; then
            [[ $w == git || $w == */git ]] && git=1
        elif [ -z "$sub" ]; then # подкоманда — первое слово не-флаг, кроме значений -C/-c/--git-dir…
            case "$w" in
                -C | -c | --git-dir | --work-tree | --namespace) skip=1 ;;
                -*) ;;
                *) if [ "$skip" = 1 ]; then skip=0; else sub=$w; fi ;;
            esac
        else
            case "$w" in
                --no-veri*) block "git $sub $w" ;;
                # -n в кластере коротких флагов до первого флага со значением: -an, -nm; -uno — значение -u.
                -[a-zA-Z]*) [[ $sub == commit && ${w%%[mFcCtSu]*} == *n* ]] && block "git commit $w" ;;
            esac
        fi
    done
done <<<"$segments"
exit 0
