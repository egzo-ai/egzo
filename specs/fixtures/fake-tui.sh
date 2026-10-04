#!/bin/sh
# A stand-in for a harness TUI, with one behaviour per FAKE_TUI value, so the session specs can
# observe exactly what the session layer does to a terminal program.
mode="${FAKE_TUI:-cat}"
esc=$(printf '\033')
clean() { printf '%s' "$1" | sed -e "s/${esc}\[20[01]~//g" -e 's/[^[:print:]]//g' -e 's/\\/\\\\/g' -e 's/"/\\"/g'; }

case "$mode" in
cat) # raw terminal, every byte comes straight back
	stty raw -echo
	printf 'READY\r\n'
	exec cat
	;;
lines) # cooked terminal, one line at a time, tells when it sees end of input
	stty -echo
	printf 'READY\r\n'
	while IFS= read -r line; do printf 'line:%s\r\n' "$line"; done
	printf 'eof-seen\r\n'
	sleep 600
	;;
resize) # reports its window size whenever it changes
	stty -echo
	printf 'READY size:%s\r\n' "$(stty size)"
	trap 'printf "size:%s\r\n" "$(stty size)"' WINCH
	while :; do sleep 0.2; done
	;;
env)
	printf 'READY TERM=%s COLORTERM=%s\r\n' "$TERM" "$COLORTERM"
	while :; do sleep 1; done
	;;
sigint) # Ctrl-C must reach the program as a key, not stop the session
	stty -echo
	trap 'printf "got-int\r\n"' INT
	printf 'READY\r\n'
	while :; do sleep 0.2; done
	;;
draw) # repaints the whole screen when it is resized, like a real TUI
	stty -echo
	n=0
	paint() { n=$((n + 1)); printf '\033[2J\033[HFRAME:%s size:%s\r\n' "$n" "$(stty size)"; }
	trap paint WINCH
	paint
	while :; do sleep 0.2; done
	;;
emit) # escape sequences a terminal passes on: OSC 52, OSC 8, truecolor, wide characters
	printf 'READY\r\n'
	printf '\033]52;c;aGVsbG8=\007'
	printf '\033]8;;http://example.test/\007link\033]8;;\007\r\n'
	printf '\033[38;2;1;2;3mtruecolor\033[0m\r\n'
	printf 'wide:\344\275\240\345\245\275\360\237\230\200\r\n'
	while :; do sleep 1; done
	;;
hooks) # a harness with hooks: reports its life cycle to the control sidecar the way Claude Code does
	stty -echo
	trap : INT
	printf 'READY\r\n'
	[ "${FAKE_HOOKS:-1}" = 1 ] && egzo hook SessionStart </dev/null
	buf=""
	pasting=0
	while IFS= read -r line; do
		case "$line" in *"${esc}[200~"*) pasting=1 ;; esac
		if [ "$pasting" = 0 ] && [ -z "$line" ]; then continue; fi
		buf="$buf$line "
		case "$line" in *"${esc}[201~"*) pasting=0 ;; esac
		[ "$pasting" = 1 ] && continue
		text=$(clean "$buf")
		buf=""
		[ -z "$text" ] && continue
		if [ "${FAKE_HOOKS:-1}" = 1 ] && [ "${FAKE_ACK:-1}" = 1 ]; then
			printf '{"prompt":"%s"}' "$text" | egzo hook UserPromptSubmit
		fi
		printf 'got:%s\r\n' "$text"
		case "$text" in
		*ask-permission*) printf '{"message":"Claude needs your permission to use Bash"}' | egzo hook Notification; continue ;;
		*idle-prompt*) printf '{"message":"Claude is waiting for your input"}' | egzo hook Notification; continue ;;
		esac
		sleep "${FAKE_WORK:-1}"
		[ "${FAKE_HOOKS:-1}" = 1 ] && printf '{}' | egzo hook Stop
	done
	;;
exit) # leaves straight away
	printf 'bye\r\n'
	exit 3
	;;
esac
