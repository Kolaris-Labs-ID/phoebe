# Fixture API on :8080 for ScreenshotTests (api.md shapes, in-memory). Run: python3 tools/mock_api.py
import json, re, uuid, datetime, threading
from http.server import ThreadingHTTPServer, BaseHTTPRequestHandler
from urllib.parse import urlparse, parse_qs

def ts(dt): return dt.strftime("%Y-%m-%dT%H:%M:%S.") + f"{dt.microsecond//1000:03d}Z"
NOW = datetime.datetime(2026, 11, 6, 1, 0, tzinfo=datetime.timezone.utc)
def at(days=0, hours=0, minutes=0): return ts(NOW + datetime.timedelta(days=days, hours=hours, minutes=minutes))
U = lambda: str(uuid.uuid4())
LOCK = threading.Lock()

USER = {"id": "11111111-1111-4111-8111-111111111111", "display_name": "Sari Wulandari", "language": "id", "timezone": "Asia/Jakarta", "created_at": at(-10)}
F1 = {"id": U(), "name": "Klien A", "meeting_count": 2, "created_at": at(-9), "updated_at": at(-9)}
F2 = {"id": U(), "name": "Internal", "meeting_count": 1, "created_at": at(-9), "updated_at": at(-9)}
T1 = {"id": U(), "name": "anggaran", "meeting_count": 1, "created_at": at(-9), "updated_at": at(-9)}
T2 = {"id": U(), "name": "mingguan", "meeting_count": 2, "created_at": at(-9), "updated_at": at(-9)}
FOLDERS, TAGS = [F1, F2], [T1, T2]

SEGS = [
 ("Selamat pagi semua, kita mulai rapat anggaran kuartal empat.", 0, 4200),
 ("Pertama, budget marketing diusulkan naik sepuluh persen dibanding kuartal lalu.", 4200, 11800),
 ("Saya setuju, tapi kita perlu rincian per kanal sebelum diputuskan.", 11800, 17500),
 ("Oke, Rina tolong siapkan proposal vendor paling lambat minggu depan.", 17500, 24000),
 ("Untuk event offline, masih belum jelas siapa penanggung jawabnya.", 24000, 30500),
 ("Budi akan cek ketersediaan venue dan kirim opsi hari Jumat.", 30500, 37200),
 ("Ada pertanyaan soal kontrak agensi yang habis bulan Desember.", 37200, 43900),
 ("Kita bahas lagi di rapat berikutnya setelah legal memberi masukan.", 43900, 51000),
 ("Terima kasih semua, rapat selesai.", 51000, 54000),
]
def tref(i): s = SEGS[i]; return {"type": "transcript", "segment_id": f"s{i+1}", "start_ms": s[1], "end_ms": s[2]}

def make_meeting(title, started, folder, tags, state, participants, duration=None, mom_review=None, docs=(), active=(), failed=None, src="recording"):
    mid = U()
    m = {"id": mid, "source": src, "title": title, "started_at": started, "timezone": "Asia/Jakarta", "language": "id",
         "participants": participants, "metadata_revision": 1, "folder_id": folder, "tag_ids": tags,
         "pipeline_state": state, "created_at": started or at(-1), "updated_at": at(-0, -1),
         "audio": None, "upload": None, "transcript": None,
         "documents": {"manual_notes": None, "ai_notes": None, "mom": None},
         "jobs": {"transcribe": None, "generate_notes": None, "generate_mom": None}}
    if duration:
        m["audio"] = {"id": U(), "content_type": "audio/mp4", "size_bytes": duration * 3, "duration_ms": duration, "sha256": "0"*64,
                      "available": True, "deleted_reason": None, "retain_until": at(29), "created_at": started}
    return {"m": m, "mom_review": mom_review, "active": list(active), "failed": failed, "revs": {}, "transcript": None}

M = []
def job(m, t, status="succeeded", err=None):
    return {"id": U(), "meeting_id": m["id"], "type": t, "trigger": "system" if t != "transcribe" else "user", "status": status,
            "attempts": 1, "max_attempts": 3, "error_category": err, "result": None, "created_at": at(-1), "started_at": at(-1),
            "finished_at": at(-1) if status in ("succeeded", "failed") else None}

def add_doc(rec, kind, content, origin="ai_generated", reviewed=False, stale=False):
    m = rec["m"]
    did = U(); rid = U()
    tr = rec["transcript"]
    src = {"transcript_revision_id": tr["id"] if tr and kind != "manual_notes" else None, "transcript_revision_number": 1 if tr and kind != "manual_notes" else None,
           "manual_notes_revision_id": None, "manual_notes_revision_number": None, "ai_notes_revision_id": None, "ai_notes_revision_number": None}
    rev = {"id": rid, "document_id": did, "kind": kind, "revision_number": 1, "origin": origin, "restored_from_revision_number": None,
           "reviewed_at": at(-0, -2) if reviewed else None, "created_at": at(-0, -3), "source": src, "content": content}
    rec["revs"][kind] = [rev]
    m["documents"][kind] = {"id": did, "kind": kind, "current_revision_number": 1, "current_revision_id": rid,
        "review_status": "reviewed" if reviewed else "draft", "is_stale": stale, "stale_reasons": ["transcript"] if stale else [], "updated_at": at(-0, -3)}

def seed():
    r1 = make_meeting("Rapat anggaran Q4", at(-0, -6), F1["id"], [T1["id"], T2["id"]], "notes_ready", ["Budi", "Sari", "Rina"], duration=54000, mom_review="draft")
    m = r1["m"]
    r1["transcript"] = {"id": U(), "revision_number": 1, "kind": "original", "language": "id", "created_at": at(-0, -5),
                        "segments": [{"id": f"s{i+1}", "start_ms": s[1], "end_ms": s[2], "text": s[0]} for i, s in enumerate(SEGS)]}
    m["transcript"] = {"current_revision_id": r1["transcript"]["id"], "current_revision_number": 1}
    m["jobs"] = {"transcribe": job(m, "transcribe"), "generate_notes": job(m, "generate_notes"), "generate_mom": job(m, "generate_mom")}
    add_doc(r1, "manual_notes", {"schema_version": 1, "items": [
        {"id": "h_n1", "text": "Budget marketing naik 10%?", "at_ms": 6000},
        {"id": "h_n2", "text": "Rina: proposal vendor", "at_ms": 19000}]}, origin="user_edit")
    add_doc(r1, "ai_notes", {"schema_version": 1, "language": "id", "sections": [
        {"id": "sec_1", "heading": "Anggaran marketing", "style": "bullet", "items": [
            {"id": "it_1", "text": "Budget marketing diusulkan naik 10% dibanding Q3.", "origin": "ai", "edited": False, "sources": [tref(1)]},
            {"id": "it_2", "text": "Perlu rincian per kanal sebelum diputuskan.", "origin": "ai", "edited": False, "sources": [tref(2)]}]},
        {"id": "sec_2", "heading": "Event offline", "style": "bullet", "items": [
            {"id": "it_3", "text": "Penanggung jawab event offline belum jelas.", "origin": "ai", "edited": False, "sources": [tref(4)]},
            {"id": "it_4", "text": "Budi mengecek venue dan mengirim opsi hari Jumat.", "origin": "ai", "edited": True, "sources": [tref(5)]}]}]})
    add_doc(r1, "mom", {"schema_version": 1, "language": "id",
        "meeting_info": {"title": "Rapat anggaran Q4", "started_at": m["started_at"], "timezone": "Asia/Jakarta", "participants": ["Budi", "Sari", "Rina"],
                         "agenda": [{"id": "it_a1", "text": "Anggaran marketing Q4", "origin": "ai", "edited": False, "sources": [tref(0)]}]},
        "sections": [
          {"id": "summary", "type": "summary", "heading": "Ringkasan", "style": "paragraph", "items": [
              {"id": "it_s1", "text": "Tim membahas usulan kenaikan budget marketing 10% dan persiapan event offline.", "origin": "ai", "edited": False, "sources": [tref(1)]}]},
          {"id": "discussion", "type": "discussion", "heading": "Pembahasan", "style": "bullet", "items": [
              {"id": "it_t1", "title": "Budget marketing", "origin": "ai", "edited": False, "sources": [tref(1)], "points": [
                  {"id": "it_p1", "text": "Naik 10% dibanding kuartal lalu.", "origin": "ai", "edited": False, "sources": [tref(1)]},
                  {"id": "it_p2", "text": "Butuh rincian per kanal.", "origin": "ai", "edited": False, "sources": [tref(2)]}]}]},
          {"id": "decisions", "type": "decisions", "heading": "Keputusan", "style": "numbered", "items": [
              {"id": "it_d1", "text": "Keputusan budget ditunda sampai rincian per kanal tersedia.", "origin": "ai", "edited": False, "sources": [tref(2)]}]},
          {"id": "action_items", "type": "action_items", "heading": "Action items", "style": None, "items": [
              {"id": "it_x1", "description": "Siapkan proposal vendor", "pic": "Rina", "deadline": "2026-11-13", "deadline_phrase": "minggu depan", "origin": "ai", "edited": False, "sources": [tref(3)]},
              {"id": "it_x2", "description": "Kirim opsi venue", "pic": "Budi", "deadline": None, "deadline_phrase": "hari Jumat", "origin": "ai", "edited": False, "sources": [tref(5)]},
              {"id": "it_x3", "description": "Tentukan penanggung jawab event offline", "pic": None, "deadline": None, "deadline_phrase": None, "origin": "ai", "edited": False, "sources": [tref(4)]}]},
          {"id": "open_issues", "type": "open_issues", "heading": "Hal terbuka", "style": "bullet", "items": [
              {"id": "it_o1", "text": "Kontrak agensi habis Desember.", "origin": "ai", "edited": False, "sources": [tref(6)]}]},
          {"id": "follow_ups", "type": "follow_ups", "heading": "Tindak lanjut", "style": "bullet", "items": [
              {"id": "it_f1", "text": "Bahas lagi setelah masukan legal.", "origin": "human", "edited": False, "sources": []}]}]})
    M.append(r1)
    r2 = make_meeting("Sinkronisasi tim produk", at(-1, -3), F2["id"], [T2["id"]], "generating_notes", ["Andi", "Sari"], duration=1830000)
    r2["transcript"] = {"id": U(), "revision_number": 1, "kind": "original", "language": "id", "created_at": at(-1), "segments": [{"id": "s1", "start_ms": 0, "end_ms": 5000, "text": "Halo semua."}]}
    r2["m"]["transcript"] = {"current_revision_id": r2["transcript"]["id"], "current_revision_number": 1}
    r2["m"]["jobs"]["transcribe"] = job(r2["m"], "transcribe"); r2["m"]["jobs"]["generate_notes"] = job(r2["m"], "generate_notes", "running")
    r2["active"] = ["generate_notes"]
    M.append(r2)
    r3 = make_meeting("Diskusi vendor", at(-2, -2), F1["id"], [], "transcription_failed", [], duration=600000, failed="transcription")
    r3["m"]["jobs"]["transcribe"] = job(r3["m"], "transcribe", "failed", "no_speech")
    M.append(r3)
    r4 = make_meeting("Retro sprint 42", at(-4), None, [], "notes_ready", [], duration=2400000, mom_review="reviewed")
    r4["transcript"] = {"id": U(), "revision_number": 1, "kind": "original", "language": "id", "created_at": at(-4), "segments": [{"id": "s1", "start_ms": 0, "end_ms": 5000, "text": "Mulai retro."}]}
    r4["m"]["transcript"] = {"current_revision_id": r4["transcript"]["id"], "current_revision_number": 1}
    M.append(r4)
seed()

def find(mid):
    return next((r for r in M if r["m"]["id"] == mid), None)

def list_item(r):
    m = r["m"]
    return {"id": m["id"], "source": m["source"], "title": m["title"], "started_at": m["started_at"], "timezone": m["timezone"],
            "folder_id": m["folder_id"], "tag_ids": m["tag_ids"], "pipeline_state": m["pipeline_state"],
            "duration_ms": m["audio"]["duration_ms"] if m["audio"] else None, "audio_available": bool(m["audio"]),
            "mom_review_status": r["mom_review"], "active_job_types": r["active"], "failed_stage": r["failed"],
            "transcription_error_category": (m["jobs"]["transcribe"] or {}).get("error_category"), "created_at": m["created_at"], "updated_at": m["updated_at"]}

USAGE = {"entitlement": {"plan": "trial", "status": "active", "period_start": at(-10), "period_end": at(20), "quota_seconds": 7200},
         "consumed_seconds": 2520, "reserved_seconds": 0, "remaining_seconds": 4680,
         "ai_generations": {"limit_per_day": 30, "used_last_24h": 3, "next_available_at": None}}
CONFIG = {"max_audio_duration_ms": 7200000, "max_upload_bytes": 262144000, "upload_chunk_max_bytes": 33554432,
          "supported_content_types": ["audio/mp4", "audio/mpeg", "audio/wav"], "audio_retention_days": 30, "languages": ["id", "en"]}
EXPORTS = {}

class H(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    def log_message(self, *a): pass
    def send(self, code, obj=None, raw=None, ctype="application/json"):
        body = raw if raw is not None else (json.dumps(obj).encode() if obj is not None else b"")
        self.send_response(code)
        if body: self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers(); self.wfile.write(body)
    def body(self):
        n = int(self.headers.get("Content-Length") or 0)
        d = self.rfile.read(n) if n else b""
        try: return json.loads(d) if d else {}
        except Exception: return {}
    def err(self, code, c): self.send(code, {"error": {"code": c, "message": c, "details": {}, "request_id": U()}})
    def do_GET(self): self.route("GET")
    def do_POST(self): self.route("POST")
    def do_PUT(self): self.route("PUT")
    def do_PATCH(self): self.route("PATCH")
    def do_DELETE(self): self.route("DELETE")
    def route(self, method):
        with LOCK:
            u = urlparse(self.path); p = u.path; q = parse_qs(u.query)
            b = self.body() if method in ("POST", "PUT", "PATCH") else {}
            print(method, p, flush=True)
            if p == "/healthz": return self.send(200, {"status": "ok"})
            if p == "/v1/config": return self.send(200, CONFIG)
            if p == "/v1/auth/dev": return self.send(200, {"token": "phb_mock", "expires_at": at(30), "created": False, "user": USER})
            if p == "/v1/auth/logout": return self.send(204)
            if p == "/v1/me":
                if method == "PATCH": USER.update({k: v for k, v in b.items() if k in USER})
                if method == "DELETE": return self.send(202, {"purge_scheduled_at": at(30)})
                return self.send(200, {"user": USER})
            if p == "/v1/usage": return self.send(200, USAGE)
            if p == "/v1/events": return self.send(202, {"accepted": len(b.get("events", []))})
            if p in ("/v1/folders", "/v1/tags"):
                coll = FOLDERS if p == "/v1/folders" else TAGS; key = "folder" if coll is FOLDERS else "tag"
                if method == "POST":
                    o = {"id": b.get("id") or U(), "name": b["name"], "meeting_count": 0, "created_at": at(), "updated_at": at()}
                    coll.append(o); return self.send(201, {key: o})
                return self.send(200, {"items": sorted(coll, key=lambda x: x["name"].lower())})
            mt = re.match(r"^/v1/(folders|tags)/([^/]+)$", p)
            if mt:
                coll = FOLDERS if mt.group(1) == "folders" else TAGS; key = mt.group(1)[:-1]
                o = next((x for x in coll if x["id"] == mt.group(2)), None)
                if not o: return self.err(404, "not_found")
                if method == "DELETE": coll.remove(o); return self.send(204)
                o["name"] = b.get("name", o["name"]); return self.send(200, {key: o})
            if p == "/v1/meetings":
                if method == "POST":
                    r = make_meeting(b.get("title", ""), b.get("started_at"), b.get("folder_id"), b.get("tag_ids", []), "awaiting_audio", b.get("participants", []), src=b.get("source", "recording"))
                    r["m"]["id"] = b.get("id") or r["m"]["id"]; M.insert(0, r)
                    return self.send(201, {"meeting": r["m"]})
                items = M
                if "q" in q: items = [r for r in items if q["q"][0].lower() in r["m"]["title"].lower()]
                if "folder_id" in q:
                    f = q["folder_id"][0]; items = [r for r in items if (r["m"]["folder_id"] is None if f == "none" else r["m"]["folder_id"] == f)]
                if "tag_id" in q: items = [r for r in items if q["tag_id"][0] in r["m"]["tag_ids"]]
                return self.send(200, {"items": [list_item(r) for r in items], "next_cursor": None})
            if p == "/v1/exports" and method == "POST":
                rev = next((v for r in M for revs in r["revs"].values() for v in revs if v["id"] == b["document_revision_id"]), None)
                if not rev: return self.err(404, "not_found")
                fmt = b["format"]; eid = U()
                ext = {"md": "md", "whatsapp": "txt", "docx": "docx", "pdf": "pdf"}[fmt]
                ct = {"md": "text/markdown; charset=utf-8", "whatsapp": "text/plain; charset=utf-8", "docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "pdf": "application/pdf"}[fmt]
                text = "*Rapat anggaran Q4*\n\nRingkasan\nTim membahas usulan kenaikan budget marketing 10%." if fmt in ("md", "whatsapp") else None
                data = (text or "%PDF-1.4\n%mock\n").encode()
                draft = rev["reviewed_at"] is None
                e = {"id": eid, "document_revision_id": rev["id"], "kind": rev["kind"], "format": fmt,
                     "filename": f"rapat-anggaran-q4-20261106-{rev['kind'].replace('_','-')}{'-draft' if draft else ''}.{ext}",
                     "content_type": ct, "size_bytes": len(data), "is_draft": draft, "created_at": at()}
                EXPORTS[eid] = (e, data)
                return self.send(201, {"export": e, "download_url": f"http://localhost:8080/v1/files/export/{eid}?exp=1&sig=x", "download_expires_at": at(0, 1), "text": text})
            mt = re.match(r"^/v1/files/export/([^/]+)$", p)
            if mt and mt.group(1) in EXPORTS:
                e, data = EXPORTS[mt.group(1)]; return self.send(200, raw=data, ctype=e["content_type"])
            mt = re.match(r"^/v1/meetings/([^/]+)(/.*)?$", p)
            if mt:
                r = find(mt.group(1)); rest = mt.group(2) or ""
                if not r: return self.err(404, "not_found")
                m = r["m"]
                if rest == "":
                    if method == "PATCH":
                        for k in ("title", "started_at", "timezone", "language", "participants", "folder_id", "tag_ids"):
                            if k in b: m[k] = b[k]
                        if any(k in b for k in ("title", "started_at", "timezone", "language", "participants")): m["metadata_revision"] += 1
                        m["updated_at"] = ts(datetime.datetime.now(datetime.timezone.utc))
                    if method == "DELETE": M.remove(r); return self.send(202, {"purge_scheduled_at": at(30)})
                    return self.send(200, {"meeting": m})
                if rest == "/jobs": return self.send(200, {"items": [j for j in m["jobs"].values() if j]})
                if rest == "/transcript":
                    return self.send(200, {"revision": r["transcript"]}) if r["transcript"] else self.err(409, "transcript_not_ready")
                if rest == "/transcript/revisions":
                    t = r["transcript"]; return self.send(200, {"items": [{**t, "segments": None}] if t else [], "next_cursor": None})
                if rest.startswith("/transcript/revisions/"): return self.send(200, {"revision": r["transcript"]})
                if rest == "/audio/url": return self.send(200, {"url": "http://localhost:8080/v1/files/audio/x?exp=1&sig=x", "expires_at": at(0, 1)})
                md = re.match(r"^/documents/(manual_notes|ai_notes|mom)(/.*)?$", rest)
                if md:
                    kind, sub = md.group(1), md.group(2) or ""
                    revs = r["revs"].get(kind, []); doc = m["documents"][kind]
                    if sub == "" and method == "GET":
                        return self.send(200, {"document": doc, "revision": revs[-1]}) if doc else self.err(404, "not_found")
                    if sub == "" and method == "PUT":
                        if doc and b.get("expected_revision") != doc["current_revision_number"]:
                            return self.send(409, {"error": {"code": "revision_conflict", "message": "", "details": {"current_revision_number": doc["current_revision_number"], "current_revision_id": doc["current_revision_id"]}, "request_id": U()}})
                        n = (doc["current_revision_number"] if doc else 0) + 1; rid = U(); did = doc["id"] if doc else U()
                        src = revs[-1]["source"] if revs else {k: None for k in ("transcript_revision_id", "transcript_revision_number", "manual_notes_revision_id", "manual_notes_revision_number", "ai_notes_revision_id", "ai_notes_revision_number")}
                        rev = {"id": rid, "document_id": did, "kind": kind, "revision_number": n, "origin": "user_edit", "restored_from_revision_number": None, "reviewed_at": None, "created_at": ts(datetime.datetime.now(datetime.timezone.utc)), "source": src, "content": b["content"]}
                        revs.append(rev); r["revs"][kind] = revs
                        m["documents"][kind] = doc = {"id": did, "kind": kind, "current_revision_number": n, "current_revision_id": rid, "review_status": "draft", "is_stale": False, "stale_reasons": [], "updated_at": rev["created_at"]}
                        m["updated_at"] = rev["created_at"]
                        return self.send(201, {"document": doc, "revision": rev})
                    if sub == "/revisions": return self.send(200, {"items": [{**v, "content": None} for v in reversed(revs)], "next_cursor": None})
                    mr = re.match(r"^/revisions/(\d+)$", sub)
                    if mr: return self.send(200, {"revision": revs[int(mr.group(1)) - 1]})
                    if sub == "/review":
                        revs[-1]["reviewed_at"] = at() if b["status"] == "reviewed" else None
                        doc["review_status"] = b["status"]; r["mom_review"] = b["status"] if kind == "mom" else r["mom_review"]
                        return self.send(200, {"document": doc})
                    if sub == "/generate": return self.send(202, {"job": job(m, "generate_mom" if kind == "mom" else "generate_notes", "queued")})
                return self.err(404, "not_found")
            self.err(404, "not_found")

ThreadingHTTPServer(("0.0.0.0", 8080), H).serve_forever()
