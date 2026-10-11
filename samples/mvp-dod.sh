#!/bin/sh
# The MVP's Definition of done run for items 3 and 6 (plan m2h, D2):
#
#   mvp-dod.sh errors <2.0.21|2.4.20> <outdir>   build.errors on one KGP cell (A1, A2)
#   mvp-dod.sh footprint <outdir> [avd]          idle footprint per process (A4)
#   mvp-dod.sh check-errors <kgp> <dir>          the assertions over a saved errors run
#   mvp-dod.sh check-footprint <dir>             the verdict over a saved footprint run
#
# It drives a built daemon through `droidyard call` under a scratch state, in a scratch
# copy of samples/golden (K1), never in place. Its launch, identity check and cleanup are
# .claude/skills/verify-daemon/SKILL.md §Launch, §Doctor and §Cleanup transcribed; change
# them there and here together. Requires POSIX sh, jq, go, a JDK 17 and the host Android
# SDK. The check-* modes need only jq. Nothing lints this file: it is kept true by being
# rerun. A device phase (footprint with an avd) needs K5's environment, see usage below.
#
# The waited `droidyard call` envelope is {id, operation, result}: for a capability that
# completed, .result is the capability's own result (adapters/cli, internal/capability
# envelope.go). Every assertion below reads that shape.

set -u

TAB=$(printf '\t')
SELF=$0
case $SELF in /*) ;; *) SELF=$PWD/$SELF ;; esac
REPO=$(cd -P "$(dirname "$SELF")/.." && pwd)
SDK=${ANDROID_HOME:-${ANDROID_SDK_ROOT:-$HOME/Library/Android/sdk}}

SRC_FILE=app/src/main/kotlin/dev/droidyard/golden/MainActivity.kt
ERR_LINE=35
ERR_COL=9
APP_ID=dev.droidyard.golden
NEW_KGP=2.4.20
OLD_KGP=2.0.21
RSS_BUDGET_KIB=48828 # 50 x 10^6 bytes, the stricter reading (D4)

die() {
	echo "mvp-dod: $*" >&2
	exit 1
}

usage() {
	cat >&2 <<EOF
usage: mvp-dod.sh errors <2.0.21|2.4.20> <outdir>
       mvp-dod.sh footprint <outdir> [avd]
       mvp-dod.sh check-errors <2.0.21|2.4.20> <dir>
       mvp-dod.sh check-footprint <dir>
footprint with an avd needs K5's environment: ANDROID_AVD_HOME, ADB (the guard script),
and ANDROID_ADB_SERVER_PORT naming the run's own adb server (not 5037).
EOF
	exit 2
}

# ---------------------------------------------------------------------------------------
# The assertions. Pure functions over saved files: no daemon, no build.

# check_errors <kgp> <dir>: A1 for 2.0.21, A2 for 2.4.20. Files are the envelopes
# `droidyard call` printed. Exits non-zero naming the first mismatch.
check_errors() {
	kgp=$1
	d=$2
	case $kgp in "$OLD_KGP" | "$NEW_KGP") ;; *) die "kgp must be $OLD_KGP or $NEW_KGP, not $kgp" ;; esac
	for f in build-ok build-failed errors build-restored; do
		[ -s "$d/$f.json" ] || die "$d/$f.json is missing or empty"
		jq -e . "$d/$f.json" >/dev/null 2>&1 || die "$d/$f.json is not JSON"
	done
	file=app/src/main/kotlin/dev/droidyard/golden/MainActivity.kt

	jq -e '.error == null' "$d/build-failed.json" "$d/errors.json" >/dev/null ||
		die "a call returned an error envelope instead of a result"
	[ "$(jq -r .result.outcome "$d/build-ok.json")" = success ] ||
		die "the first build did not succeed (outcome $(jq -r .result.outcome "$d/build-ok.json"))"
	[ "$(jq -r .result.outcome "$d/build-failed.json")" = failed ] ||
		die "the build with the injected error did not fail (outcome $(jq -r .result.outcome "$d/build-failed.json"))"
	[ "$(jq -r .result.outcome "$d/build-restored.json")" = success ] ||
		die "the restored build did not succeed (A3; outcome $(jq -r .result.outcome "$d/build-restored.json"))"

	[ "$(jq -r .result.kgp "$d/build-failed.json")" = "$kgp" ] ||
		die "the failed build says kgp $(jq -r .result.kgp "$d/build-failed.json"), want $kgp"
	[ "$(jq -r .result.buildId "$d/build-failed.json")" = "$(jq -r .result.buildId "$d/errors.json")" ] ||
		die "build.errors answered for another build than the failed one"

	# Which record is the injected error: its file, line and column, whatever its source.
	n=$(jq --arg f "$file" --argjson l "$ERR_LINE" --argjson c "$ERR_COL" \
		'[.result.errors[] | select(.file == $f and .line == $l and .column == $c)] | length' "$d/errors.json")
	[ "$n" = 1 ] || die "build.errors holds $n records at $file:$ERR_LINE:$ERR_COL, want exactly 1 (a merged event and console pair is one record)"
	total=$(jq '.result.errors | length' "$d/errors.json")
	[ "$total" = 1 ] || die "build.errors holds $total error records, want 1"
	jq -e '.result.errors[0].message | contains("droidyardAcceptanceMissing")' "$d/errors.json" >/dev/null ||
		die "the error's message does not name droidyardAcceptanceMissing"
	[ "$(jq -r '.result.errors[0].severity' "$d/errors.json")" = error ] || die "the record is not severity error"

	case $kgp in
	"$OLD_KGP") # A1: the console extractor is the primary path.
		[ "$(jq -r .result.expectedSources.kotlin "$d/build-failed.json")" = console ] ||
			die "A1: expectedSources.kotlin is $(jq -r .result.expectedSources.kotlin "$d/build-failed.json"), want console"
		[ "$(jq -r '.result.errors[0].tool' "$d/errors.json")" = kotlin ] || die "A1: the record's tool is not kotlin"
		[ "$(jq -r '.result.errors[0].source' "$d/errors.json")" = console ] ||
			die "A1: the record's source is $(jq -r '.result.errors[0].source' "$d/errors.json"), want console"
		jq -e '[.result.errors[] | select(.source == "event")] | length == 0' "$d/errors.json" >/dev/null ||
			die "A1: an error record has source event"
		;;
	"$NEW_KGP") # A2: the Problems event, with the console's line merged into it.
		[ "$(jq -r .result.expectedSources.kotlin "$d/build-failed.json")" = problems ] ||
			die "A2: expectedSources.kotlin is $(jq -r .result.expectedSources.kotlin "$d/build-failed.json"), want problems"
		[ "$(jq -r '.result.errors[0].source' "$d/errors.json")" = event ] ||
			die "A2: the record's source is $(jq -r '.result.errors[0].source' "$d/errors.json"), want event"
		[ "$(jq -r '.result.errors[0].problemId' "$d/errors.json")" = UNRESOLVED_REFERENCE ] ||
			die "A2: the record's problemId is $(jq -r '.result.errors[0].problemId' "$d/errors.json"), want UNRESOLVED_REFERENCE"
		jq -e '.result.extraction.sources | (index("event") != null) and (index("console") != null)' "$d/build-failed.json" >/dev/null ||
			die "A2: extraction.sources is $(jq -c .result.extraction.sources "$d/build-failed.json"), want event and console"
		;;
	esac
	echo "check-errors $kgp: ok ($(jq -c '.result.extraction.sources' "$d/build-failed.json") sources; $(jq -r '.result.errors[0] | "\(.tool)/\(.source) \(.file):\(.line):\(.column)"' "$d/errors.json"))"
}

# footprint_table <dir>: footprint.txt from footprint.tsv and marks.txt (D5).
footprint_table() {
	d=$1
	{
		echo "# rss_30s: first sample 30 s after the last call (after the last build for Gradle and Kotlin daemons); exited_at: seconds after the last call (core, emulator, adb) or after the last build (sidecar, Gradle and Kotlin daemons)"
		awk -F"$TAB" -v OFS="$TAB" -v lc="$(mark "$d" last_call)" -v be="$(mark "$d" build_end)" '
			function ref30(role) { return (role == "gradle-daemon" || role == "kotlin-daemon") ? be : lc }
			function refexit(role) { return (role == "core" || role == "emulator" || role == "adb-server") ? lc : be }
			{ k = $2 SUBSEP $3; if (!(k in seen)) { seen[k] = 1; order[++n] = k; role[k] = $3; ours[k] = $4; pid[k] = $2 }
			  if ($5 != "-") { if ($5 + 0 > max[k] + 0) max[k] = $5; last[k] = $5; alive[k] = 1 }
			  else if (!(k in gone)) gone[k] = $1
			  if (!(k in r30) && $1 >= ref30($3) + 30) r30[k] = $5 }
			END {
				print "role", "pid", "ours", "rss_30s_KiB", "rss_end_KiB", "max_KiB", "exited_at_s_after_ref"
				for (i = 1; i <= n; i++) { k = order[i]
					print role[k], pid[k], ours[k], (k in r30 ? r30[k] : "-"), (k in last ? last[k] : "-"), (k in max ? max[k] : "-"),
						(k in gone ? gone[k] - refexit(role[k]) : "alive") } }' "$d/footprint.tsv"
	} >"$d/footprint.txt"
}

mark() { sed -n "s/^$2=//p" "$1/marks.txt" | tail -1; }

sidecar_pid() { jq -r '[.processes[]? | select(.kind == "shared-sidecar") | .pid][0] // empty' "$1" 2>/dev/null; }

# check_footprint <dir>: the verdict. Exits non-zero naming the first failure.
check_footprint() {
	d=$1
	for f in footprint.tsv marks.txt processes.window.json processes.after.json processes.restart.json restart.json daemon.log; do
		[ -e "$d/$f" ] || die "$d/$f is missing"
	done
	footprint_table "$d"
	be=$(mark "$d" build_end)
	avd=$(mark "$d" avd)
	port=$(mark "$d" adb_port)
	if [ -n "$avd" ]; then
		case $port in "" | 5037) die "an AVD was named and the adb server port is '$port'" ;; esac
	fi

	core_final=$(awk -F"$TAB" '$3 == "core" && $5 != "-" { v = $5 } END { print v }' "$d/footprint.tsv")
	[ -n "$core_final" ] || die "no live sample of the core"
	[ "$core_final" -lt "$RSS_BUDGET_KIB" ] || die "the core's final RSS is $core_final KiB, not under $RSS_BUDGET_KIB"
	core_dead=$(awk -F"$TAB" '$3 == "core" && $5 == "-"' "$d/footprint.tsv" | wc -l | tr -d ' ')
	[ "$core_dead" = 0 ] || die "the core has a dead sample"

	old=$(sidecar_pid "$d/processes.window.json")
	[ -n "$old" ] || die "processes.window.json holds no sidecar record: the window began with no sidecar"
	alive_late=$(awk -F"$TAB" -v p="$old" -v be="$be" '$2 == p && $5 != "-" && $1 - be >= 660' "$d/footprint.tsv" | wc -l | tr -d ' ')
	[ "$alive_late" = 0 ] || die "the sidecar (pid $old) was alive 11:00 after the last build"
	gone_at=$(awk -F"$TAB" -v p="$old" '$2 == p && $5 == "-" && (m == "" || $1 + 0 < m) { m = $1 + 0 } END { print m }' "$d/footprint.tsv")
	[ -n "$gone_at" ] || die "the sidecar (pid $old) was never observed gone"
	[ $((gone_at - be)) -ge 600 ] || die "the sidecar (pid $old) was gone $((gone_at - be)) s after the last build, before 10:00"

	[ -z "$(sidecar_pid "$d/processes.after.json")" ] || die "processes.json still holds a sidecar record after its exit"
	if grep -Eq "\(pid $old\)( exited unexpectedly|: killed; its group outlived|:.*killing its group)" "$d/daemon.log"; then
		die "daemon.log holds an unexpected-exit or forced-kill line for the old sidecar pid $old"
	fi
	[ "$(jq -r .result.outcome "$d/restart.json" 2>/dev/null)" = success ] || die "the restart build did not succeed"
	new=$(sidecar_pid "$d/processes.restart.json")
	[ -n "$new" ] || die "no sidecar record after the restart build"
	[ "$new" != "$old" ] || die "the restart build reused the old sidecar pid $old"
	echo "check-footprint: ok (core $core_final KiB; sidecar $old gone $((gone_at - be)) s after its last build; restart on pid $new)"
}

# ---------------------------------------------------------------------------------------
# Setup shared by errors and footprint.

# absdir <path>: the path made absolute with symlinks of its deepest existing ancestor
# resolved, for a directory that may not exist yet.
absdir() {
	case $1 in /*) p=$1 ;; *) p=$PWD/$1 ;; esac
	rest=
	while [ ! -d "$p" ]; do
		rest=/$(basename "$p")$rest
		p=$(dirname "$p")
	done
	echo "$(cd -P "$p" && pwd)$rest"
}

# refuse_repo_outdir: an output directory inside the repository would put a run's files in
# the tree (K1).
refuse_repo_outdir() {
	case $1/ in "$REPO"/*) die "the output directory $1 is inside the repository $REPO" ;; esac
}

# prepare <kgp> <outdir>: K1's refusals, the copy, D1's rewrite. Sets OUT, G, E.
prepare() {
	kgp=$1
	OUT=$(absdir "$2")
	refuse_repo_outdir "$OUT"
	[ -z "$(git -C "$REPO" status --porcelain -- samples/golden)" ] ||
		die "samples/golden has uncommitted changes: the copy is of HEAD, so the run would not match the tree"
	if [ -e "$OUT" ] && [ -n "$(ls -A "$OUT")" ]; then die "$OUT exists and is not empty"; fi
	mkdir -p "$OUT/evidence" || die "cannot create $OUT"
	E=$OUT/evidence
	G=$OUT/golden
	mkdir -p "$G"
	git -C "$REPO" archive HEAD samples/golden | tar -x -C "$G" --strip-components 2 || die "git archive of samples/golden failed"
	cp "$G/$SRC_FILE" "$OUT/MainActivity.kt.orig" || die "$SRC_FILE is not in the copy"
	n=$(grep -rhoF "$OLD_KGP" "$G" | wc -l | tr -d ' ')
	[ "$n" = 3 ] || die "the copy holds $n occurrences of $OLD_KGP, want exactly 3 (D1)"
	if [ "$kgp" = "$NEW_KGP" ]; then
		for f in settings.gradle.kts build-logic/build.gradle.kts; do
			sed "s/$OLD_KGP/$NEW_KGP/g" "$G/$f" >"$G/$f.new" && mv "$G/$f.new" "$G/$f" || die "rewrite of $f failed"
		done
		[ "$(grep -rhoF "$NEW_KGP" "$G" | wc -l | tr -d ' ')" = 3 ] || die "the rewrite did not produce exactly 3 occurrences of $NEW_KGP"
		[ "$(grep -rhoF "$OLD_KGP" "$G" | wc -l | tr -d ' ')" = 0 ] || die "the rewrite left an occurrence of $OLD_KGP"
	fi
	printf 'sdk.dir=%s\n' "$SDK" >"$G/local.properties"
	echo "$kgp" >"$OUT/kgp.txt"
}

# stamps: K2's before-state, K3's and K4's stamps.
stamps() {
	[ -d "$SDK" ] || die "the host SDK $SDK does not exist"
	touch "$OUT/gradle.stamp" "$OUT/sdk.stamp"
	sleep 1
	externals >"$OUT/k2.before"
}

# externals: the pids K2 says must not change: the shared adb server on 5037 and, when
# DROIDYARD_OWNER_PID names it, the owner's daemon, each with its start time.
externals() {
	for p in $(lsof -nP -iTCP:5037 -sTCP:LISTEN -t 2>/dev/null | sort -u) ${DROIDYARD_OWNER_PID:-}; do
		printf '%s\t%s\n' "$p" "$(ps -o lstart= -p "$p" 2>/dev/null)"
	done
}

# launch: verify-daemon §Launch with this script's state under /tmp/dyh.*, the nodump
# binary (K1), then §Doctor. Sets R, DY, PID, SOCK, HP.
launch() {
	R=$(mktemp -d /tmp/dyh.XXXXXX) || die "mktemp failed"
	R=$(cd -P "$R" && pwd)
	echo "$R" >"$OUT/state-root.txt"
	export DROIDYARD_STATE_DIR="$R/state" DROIDYARD_CONFIG_DIR="$R/config"
	export ANDROID_SERIAL=droidyard-m2h-none # K3: no device task can find a device
	if [ -z "${JAVA_HOME:-}" ] && [ -x /usr/libexec/java_home ]; then
		JAVA_HOME=$(/usr/libexec/java_home -v 17) || die "no JDK 17"
		export JAVA_HOME
	fi
	SOCK="$DROIDYARD_STATE_DIR/run/droidyard.sock"
	(cd "$REPO" && go build -tags droidyard_nodump -o "$R/droidyard" ./cmd/droidyard) || die "go build failed"
	DY=$R/droidyard
	{ uname -sr; go version; git -C "$REPO" describe --always --dirty; echo "tags: droidyard_nodump"; } >"$E/env"
	run pre-health "$DY" call --no-autostart system.health && die "STOP: a daemon that is not yours answered"
	"$DY" serve >"$E/serve.out" 2>&1 &
	PID=$!
	i=0
	while [ $i -lt 50 ]; do
		[ -S "$SOCK" ] && break
		sleep 0.2
		i=$((i + 1))
	done
	doctor
}

# run <name> <cmd...>: verify-daemon §Launch's run, keeping the command, stdout, stderr
# and exit code under $E.
run() {
	n=$1
	shift
	printf '%s\n' "$*" >"$E/$n.cmd"
	"$@" </dev/null >"$E/$n.out" 2>"$E/$n.err"
	s=$?
	echo $s >"$E/$n.exit"
	return $s
}

# doctor: verify-daemon §Doctor. Any failure stops the run, with nothing repaired.
doctor() {
	kill -0 "$PID" || die "doctor: the daemon is not alive"
	jq -e --arg p "$PID" --arg s "$SOCK" '(.pid|tostring)==$p and .socket==$s' "$DROIDYARD_STATE_DIR/daemon.lock" >/dev/null ||
		die "doctor: the lock does not name the daemon this run started"
	lsof -a -p "$PID" -U -Fn | grep -qxF "n$SOCK" || die "doctor: the daemon is not bound to the scratch socket"
	run doctor-status "$DY" status && grep -qw "$PID" "$E/doctor-status.out" || die "doctor: status does not name pid $PID"
	run doctor-health "$DY" call --no-autostart system.health || die "doctor: system.health failed"
	HP=$(jq -r .http "$DROIDYARD_STATE_DIR/daemon.lock" | sed 's/.*://')
	lsof -nP -a -p "$PID" -iTCP -sTCP:LISTEN -Fn | grep -qxF "n127.0.0.1:$HP" || die "doctor: the lock's http port is not the daemon's"
}

# trust_root: the human channel (ADR-0015 §5), under the scratch configuration.
trust_root() {
	"$DY" trust "$G" >"$E/trust.out" 2>"$E/trust.err" || die "droidyard trust failed: $(cat "$E/trust.err")"
}

# call <name> <capability> [params]: a waited call against the copy, keeping its envelope
# under $E/<name>.out. Returns the CLI's status (1 iff the envelope carries an error).
call() {
	if [ $# -ge 3 ]; then
		run "$1" "$DY" call --no-autostart --workspace "$G" "$2" --params "$3"
	else
		run "$1" "$DY" call --no-autostart --workspace "$G" "$2"
	fi
}

# gcall <name> <capability> <params>: the same for a global capability (emulator.*, and
# device.* but install), which refuses a workspace (REQUEST_UNEXPECTED_WORKSPACE).
gcall() {
	run "$1" "$DY" call --no-autostart "$2" --params "$3"
}

# The golden path's build half (D4, D3): assemble, inject the error, build, errors,
# restore, build. Writes the four JSON files and returns; check-errors judges them.
build_phase() {
	call open workspace.open || die "workspace.open failed: $(cat "$E/open.out")"
	call modules project.modules || die "project.modules failed"
	call build-ok build.assemble '{"module":":app","variant":"debug"}' || die "build.assemble failed to run: $(cat "$E/build-ok.out")"
	cp "$E/build-ok.out" "$OUT/build-ok.json"
	# D3: the call on the line after the marker, eight spaces in, so the error is at 35:9.
	awk '{ print } /golden-path: compile-error site/ { print "        droidyardAcceptanceMissing()" }' \
		"$OUT/MainActivity.kt.orig" >"$G/$SRC_FILE.new" && mv "$G/$SRC_FILE.new" "$G/$SRC_FILE" || die "the injection failed"
	[ "$(sed -n "${ERR_LINE}p" "$G/$SRC_FILE")" = "        droidyardAcceptanceMissing()" ] || die "the injected call is not on line $ERR_LINE"
	call build-failed build.assemble '{"module":":app","variant":"debug"}' || die "the failing build errored as a call: $(cat "$E/build-failed.out")"
	cp "$E/build-failed.out" "$OUT/build-failed.json"
	bid=$(jq -r .result.buildId "$OUT/build-failed.json")
	call errors build.errors "{\"buildId\":\"$bid\"}" || die "build.errors failed"
	cp "$E/errors.out" "$OUT/errors.json"
	cp "$OUT/MainActivity.kt.orig" "$G/$SRC_FILE"
	call build-restored build.assemble '{"module":":app","variant":"debug"}' || die "the restoring build errored as a call"
	cp "$E/build-restored.out" "$OUT/build-restored.json"
}

# downloads_and_sdk: K3's list of what arrived, K4's SDK stamp. Both land in $OUT; a
# non-empty SDK list fails the run.
downloads_and_sdk() {
	find "$HOME/.gradle/caches/modules-2/files-2.1" "$HOME/.gradle/wrapper/dists" -newer "$OUT/gradle.stamp" -type f 2>/dev/null |
		while read -r f; do printf '%s\t%s\n' "$(wc -c <"$f" | tr -d ' ')" "$f"; done >"$OUT/downloads.txt"
	echo "downloads: $(wc -l <"$OUT/downloads.txt" | tr -d ' ') files, $(awk -F"$TAB" '{ s += $1 } END { print s + 0 }' "$OUT/downloads.txt") bytes" >"$OUT/downloads.summary"
	find "$SDK" -maxdepth 3 -newer "$OUT/sdk.stamp" 2>/dev/null | grep -v '/\.knownPackages$' >"$OUT/sdk-changes.txt"
	[ ! -s "$OUT/sdk-changes.txt" ] || SDK_CHANGED=1
}

# save_log: the daemon's log into $OUT/daemon.log. A daemon started with `serve` in the
# foreground logs to its standard error (serve.out); one auto-started logs to
# <state>/daemon.log (cmd/droidyard/serve.go). Both are kept when both exist.
save_log() {
	cat "$DROIDYARD_STATE_DIR/daemon.log" "$E/serve.out" 2>/dev/null >"$OUT/daemon.log"
}

externals_unchanged() {
	externals >"$OUT/k2.after"
	cmp -s "$OUT/k2.before" "$OUT/k2.after" || die "K2: the owner's processes changed (k2.before, k2.after)"
}

# snapshot / count_orphans: verify-daemon §Cleanup.
snapshot() {
	desc() { for c in $(pgrep -P "$1"); do echo "$c"; desc "$c"; done; }
	{ echo "$PID"; desc "$PID"; jq -r '.. | .pid? // empty' "$DROIDYARD_STATE_DIR/processes.json" 2>/dev/null; } | sort -u |
		while read -r p; do s=$(ps -o lstart= -p "$p") && printf '%s\t%s\t%s\n' "$p" "$s" "$(ps -o command= -p "$p")"; done >"$E/$1.procs"
	cp "$DROIDYARD_STATE_DIR/processes.json" "$E/$1.processes.json" 2>/dev/null
}

count_orphans() {
	while IFS="$TAB" read -r p s c; do
		[ "$(ps -o lstart= -p "$p" 2>/dev/null)" = "$s" ] && printf '%s\t%s\n' "$p" "$c"
	done <"$E/$1.procs" >"$E/$1.survivors"
	# K6: the Gradle and Kotlin daemons, the adb server and emulators are excluded, by what
	# they are, never by widening a pattern: the sidecar's command matches none of these.
	grep -E "org\.gradle\.launcher\.daemon\.bootstrap\.GradleDaemon|org\.jetbrains\.kotlin\.daemon\.KotlinCompileDaemon|${TAB}([^ ]*/)?(adb|emulator|qemu-system[^ ]*) " "$E/$1.survivors" >"$E/$1.excluded"
	grep -vxFf "$E/$1.excluded" "$E/$1.survivors" >"$E/$1.orphans"
	wc -l <"$E/$1.orphans" | tr -d ' '
}

CLEANED=
cleanup() {
	rc=$?
	trap '' EXIT
	[ -z "$CLEANED" ] || exit $rc
	CLEANED=1
	[ -n "${PID:-}" ] || exit $rc
	[ -z "${CAFF:-}" ] || kill "$CAFF" 2>/dev/null
	if [ -n "${SERIAL:-}" ] && [ -z "${STOPPED_EMU:-}" ]; then
		run emulator-stop-cleanup "$DY" call --no-autostart emulator.stop --params "{\"device\":\"$SERIAL\"}"
	fi
	snapshot final
	run stop "$DY" stop
	i=0
	while [ $i -lt 50 ]; do
		kill -0 "$PID" 2>/dev/null || break
		sleep 0.2
		i=$((i + 1))
	done
	kill -0 "$PID" 2>/dev/null && kill -TERM "$PID"
	n=$(count_orphans final)
	[ -e "$DROIDYARD_STATE_DIR/run/http-token" ] && {
		echo "mvp-dod: FAIL: token file left" >&2
		rc=1
	}
	if [ "$n" != 0 ]; then
		echo "mvp-dod: FAIL: $n orphans after stop (K6): see $E/final.orphans" >&2
		rc=1
	fi
	save_log
	if [ -n "${ADB_PORT_RUN:-}" ]; then # K5: the run's own adb server, never 5037
		env -u ANDROID_ADB_SERVER_PORT "$SDK/platform-tools/adb" -P "$ADB_PORT_RUN" kill-server 2>/dev/null
	fi
	echo "state and evidence: $R, $OUT" >&2
	exit $rc
}

# ---------------------------------------------------------------------------------------
# errors

errors_mode() {
	[ $# -eq 2 ] || usage
	case $1 in "$OLD_KGP" | "$NEW_KGP") ;; *) die "kgp must be $OLD_KGP or $NEW_KGP, not $1" ;; esac
	prepare "$1" "$2"
	stamps
	SDK_CHANGED=
	trap cleanup EXIT
	launch
	trust_root
	build_phase
	downloads_and_sdk
	save_log
	{
		echo "kgp $1"
		for f in build-ok build-failed build-restored; do echo "$f: $(jq -r '"\(.result.outcome) kgp=\(.result.kgp) expected=\(.result.expectedSources.kotlin) sources=\(.result.extraction.sources)"' "$OUT/$f.json")"; done
		echo "record: $(jq -c '.result.errors[0]' "$OUT/errors.json")"
		cat "$OUT/downloads.summary"
	} >"$OUT/summary.txt"
	[ -z "$SDK_CHANGED" ] || die "K4: $SDK changed during the run: see $OUT/sdk-changes.txt"
	check_errors "$1" "$OUT" || exit 1
	externals_unchanged
}

# ---------------------------------------------------------------------------------------
# footprint

# scan_tracked: adds to tracked.tsv the pids this run's footprint concerns that are not
# in the before lists (D5): Gradle and Kotlin daemons, and, with an AVD, its emulator
# processes and the adb server on the run's port. A pid in a before list is not added.
scan_tracked() {
	ps -axo pid=,command= | while read -r p c; do
		case $c in
		*org.gradle.launcher.daemon.bootstrap.GradleDaemon*) r=gradle-daemon ;;
		*org.jetbrains.kotlin.daemon.KotlinCompileDaemon*) r=kotlin-daemon ;;
		*"-avd $AVD"*) [ -n "$AVD" ] && r=emulator || continue ;;
		*) continue ;;
		esac
		grep -qxF "$p" "$OUT/before.pids" && continue
		grep -q "^$p$TAB" "$OUT/tracked.tsv" || printf '%s\t%s\tno\n' "$p" "$r" >>"$OUT/tracked.tsv"
	done
	if [ -n "$AVD" ]; then
		for p in $(lsof -nP -iTCP:"$ADB_PORT_RUN" -sTCP:LISTEN -t 2>/dev/null | sort -u); do
			grep -q "^$p$TAB" "$OUT/tracked.tsv" || printf '%s\tadb-server\tno\n' "$p" >>"$OUT/tracked.tsv"
		done
	fi
	sp=$(sidecar_pid "$DROIDYARD_STATE_DIR/processes.json")
	[ -z "$sp" ] || grep -q "^$sp$TAB" "$OUT/tracked.tsv" || printf '%s\tsidecar\tyes\n' "$sp" >>"$OUT/tracked.tsv"
}

# sample: one row per tracked pid into footprint.tsv: time, pid, role, ours, RSS in KiB
# (ps -o rss=), or - once the pid is gone.
sample() {
	scan_tracked
	t=$(date +%s)
	while IFS="$TAB" read -r p r o; do
		v=$(ps -o rss= -p "$p" 2>/dev/null | tr -d ' ')
		printf '%s\t%s\t%s\t%s\t%s\n' "$t" "$p" "$r" "$o" "${v:--}" >>"$OUT/footprint.tsv"
	done <"$OUT/tracked.tsv"
}

before_lists() {
	ps -axo pid=,command= | awk '/org.gradle.launcher.daemon.bootstrap.GradleDaemon|org.jetbrains.kotlin.daemon.KotlinCompileDaemon|-avd /{ print $1 }' >"$OUT/before.pids"
	[ -z "$AVD" ] || lsof -nP -iTCP:"$ADB_PORT_RUN" -sTCP:LISTEN -t 2>/dev/null | sort -u >>"$OUT/before.pids"
	: >"$OUT/footprint.tsv"
	printf '%s\tcore\tyes\n' "$PID" >"$OUT/tracked.tsv"
}

footprint_mode() {
	[ $# -ge 1 ] && [ $# -le 2 ] || usage
	AVD=${2:-}
	ADB_PORT_RUN=
	if [ -n "$AVD" ]; then
		case ${ANDROID_ADB_SERVER_PORT:-} in "" | 5037) die "K5: ANDROID_ADB_SERVER_PORT must name the run's own adb server, not '${ANDROID_ADB_SERVER_PORT:-}'" ;; esac
		[ -n "${ANDROID_AVD_HOME:-}" ] || die "K5: ANDROID_AVD_HOME must name the scratch AVD directory"
		[ -x "${ADB:-}" ] || die "K5: ADB must name the guard script"
		ADB_PORT_RUN=$ANDROID_ADB_SERVER_PORT
	fi
	prepare "$OLD_KGP" "$1"
	stamps
	SDK_CHANGED=
	SERIAL=
	trap cleanup EXIT
	launch
	trust_root
	before_lists
	[ -z "${CAFF:-}" ] && command -v caffeinate >/dev/null && {
		caffeinate -i -w $$ &
		CAFF=$!
	}

	build_phase
	cp "$OUT/build-restored.json" "$OUT/build-last.json"
	build_end=$(date +%s)
	sleep 30
	sample # D4: the Gradle and Kotlin daemons, 30 s after the last build
	last_call=$(date +%s)

	if [ -n "$AVD" ]; then
		gcall emu-start emulator.start "{\"avd\":\"$AVD\"}" || die "emulator.start failed: $(cat "$E/emu-start.out")"
		SERIAL=$(jq -r .result.device "$E/emu-start.out")
		[ -n "$SERIAL" ] && [ "$SERIAL" != null ] || die "emulator.start returned no serial"
		call install device.install "{\"device\":\"$SERIAL\",\"build\":{\"module\":\":app\",\"variant\":\"debug\"}}" || die "device.install failed"
		gcall launch device.launch "{\"device\":\"$SERIAL\",\"applicationId\":\"$APP_ID\"}" || die "device.launch failed"
		gcall logs device.logs "{\"device\":\"$SERIAL\",\"applicationId\":\"$APP_ID\",\"maxLines\":50}" || die "device.logs failed"
		gcall shot device.screenshot "{\"device\":\"$SERIAL\"}" || die "device.screenshot failed"
		gcall uitree device.uiTree "{\"device\":\"$SERIAL\"}" || die "device.uiTree failed"
		gcall input device.inputText "{\"device\":\"$SERIAL\",\"text\":\"Droid\",\"target\":{\"id\":\"name\"}}" || die "device.inputText failed"
		gcall tap device.tapElement "{\"device\":\"$SERIAL\",\"target\":{\"id\":\"submit\"},\"expect\":[{\"appears\":{\"id\":\"greeting\"}}]}" || die "device.tapElement failed"
		last_call=$(date +%s)
	fi

	snapshot window
	cp "$DROIDYARD_STATE_DIR/processes.json" "$OUT/processes.window.json"
	old_sidecar=$(sidecar_pid "$OUT/processes.window.json")
	[ -n "$old_sidecar" ] || die "no sidecar record when the window began: the builds did not leave one"
	{
		echo "build_end=$build_end"
		echo "last_call=$last_call"
		echo "old_sidecar=$old_sidecar"
		echo "avd=$AVD"
		echo "adb_port=$ADB_PORT_RUN"
	} >"$OUT/marks.txt"

	# D4: no call for 16 minutes, sampled every 30 s from the last call.
	k=0
	while [ $k -le 32 ]; do
		sample
		[ $k -lt 32 ] && sleep 30
		k=$((k + 1))
	done
	cp "$DROIDYARD_STATE_DIR/processes.json" "$OUT/processes.after.json"

	# The restart: a build on a new sidecar.
	call restart build.assemble '{"module":":app","variant":"debug"}' || die "the restart build errored as a call"
	cp "$E/restart.out" "$OUT/restart.json"
	cp "$DROIDYARD_STATE_DIR/processes.json" "$OUT/processes.restart.json"

	if [ -n "$SERIAL" ]; then
		gcall emu-stop emulator.stop "{\"device\":\"$SERIAL\"}" || die "emulator.stop failed"
		STOPPED_EMU=1
	fi
	downloads_and_sdk
	save_log
	[ -z "$SDK_CHANGED" ] || die "K4: $SDK changed during the run: see $OUT/sdk-changes.txt"
	check_footprint "$OUT" || exit 1
	externals_unchanged
}

# ---------------------------------------------------------------------------------------

[ $# -ge 1 ] || usage
mode=$1
shift
case $mode in
errors) errors_mode "$@" ;;
footprint) footprint_mode "$@" ;;
check-errors)
	[ $# -eq 2 ] || usage
	check_errors "$1" "$2"
	;;
check-footprint)
	[ $# -eq 1 ] || usage
	check_footprint "$1"
	;;
*) usage ;;
esac
