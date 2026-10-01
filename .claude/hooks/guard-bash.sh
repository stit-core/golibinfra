#!/usr/bin/env bash
# .claude/hooks/guard-bash.sh — PreToolUse(Bash): не пускает из сессии Claude обходы git-хуков.
# exit 2 блокирует вызов, stderr уходит Claude.
#
# Блокирует: git commit с -n; git с --no-veri… (любая подкоманда); core.hooksPath в команде с git
# (прочитать — git config --list); по пути git-hooks или pre-push — всё, кроме чтения, и запись в
# него перенаправлением. Команда проверяется построчно; тела heredoc — данные (сообщение коммита
# может упоминать флаги); флаги в строках в кавычках не ищутся — `--no-veri…` где угодно в команде
# держит deny .claude/settings.json. Это страховка от «удобного» обхода, а не от обфускации.
# Ослабить сам гейт окружением не даёт make check.
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

# Разделители команд внутри кавычек — часть аргумента (`-m "fix(gate): …"`), а не граница.
quoted_seps() {
    awk '{
        out = ""; q = ""
        for (i = 1; i <= length($0); i++) {
            c = substr($0, i, 1)
            if (q == "" && (c == "\"" || c == "\047")) q = c
            else if (c == q) q = ""
            else if (q == "\"" && c == "\\") { out = out c; i++; c = substr($0, i, 1) }
            else if (q != "" && index(";&|()`", c)) c = " "
            out = out c
        }
        print out
    }'
}

# Изменённый, неисполняемый или удалённый хук git молча пропускает. Сегмент с путём git-hooks
# или pre-push проходит, только если это чтение: readers, git без правки дерева, sed без -i,
# bash -n, запуск самого хука (и bash <хук>) — и без записи в хук перенаправлением; cd туда — нет,
# дальше путь в команде не виден. Слова — с кавычками: путь в кавычках команду не прячет.
hook='(^|[[:space:]/"'\''=])(git-hooks|pre-push)([[:space:]/"'\'']|$)'
run_hook='^([.[:alnum:]_~-]*/)*pre-push$'
readers='cat|head|tail|less|more|grep|egrep|fgrep|rg|ls|stat|file|wc|diff|cmp|md5sum|sha256sum|shellcheck|test|\[|echo|printf'
git_readers='add|blame|cat-file|commit|diff|grep|log|ls-files|ls-tree|push|rev-parse|show|status|tag'
while IFS= read -r seg; do
    [[ $seg =~ $hook ]] || continue
    while IFS= read -r target; do
        [[ $target =~ $hook ]] && block "запись в scripts/git-hooks"
    done <<<"$(printf '%s' "$seg" | grep -oE '>[>|]?[[:space:]]*[^[:space:]>]+' | sed -E 's/^>[>|]?[[:space:]]*//')"
    read -r -a words <<<"$seg"
    i=0
    while ((i < ${#words[@]})) && [[ ${words[i]} == *=* || ${words[i]} =~ ^(sudo|env|command)$ ]]; do
        i=$((i + 1))
    done
    cmd=${words[i]-}
    name=${cmd##*/}
    case "$name" in
        git)
            sub="" skip=0
            for w in "${words[@]:i+1}"; do # подкоманда — первое слово не-флаг, кроме значений -C/-c…
                case "$w" in
                    -C | -c | --git-dir | --work-tree | --namespace) skip=1 ;;
                    -*) ;;
                    *) if [ "$skip" = 1 ]; then skip=0; else sub=$w && break; fi ;;
                esac
            done
            [[ $sub =~ ^($git_readers)$ ]] && continue ;;
        sed) [[ " ${words[*]:i+1} " =~ [[:space:]](-[a-zA-Z]*i|--in-place) ]] || continue ;;
        bash | sh) [[ ${words[i + 1]-} == -n || ${words[i + 1]-} =~ $run_hook ]] && continue ;;
        *) [[ $name =~ ^($readers)$ || $cmd =~ $run_hook ]] && continue ;;
    esac
    block "${name:-команда} по scripts/git-hooks"
done <<<"$(printf '%s' "$text" | sed -e ':a' -e '/\\$/{N;s/\\\n//;ba' -e '}' | quoted_seps | sed 's/>|/>/g' |
    tr ';&|()`' '\n\n\n\n\n\n')"

# Флаги git ищем вне кавычек, по сегментам: строки (с учётом `\` в конце) и части между
# ; & | ( ) и обратными кавычками.
segments=$(printf '%s' "$text" | sed -e ':a' -e '/\\$/{N;s/\\\n//;ba' -e '}' | tr '\n' ';' |
    sed -E -e 's/"([^"\\]|\\.)*"//g' -e "s/'[^']*'//g" | tr ';&|()`' '\n\n\n\n\n\n')
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
                # -n в кластере коротких флагов до первого флага со значением: -an, -nm.
                -[a-zA-Z]*) [[ $sub == commit && ${w%%[mFcCtS]*} == *n* ]] && block "git commit $w" ;;
            esac
        fi
    done
done <<<"$segments"
exit 0
