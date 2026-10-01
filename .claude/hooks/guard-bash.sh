#!/usr/bin/env bash
# .claude/hooks/guard-bash.sh — PreToolUse(Bash): не пускает из сессии Claude обходы git-хуков.
# exit 2 блокирует вызов, stderr уходит Claude.
#
# Блокирует: git commit с -n; git с --no-veri… (любая подкоманда); core.hooksPath в команде с git
# (прочитать — git config --list). Команда проверяется построчно; тела heredoc — данные (сообщение
# коммита может упоминать флаги); флаги в строках в кавычках не ищутся. Это страховка от
# «удобного» обхода, а не от обфускации. Ослабить сам гейт окружением не даёт make check.
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
