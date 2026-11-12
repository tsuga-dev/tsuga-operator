#!/usr/bin/env bash
# Runs the setup Job script and postgres-setup.sql against postgres:17 in
# docker and checks which roles they create, mark, and change.
# Usage: bash hack/test-postgres-setup.sh
# Scenario functions s1..s9 are called through scenario().
# shellcheck disable=SC2317,SC2329
set -euo pipefail
cd "$(dirname "$0")/.."

name="tsuga-pg-setup-test-$$"
work="$(mktemp -d)"
cleanup() {
	docker rm -f "$name" >/dev/null 2>&1 || true
	rm -rf "$work"
}
trap cleanup EXIT

marker='managed-by: tsuga-operator'

PG_SETUP_SCRIPT_OUT="$work/setup.sh" go test ./internal/collection/ -run '^TestDumpPostgresSetupScript$' -count=1 >/dev/null
[[ -s "$work/setup.sh" ]] || {
	echo "setup script not dumped" >&2
	exit 1
}

# --auth=scram-sha-256 replaces the image's trust entries for local and
# 127.0.0.1, so a wrong password really fails.
docker run -d --name "$name" \
	-e POSTGRES_PASSWORD=admin \
	-e POSTGRES_INITDB_ARGS=--auth=scram-sha-256 \
	postgres:17 -c shared_preload_libraries=pg_stat_statements >/dev/null
docker exec "$name" mkdir -p /scripts
docker cp internal/collection/assets/postgres-setup.sql "$name:/scripts/monitoring-setup.sql" >/dev/null
docker cp "$work/setup.sh" "$name:/setup.sh" >/dev/null
for _ in $(seq 60); do
	docker exec "$name" pg_isready -q -h 127.0.0.1 && break
	sleep 1
done

admin_sql() {
	docker exec -e PGPASSWORD=admin -e PGOPTIONS='-c client_min_messages=warning' "$name" psql -X -h 127.0.0.1 -U postgres -d postgres -v ON_ERROR_STOP=1 -Atq -c "$1"
}

reset_role() {
	if [[ -n "$(admin_sql "SELECT 1 FROM pg_roles WHERE rolname = '$1'")" ]]; then
		admin_sql "DROP OWNED BY $1; DROP ROLE $1" >/dev/null
	fi
}

role_comment() {
	admin_sql "SELECT coalesce(shobj_description(oid, 'pg_authid'), '<null>') FROM pg_roles WHERE rolname = '$1'"
}

expect_comment() {
	local got
	got="$(role_comment "$1")"
	if [[ "$got" != "$2" ]]; then
		echo "  comment on $1 = '$got', want '$2'" >&2
		return 1
	fi
}

expect_login() {
	if ! docker exec -e PGPASSWORD="$2" "$name" psql -X -h 127.0.0.1 -U "$1" -d postgres -Atqc 'SELECT 1' >/dev/null 2>&1; then
		echo "  $1 cannot log in with password $2" >&2
		return 1
	fi
}

expect_no_login() {
	if docker exec -e PGPASSWORD="$2" "$name" psql -X -h 127.0.0.1 -U "$1" -d postgres -Atqc 'SELECT 1' >/dev/null 2>&1; then
		echo "  $1 can log in with password $2" >&2
		return 1
	fi
}

expect_execute() {
	if [[ "$(admin_sql "SELECT has_schema_privilege('$1', 'otel', 'USAGE') AND has_function_privilege('$1', 'otel.pg_top_queries()', 'EXECUTE')")" != t ]]; then
		echo "  $1 cannot use otel.pg_top_queries()" >&2
		return 1
	fi
}

# expect_monitor ROLE t|f: whether ROLE is a pg_monitor member.
expect_monitor() {
	local got
	got="$(admin_sql "SELECT pg_has_role('$1', 'pg_monitor', 'MEMBER')")"
	if [[ "$got" != "$2" ]]; then
		echo "  $1 pg_monitor member = $got, want $2" >&2
		return 1
	fi
}

# run_setup ROLE PASSWORD ADOPT: runs the Job script as the Job would, output
# in $work/out, exit code in $setup_rc. ADMIN_USER/ADMIN_PASSWORD override the
# admin login.
run_setup() {
	set +e
	docker exec \
		-e PGHOST=127.0.0.1 -e PGPORT=5432 -e PGDATABASE=postgres \
		-e PGUSER="${ADMIN_USER:-postgres}" -e PGPASSWORD="${ADMIN_PASSWORD:-admin}" \
		-e MONITOR_ROLE="$1" -e OTEL_MONITOR_PASSWORD="$2" -e ADOPT_LEGACY_ROLE="$3" \
		"$name" sh /setup.sh >"$work/out" 2>&1
	setup_rc=$?
	set -e
}

expect_rc() {
	if [[ "$setup_rc" != "$1" ]]; then
		echo "  setup exited $setup_rc, want $1. Output:" >&2
		sed 's/^/    /' "$work/out" >&2
		return 1
	fi
}

failed=0
scenario() {
	local n="$1" desc="$2"
	shift 2
	set +e
	(
		set -e
		"$@"
	)
	local rc=$?
	set -e
	if [[ $rc -eq 0 ]]; then
		echo "OK   $n: $desc"
	else
		echo "FAIL $n: $desc"
		failed=1
	fi
}

s1() {
	reset_role otel_monitor
	run_setup otel_monitor pw-one on
	expect_rc 0
	expect_comment otel_monitor "$marker"
	expect_login otel_monitor pw-one
	expect_execute otel_monitor
}

s2() {
	run_setup otel_monitor pw-two on
	expect_rc 0
	expect_login otel_monitor pw-two
	expect_no_login otel_monitor pw-one
}

s3() {
	reset_role dba_mon
	admin_sql "CREATE ROLE dba_mon LOGIN PASSWORD 'pw-x'"
	run_setup dba_mon pw-x off
	expect_rc 0
	expect_comment dba_mon '<null>'
	expect_login dba_mon pw-x
	expect_monitor dba_mon t
}

s4() {
	reset_role dba_mon
	admin_sql "CREATE ROLE dba_mon LOGIN PASSWORD 'pw-x'"
	run_setup dba_mon pw-y off
	expect_rc 1
	grep -q 'role dba_mon exists, is not managed by tsuga-operator, and the password in the Secret does not match' "$work/out" ||
		{
			echo "  mismatch message missing" >&2
			return 1
		}
	expect_comment dba_mon '<null>'
	expect_login dba_mon pw-x
}

s5() {
	reset_role otel_monitor
	admin_sql "CREATE ROLE otel_monitor LOGIN PASSWORD 'pw-z'"
	run_setup otel_monitor pw-z on
	expect_rc 0
	expect_comment otel_monitor "$marker"
	run_setup otel_monitor pw-rotated on
	expect_rc 0
	expect_login otel_monitor pw-rotated
}

s6() {
	reset_role otel_monitor
	admin_sql "CREATE ROLE otel_monitor LOGIN PASSWORD 'pw-z'"
	run_setup otel_monitor pw-wrong on
	expect_rc 1
	expect_comment otel_monitor '<null>'
	expect_login otel_monitor pw-z
}

s7() {
	reset_role a_mon
	reset_role b_mon
	# Functions are executable by PUBLIC by default, which would hide a lost
	# grant. Hardened servers revoke that.
	admin_sql "ALTER DEFAULT PRIVILEGES REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC;
		REVOKE EXECUTE ON ALL FUNCTIONS IN SCHEMA otel FROM PUBLIC"
	run_setup a_mon pw-a off
	expect_rc 0
	run_setup b_mon pw-b off
	expect_rc 0
	expect_execute a_mon
	expect_execute b_mon
	expect_login a_mon pw-a
}

s8() {
	reset_role hand_mon
	docker exec -e PGPASSWORD=admin "$name" \
		psql -X -h 127.0.0.1 -U postgres -d postgres -v ON_ERROR_STOP=1 -v role=hand_mon \
		-f /scripts/monitoring-setup.sql >"$work/out" 2>&1 ||
		{
			echo "  psql -f failed:" >&2
			sed 's/^/    /' "$work/out" >&2
			return 1
		}
	if grep -qiE 'error|warning' "$work/out"; then
		echo "  psql -f printed:" >&2
		sed 's/^/    /' "$work/out" >&2
		return 1
	fi
	expect_comment hand_mon "$marker"
	expect_execute hand_mon
}

# Under pg_hba trust any password logs in, so the login proves nothing.
s9() {
	reset_role otel_monitor
	admin_sql "CREATE ROLE otel_monitor LOGIN PASSWORD 'pw-dba'"
	local hba before after
	hba="$(admin_sql 'SHOW hba_file')"
	docker exec "$name" sed -i "1i host all otel_monitor 127.0.0.1/32 trust" "$hba"
	admin_sql 'SELECT pg_reload_conf()' >/dev/null
	before="$(admin_sql "SELECT rolpassword FROM pg_authid WHERE rolname = 'otel_monitor'")"
	run_setup otel_monitor pw-generated on
	after="$(admin_sql "SELECT rolpassword FROM pg_authid WHERE rolname = 'otel_monitor'")"
	docker exec "$name" sed -i '1d' "$hba"
	admin_sql 'SELECT pg_reload_conf()' >/dev/null
	expect_comment otel_monitor '<null>'
	if [[ "$before" != "$after" ]]; then
		echo "  otel_monitor password changed" >&2
		return 1
	fi
	expect_login otel_monitor pw-dba
}

# pg_monitor reads every query text in pg_stat_statements, so another
# monitoring role would read a password sent in plain text.
s10() {
	reset_role leak_mon
	run_setup leak_mon pw-leak-first off
	expect_rc 0
	run_setup leak_mon pw-leak-second off
	expect_rc 0
	expect_login leak_mon pw-leak-second
	local leaked
	leaked="$(admin_sql "SELECT count(*) FROM pg_stat_statements WHERE query LIKE '%pw-leak-%'")"
	if [[ "$leaked" != 0 ]]; then
		echo "  pg_stat_statements holds the password in $leaked statements" >&2
		return 1
	fi
}

s11() {
	reset_role dba2_mon
	admin_sql "CREATE ROLE dba2_mon LOGIN PASSWORD 'pw-x'"
	run_setup dba2_mon pw-y off
	expect_rc 1
	expect_monitor dba2_mon f
}

# otel belongs to postgres, which admin_b does not hold, so its grants are
# skipped and the role cannot read the functions.
s12() {
	reset_role b_only_mon
	reset_role admin_b
	admin_sql "REVOKE USAGE ON SCHEMA otel FROM pg_monitor;
		CREATE ROLE admin_b LOGIN CREATEROLE PASSWORD 'admin-b';
		GRANT pg_monitor TO admin_b WITH ADMIN OPTION"
	ADMIN_USER=admin_b ADMIN_PASSWORD=admin-b run_setup b_only_mon pw-b off
	admin_sql "GRANT USAGE ON SCHEMA otel TO pg_monitor"
	expect_rc 1
	grep -q 'cannot use the otel schema' "$work/out" ||
		{
			echo "  otel schema message missing:" >&2
			sed 's/^/    /' "$work/out" >&2
			return 1
		}
}

scenario 1 "fresh server, default role is created and marked" s1
scenario 2 "re-run with a new password rotates it" s2
scenario 3 "unmarked role with the matching password is left alone" s3
scenario 4 "unmarked role with another password fails, password kept" s4
scenario 5 "legacy otel_monitor proven by login is adopted, then rotates" s5
scenario 6 "legacy otel_monitor with the wrong password is not adopted" s6
scenario 7 "a second role does not remove the first role's EXECUTE" s7
scenario 8 "psql -v role=x -f by hand creates and marks x" s8
scenario 9 "legacy otel_monitor under trust auth is not adopted" s9
scenario 10 "pg_stat_statements never holds the plain-text password" s10
scenario 11 "unmarked role with another password gets no pg_monitor" s11
scenario 12 "an admin that does not own otel fails instead of going Ready" s12

exit "$failed"
