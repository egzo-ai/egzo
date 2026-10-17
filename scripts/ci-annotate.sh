#!/bin/sh
# Turn the interesting part of a CI log into workflow annotations. The logs of a run need a login to read; annotations are
# in the run's public check data, so this is how a failure can be read without opening the job.
#
#   scripts/ci-annotate.sh TITLE LOGFILE [tail|specs]
#
# `tail` annotates the last lines of the log. `specs` annotates the pytest summary: the failed specs, the first assertion
# messages and the last lines.
set -u

title=$1 log=$2 mode=${3:-tail}
[ -s "$log" ] || { echo "::warning title=$title::no log at $log"; exit 0; }

# an annotation message is one line: % and newlines are escaped as the runner expects
emit() { # level, title, text on stdin
	text=$(head -c 7000 | sed -e 's/%/%25/g' -e 's/\r//g' | awk 'BEGIN{ORS="%0A"} {print}')
	echo "::$1 title=$2::$text"
}

case "$mode" in
specs)
	grep -E '^(FAILED|ERROR) ' "$log" | cut -c1-230 | head -30 | emit error "$title: failed specs"
	grep -E '^E  ' "$log" | cut -c1-230 | head -40 | emit error "$title: first assertion messages"
	tail -n 30 "$log" | cut -c1-200 | emit error "$title: end of the log"
	;;
*)
	tail -n 40 "$log" | cut -c1-240 | emit error "$title: end of the log"
	;;
esac
