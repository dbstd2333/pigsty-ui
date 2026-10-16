# -*- coding: utf-8 -*-
"""Pigsty Console ansible callback plugin.

Writes one ndjson event per line to the file named by the environment variable
``PIGSTY_EVENTS_FILE``; the console tails that file and streams the events to the
browser. Tasks marked ``no_log`` never emit a runner result, so secrets rendered
inside them cannot leak into the console log.
"""

from __future__ import annotations

import json
import os
import time

from ansible.plugins.callback import CallbackBase

DOCUMENTATION = """
    name: pigsty_events
    type: notification
    short_description: stream ansible events as ndjson to the Pigsty Console
    description:
      - Emits playbook_start, play_start, task_start, runner_result and stats
        events as one JSON object per line.
      - Two columns of protection: this plugin skips no_log tasks, and the
        console masks the values of sensitive inventory parameters.
    requirements:
      - Enabled via ANSIBLE_CALLBACKS_ENABLED=pigsty_events
"""


class CallbackModule(CallbackBase):
    """Stream ansible execution events as ndjson."""

    CALLBACK_VERSION = 2.0
    CALLBACK_TYPE = "notification"
    CALLBACK_NAME = "pigsty_events"
    CALLBACK_NEEDS_ENABLED = True

    def __init__(self, display=None):
        super().__init__(display=display)
        self._path = os.environ.get("PIGSTY_EVENTS_FILE", "")
        self._play = ""
        self._playbook = ""
        self._started = {}

    # ------------------------------------------------------------------ #
    # helpers
    # ------------------------------------------------------------------ #
    def _emit(self, payload):
        """Append one event line; never break a playbook because of logging."""
        if not self._path:
            return
        payload.setdefault("ts", self._now())
        try:
            with open(self._path, "a", encoding="utf-8") as handle:
                handle.write(json.dumps(payload, ensure_ascii=False) + "\n")
        except OSError:
            pass

    @staticmethod
    def _now():
        """Return the current UTC timestamp in RFC 3339 form."""
        return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())

    @staticmethod
    def _task_id(task):
        """Return the stable uuid of a task."""
        return str(getattr(task, "_uuid", "") or "")

    @staticmethod
    def _no_log(task):
        """Report whether a task opted out of logging its result."""
        return bool(getattr(task, "no_log", False))

    @staticmethod
    def _task_name(task):
        """Return the qualified name of a task."""
        try:
            return str(task.get_name())
        except Exception:  # noqa: BLE001 - never break the run for a name
            return str(task)

    def _runner(self, result, status):
        """Emit one runner result, honouring no_log."""
        task = getattr(result, "_task", None)
        if task is None or self._no_log(task):
            return
        task_id = self._task_id(task)
        started = self._started.pop(task_id, None)
        duration = int((time.time() - started) * 1000) if started else None
        payload = {
            "type": "runner_result",
            "play": self._play,
            "playbook": self._playbook,
            "task": self._task_name(task),
            "task_id": task_id,
            "host": str(result._host.get_name()),
            "status": status,
        }
        if duration is not None:
            payload["duration_ms"] = duration
        message = self._message(result)
        if message:
            payload["msg"] = message
        self._emit(payload)

    @staticmethod
    def _message(result):
        """Extract a short human readable message from a result."""
        data = getattr(result, "_result", None)
        if not isinstance(data, dict):
            return ""
        text = data.get("msg") or data.get("stderr") or ""
        if data.get("stdout") and not text:
            text = data["stdout"]
        text = str(text).strip()
        if len(text) > 400:
            text = text[:400] + "..."
        return text

    # ------------------------------------------------------------------ #
    # playbook level callbacks
    # ------------------------------------------------------------------ #
    def v2_playbook_on_start(self, playbook):
        """Emit the playbook start event."""
        name = os.path.basename(str(getattr(playbook, "_file_name", "") or "playbook.yml"))
        self._playbook = name
        self._emit({"type": "playbook_start", "playbook": name})

    def v2_playbook_on_play_start(self, play):
        """Remember and emit the play name."""
        try:
            self._play = str(play.get_name())
        except Exception:  # noqa: BLE001
            self._play = ""
        self._emit({"type": "play_start", "play": self._play, "playbook": self._playbook})

    def v2_playbook_on_task_start(self, task, is_conditional):
        """Emit the task start event."""
        task_id = self._task_id(task)
        self._started[task_id] = time.time()
        self._emit({
            "type": "task_start",
            "play": self._play,
            "playbook": self._playbook,
            "task": self._task_name(task),
            "task_id": task_id,
        })

    def v2_playbook_on_stats(self, stats):
        """Emit the per host summary of a playbook run."""
        hosts = {}
        for host in stats.processed.keys():
            summary = stats.summarize(host)
            hosts[host] = {
                "ok": summary.get("ok", 0),
                "changed": summary.get("changed", 0),
                "failed": summary.get("failures", 0),
                "unreachable": summary.get("unreachable", 0),
                "skipped": summary.get("skipped", 0),
            }
        self._emit({"type": "stats", "playbook": self._playbook, "hosts": hosts})

    # ------------------------------------------------------------------ #
    # runner callbacks
    # ------------------------------------------------------------------ #
    def v2_runner_on_ok(self, result):
        """Emit a successful (or changed) task result."""
        self._runner(result, "changed" if result._result.get("changed", False) else "ok")

    def v2_runner_on_failed(self, result, ignore_errors=False):
        """Emit a failed task result."""
        self._runner(result, "failed")

    def v2_runner_on_skipped(self, result):
        """Emit a skipped task result."""
        self._runner(result, "skipped")

    def v2_runner_on_unreachable(self, result):
        """Emit an unreachable host result."""
        self._runner(result, "unreachable")

    def v2_runner_on_async_failed(self, result):
        """Emit an async task failure."""
        self._runner(result, "failed")
