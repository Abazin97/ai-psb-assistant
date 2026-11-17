"""HTTP transport for the existing ML question/answer contract.

The successful wire format mirrors solution.py's CSV columns:
    request:  {"id": "...", "question": "..."}
    response: {"id": "...", "kind": "answer|clarification", "answer": "...", "sources": [...]}
"""

from __future__ import annotations

import hashlib
import io
import json
import logging
import os
import threading
import time
import zipfile
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from xml.sax.saxutils import escape
from urllib.parse import quote, urlsplit

import httpx

import solution as ml


LOG = logging.getLogger("psb.ml.http")
MAX_BODY_BYTES = 1 << 20
DOCX_CONTENT_TYPE = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
CLARIFICATION_ANSWER = (
    "Уточните, пожалуйста, юридический вопрос: укажите документ, сторону "
    "или обстоятельства, которые нужно проверить по базе."
)


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


def source_min_cosine() -> float:
    value = os.getenv("ML_SOURCE_MIN_COSINE", "0.45")
    try:
        threshold = float(value)
    except ValueError as exc:
        raise ValueError("ML_SOURCE_MIN_COSINE must be a number from 0 to 1") from exc
    if not 0 <= threshold <= 1:
        raise ValueError("ML_SOURCE_MIN_COSINE must be a number from 0 to 1")
    return threshold


class Runtime:
    def __init__(self) -> None:
        self.archive = ml.find_file("kb.zip", "knowledge-base.zip", "knowledge_base.zip")
        self.documents = ml.load_documents(self.archive)
        self.chunks = ml.make_chunks(self.documents)
        self.retriever = ml.Retriever(self.documents, self.chunks)
        self.kb_hash = sha256_file(self.archive)
        self.document_ids = {
            document.id: f"doc-{hashlib.sha256(document.name.encode('utf-8')).hexdigest()[:24]}"
            for document in self.documents
        }
        self.documents_by_id = {
            self.document_ids[document.id]: document for document in self.documents
        }

        self.context_chars = ml.env_int("CONTEXT_CHARS", 34000, 7500, 70000)
        self.context_top_n = ml.env_int("CONTEXT_TOP_N", 10, 4, 20)
        self.candidate_count = ml.env_int("CANDIDATE_COUNT", 48, 16, 80)
        self.request_budget = ml.env_int("ML_REQUEST_BUDGET_SECONDS", 130, 60, 3600)
        self.min_source_cosine = source_min_cosine()
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

    def answer(self, question: str) -> tuple[str, str, list[dict]]:
        if not self.settings:
            raise ServiceUnavailable(self.settings_error or "LLM is not configured")
        if not self.slots.acquire(blocking=False):
            raise ServiceBusy("Too many requests are being processed")
        try:
            return self._answer(question)
        finally:
            self.slots.release()

    def _answer(self, question: str) -> tuple[str, str, list[dict]]:
        if not question.strip():
            return CLARIFICATION_ANSWER, "clarification", []

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
        used_blocks = blocks
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
                used_blocks = compact
                answer = api.generate(ml.messages(question, self.documents, compact))
        except Exception as exc:
            raise InferenceFailed(type(exc).__name__) from exc
        if not self._has_relevant_evidence(question, vector):
            LOG.info("No relevant source found; returning clarification without sources")
            return CLARIFICATION_ANSWER, "clarification", []
        return answer, "answer", self._sources_for_context(order, used_blocks)

    def _has_relevant_evidence(self, question: str, vector: list[float] | None) -> bool:
        """Decide whether retrieved passages are strong enough to cite.

        This is a local post-retrieval gate: it does not alter the prompt or
        payload sent to the external LLM. Exact document/statute mentions and
        lexical overlap provide the primary signal; cached embeddings help
        with paraphrases when available.
        """
        turns = ml.user_turns(question)
        last_question = turns[-1] if turns else question
        question_terms = set(ml.tokens(last_question))

        # Exact contract, counterparty, or statutory references are strong
        # anchors, including short questions such as "а по ст. 59 ТК РФ?".
        if any(ml.mentions(document, last_question) for document in self.documents):
            return True

        if question_terms:
            # Gate on the latest user turn, not the packed dialogue. That way
            # an unrelated new question cannot inherit relevance from an old
            # legal question in the same chat.
            scores = self.retriever.bm25.scores(last_question)
            for chunk_index in ml.ranking(scores)[:8]:
                if scores[chunk_index] <= 0:
                    break
                chunk = self.chunks[chunk_index]
                passage_terms = set(ml.tokens(chunk.text + " " + chunk.prefix))
                overlap = question_terms & passage_terms
                coverage = len(overlap) / len(question_terms)
                min_overlap = 1 if len(question_terms) == 1 and len(turns) > 1 else 2
                if len(overlap) >= min_overlap and coverage >= 0.25:
                    return True

        # The query vector is already computed for normal retrieval. It can
        # support paraphrases when this is a single-turn chat; for later turns
        # it also contains prior questions, so lexical gating above is safer.
        if vector is not None and len(turns) <= 1 and self.retriever.embeddings:
            best_similarity = max(
                sum(left * right for left, right in zip(vector, embedding))
                for embedding in self.retriever.embeddings
            )
            return best_similarity >= self.min_source_cosine
        return False

    def _sources_for_context(self, order: list[int], blocks: list[str]) -> list[dict]:
        sources = []
        seen = set()
        for chunk_index in order:
            if len(sources) >= self.context_top_n:
                break
            chunk = self.chunks[chunk_index]
            doc = self.documents[chunk.doc_id]
            if not chunk.text or not any(
                block.startswith(f"ИСТОЧНИК: {doc.title}\n") and chunk.text in block
                for block in blocks
            ):
                continue
            doc_id = self.document_ids[doc.id]
            key = (doc_id, chunk.location)
            if key in seen:
                continue
            seen.add(key)
            sources.append({
                "ordinal": len(sources) + 1,
                "doc_id": doc_id,
                "doc_title": doc.title,
                "fragment_id": chunk.location,
                "text": chunk.text,
            })
        return sources

    def document_file(self, doc_id: str) -> tuple[bytes, str, str] | None:
        document = self.documents_by_id.get(doc_id)
        if document is None:
            return None
        original_name = Path(document.name).name
        filename = f"{Path(original_name).stem}.docx"
        with zipfile.ZipFile(self.archive) as archive:
            original = archive.read(document.name)
        if Path(original_name).suffix.lower() == ".docx":
            content = original
        else:
            content = text_to_docx(document.text)
        return content, DOCX_CONTENT_TYPE, filename

    def close(self) -> None:
        self.client.close()


def text_to_docx(text: str) -> bytes:
    word_ns = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
    paragraphs = []
    for line in text.split("\n"):
        if line:
            paragraphs.append(
                f'<w:p><w:r><w:t xml:space="preserve">{escape(line)}</w:t></w:r></w:p>'
            )
        else:
            paragraphs.append("<w:p/>")
    document_xml = (
        '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
        f'<w:document xmlns:w="{word_ns}"><w:body>{"".join(paragraphs)}'
        '<w:sectPr><w:pgSz w:w="12240" w:h="15840"/>'
        '<w:pgMar w:top="1440" w:right="1440" w:bottom="1440" w:left="1440" '
        'w:header="720" w:footer="720" w:gutter="0"/></w:sectPr></w:body></w:document>'
    )
    content_types = (
        '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
        '<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">'
        '<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>'
        '<Default Extension="xml" ContentType="application/xml"/>'
        '<Override PartName="/word/document.xml" '
        'ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>'
        '</Types>'
    )
    relationships = (
        '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
        '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">'
        '<Relationship Id="rId1" '
        'Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" '
        'Target="word/document.xml"/></Relationships>'
    )
    output = io.BytesIO()
    with zipfile.ZipFile(output, "w", compression=zipfile.ZIP_DEFLATED) as archive:
        archive.writestr("[Content_Types].xml", content_types)
        archive.writestr("_rels/.rels", relationships)
        archive.writestr("word/document.xml", document_xml)
    return output.getvalue()


def make_handler(runtime: Runtime):
    class Handler(BaseHTTPRequestHandler):
        server_version = "PSB-ML-HTTP/1.0"

        def do_GET(self) -> None:  # noqa: N802 - BaseHTTPRequestHandler API
            path = urlsplit(self.path).path
            if path == "/health":
                self._json(200, runtime.health())
                return
            if path.startswith("/documents/"):
                doc_id = path.removeprefix("/documents/")
                document = runtime.document_file(doc_id)
                if document is None:
                    self._json(404, {"error": {"code": "document_not_found", "message": "Document not found"}})
                    return
                content, content_type, filename = document
                self._file(content, content_type, filename)
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
                answer, kind, sources = runtime.answer(question)
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
            self._json(200, {"id": request_id, "kind": kind, "answer": answer, "sources": sources})

        def _file(self, data: bytes, content_type: str, filename: str) -> None:
            self.send_response(200)
            self.send_header("Content-Type", content_type)
            self.send_header("Content-Length", str(len(data)))
            self.send_header("Content-Disposition", f"attachment; filename*=UTF-8''{quote(filename, safe='')}")
            self.send_header("Cache-Control", "private, max-age=300")
            self.send_header("X-Content-Type-Options", "nosniff")
            self.end_headers()
            self.wfile.write(data)

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
