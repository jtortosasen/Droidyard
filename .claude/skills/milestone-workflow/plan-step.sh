#!/bin/sh
# Prints what an agent needs from a plan for one step, so a brief can carry it instead of
# the whole plan: the plan header, the step's section, the acceptance items it proves,
# and every §Changes entry that names it.
#   plan-step.sh docs/plans/<plan>.md <step-number>
set -eu
plan=$1 n=$2
awk -v n="$n" '
	NR<=12 && /^- (Status|Milestone|Contract):/ { print; next }
	/^## / { section=$0 }
	/^### / { instep = ($0 ~ "^### " n "\\. ") }
	instep { print; next }
	section=="## Acceptance" && $0 ~ ("Proven by:.*(step|steps) [^.]*\\<" n "\\>") { print; next }
	section=="## Changes" && $0 ~ ("step " n "[^0-9]|steps [^.]*\\<" n "\\>") { print "Changes: " $0; next }
' "$plan"
