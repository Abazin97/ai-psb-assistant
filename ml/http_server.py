"""HTTP transport for the existing ML question/answer contract.

The successful wire format mirrors solution.py's CSV columns:
    request:  {"id": "...", "question": "..."}
    response: {"id": "...", "answer": "..."}
"""

from __future__ import annotations

import hashlib
import json
import logging
import os
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import urlsplit

import httpx

import solution as ml


LOG = logging.getLogger("psb.ml.http")
MAX_BODY_BYTES = 1 << 20


class ServiceUnavailable(RuntimeError):
    pass


class ServiceBusy(RuntimeError):
    pass


class InferenceFailed(RuntimeError):
    pass


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


class Runtime:
    def __init__(self) -> None:
        self.archive = ml.find_file("kb.zip", "knowledge-base.zip", "knowledge_base.zip")
        self.documents = ml.load_documents(self.archive)
        self.chunks = ml.make_chunks(self.documents)
        self.retriever = ml.Retriever(self.documents, self.chunks)
        self.kb_hash = sha256_file(self.archive)

        self.context_chars = ml.env_int("CONTEXT_CHARS", 34000, 7500, 70000)
        self.context_top_n = ml.env_int("CONTEXT_TOP_N", 10, 4, 20)
        self.candidate_count = ml.env_int("CANDIDATE_COUNT", 48, 16, 80)
        self.request_budget = ml.env_int("ML_REQUEST_BUDGET_SECONDS", 130, 60, 3600)
        self.use_embeddings = ml.enabled("USE_EMBEDDINGS", default=True)
        self.use_reranker = ml.enabled("USE_RERANKER", default=True)
        self.max_workers = ml.env_int("MAX_WORKERS", 4, 1, 8)
        self.slots = threading.BoundedSemaphore(self.max_workers)
        self.client = httpx.Client(
            follow_redirects=False,
            limits=httpx.Limits(max_connections=self.max_workers + 4),
        )

        self.settings = None
        self.settings_error = ""
        try:
            self.settings = ml.Settings.from_env()
        except ValueError as exc:
            self.settings_error = str(exc)
            LOG.warning("LLM is not configured: %s", exc)

        if self.settings and self.use_embeddings:
            bootstrap = ml.API(
                self.client,
                self.settings,
                deadline=time.monotonic() + 180,
            )
            try:
                # Cache document vectors once at startup. Each request only
                # embeds its own retrieval query.
                ml.initialize_dense(bootstrap, self.retriever, [], time_limit=150)
                LOG.info("Dense index initialized (%d chunks)", len(self.chunks))
            except Exception as exc:
                self.retriever.embeddings = None
                LOG.warning(
                    "Embeddings unavailable (%s); using BM25 retrieval",
                    type(exc).__name__,
                )

    def health(self) -> dict:
        return {
            "status": "ok" if self.settings else "degraded",
            "kb_hash": self.kb_hash,
            "chunks": len(self.chunks),
            "mode": "live" if self.settings else "unconfigured",
            "llm_configured": self.settings is not None,
        }

    def answer(self, question: str) -> str:
        if not self.settings:
            raise ServiceUnavailable(self.settings_error or "LLM is not configured")
        if not self.slots.acquire(blocking=False):
            raise ServiceBusy("Too many requests are being processed")
        try:
            return self._answer(question)
        finally:
            self.slots.release()

    def _answer(self, question: str) -> str:
        if not question.strip():
            return "Уточните, пожалуйста, ваш вопрос."

        started = time.monotonic()
        api = ml.API(
            self.client,
            self.settings,
            deadline=started + self.request_budget,
        )
        query = ml.retrieval_query(question)
        vector = None
        if self.retriever.embeddings is not None:
            try:
                vector = api.embeddings([query], deadline=api.deadline)[0]
            except Exception as exc:
                LOG.warning(
                    "Query embedding unavailable (%s); using lexical retrieval",
                    type(exc).__name__,
                )

        order = self.retriever.candidates(
            query,
            vector,
            limit=self.candidate_count,
        )
        if self.use_reranker and order and api.deadline - time.monotonic() > 100:
            try:
                positions = api.rerank(
                    query,
                    [self.retriever.texts[index] for index in order],
                    top_n=14,
                )
                order = list(dict.fromkeys([order[index] for index in positions] + order))
            except Exception as exc:
                LOG.warning(
                    "Reranker unavailable (%s); keeping hybrid retrieval order",
                    type(exc).__name__,
                )

        blocks = self.retriever.context(
            question,
            order,
            max_chars=self.context_chars,
            top_n=self.context_top_n,
        )
        try:
            try:
                answer = api.generate(ml.messages(question, self.documents, blocks))
            except ml.APIStatusError as exc:
                if exc.status_code not in {400, 413, 422}:
                    raise
                compact = self.retriever.context(
                    question,
                    order,
                    max_chars=min(7500, self.context_chars),
                    top_n=4,
                )
                if sum(map(len, compact)) >= sum(map(len, blocks)):
                    raise
                LOG.warning("Generation rejected full context; retrying with compact context")
                answer = api.generate(ml.messages(question, self.documents, compact))
        except Exception as exc:
            raise InferenceFailed(type(exc).__name__) from exc
        return answer

    def close(self) -> None:
        self.client.close()


def make_handler(runtime: Runtime):
    class Handler(BaseHTTPRequestHandler):
        server_version = "PSB-ML-HTTP/1.0"

        def do_GET(self) -> None:  # noqa: N802 - BaseHTTPRequestHandler API
            path = urlsplit(self.path).path
            if path == "/health":
                self._json(200, runtime.health())
                return
            self._json(404, {"error": {"code": "not_found", "message": "Not found"}})

        def do_POST(self) -> None:  # noqa: N802 - BaseHTTPRequestHandler API
            if urlsplit(self.path).path != "/answer":
                self._json(404, {"error": {"code": "not_found", "message": "Not found"}})
                return
            if self.headers.get_content_type() != "application/json":
                self._json(415, {"error": {"code": "unsupported_media_type", "message": "Expected application/json"}})
                return
            raw_length = self.headers.get("Content-Length", "")
            try:
                length = int(raw_length)
            except ValueError:
                self._json(400, {"error": {"code": "invalid_request", "message": "Invalid Content-Length"}})
                return
            if length < 0 or length > MAX_BODY_BYTES:
                self._json(413, {"error": {"code": "request_too_large", "message": "Request body is too large"}})
                return
            try:
                payload = json.loads(self.rfile.read(length))
            except (UnicodeDecodeError, json.JSONDecodeError):
                self._json(400, {"error": {"code": "invalid_request", "message": "Invalid JSON"}})
                return
            if not isinstance(payload, dict) or set(payload) != {"id", "question"}:
                self._json(400, {"error": {"code": "invalid_request", "message": "Expected exactly id and question"}})
                return
            request_id, question = payload["id"], payload["question"]
            if not isinstance(request_id, str) or not request_id.strip() or len(request_id) > 128:
                self._json(400, {"error": {"code": "invalid_request", "message": "Invalid id"}})
                return
            if not isinstance(question, str):
                self._json(400, {"error": {"code": "invalid_request", "message": "Question must be a string"}})
                return

            try:
                answer = runtime.answer(question)
            except ServiceUnavailable:
                self._json(503, {"error": {"code": "llm_unavailable", "message": "LLM is not configured"}})
                return
            except ServiceBusy:
                self._json(503, {"error": {"code": "busy", "message": "ML service is busy"}})
                return
            except InferenceFailed as exc:
                LOG.exception("Inference failed: %s", exc)
                self._json(502, {"error": {"code": "inference_failed", "message": "Could not generate an answer"}})
                return
            # Keep the success body aligned with the module's id,answer output.
            self._json(200, {"id": request_id, "answer": answer})

        def _json(self, status: int, payload: dict) -> None:
            data = json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
            self.send_response(status)
            self.send_header("Content-Type", "application/json; charset=utf-8")
            self.send_header("Content-Length", str(len(data)))
            self.send_header("Cache-Control", "no-store")
            self.end_headers()
            self.wfile.write(data)

        def log_message(self, fmt: str, *args) -> None:
            LOG.info("%s - %s", self.address_string(), fmt % args)

    return Handler


def main() -> None:
    logging.basicConfig(
        level=os.getenv("LOG_LEVEL", "INFO").upper(),
        format="%(asctime)s %(levelname)s %(name)s %(message)s",
    )
    runtime = Runtime()
    port = int(os.getenv("PORT", "8000"))
    if not 1 <= port <= 65535:
        raise ValueError("PORT must be between 1 and 65535")
    server = ThreadingHTTPServer(("0.0.0.0", port), make_handler(runtime))
    server.daemon_threads = True
    LOG.info(
        "ML HTTP server listening on port %d (%d documents, %d chunks)",
        port,
        len(runtime.documents),
        len(runtime.chunks),
    )
    try:
        server.serve_forever()
    finally:
        server.server_close()
        runtime.close()


if __name__ == "__main__":
    main()
