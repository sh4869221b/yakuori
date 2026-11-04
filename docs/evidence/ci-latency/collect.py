#!/usr/bin/env python3
"""Read-only GitHub Actions timing collector. Requires authenticated `gh`.

Examples:
  python3 /tmp/yakuori-ci-measure.py --group baseline=RUN1,RUN2,RUN3 --output report.json
  python3 /tmp/yakuori-ci-measure.py RUN1 RUN2 --logs --output report.json

All attempts and paginated jobs are retained, including failures. Group wall times
use earliest start through latest finish, never the sum of parallel job durations.
No workflow is created, modified, retried, or cancelled by this program.
"""

import argparse
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timezone
import json
from pathlib import Path
import re
import subprocess
import sys


def timestamp(value):
    return datetime.fromisoformat(value.replace("Z", "+00:00")) if value else None


def seconds(start, end):
    return (timestamp(end) - timestamp(start)).total_seconds() if start and end else None


def first(values):
    return min((v for v in values if v), default=None)


def last(values):
    return max((v for v in values if v), default=None)


def safe_error(message):
    # Redirect URLs can contain signed query credentials. Never emit those URLs.
    return re.sub(r"https?://[^\s\"<>]+", "[remote URL redacted]", message).strip()[:500]


def gh(endpoint, raw=False):
    result = subprocess.run(
        ["gh", "api", "--method", "GET", "-H", "Accept: application/vnd.github+json", endpoint],
        capture_output=True, text=True, timeout=60,
    )
    if result.returncode:
        raise RuntimeError(safe_error(result.stderr) or f"gh exited {result.returncode}")
    return result.stdout if raw else json.loads(result.stdout)


def evidence_from_logs(repo, job_id):
    # Only narrowly relevant lines are retained. Full logs never enter the report.
    patterns = {
        "runner_cpu": re.compile(r"model name\s*:|cpu model\s*:|architecture\s*:|CPU\(s\)\s*:|Intel\(R\)|AMD EPYC", re.I),
        "model_fixture": re.compile(r"huggingface\.co|sha256sum|\.gguf.*(?:OK|sha256)|(?:model|fixture).*SHA-?256", re.I),
        "experiment": re.compile(r"(?:long.?context|token.?counted|generation).*(?:elapsed|duration|tokens|deadline)|(?:elapsed|duration).*(?:cpu|inference|token)", re.I),
    }
    try:
        data = gh(f"repos/{repo}/actions/jobs/{job_id}/logs", raw=True)
    except (RuntimeError, subprocess.TimeoutExpired) as exc:
        return {"status": "unavailable", "error": safe_error(str(exc))}
    selected = {key: [] for key in patterns}
    counts = {key: 0 for key in patterns}
    for line in data.splitlines():
        for key, pattern in patterns.items():
            if pattern.search(line):
                counts[key] += 1
                if len(selected[key]) < 12:
                    # Model URLs are public, but remove any query in case a log includes signed links.
                    clean = re.sub(r"(https?://[^\s?\"]+)\?[^\s\"]+", r"\1?[query redacted]", line)
                    selected[key].append(clean[:700])
    return {"status": "collected", "matching_line_counts": counts, "excerpts": selected}


def job_record(job, repo, collect_logs):
    result = {
        key: job.get(key)
        for key in ("id", "name", "status", "conclusion", "run_attempt", "head_sha", "started_at", "completed_at", "runner_name", "runner_group_name")
    }
    result["url"] = job.get("html_url")
    result["runner_labels"] = job.get("labels", [])
    result["duration_seconds"] = seconds(job.get("started_at"), job.get("completed_at"))
    result["steps"] = [
        {
            **{key: step.get(key) for key in ("number", "name", "status", "conclusion", "started_at", "completed_at")},
            "duration_seconds": seconds(step.get("started_at"), step.get("completed_at")),
        }
        for step in job.get("steps", [])
    ]
    if collect_logs and ("cpu" in job.get("name", "").lower() or "gguf" in job.get("name", "").lower()):
        result["log_evidence"] = evidence_from_logs(repo, job["id"])
    return result


def collect_run(repo, run_id, collect_logs):
    prefix = f"repos/{repo}/actions/runs/{run_id}"
    meta = gh(prefix)
    result = {
        key: meta.get(key)
        for key in ("id", "name", "path", "workflow_id", "event", "head_sha", "head_branch", "created_at", "updated_at", "run_started_at", "run_attempt", "status", "conclusion")
    }
    result["url"] = meta.get("html_url")
    result["attempts"] = []
    for number in range(1, meta["run_attempt"] + 1):
        try:
            attempt = gh(f"{prefix}/attempts/{number}")
            jobs = []
            page = 1
            while True:
                response = gh(f"{prefix}/attempts/{number}/jobs?per_page=100&page={page}")
                jobs.extend(response["jobs"])
                if len(jobs) >= response["total_count"] or not response["jobs"]:
                    break
                page += 1
            records = [job_record(job, repo, collect_logs) for job in jobs]
            started = first(job.get("started_at") for job in jobs)
            finished = last(job.get("completed_at") for job in jobs)
            complete = bool(jobs) and all(job.get("status") == "completed" for job in jobs)
            row = {
                "attempt": number,
                "url": f"https://github.com/{repo}/actions/runs/{run_id}/attempts/{number}",
                "status": attempt.get("status"),
                "conclusion": attempt.get("conclusion"),
                "run_created_at": meta.get("created_at"),
                "attempt_created_at": attempt.get("created_at"),
                "run_started_at": attempt.get("run_started_at"),
                "run_updated_at": attempt.get("updated_at"),
                "first_job_started_at": started,
                "latest_job_completed_at": finished,
                "all_jobs_terminal": complete,
                "job_execution_span_seconds": seconds(started, finished) if complete else None,
                "run_start_to_all_jobs_finished_seconds": seconds(attempt.get("run_started_at"), finished) if complete else None,
                "run_start_to_first_job_seconds": seconds(attempt.get("run_started_at"), started),
                "jobs": records,
            }
            result["attempts"].append(row)
        except (RuntimeError, subprocess.TimeoutExpired, ValueError, KeyError) as exc:
            result["attempts"].append({"attempt": number, "collection_error": safe_error(str(exc)), "jobs": []})
    return result


def group_window(members, expected_members):
    # Each member is one workflow attempt; original run creation is distinct from rerun start.
    jobs = [job for member in members for job in member.get("jobs", [])]
    complete = bool(members) and len(members) == expected_members and all(member.get("all_jobs_terminal") and member.get("status") == "completed" for member in members)
    starts = first(member.get("run_started_at") for member in members)
    created = first(member.get("run_created_at") for member in members)
    job_start = first(job.get("started_at") for job in jobs)
    finish = last(job.get("completed_at") for job in jobs)
    failures = [
        {"id": job["id"], "name": job["name"], "conclusion": job.get("conclusion"), "url": job["url"]}
        for job in jobs if job.get("status") == "completed" and job.get("conclusion") != "success"
    ]
    return {
        "complete": complete,
        "all_successful": complete and all(member.get("conclusion") == "success" for member in members) and not failures,
        "expected_workflows": expected_members,
        "collected_workflows": len(members),
        "earliest_original_run_created_at": created,
        "earliest_attempt_run_started_at": starts,
        "first_job_started_at": job_start,
        "latest_job_completed_at": finish,
        "seconds_from_attempt_start_until_all_jobs_finish": seconds(starts, finish) if complete else None,
        "seconds_from_first_job_until_all_jobs_finish": seconds(job_start, finish) if complete else None,
        "seconds_since_original_run_creation": seconds(created, finish) if complete else None,
        "maximum_single_job_seconds": max((job["duration_seconds"] for job in jobs if job["duration_seconds"] is not None), default=None),
        "non_successful_jobs": failures,
    }


def build_report(repo, groups, runs):
    indexed = {run["id"]: run for run in runs}
    summaries = []
    for label, ids in groups:
        selected = [indexed[run_id] for run_id in ids]
        latest = [run["attempts"][-1] for run in selected if run.get("attempts")]
        attempt_numbers = sorted({attempt["attempt"] for run in selected for attempt in run.get("attempts", [])})
        summaries.append({
            "label": label,
            "run_ids": ids,
            "head_shas": sorted({run["head_sha"] for run in selected if run.get("head_sha")}),
            "latest_attempts": group_window(latest, len(ids)),
            "by_attempt_number": [
                {"attempt": number, **group_window([attempt for run in selected for attempt in run.get("attempts", []) if attempt["attempt"] == number], len(ids))}
                for number in attempt_numbers
            ],
        })
    return {
        "schema_version": 1,
        "collected_at": datetime.now(timezone.utc).isoformat().replace("+00:00", "Z"),
        "repository": repo,
        "notes": [
            "Read-only GET requests through gh api; all available attempts and paginated jobs retained.",
            "Group durations measure earliest start to latest job finish, not the sum of parallel job durations.",
            "run_created_at is original workflow creation from the run endpoint; attempt_created_at and run_started_at distinguish later attempts.",
            "GitHub run updated_at is retained as metadata and is not treated as exact workflow completion.",
            "Maximum single-job time excludes start offsets and is not exact group wall time.",
            "Incomplete or failed collection yields null group duration; failed completed workflows remain timed.",
            "by_attempt_number is incomplete when some selected workflows lack that attempt; latest_attempts may mix attempt numbers.",
            "Runner labels/name do not establish identical CPU hardware or host load.",
        ],
        "groups": summaries,
        "runs": runs,
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("run_ids", nargs="*", type=int)
    parser.add_argument("--repo", default="sh4869221b/yakuori")
    parser.add_argument("--group", action="append", default=[], metavar="LABEL=ID,ID", help="Repeat to compare coherent workflow groups.")
    parser.add_argument("--output", type=Path)
    parser.add_argument("--logs", action="store_true", help="Try collecting bounded CPU/model excerpts from CPU jobs; errors are retained.")
    parser.add_argument("--workers", type=int, default=4)
    args = parser.parse_args()
    groups = []
    for value in args.group:
        try:
            label, raw_ids = value.split("=", 1)
            ids = list(dict.fromkeys(int(item) for item in raw_ids.split(",")))
            if not label or not ids:
                raise ValueError()
            groups.append((label, ids))
        except ValueError:
            parser.error(f"Invalid --group {value!r}; use LABEL=ID,ID")
    if args.run_ids:
        groups.append(("supplied", list(dict.fromkeys(args.run_ids))))
    if not groups:
        parser.error("Supply run IDs or at least one --group.")
    ids = list(dict.fromkeys(run_id for _, values in groups for run_id in values))

    def collect(run_id):
        try:
            return collect_run(args.repo, run_id, args.logs)
        except (RuntimeError, subprocess.TimeoutExpired, ValueError, KeyError) as exc:
            return {"id": run_id, "collection_error": safe_error(str(exc)), "attempts": []}

    with ThreadPoolExecutor(max_workers=max(1, args.workers)) as pool:
        runs = list(pool.map(collect, ids))
    report = build_report(args.repo, groups, runs)
    output = json.dumps(report, ensure_ascii=False, indent=2) + "\n"
    if args.output:
        args.output.write_text(output)
        summary = {"output": str(args.output), "runs": len(runs), "attempts": sum(len(run["attempts"]) for run in runs), "groups": [{"label": group["label"], **group["latest_attempts"]} for group in report["groups"]]}
        print(json.dumps(summary, ensure_ascii=False))
    else:
        print(output, end="")
    if any(run.get("collection_error") or any(attempt.get("collection_error") for attempt in run["attempts"]) for run in runs):
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
